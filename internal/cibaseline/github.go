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
	"slices"
	"strconv"
	"strings"
	"time"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

const (
	// DefaultCollectionMaxElapsed bounds the wall-clock duration of one fetch.
	DefaultCollectionMaxElapsed = 10 * time.Minute
	// DefaultCollectionMaxRequests bounds all HTTP attempts, including retries.
	DefaultCollectionMaxRequests = 10_000
	// DefaultCollectionMaxRetries bounds rate-limit retries across one fetch.
	DefaultCollectionMaxRetries = 3
)

var (
	// ErrCollectionBudgetExhausted identifies request or elapsed-limit failures.
	ErrCollectionBudgetExhausted = errors.New("GitHub collection budget exhausted")
	// ErrGitHubRateLimited identifies a rate limit whose retry allowance was exhausted.
	ErrGitHubRateLimited = errors.New("GitHub rate limited")
)

// GitHubCollector reads Actions metadata only. The supplied token needs
// Actions and repository metadata read access; collection never mutates state.
type GitHubCollector struct {
	Client      *http.Client
	BaseURL     string
	Token       string
	Now         func() time.Time
	Wait        func(context.Context, time.Duration) error
	MaxElapsed  time.Duration
	MaxRequests int
	MaxRetries  int

	budget *collectionBudget
}

type collectionBudget struct {
	parent      context.Context
	now         func() time.Time
	wait        func(context.Context, time.Duration) error
	startedAt   time.Time
	maxElapsed  time.Duration
	maxRequests int
	maxRetries  int
	requests    int
	retries     int
}

type githubRunsPage struct {
	TotalCount int         `json:"total_count"`
	Runs       []githubRun `json:"workflow_runs"`
}

func (page *githubRunsPage) UnmarshalJSON(data []byte) error {
	type githubRunsPageJSON githubRunsPage

	var decoded githubRunsPageJSON
	if err := unmarshalRequiredGitHubObject(data, "workflow runs page", &decoded, "total_count", "workflow_runs"); err != nil {
		return err
	}

	*page = githubRunsPage(decoded)

	return nil
}

type githubRun struct {
	ID           int64               `json:"id"`
	Attempt      int                 `json:"run_attempt"`
	Path         string              `json:"path"`
	Event        string              `json:"event"`
	HeadSHA      string              `json:"head_sha"`
	HeadBranch   string              `json:"head_branch"`
	PullRequests []githubPullRequest `json:"pull_requests"`
	CreatedAt    time.Time           `json:"created_at"`
	StartedAt    time.Time           `json:"run_started_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
	Status       string              `json:"status"`
	Conclusion   string              `json:"conclusion"`
}

type githubPullRequest struct {
	Number int64 `json:"number"`
}

func (run *githubRun) UnmarshalJSON(data []byte) error {
	type githubRunJSON githubRun

	var decoded githubRunJSON

	err := unmarshalGitHubObject(
		data,
		"workflow run",
		&decoded,
		[]string{"head_branch", "run_started_at"},
		"id", "run_attempt", "path", "event", "head_sha", "head_branch", "pull_requests",
		"created_at", "run_started_at", "updated_at", "status", "conclusion",
	)
	if err != nil {
		return err
	}

	*run = githubRun(decoded)

	return nil
}

type githubJobsPage struct {
	TotalCount int         `json:"total_count"`
	Jobs       []githubJob `json:"jobs"`
}

func (page *githubJobsPage) UnmarshalJSON(data []byte) error {
	type githubJobsPageJSON githubJobsPage

	var decoded githubJobsPageJSON
	if err := unmarshalRequiredGitHubObject(data, "jobs page", &decoded, "total_count", "jobs"); err != nil {
		return err
	}

	*page = githubJobsPage(decoded)

	return nil
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

func (job *githubJob) UnmarshalJSON(data []byte) error {
	type githubJobJSON githubJob

	var decoded githubJobJSON

	err := unmarshalGitHubObject(
		data,
		"job",
		&decoded,
		[]string{"started_at", "completed_at", "runner_name", "runner_group_name"},
		"id", "name", "status", "conclusion", "created_at", "started_at", "completed_at",
		"runner_name", "runner_group_name", "labels",
	)
	if err != nil {
		return err
	}

	*job = githubJob(decoded)

	return nil
}

type githubArtifactsPage struct {
	TotalCount int              `json:"total_count"`
	Artifacts  []githubArtifact `json:"artifacts"`
}

func (page *githubArtifactsPage) UnmarshalJSON(data []byte) error {
	type githubArtifactsPageJSON githubArtifactsPage

	var decoded githubArtifactsPageJSON
	if err := unmarshalRequiredGitHubObject(data, "artifacts page", &decoded, "total_count", "artifacts"); err != nil {
		return err
	}

	*page = githubArtifactsPage(decoded)

	return nil
}

type githubCachesPage struct {
	TotalCount int           `json:"total_count"`
	Caches     []githubCache `json:"actions_caches"`
}

func (page *githubCachesPage) UnmarshalJSON(data []byte) error {
	type githubCachesPageJSON githubCachesPage

	var decoded githubCachesPageJSON
	if err := unmarshalRequiredGitHubObject(data, "caches page", &decoded, "total_count", "actions_caches"); err != nil {
		return err
	}

	*page = githubCachesPage(decoded)

	return nil
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

	var decoded githubArtifactJSON

	err := unmarshalGitHubObject(
		data,
		"artifact",
		&decoded,
		[]string{"created_at", "updated_at", "expires_at"},
		"id", "name", "size_in_bytes", "expired", "created_at", "updated_at", "expires_at",
	)
	if err != nil {
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

func (cache *githubCache) UnmarshalJSON(data []byte) error {
	type githubCacheJSON githubCache

	var decoded githubCacheJSON
	if err := unmarshalRequiredGitHubObject(
		data,
		"cache",
		&decoded,
		"id", "key", "ref", "size_in_bytes", "created_at", "last_accessed_at",
	); err != nil {
		return err
	}

	*cache = githubCache(decoded)

	return nil
}

type githubBillable struct {
	TotalMS int64 `json:"total_ms"`
}

func (billable *githubBillable) UnmarshalJSON(data []byte) error {
	type githubBillableJSON githubBillable

	var decoded githubBillableJSON
	if err := unmarshalRequiredGitHubObject(data, "billable usage", &decoded, "total_ms"); err != nil {
		return err
	}

	*billable = githubBillable(decoded)

	return nil
}

type githubTiming struct {
	Billable map[string]githubBillable `json:"billable"`
}

func (timing *githubTiming) UnmarshalJSON(data []byte) error {
	type githubTimingJSON githubTiming

	var decoded githubTimingJSON
	if err := unmarshalRequiredGitHubObject(data, "run timing", &decoded, "billable"); err != nil {
		return err
	}

	*timing = githubTiming(decoded)

	return nil
}

func unmarshalRequiredGitHubObject(data []byte, kind string, target any, required ...string) error {
	return unmarshalGitHubObject(data, kind, target, nil, required...)
}

func unmarshalGitHubObject(
	data []byte,
	kind string,
	target any,
	nullable []string,
	required ...string,
) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	if fields == nil {
		return fmt.Errorf("GitHub %s must be an object", kind)
	}

	nullableFields := make(map[string]bool, len(nullable))
	for _, name := range nullable {
		nullableFields[name] = true
	}

	for _, name := range required {
		value, exists := fields[name]
		if !exists {
			return fmt.Errorf("GitHub %s is missing required field %s", kind, name)
		}

		if strings.TrimSpace(string(value)) == "null" && !nullableFields[name] {
			return fmt.Errorf("GitHub %s field %s must not be null", kind, name)
		}
	}

	return json.Unmarshal(data, target)
}

func (collector GitHubCollector) Fetch(ctx context.Context, repository string, since time.Time, inventory Inventory) (GitHubSnapshot, error) {
	configured, err := collector.configured()
	if err != nil {
		return GitHubSnapshot{}, err
	}

	collector = configured

	if !repositoryPattern.MatchString(repository) || since.IsZero() {
		return GitHubSnapshot{}, errors.New("repository and since are required")
	}

	if err := inventory.Validate(); err != nil {
		return GitHubSnapshot{}, fmt.Errorf("validate collection inventory: %w", err)
	}

	startedAt := collector.Now()
	until := startedAt.UTC().Truncate(time.Second)
	collectionCtx, cancel := context.WithTimeout(ctx, collector.MaxElapsed)

	defer cancel()

	collector.budget = &collectionBudget{
		parent: ctx, now: collector.Now, wait: collector.Wait, startedAt: startedAt,
		maxElapsed: collector.MaxElapsed, maxRequests: collector.MaxRequests, maxRetries: collector.MaxRetries,
	}

	snapshot := GitHubSnapshot{
		SchemaVersion: SnapshotSchemaVersion, Repository: repository, CollectedAt: until,
		RequestedSince: since.UTC(), RequestedUntil: until,
	}

	var (
		allRuns  githubRunsPage
		runPages int
	)

	runPageEndpoint := func(pageNumber int) string {
		return fmt.Sprintf(
			"/repos/%s/actions/runs?per_page=100&page=%d&status=completed&created=%s",
			repository, pageNumber,
			url.QueryEscape(since.UTC().Format(time.RFC3339)+".."+until.Format(time.RFC3339)),
		)
	}

	for pageNumber := 1; ; pageNumber++ {
		var page githubRunsPage

		if err := collector.get(collectionCtx, runPageEndpoint(pageNumber), &page); err != nil {
			return GitHubSnapshot{}, err
		}

		runPages = pageNumber

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
		if len(allRuns.Runs) > allRuns.TotalCount {
			return GitHubSnapshot{}, fmt.Errorf(
				"GitHub returned more workflow runs than the reported count: fetched %d, API reported %d",
				len(allRuns.Runs), allRuns.TotalCount,
			)
		}

		if len(page.Runs) < 100 {
			break
		}
	}

	snapshot.Complete = allRuns.TotalCount == len(allRuns.Runs)
	if !snapshot.Complete {
		return GitHubSnapshot{}, fmt.Errorf("GitHub run count mismatch: API reported %d, fetched %d", allRuns.TotalCount, len(allRuns.Runs))
	}

	seenRawRuns := make(map[int64]bool, len(allRuns.Runs))
	runIDs := make([]int64, 0, len(allRuns.Runs))

	for _, raw := range allRuns.Runs {
		if seenRawRuns[raw.ID] {
			return GitHubSnapshot{}, fmt.Errorf("GitHub returned duplicate raw workflow run ID %d", raw.ID)
		}

		seenRawRuns[raw.ID] = true
		runIDs = append(runIDs, raw.ID)
	}

	if runPages > 1 {
		err := verifyGitHubPagination(
			collector,
			collectionCtx,
			"workflow runs",
			runIDs,
			runPageEndpoint,
			func(page githubRunsPage) (int, []int64) {
				return page.TotalCount, githubRunIDs(page.Runs)
			},
		)
		if err != nil {
			return GitHubSnapshot{}, err
		}
	}

	caches, expectedCaches, err := collector.fetchCaches(collectionCtx, repository)
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

		artifacts, expectedArtifacts, err := collector.fetchArtifacts(collectionCtx, repository, raw.ID)
		if err != nil {
			return GitHubSnapshot{}, err
		}

		if err := validateArtifacts(artifacts, nil); err != nil {
			return GitHubSnapshot{}, fmt.Errorf("validate GitHub artifacts for run %d: %w", raw.ID, err)
		}

		cost := CostInput{Source: "github-actions-run-usage"}

		var timing githubTiming
		if err := collector.get(collectionCtx, fmt.Sprintf("/repos/%s/actions/runs/%d/timing", repository, raw.ID), &timing); err != nil {
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
				if err := collector.get(collectionCtx, endpoint, &attemptRaw); err != nil {
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

			jobs, expectedJobs, err := collector.fetchJobs(collectionCtx, repository, raw.ID, attempt)
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

func (collector GitHubCollector) configured() (GitHubCollector, error) {
	if collector.MaxElapsed < 0 || collector.MaxRequests < 0 || collector.MaxRetries < 0 {
		return GitHubCollector{}, errors.New("GitHub collection limits must not be negative")
	}

	if collector.Client == nil {
		collector.Client = http.DefaultClient
	}

	if collector.BaseURL == "" {
		collector.BaseURL = "https://api.github.com"
	}

	if collector.Now == nil {
		collector.Now = time.Now
	}

	if collector.Wait == nil {
		collector.Wait = waitForContext
	}

	if collector.MaxElapsed == 0 {
		collector.MaxElapsed = DefaultCollectionMaxElapsed
	}

	if collector.MaxRequests == 0 {
		collector.MaxRequests = DefaultCollectionMaxRequests
	}

	if collector.MaxRetries == 0 {
		collector.MaxRetries = DefaultCollectionMaxRetries
	}

	return collector, nil
}

func waitForContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (collector GitHubCollector) get(ctx context.Context, endpoint string, target any) error {
	if collector.budget == nil {
		configured, err := collector.configured()
		if err != nil {
			return err
		}

		collector = configured
		collector.budget = &collectionBudget{
			parent: ctx, now: collector.Now, wait: collector.Wait, startedAt: collector.Now(),
			maxElapsed: collector.MaxElapsed, maxRequests: collector.MaxRequests, maxRetries: collector.MaxRetries,
		}
	}

	for {
		if err := collector.budget.beforeRequest(ctx); err != nil {
			return err
		}

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
			return collector.budget.classifyContextError(ctx, err)
		}

		if response.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()

			if readErr != nil {
				if contextErr := collector.budget.contextError(ctx); contextErr != nil {
					return contextErr
				}

				return fmt.Errorf("read GitHub GET %s error response: %w", endpoint, readErr)
			}

			limit, limited, limitErr := parseRateLimit(response, body, collector.budget.now())
			if limitErr != nil {
				return fmt.Errorf("malformed GitHub rate-limit response for GET %s: %w", endpoint, limitErr)
			}

			if limited {
				if err := collector.budget.retryRateLimit(ctx, endpoint, response.StatusCode, limit); err != nil {
					return err
				}

				continue
			}

			return githubHTTPError{
				StatusCode: response.StatusCode,
				Message:    fmt.Sprintf("GitHub GET %s: %s: %s", endpoint, response.Status, strings.TrimSpace(string(body))),
			}
		}

		decoder := json.NewDecoder(response.Body)
		if err := decoder.Decode(target); err != nil {
			_ = response.Body.Close()

			if contextErr := collector.budget.contextError(ctx); contextErr != nil {
				return contextErr
			}

			return fmt.Errorf("decode GitHub GET %s: malformed or incomplete response: %w", endpoint, err)
		}

		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			_ = response.Body.Close()

			if contextErr := collector.budget.contextError(ctx); contextErr != nil {
				return contextErr
			}

			if err == nil {
				return fmt.Errorf("decode GitHub GET %s: malformed response has a trailing JSON value", endpoint)
			}

			return fmt.Errorf("decode GitHub GET %s: malformed or incomplete response: %w", endpoint, err)
		}

		if err := response.Body.Close(); err != nil {
			return fmt.Errorf("close GitHub GET %s response: %w", endpoint, err)
		}

		if err := collector.budget.afterResponse(ctx); err != nil {
			return err
		}

		return nil
	}
}

type rateLimit struct {
	kind           string
	delay          time.Duration
	defaultBackoff bool
}

func parseRateLimit(response *http.Response, body []byte, now time.Time) (rateLimit, bool, error) {
	remaining := strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining"))
	retryAfter := strings.TrimSpace(response.Header.Get("Retry-After"))
	message := strings.ToLower(string(body))

	limited := response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden &&
			(remaining == "0" || retryAfter != "" || strings.Contains(message, "rate limit") ||
				strings.Contains(message, "abuse detection")))
	if !limited {
		return rateLimit{}, false, nil
	}

	kind := "secondary"

	if remaining != "" {
		remainingCount, err := strconv.ParseInt(remaining, 10, 64)
		if err != nil || remainingCount < 0 {
			return rateLimit{}, false, fmt.Errorf("invalid X-RateLimit-Remaining %q", remaining)
		}

		if remainingCount == 0 {
			kind = "primary"
		}
	}

	var (
		delay     time.Duration
		hasTiming bool
	)

	if retryAfter != "" {
		parsed, err := parseRetryAfter(retryAfter, now)
		if err != nil {
			return rateLimit{}, false, err
		}

		if parsed > 0 {
			delay = parsed
			hasTiming = true
		}
	}

	resetValue := strings.TrimSpace(response.Header.Get("X-RateLimit-Reset"))
	if resetValue != "" && kind == "primary" {
		resetUnix, err := strconv.ParseInt(resetValue, 10, 64)
		if err != nil {
			return rateLimit{}, false, fmt.Errorf("invalid X-RateLimit-Reset %q", resetValue)
		}

		const (
			maxDuration     = time.Duration(1<<63 - 1)
			maxResetSeconds = int64((maxDuration - time.Second) / time.Second)
		)

		nowUnix := now.Unix()
		if resetUnix < 0 || nowUnix < 0 ||
			(resetUnix > nowUnix && resetUnix-nowUnix > maxResetSeconds) {
			return rateLimit{}, false, fmt.Errorf("invalid X-RateLimit-Reset %q", resetValue)
		}

		resetDelay := time.Second
		if resetUnix > nowUnix {
			resetDelay += time.Duration(resetUnix-nowUnix) * time.Second
		}

		if resetDelay > delay {
			delay = resetDelay
		}

		hasTiming = true
	}

	if !hasTiming {
		delay = time.Minute
	}

	return rateLimit{kind: kind, delay: delay, defaultBackoff: !hasTiming}, true, nil
}

func parseRetryAfter(value string, now time.Time) (time.Duration, error) {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		const maxSeconds = int64(time.Duration(1<<63-1) / time.Second)

		if seconds < 0 || seconds > maxSeconds {
			return 0, fmt.Errorf("invalid Retry-After %q", value)
		}

		return time.Duration(seconds) * time.Second, nil
	}

	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, fmt.Errorf("invalid Retry-After %q", value)
	}

	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}

	return delay, nil
}

func (budget *collectionBudget) beforeRequest(ctx context.Context) error {
	if err := budget.contextError(ctx); err != nil {
		return err
	}

	if err := budget.elapsedError(); err != nil {
		return err
	}

	if budget.requests >= budget.maxRequests {
		return fmt.Errorf("%w: request limit %d reached", ErrCollectionBudgetExhausted, budget.maxRequests)
	}

	budget.requests++

	return nil
}

func (budget *collectionBudget) afterResponse(ctx context.Context) error {
	if err := budget.contextError(ctx); err != nil {
		return err
	}

	return budget.elapsedError()
}

func (budget *collectionBudget) retryRateLimit(
	ctx context.Context,
	endpoint string,
	statusCode int,
	limit rateLimit,
) error {
	if budget.retries >= budget.maxRetries {
		return githubRateLimitError{
			Kind: limit.kind, Endpoint: endpoint, StatusCode: statusCode,
			Message: fmt.Sprintf("retry limit %d exhausted", budget.maxRetries),
		}
	}

	if budget.requests >= budget.maxRequests {
		return fmt.Errorf(
			"%w while handling GitHub %s rate limit for GET %s: request limit %d reached",
			ErrCollectionBudgetExhausted, limit.kind, endpoint, budget.maxRequests,
		)
	}

	delay := limit.delay
	if limit.defaultBackoff {
		for retry := 0; retry < budget.retries; retry++ {
			const maxDuration = time.Duration(1<<63 - 1)

			if delay > maxDuration/2 {
				delay = maxDuration

				break
			}

			delay *= 2
		}
	}

	elapsed := budget.now().Sub(budget.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}

	remaining := budget.maxElapsed - elapsed
	if delay >= remaining {
		return fmt.Errorf(
			"%w while handling GitHub %s rate limit for GET %s: required wait %s exceeds remaining time %s",
			ErrCollectionBudgetExhausted, limit.kind, endpoint, delay, remaining,
		)
	}

	budget.retries++
	if err := budget.wait(ctx, delay); err != nil {
		return budget.classifyContextError(ctx, err)
	}

	return budget.afterResponse(ctx)
}

func (budget *collectionBudget) elapsedError() error {
	elapsed := budget.now().Sub(budget.startedAt)
	if elapsed >= budget.maxElapsed {
		return fmt.Errorf(
			"%w: elapsed limit %s reached after %s",
			ErrCollectionBudgetExhausted, budget.maxElapsed, elapsed,
		)
	}

	return nil
}

func (budget *collectionBudget) contextError(ctx context.Context) error {
	if err := budget.parent.Err(); err != nil {
		return fmt.Errorf("GitHub collection cancelled: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: elapsed limit %s reached: %w", ErrCollectionBudgetExhausted, budget.maxElapsed, err)
	}

	return nil
}

func (budget *collectionBudget) classifyContextError(ctx context.Context, err error) error {
	if contextErr := budget.contextError(ctx); contextErr != nil {
		return contextErr
	}

	return err
}

type githubHTTPError struct {
	StatusCode int
	Message    string
}

func (err githubHTTPError) Error() string {
	return err.Message
}

type githubRateLimitError struct {
	Kind       string
	Endpoint   string
	StatusCode int
	Message    string
}

func (err githubRateLimitError) Error() string {
	return fmt.Sprintf(
		"GitHub %s rate limit for GET %s (HTTP %d): %s",
		err.Kind, err.Endpoint, err.StatusCode, err.Message,
	)
}

func (err githubRateLimitError) Unwrap() error {
	return ErrGitHubRateLimited
}

func verifyGitHubPagination[Page any](
	collector GitHubCollector,
	ctx context.Context,
	kind string,
	expectedIDs []int64,
	endpoint func(int) string,
	pageIdentity func(Page) (int, []int64),
) error {
	verifiedIDs := make([]int64, 0, len(expectedIDs))

	for pageNumber := 1; ; pageNumber++ {
		var page Page
		if err := collector.get(ctx, endpoint(pageNumber), &page); err != nil {
			return err
		}

		total, pageIDs := pageIdentity(page)
		if total != len(expectedIDs) {
			return fmt.Errorf("GitHub %s count changed during pagination consistency pass", kind)
		}

		verifiedIDs = append(verifiedIDs, pageIDs...)
		if len(verifiedIDs) > len(expectedIDs) {
			return fmt.Errorf(
				"GitHub returned more %s than the reported count during pagination consistency pass: fetched %d, API reported %d",
				kind, len(verifiedIDs), len(expectedIDs),
			)
		}

		if len(pageIDs) < 100 {
			break
		}
	}

	if len(verifiedIDs) != len(expectedIDs) {
		return fmt.Errorf(
			"GitHub %s count mismatch during pagination consistency pass: API reported %d, fetched %d",
			kind, len(expectedIDs), len(verifiedIDs),
		)
	}

	if !slices.Equal(verifiedIDs, expectedIDs) {
		return fmt.Errorf("GitHub %s identities changed during pagination consistency pass", kind)
	}

	return nil
}

func githubRunIDs(runs []githubRun) []int64 {
	ids := make([]int64, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}

	return ids
}

func (collector GitHubCollector) fetchCaches(ctx context.Context, repository string) ([]Cache, int, error) {
	var (
		result   []Cache
		expected int
		pages    int
	)

	endpoint := func(pageNumber int) string {
		return fmt.Sprintf("/repos/%s/actions/caches?per_page=100&page=%d", repository, pageNumber)
	}

	for pageNumber := 1; ; pageNumber++ {
		var page githubCachesPage
		if err := collector.get(ctx, endpoint(pageNumber), &page); err != nil {
			return nil, 0, err
		}

		pages = pageNumber

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

		if len(result) > expected {
			return nil, 0, fmt.Errorf(
				"GitHub returned more caches than the reported count: fetched %d, API reported %d",
				len(result), expected,
			)
		}

		if len(page.Caches) < 100 {
			break
		}
	}

	if expected != len(result) {
		return nil, 0, fmt.Errorf("GitHub cache count mismatch: API reported %d, fetched %d", expected, len(result))
	}

	if pages > 1 {
		expectedIDs := make([]int64, 0, len(result))
		for _, cache := range result {
			expectedIDs = append(expectedIDs, cache.ID)
		}

		err := verifyGitHubPagination(
			collector,
			ctx,
			"caches",
			expectedIDs,
			endpoint,
			func(page githubCachesPage) (int, []int64) {
				ids := make([]int64, 0, len(page.Caches))
				for _, cache := range page.Caches {
					ids = append(ids, cache.ID)
				}

				return page.TotalCount, ids
			},
		)
		if err != nil {
			return nil, 0, err
		}
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
		pages    int
	)

	endpoint := func(pageNumber int) string {
		return fmt.Sprintf(
			"/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d",
			repository, runID, attempt, pageNumber,
		)
	}

	for pageNumber := 1; ; pageNumber++ {
		var page githubJobsPage

		if err := collector.get(ctx, endpoint(pageNumber), &page); err != nil {
			return nil, 0, err
		}

		pages = pageNumber

		if pageNumber == 1 {
			if page.TotalCount < 0 {
				return nil, 0, fmt.Errorf("GitHub returned a negative job count for run %d attempt %d", runID, attempt)
			}

			expected = page.TotalCount
		} else if page.TotalCount != expected {
			return nil, 0, fmt.Errorf("GitHub job count changed during pagination for run %d attempt %d", runID, attempt)
		}

		result.Jobs = append(result.Jobs, page.Jobs...)
		if len(result.Jobs) > expected {
			return nil, 0, fmt.Errorf(
				"GitHub returned more jobs than the reported count for run %d attempt %d: fetched %d, API reported %d",
				runID, attempt, len(result.Jobs), expected,
			)
		}

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

	if pages > 1 {
		expectedIDs := make([]int64, 0, len(result.Jobs))
		for _, job := range result.Jobs {
			expectedIDs = append(expectedIDs, job.ID)
		}

		err := verifyGitHubPagination(
			collector,
			ctx,
			fmt.Sprintf("jobs for run %d attempt %d", runID, attempt),
			expectedIDs,
			endpoint,
			func(page githubJobsPage) (int, []int64) {
				ids := make([]int64, 0, len(page.Jobs))
				for _, job := range page.Jobs {
					ids = append(ids, job.ID)
				}

				return page.TotalCount, ids
			},
		)
		if err != nil {
			return nil, 0, err
		}
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
		pages    int
	)

	endpoint := func(pageNumber int) string {
		return fmt.Sprintf("/repos/%s/actions/runs/%d/artifacts?per_page=100&page=%d", repository, runID, pageNumber)
	}

	for pageNumber := 1; ; pageNumber++ {
		var page githubArtifactsPage

		if err := collector.get(ctx, endpoint(pageNumber), &page); err != nil {
			return nil, 0, err
		}

		pages = pageNumber

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

		if len(result) > expected {
			return nil, 0, fmt.Errorf(
				"GitHub returned more artifacts than the reported count for run %d: fetched %d, API reported %d",
				runID, len(result), expected,
			)
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

	if pages > 1 {
		expectedIDs := make([]int64, 0, len(result))
		for _, artifact := range result {
			expectedIDs = append(expectedIDs, artifact.ID)
		}

		err := verifyGitHubPagination(
			collector,
			ctx,
			fmt.Sprintf("artifacts for run %d", runID),
			expectedIDs,
			endpoint,
			func(page githubArtifactsPage) (int, []int64) {
				ids := make([]int64, 0, len(page.Artifacts))
				for _, artifact := range page.Artifacts {
					ids = append(ids, artifact.ID)
				}

				return page.TotalCount, ids
			},
		)
		if err != nil {
			return nil, 0, err
		}
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
