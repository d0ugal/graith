package cibaseline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// GitHubCollector reads Actions metadata only. The supplied token needs
// Actions and repository metadata read access; collection never mutates state.
type GitHubCollector struct {
	Client  *http.Client
	BaseURL string
	Token   string
	Now     func() time.Time
}

type githubRunsPage struct {
	TotalCount int         `json:"total_count"`
	Runs       []githubRun `json:"workflow_runs"`
}

type githubRun struct {
	ID           int64  `json:"id"`
	Attempt      int    `json:"run_attempt"`
	Path         string `json:"path"`
	Event        string `json:"event"`
	HeadSHA      string `json:"head_sha"`
	HeadBranch   string `json:"head_branch"`
	PullRequests []struct {
		Number int64 `json:"number"`
	} `json:"pull_requests"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"run_started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
}

type githubJobsPage struct {
	TotalCount int         `json:"total_count"`
	Jobs       []githubJob `json:"jobs"`
}

type githubJob struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	RunnerName  string    `json:"runner_name"`
	RunnerGroup string    `json:"runner_group_name"`
	Labels      []string  `json:"labels"`
}

type githubArtifactsPage struct {
	TotalCount int              `json:"total_count"`
	Artifacts  []githubArtifact `json:"artifacts"`
}

type githubCachesPage struct {
	TotalCount int           `json:"total_count"`
	Caches     []githubCache `json:"actions_caches"`
}

type githubArtifact struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	SizeBytes int64      `json:"size_in_bytes"`
	Digest    string     `json:"digest"`
	Expired   bool       `json:"expired"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (artifact *githubArtifact) UnmarshalJSON(data []byte) error {
	type githubArtifactJSON githubArtifact

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	for _, required := range []string{"created_at", "updated_at", "expires_at"} {
		if _, exists := fields[required]; !exists {
			return fmt.Errorf("GitHub artifact is missing required nullable field %s", required)
		}
	}

	var decoded githubArtifactJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*artifact = githubArtifact(decoded)

	return nil
}

type githubCache struct {
	ID             int64     `json:"id"`
	Key            string    `json:"key"`
	Ref            string    `json:"ref"`
	SizeBytes      int64     `json:"size_in_bytes"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`
}

type githubTiming struct {
	Billable map[string]struct {
		TotalMS int64 `json:"total_ms"`
	} `json:"billable"`
}

func (collector GitHubCollector) Fetch(ctx context.Context, repository string, since time.Time, inventory Inventory) (GitHubSnapshot, error) {
	if collector.Client == nil {
		collector.Client = http.DefaultClient
	}

	if collector.BaseURL == "" {
		collector.BaseURL = "https://api.github.com"
	}

	if collector.Now == nil {
		collector.Now = time.Now
	}

	if !repositoryPattern.MatchString(repository) || since.IsZero() {
		return GitHubSnapshot{}, errors.New("repository and since are required")
	}

	if err := inventory.Validate(); err != nil {
		return GitHubSnapshot{}, fmt.Errorf("validate collection inventory: %w", err)
	}

	until := collector.Now().UTC().Truncate(time.Second)
	snapshot := GitHubSnapshot{
		SchemaVersion: SnapshotSchemaVersion, Repository: repository, CollectedAt: until,
		RequestedSince: since.UTC(), RequestedUntil: until,
	}

	var allRuns githubRunsPage

	for pageNumber := 1; ; pageNumber++ {
		var page githubRunsPage

		endpoint := fmt.Sprintf(
			"/repos/%s/actions/runs?per_page=100&page=%d&status=completed&created=%s",
			repository, pageNumber,
			url.QueryEscape(since.UTC().Format(time.RFC3339)+".."+until.Format(time.RFC3339)),
		)
		if err := collector.get(ctx, endpoint, &page); err != nil {
			return GitHubSnapshot{}, err
		}

		if page.TotalCount > 1000 {
			return GitHubSnapshot{}, fmt.Errorf("GitHub run query is incomplete: %d results exceeds the 1000-result API ceiling", page.TotalCount)
		}

		if pageNumber == 1 {
			if page.TotalCount < 0 {
				return GitHubSnapshot{}, errors.New("GitHub returned a negative workflow run count")
			}

			allRuns.TotalCount = page.TotalCount
		} else if page.TotalCount != allRuns.TotalCount {
			return GitHubSnapshot{}, errors.New("GitHub workflow run count changed during pagination")
		}

		allRuns.Runs = append(allRuns.Runs, page.Runs...)
		if len(page.Runs) < 100 {
			break
		}
	}

	snapshot.Complete = allRuns.TotalCount == len(allRuns.Runs)
	if !snapshot.Complete {
		return GitHubSnapshot{}, fmt.Errorf("GitHub run count mismatch: API reported %d, fetched %d", allRuns.TotalCount, len(allRuns.Runs))
	}

	caches, expectedCaches, err := collector.fetchCaches(ctx, repository)
	if err != nil {
		return GitHubSnapshot{}, err
	}

	snapshot.Caches = caches
	snapshot.ExpectedCaches = expectedCaches
	snapshot.ExpectedWorkflowRuns = allRuns.TotalCount

	if err := validateCaches(snapshot.Caches); err != nil {
		return GitHubSnapshot{}, fmt.Errorf("validate GitHub caches: %w", err)
	}

	for _, raw := range allRuns.Runs {
		if err := validateRawGitHubRun(raw); err != nil {
			return GitHubSnapshot{}, err
		}

		workflowPath := normalizeWorkflowPathReference(raw.Path)
		workflow, known := inventoryWorkflowForPath(inventory, workflowPath)

		if !known {
			return GitHubSnapshot{}, fmt.Errorf("GitHub returned unknown workflow path %q", workflowPath)
		}

		workflowID := workflow.ID

		artifacts, expectedArtifacts, err := collector.fetchArtifacts(ctx, repository, raw.ID)
		if err != nil {
			return GitHubSnapshot{}, err
		}

		if err := validateArtifacts(artifacts, nil); err != nil {
			return GitHubSnapshot{}, fmt.Errorf("validate GitHub artifacts for run %d: %w", raw.ID, err)
		}

		cost := CostInput{Source: "github-actions-run-usage"}

		var timing githubTiming
		if err := collector.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d/timing", repository, raw.ID), &timing); err != nil {
			var statusError githubHTTPError
			if !errors.As(err, &statusError) || (statusError.StatusCode != http.StatusNotFound && statusError.StatusCode != http.StatusGone) {
				return GitHubSnapshot{}, err
			}

			cost.UnavailableReason = fmt.Sprintf("GitHub run usage endpoint returned HTTP %d", statusError.StatusCode)
		} else {
			cost.Available = true

			cost.BillableMinutes = make(map[string]int64, len(timing.Billable))
			for platform, bill := range timing.Billable {
				if bill.TotalMS < 0 {
					return GitHubSnapshot{}, fmt.Errorf("GitHub returned negative run usage for %s", platform)
				}

				cost.BillableMinutes[platform] = (bill.TotalMS + 59_999) / 60_000
			}
		}

		if err := validateCost(cost); err != nil {
			return GitHubSnapshot{}, fmt.Errorf("validate GitHub cost for run %d: %w", raw.ID, err)
		}

		for attempt := 1; attempt <= raw.Attempt; attempt++ {
			var attemptRaw githubRun

			if attempt < raw.Attempt {
				endpoint := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d", repository, raw.ID, attempt)
				if err := collector.get(ctx, endpoint, &attemptRaw); err != nil {
					return GitHubSnapshot{}, err
				}

				if attemptRaw.ID != raw.ID || attemptRaw.Attempt != attempt || !sameGitHubRunIdentity(raw, attemptRaw) {
					return GitHubSnapshot{}, fmt.Errorf("GitHub returned mismatched metadata for run %d attempt %d", raw.ID, attempt)
				}
			} else {
				attemptRaw = raw
			}

			if err := validateRawGitHubRun(attemptRaw); err != nil {
				return GitHubSnapshot{}, fmt.Errorf("run %d attempt %d: %w", raw.ID, attempt, err)
			}

			run := GitHubRun{
				ID: raw.ID, Attempt: attempt, WorkflowID: workflowID, WorkflowPath: workflowPath, Event: attemptRaw.Event,
				HeadSHA: attemptRaw.HeadSHA, HeadBranch: attemptRaw.HeadBranch,
				CreatedAt: attemptRaw.CreatedAt, StartedAt: attemptRaw.StartedAt,
				UpdatedAt: attemptRaw.UpdatedAt, Status: attemptRaw.Status, Conclusion: attemptRaw.Conclusion,
			}
			run.PullRequest = pullRequestNumber(attemptRaw)

			if attempt == 1 {
				run.Artifacts, run.Cost = artifacts, cost
				run.ExpectedArtifacts = expectedArtifacts
			}

			jobs, expectedJobs, err := collector.fetchJobs(ctx, repository, raw.ID, attempt)
			if err != nil {
				return GitHubSnapshot{}, err
			}

			run.ExpectedJobs = expectedJobs
			seenRawJobs := map[int64]bool{}

			for _, rawJob := range jobs {
				if err := validateRawGitHubJob(rawJob); err != nil {
					return GitHubSnapshot{}, fmt.Errorf("run %d attempt %d: %w", raw.ID, attempt, err)
				}

				if seenRawJobs[rawJob.ID] {
					return GitHubSnapshot{}, fmt.Errorf("run %d attempt %d has duplicate raw job ID %d", raw.ID, attempt, rawJob.ID)
				}

				seenRawJobs[rawJob.ID] = true

				coordinates, err := resolveGitHubJobCoordinates(inventory, workflowID, rawJob)
				if err != nil {
					return GitHubSnapshot{}, err
				}

				for _, coordinate := range coordinates {
					run.Jobs = append(run.Jobs, GitHubJob{
						ID: rawJob.ID, Key: coordinate.Key, Coordinate: coordinate.Coordinate,
						Name: rawJob.Name, Attempt: attempt, Status: rawJob.Status,
						Conclusion: rawJob.Conclusion, CreatedAt: rawJob.CreatedAt, StartedAt: rawJob.StartedAt,
						CompletedAt: rawJob.CompletedAt, RunnerName: rawJob.RunnerName,
						RunnerGroup: rawJob.RunnerGroup, Labels: rawJob.Labels,
						SyntheticFanout: len(coordinates) > 1,
					})
				}
			}

			snapshot.Runs = append(snapshot.Runs, run)
		}
	}

	inferSuperseded(snapshot.Runs)
	snapshot.ExpectedRunAttempts = len(snapshot.Runs)

	return snapshot, nil
}

func workflowIDFromPath(workflowPath string) string {
	filePath, _, _ := strings.Cut(workflowPath, "@")

	return strings.TrimSuffix(path.Base(filePath), path.Ext(filePath))
}

func workflowFilePath(workflowPath string) string {
	filePath, _, _ := strings.Cut(workflowPath, "@")

	return filePath
}

func inventoryWorkflowForPath(inventory Inventory, workflowPath string) (Workflow, bool) {
	filePath := workflowFilePath(workflowPath)
	for _, workflow := range inventory.Workflows {
		if workflow.Path == filePath {
			return workflow, true
		}
	}

	return Workflow{}, false
}

func normalizeWorkflowPathReference(workflowPath string) string {
	filePath, reference, found := strings.Cut(strings.TrimSpace(workflowPath), "@")

	filePath = strings.TrimSpace(filePath)
	if path.IsAbs(filePath) {
		return ""
	}

	for _, segment := range strings.Split(filePath, "/") {
		if segment == ".." {
			return ""
		}
	}

	filePath = strings.TrimPrefix(path.Clean(filePath), "./")
	if filePath == "" || filePath == "." {
		return ""
	}

	if !found {
		return filePath
	}

	reference = strings.TrimSpace(reference)
	if reference == "" {
		return ""
	}

	return filePath + "@" + reference
}

func pullRequestNumber(run githubRun) int64 {
	if run.Event != "pull_request" || len(run.PullRequests) == 0 {
		return 0
	}

	return run.PullRequests[0].Number
}

func validateRawGitHubRun(run githubRun) error {
	if run.ID <= 0 || run.Attempt < 1 || normalizeWorkflowPathReference(run.Path) == "" ||
		strings.TrimSpace(run.Event) == "" || strings.TrimSpace(run.HeadSHA) == "" ||
		run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) ||
		(!run.StartedAt.IsZero() &&
			(run.StartedAt.Before(run.CreatedAt) || run.StartedAt.After(run.UpdatedAt))) ||
		run.Status != "completed" {
		return fmt.Errorf("GitHub returned malformed run %d", run.ID)
	}

	if _, err := normalizeOutcome(run.Status, run.Conclusion); err != nil {
		return fmt.Errorf("GitHub returned malformed run %d conclusion: %w", run.ID, err)
	}

	if run.Event == "pull_request" {
		for _, pullRequest := range run.PullRequests {
			if pullRequest.Number <= 0 {
				return fmt.Errorf("GitHub returned malformed pull request coordinate for run %d", run.ID)
			}
		}
	}

	return nil
}

func validateRawGitHubJob(job githubJob) error {
	outcome, err := normalizeOutcome(job.Status, job.Conclusion)
	if job.ID <= 0 || strings.TrimSpace(job.Name) == "" || job.Status != "completed" || err != nil {
		return fmt.Errorf("GitHub returned malformed job %d", job.ID)
	}

	if job.CreatedAt.IsZero() {
		return fmt.Errorf("GitHub returned malformed job %d timestamps", job.ID)
	}

	if outcome != "skipped" && (job.StartedAt.IsZero() || job.CompletedAt.IsZero() ||
		job.StartedAt.Before(job.CreatedAt) || job.CompletedAt.Before(job.StartedAt)) {
		return fmt.Errorf("GitHub returned malformed job %d timestamps", job.ID)
	}

	seenLabels := map[string]bool{}
	for _, label := range job.Labels {
		if strings.TrimSpace(label) == "" || seenLabels[label] {
			return fmt.Errorf("GitHub returned malformed job %d labels", job.ID)
		}

		seenLabels[label] = true
	}

	return nil
}

func sameGitHubRunIdentity(first, next githubRun) bool {
	return normalizeWorkflowPathReference(first.Path) == normalizeWorkflowPathReference(next.Path) &&
		first.Event == next.Event &&
		first.HeadSHA == next.HeadSHA &&
		first.HeadBranch == next.HeadBranch &&
		pullRequestNumber(first) == pullRequestNumber(next)
}

func inferSuperseded(runs []GitHubRun) {
	createdByRun := make(map[int64]time.Time)
	for _, run := range runs {
		createdAt, exists := createdByRun[run.ID]
		if !exists || run.CreatedAt.Before(createdAt) {
			createdByRun[run.ID] = run.CreatedAt
		}
	}

	for i := range runs {
		if runs[i].Conclusion != "cancelled" {
			continue
		}

		for j := range runs {
			if runs[j].ID == runs[i].ID || !runs[j].CreatedAt.After(runs[i].CreatedAt) ||
				runs[j].WorkflowID != runs[i].WorkflowID || runs[j].Event != runs[i].Event {
				continue
			}

			samePR := runs[i].PullRequest != 0 && runs[i].PullRequest == runs[j].PullRequest

			sameBranch := runs[i].PullRequest == 0 && runs[i].HeadBranch != "" && runs[i].HeadBranch == runs[j].HeadBranch
			if samePR || sameBranch {
				currentCreatedAt := createdByRun[runs[i].SupersededBy]
				if runs[i].SupersededBy == 0 || runs[j].CreatedAt.Before(currentCreatedAt) ||
					(runs[j].CreatedAt.Equal(currentCreatedAt) && runs[j].ID < runs[i].SupersededBy) {
					runs[i].SupersededBy = runs[j].ID
					runs[i].SupersessionBasis = "inferred-later-run-same-workflow-event-and-change-coordinate"
				}
			}
		}
	}
}

func (collector GitHubCollector) get(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(collector.BaseURL, "/")+endpoint, nil)
	if err != nil {
		return err
	}

	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	if collector.Token != "" {
		request.Header.Set("Authorization", "Bearer "+collector.Token)
	}

	response, err := collector.Client.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))

		return githubHTTPError{
			StatusCode: response.StatusCode,
			Message:    fmt.Sprintf("GitHub GET %s: %s: %s", endpoint, response.Status, strings.TrimSpace(string(body))),
		}
	}

	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode GitHub GET %s: %w", endpoint, err)
	}

	return nil
}

type githubHTTPError struct {
	StatusCode int
	Message    string
}

func (err githubHTTPError) Error() string {
	return err.Message
}

func (collector GitHubCollector) fetchCaches(ctx context.Context, repository string) ([]Cache, int, error) {
	var (
		result   []Cache
		expected int
	)

	for pageNumber := 1; ; pageNumber++ {
		var page githubCachesPage
		if err := collector.get(ctx, fmt.Sprintf("/repos/%s/actions/caches?per_page=100&page=%d", repository, pageNumber), &page); err != nil {
			return nil, 0, err
		}

		if pageNumber == 1 {
			if page.TotalCount < 0 {
				return nil, 0, errors.New("GitHub returned a negative cache count")
			}

			expected = page.TotalCount
		} else if page.TotalCount != expected {
			return nil, 0, errors.New("GitHub cache count changed during pagination")
		}

		for _, cache := range page.Caches {
			result = append(result, Cache(cache))
		}

		if len(page.Caches) < 100 {
			break
		}
	}

	if expected != len(result) {
		return nil, 0, fmt.Errorf("GitHub cache count mismatch: API reported %d, fetched %d", expected, len(result))
	}

	return result, expected, nil
}

func (collector GitHubCollector) fetchJobs(
	ctx context.Context,
	repository string,
	runID int64,
	attempt int,
) ([]githubJob, int, error) {
	var (
		result   = githubJobsPage{}
		expected int
	)

	for pageNumber := 1; ; pageNumber++ {
		var page githubJobsPage

		endpoint := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", repository, runID, attempt, pageNumber)
		if err := collector.get(ctx, endpoint, &page); err != nil {
			return nil, 0, err
		}

		if pageNumber == 1 {
			if page.TotalCount < 0 {
				return nil, 0, fmt.Errorf("GitHub returned a negative job count for run %d attempt %d", runID, attempt)
			}

			expected = page.TotalCount
		} else if page.TotalCount != expected {
			return nil, 0, fmt.Errorf("GitHub job count changed during pagination for run %d attempt %d", runID, attempt)
		}

		result.Jobs = append(result.Jobs, page.Jobs...)
		if len(page.Jobs) < 100 {
			break
		}
	}

	if expected != len(result.Jobs) {
		return nil, 0, fmt.Errorf(
			"GitHub job count mismatch for run %d attempt %d: API reported %d, fetched %d",
			runID, attempt, expected, len(result.Jobs),
		)
	}

	return result.Jobs, expected, nil
}

func (collector GitHubCollector) fetchArtifacts(
	ctx context.Context,
	repository string,
	runID int64,
) ([]Artifact, int, error) {
	var (
		result   []Artifact
		expected int
	)

	for pageNumber := 1; ; pageNumber++ {
		var page githubArtifactsPage

		endpoint := fmt.Sprintf("/repos/%s/actions/runs/%d/artifacts?per_page=100&page=%d", repository, runID, pageNumber)
		if err := collector.get(ctx, endpoint, &page); err != nil {
			return nil, 0, err
		}

		if pageNumber == 1 {
			if page.TotalCount < 0 {
				return nil, 0, fmt.Errorf("GitHub returned a negative artifact count for run %d", runID)
			}

			expected = page.TotalCount
		} else if page.TotalCount != expected {
			return nil, 0, fmt.Errorf("GitHub artifact count changed during pagination for run %d", runID)
		}

		for _, artifact := range page.Artifacts {
			result = append(result, Artifact(artifact))
		}

		if len(page.Artifacts) < 100 {
			break
		}
	}

	if expected != len(result) {
		return nil, 0, fmt.Errorf(
			"GitHub artifact count mismatch for run %d: API reported %d, fetched %d",
			runID, expected, len(result),
		)
	}

	return result, expected, nil
}

type resolvedJobCoordinate struct {
	Key        string
	Coordinate string
}

func resolveJobCoordinates(inventory Inventory, workflowID, name string) []resolvedJobCoordinate {
	for _, workflow := range inventory.Workflows {
		if workflow.ID != workflowID {
			continue
		}

		for _, job := range workflow.Jobs {
			if job.Name == name {
				result := make([]resolvedJobCoordinate, 0, len(job.Coordinates))
				for _, coordinate := range job.Coordinates {
					result = append(result, resolvedJobCoordinate{Key: job.ID, Coordinate: coordinate})
				}

				return result
			}

			for index, githubName := range job.GitHubNames {
				if githubName == name {
					return []resolvedJobCoordinate{{Key: job.ID, Coordinate: job.Coordinates[index]}}
				}
			}
		}
	}

	return nil
}

func resolveGitHubJobCoordinates(
	inventory Inventory,
	workflowID string,
	job githubJob,
) ([]resolvedJobCoordinate, error) {
	coordinates := resolveJobCoordinates(inventory, workflowID, job.Name)
	if len(coordinates) == 0 {
		return nil, fmt.Errorf("unknown GitHub job %q in %s", job.Name, workflowID)
	}

	if len(coordinates) > 1 && (job.Status != "completed" || job.Conclusion != "skipped") {
		return nil, fmt.Errorf("unexpanded matrix job %q in %s is not completed/skipped", job.Name, workflowID)
	}

	return coordinates, nil
}

func ParseSince(value string, now time.Time) (time.Time, error) {
	if days, err := strconv.Atoi(value); err == nil {
		if days < 1 || days > 28 {
			return time.Time{}, errors.New("day lookback must be between 1 and 28")
		}

		return now.UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
	}

	return time.Parse(time.RFC3339, value)
}
