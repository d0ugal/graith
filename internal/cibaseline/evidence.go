package cibaseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SnapshotSchemaVersion = 2
	EvidenceSchemaVersion = 2
)

var artifactDigestPattern = regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`)

type GitHubSnapshot struct {
	SchemaVersion        int         `json:"schema_version"`
	Repository           string      `json:"repository"`
	CollectedAt          time.Time   `json:"collected_at"`
	RequestedSince       time.Time   `json:"requested_since"`
	RequestedUntil       time.Time   `json:"requested_until"`
	Complete             bool        `json:"complete"`
	ExpectedWorkflowRuns int         `json:"expected_workflow_runs"`
	ExpectedRunAttempts  int         `json:"expected_run_attempts"`
	ExpectedCaches       int         `json:"expected_caches"`
	Runs                 []GitHubRun `json:"runs"`
	Caches               []Cache     `json:"cache_observations,omitempty"`
}

type GitHubRun struct {
	ID                int64       `json:"id"`
	Attempt           int         `json:"run_attempt"`
	WorkflowID        string      `json:"workflow_id"`
	WorkflowPath      string      `json:"workflow_path"`
	Event             string      `json:"event"`
	HeadSHA           string      `json:"head_sha"`
	HeadBranch        string      `json:"head_branch,omitempty"`
	PullRequest       int64       `json:"pull_request,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	StartedAt         time.Time   `json:"run_started_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
	Status            string      `json:"status"`
	Conclusion        string      `json:"conclusion"`
	ExpectedJobs      int         `json:"expected_jobs"`
	ExpectedArtifacts int         `json:"expected_artifacts"`
	SupersededBy      int64       `json:"superseded_by,omitempty"`
	SupersessionBasis string      `json:"supersession_basis,omitempty"`
	Jobs              []GitHubJob `json:"jobs"`
	Artifacts         []Artifact  `json:"artifacts,omitempty"`
	Cost              CostInput   `json:"cost_input"`
}

type GitHubJob struct {
	ID              int64     `json:"id"`
	Key             string    `json:"key"`
	Coordinate      string    `json:"coordinate"`
	Name            string    `json:"name"`
	Attempt         int       `json:"attempt"`
	Status          string    `json:"status"`
	Conclusion      string    `json:"conclusion"`
	CreatedAt       time.Time `json:"created_at"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
	RunnerName      string    `json:"runner_name,omitempty"`
	RunnerGroup     string    `json:"runner_group_name,omitempty"`
	Labels          []string  `json:"labels,omitempty"`
	SyntheticFanout bool      `json:"synthetic_fanout,omitempty"`
}

type Artifact struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	SizeBytes int64      `json:"size_in_bytes"`
	Digest    string     `json:"digest,omitempty"`
	Expired   bool       `json:"expired"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (artifact *Artifact) UnmarshalJSON(data []byte) error {
	type artifactJSON Artifact

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	for _, required := range []string{"created_at", "updated_at", "expires_at"} {
		if _, exists := fields[required]; !exists {
			return fmt.Errorf("artifact is missing required nullable field %s", required)
		}
	}

	known := map[string]bool{
		"id": true, "name": true, "size_in_bytes": true, "digest": true, "expired": true,
		"created_at": true, "updated_at": true, "expires_at": true,
	}
	for field := range fields {
		if !known[field] {
			return fmt.Errorf("artifact has unknown field %s", field)
		}
	}

	var decoded artifactJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*artifact = Artifact(decoded)

	return nil
}

type Cache struct {
	ID             int64     `json:"id"`
	Key            string    `json:"key"`
	Ref            string    `json:"ref"`
	SizeBytes      int64     `json:"size_in_bytes"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`
}

type CostInput struct {
	BillableMinutes   map[string]int64 `json:"billable_minutes,omitempty"`
	RunnerLabels      []string         `json:"runner_labels,omitempty"`
	Available         bool             `json:"available"`
	Source            string           `json:"source,omitempty"`
	UnavailableReason string           `json:"unavailable_reason,omitempty"`
}

type Evidence struct {
	SchemaVersion        int                      `json:"schema_version"`
	InventoryDigest      string                   `json:"inventory_digest"`
	InventoryRebind      *InventoryRebindManifest `json:"inventory_rebind,omitempty"`
	Repository           string                   `json:"repository"`
	CollectedAt          time.Time                `json:"collected_at"`
	RequestedSince       time.Time                `json:"requested_since"`
	RequestedUntil       time.Time                `json:"requested_until"`
	Complete             bool                     `json:"complete"`
	ExpectedWorkflowRuns int                      `json:"expected_workflow_runs"`
	ExpectedRunAttempts  int                      `json:"expected_run_attempts"`
	ExpectedCaches       int                      `json:"expected_caches"`
	RunAttempts          []RunAttemptEvidence     `json:"run_attempts"`
	Observations         []RunEvidence            `json:"observations"`
	RunResources         []RunResourceEvidence    `json:"run_resources,omitempty"`
	Caches               []Cache                  `json:"cache_observations,omitempty"`
	Digest               string                   `json:"digest"`
}

type RunAttemptEvidence struct {
	RunID             int64     `json:"run_id"`
	Attempt           int       `json:"attempt"`
	WorkflowID        string    `json:"workflow_id"`
	WorkflowPath      string    `json:"workflow_path"`
	Event             string    `json:"event"`
	HeadSHA           string    `json:"head_sha"`
	HeadBranch        string    `json:"head_branch,omitempty"`
	PullRequest       int64     `json:"pull_request,omitempty"`
	Outcome           string    `json:"outcome"`
	Conclusion        string    `json:"conclusion"`
	ExpectedJobs      int       `json:"expected_jobs"`
	CreatedAt         time.Time `json:"created_at"`
	StartedAt         time.Time `json:"started_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	SupersededBy      int64     `json:"superseded_by,omitempty"`
	RunDisposition    string    `json:"run_disposition,omitempty"`
	SupersessionBasis string    `json:"supersession_basis,omitempty"`
}

type RunResourceEvidence struct {
	RunID             int64      `json:"run_id"`
	ExpectedArtifacts int        `json:"expected_artifacts"`
	Artifacts         []Artifact `json:"artifacts,omitempty"`
	Cost              CostInput  `json:"cost_input"`
}

type RunEvidence struct {
	RunID               int64     `json:"run_id"`
	JobID               int64     `json:"job_id"`
	Attempt             int       `json:"attempt"`
	Retry               bool      `json:"retry"`
	RecoveredAfterRetry bool      `json:"recovered_after_retry"`
	WorkflowID          string    `json:"workflow_id"`
	Event               string    `json:"event"`
	HeadSHA             string    `json:"head_sha"`
	HeadBranch          string    `json:"head_branch,omitempty"`
	PullRequest         int64     `json:"pull_request,omitempty"`
	Coordinate          string    `json:"actual_mode_coordinate"`
	Outcome             string    `json:"outcome"`
	JobStatus           string    `json:"job_status"`
	JobConclusion       string    `json:"job_conclusion"`
	FirstOutcome        string    `json:"first_outcome"`
	QueueMillis         int64     `json:"queue_millis"`
	ExecutionMillis     int64     `json:"execution_millis"`
	RunCreatedAt        time.Time `json:"run_created_at"`
	CreatedAt           time.Time `json:"created_at"`
	StartedAt           time.Time `json:"started_at"`
	CompletedAt         time.Time `json:"completed_at"`
	SupersededBy        int64     `json:"superseded_by,omitempty"`
	RunDisposition      string    `json:"run_disposition,omitempty"`
	SupersessionBasis   string    `json:"supersession_basis,omitempty"`
	RunnerName          string    `json:"runner_name,omitempty"`
	RunnerGroup         string    `json:"runner_group_name,omitempty"`
	RunnerLabels        []string  `json:"runner_labels,omitempty"`
	SyntheticFanout     bool      `json:"synthetic_fanout,omitempty"`
}

func Collect(snapshot GitHubSnapshot, inventory Inventory) (Evidence, error) {
	if err := inventory.Validate(); err != nil {
		return Evidence{}, fmt.Errorf("validate collection inventory: %w", err)
	}

	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return Evidence{}, fmt.Errorf("unsupported snapshot schema %d", snapshot.SchemaVersion)
	}

	if snapshot.Repository == "" || snapshot.CollectedAt.IsZero() || snapshot.RequestedSince.IsZero() ||
		snapshot.RequestedUntil.IsZero() || !snapshot.RequestedUntil.After(snapshot.RequestedSince) {
		return Evidence{}, errors.New("snapshot repository, collection time, and query window are required")
	}

	if !snapshot.Complete || snapshot.ExpectedWorkflowRuns < 0 || snapshot.ExpectedRunAttempts < 0 ||
		snapshot.ExpectedCaches < 0 || snapshot.ExpectedRunAttempts != len(snapshot.Runs) ||
		snapshot.ExpectedCaches != len(snapshot.Caches) {
		return Evidence{}, errors.New("snapshot has incomplete or inconsistent authoritative counts")
	}

	if err := validateCaches(snapshot.Caches); err != nil {
		return Evidence{}, err
	}

	jobs := map[string]Job{}
	workflowPaths := map[string]string{}

	for _, workflow := range inventory.Workflows {
		workflowPaths[workflow.ID] = workflow.Path

		for _, job := range workflow.Jobs {
			jobs[workflow.ID+"/"+job.ID] = job
		}
	}

	type groupKey struct {
		run        int64
		coordinate string
	}

	first := map[groupKey]string{}

	runs := append([]GitHubRun(nil), snapshot.Runs...)
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].ID == runs[j].ID {
			return runs[i].Attempt < runs[j].Attempt
		}

		return runs[i].ID < runs[j].ID
	})

	evidence := Evidence{
		SchemaVersion: EvidenceSchemaVersion, InventoryDigest: inventory.Digest, Repository: snapshot.Repository,
		CollectedAt: snapshot.CollectedAt, RequestedSince: snapshot.RequestedSince,
		RequestedUntil: snapshot.RequestedUntil, Complete: true,
		ExpectedWorkflowRuns: snapshot.ExpectedWorkflowRuns,
		ExpectedRunAttempts:  snapshot.ExpectedRunAttempts,
		ExpectedCaches:       snapshot.ExpectedCaches,
		Caches:               append([]Cache(nil), snapshot.Caches...),
	}
	resources := map[int64]RunResourceEvidence{}
	artifactIDs := map[int64]bool{}
	logicalRuns := map[int64]bool{}

	for _, run := range runs {
		if run.ID == 0 || run.Attempt < 1 || run.WorkflowID == "" || run.WorkflowPath == "" ||
			normalizeWorkflowPathReference(run.WorkflowPath) != run.WorkflowPath ||
			workflowFilePath(run.WorkflowPath) != workflowPaths[run.WorkflowID] ||
			strings.TrimSpace(run.Event) == "" || strings.TrimSpace(run.HeadSHA) == "" ||
			run.PullRequest < 0 || run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() ||
			run.UpdatedAt.Before(run.CreatedAt) ||
			(!run.StartedAt.IsZero() &&
				(run.StartedAt.Before(run.CreatedAt) || run.StartedAt.After(run.UpdatedAt))) ||
			run.ExpectedJobs < 0 || run.ExpectedArtifacts < 0 {
			return Evidence{}, fmt.Errorf("malformed run %d", run.ID)
		}

		logicalRuns[run.ID] = true

		if run.Attempt == 1 && run.ExpectedArtifacts != len(run.Artifacts) {
			return Evidence{}, fmt.Errorf(
				"run %d artifact count mismatch: expected %d got %d",
				run.ID, run.ExpectedArtifacts, len(run.Artifacts),
			)
		}

		if run.Attempt > 1 && len(run.Artifacts) != 0 {
			return Evidence{}, fmt.Errorf("run %d attempt %d repeats run-scoped artifacts", run.ID, run.Attempt)
		}

		if run.Attempt > 1 && run.ExpectedArtifacts != 0 {
			return Evidence{}, fmt.Errorf("run %d attempt %d repeats the artifact count", run.ID, run.Attempt)
		}

		if run.Attempt > 1 && !emptyCostInput(run.Cost) {
			return Evidence{}, fmt.Errorf("run %d attempt %d repeats run-scoped cost input", run.ID, run.Attempt)
		}

		if err := validateArtifacts(run.Artifacts, artifactIDs); err != nil {
			return Evidence{}, fmt.Errorf("run %d: %w", run.ID, err)
		}

		runOutcome, err := normalizeOutcome(run.Status, run.Conclusion)
		if err != nil || run.Status != "completed" {
			return Evidence{}, fmt.Errorf("run %d: unsupported status/conclusion %q/%q", run.ID, run.Status, run.Conclusion)
		}

		runAttempt := RunAttemptEvidence{
			RunID: run.ID, Attempt: run.Attempt, WorkflowID: run.WorkflowID, WorkflowPath: run.WorkflowPath,
			Event: run.Event, HeadSHA: run.HeadSHA, HeadBranch: run.HeadBranch,
			PullRequest: run.PullRequest, Outcome: runOutcome, Conclusion: run.Conclusion,
			ExpectedJobs: run.ExpectedJobs, CreatedAt: run.CreatedAt,
			StartedAt: run.StartedAt, UpdatedAt: run.UpdatedAt,
		}
		if runOutcome == "cancelled" && run.SupersededBy != 0 {
			runAttempt.SupersededBy = run.SupersededBy
			runAttempt.RunDisposition = "superseded"
			runAttempt.SupersessionBasis = run.SupersessionBasis
		}

		evidence.RunAttempts = append(evidence.RunAttempts, runAttempt)

		runJobIDs := map[int64]bool{}

		for _, job := range run.Jobs {
			inventoryJob, known := jobs[run.WorkflowID+"/"+job.Key]
			if job.Key == "" || !known {
				return Evidence{}, fmt.Errorf("unknown job coordinate %s/%s", run.WorkflowID, job.Key)
			}

			if job.ID <= 0 || job.Attempt < 1 || job.Attempt != run.Attempt {
				return Evidence{}, fmt.Errorf("job %d attempt %d does not match run attempt %d", job.ID, job.Attempt, run.Attempt)
			}

			runJobIDs[job.ID] = true

			outcome, err := normalizeOutcome(job.Status, job.Conclusion)
			if err != nil {
				return Evidence{}, fmt.Errorf("job %d: %w", job.ID, err)
			}

			if job.Status != "completed" {
				return Evidence{}, fmt.Errorf("job %d is not completed (status %q)", job.ID, job.Status)
			}

			var queueMillis, executionMillis int64

			if outcome != "skipped" {
				if job.StartedAt.IsZero() || job.CompletedAt.IsZero() ||
					job.StartedAt.Before(job.CreatedAt) || job.CompletedAt.Before(job.StartedAt) {
					return Evidence{}, fmt.Errorf("job %d has invalid timestamps", job.ID)
				}

				queueMillis = job.StartedAt.Sub(job.CreatedAt).Milliseconds()
				executionMillis = job.CompletedAt.Sub(job.StartedAt).Milliseconds()
			}

			coordinate := job.Coordinate
			if coordinate == "" && len(inventoryJob.Coordinates) == 1 {
				coordinate = inventoryJob.Coordinates[0]
			}

			if !contains(inventoryJob.Coordinates, coordinate) {
				return Evidence{}, fmt.Errorf("unknown job coordinate %q for %s/%s", coordinate, run.WorkflowID, job.Key)
			}

			key := groupKey{run.ID, coordinate}
			if _, ok := first[key]; !ok {
				first[key] = outcome
			}

			evidence.Observations = append(evidence.Observations, RunEvidence{
				RunID: run.ID, JobID: job.ID, Attempt: job.Attempt, Retry: job.Attempt > 1,
				RecoveredAfterRetry: job.Attempt > 1 && first[key] == "failed" && outcome == "passed",
				WorkflowID:          run.WorkflowID, Event: run.Event,
				HeadSHA: run.HeadSHA, HeadBranch: run.HeadBranch, PullRequest: run.PullRequest,
				Coordinate: coordinate, Outcome: outcome, JobStatus: job.Status,
				JobConclusion: job.Conclusion, FirstOutcome: first[key],
				QueueMillis: queueMillis, ExecutionMillis: executionMillis,
				RunCreatedAt: run.CreatedAt, CreatedAt: job.CreatedAt,
				StartedAt: job.StartedAt, CompletedAt: job.CompletedAt,
				RunnerName: job.RunnerName, RunnerGroup: job.RunnerGroup, RunnerLabels: job.Labels,
				SyntheticFanout: job.SyntheticFanout,
			})

			if runAttempt.RunDisposition == "superseded" {
				index := len(evidence.Observations) - 1
				evidence.Observations[index].SupersededBy = runAttempt.SupersededBy
				evidence.Observations[index].RunDisposition = runAttempt.RunDisposition
				evidence.Observations[index].SupersessionBasis = runAttempt.SupersessionBasis
			}
		}

		if len(runJobIDs) != run.ExpectedJobs {
			return Evidence{}, fmt.Errorf(
				"run %d attempt %d job count mismatch: expected %d got %d",
				run.ID, run.Attempt, run.ExpectedJobs, len(runJobIDs),
			)
		}

		if run.ExpectedJobs == 0 && !oneOf(run.Conclusion, "startup_failure", "cancelled") {
			return Evidence{}, fmt.Errorf("run %d attempt %d has ineligible zero-job conclusion %q", run.ID, run.Attempt, run.Conclusion)
		}

		if run.Attempt == 1 {
			resources[run.ID] = RunResourceEvidence{
				RunID: run.ID, ExpectedArtifacts: run.ExpectedArtifacts,
				Artifacts: append([]Artifact(nil), run.Artifacts...), Cost: run.Cost,
			}
		}
	}

	if len(logicalRuns) != snapshot.ExpectedWorkflowRuns {
		return Evidence{}, fmt.Errorf(
			"workflow run count mismatch: expected %d got %d",
			snapshot.ExpectedWorkflowRuns, len(logicalRuns),
		)
	}

	sort.Slice(evidence.Observations, func(i, j int) bool {
		a, b := evidence.Observations[i], evidence.Observations[j]
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}

		if a.Coordinate != b.Coordinate {
			return a.Coordinate < b.Coordinate
		}

		return a.Attempt < b.Attempt
	})
	sort.Slice(evidence.RunAttempts, func(i, j int) bool {
		if evidence.RunAttempts[i].RunID != evidence.RunAttempts[j].RunID {
			return evidence.RunAttempts[i].RunID < evidence.RunAttempts[j].RunID
		}

		return evidence.RunAttempts[i].Attempt < evidence.RunAttempts[j].Attempt
	})
	sort.Slice(evidence.Caches, func(i, j int) bool { return evidence.Caches[i].ID < evidence.Caches[j].ID })

	for _, resource := range resources {
		sort.Slice(resource.Artifacts, func(i, j int) bool { return resource.Artifacts[i].ID < resource.Artifacts[j].ID })
		evidence.RunResources = append(evidence.RunResources, resource)
	}

	sort.Slice(evidence.RunResources, func(i, j int) bool { return evidence.RunResources[i].RunID < evidence.RunResources[j].RunID })
	evidence.Digest = ""

	data, err := json.Marshal(evidence)
	if err != nil {
		return Evidence{}, err
	}

	evidence.Digest = sum(data)

	if err := evidence.Replay(inventory); err != nil {
		return Evidence{}, fmt.Errorf("validate collected evidence: %w", err)
	}

	return evidence, nil
}

func (e Evidence) Replay(inventory Inventory) error {
	if e.SchemaVersion != EvidenceSchemaVersion {
		return fmt.Errorf("unsupported evidence schema %d", e.SchemaVersion)
	}

	if err := inventory.Validate(); err != nil {
		return fmt.Errorf("validate inventory for replay: %w", err)
	}

	if e.InventoryDigest == "" || e.InventoryDigest != inventory.Digest {
		return fmt.Errorf("evidence inventory digest mismatch: got %s want %s", e.InventoryDigest, inventory.Digest)
	}

	digestEvidence := e
	digestEvidence.Digest = ""

	data, err := json.Marshal(digestEvidence)
	if err != nil {
		return err
	}

	if sum(data) != e.Digest {
		return errors.New("evidence digest mismatch")
	}

	if e.Repository == "" || e.CollectedAt.IsZero() || e.RequestedSince.IsZero() ||
		e.RequestedUntil.IsZero() || !e.RequestedUntil.After(e.RequestedSince) || !e.Complete {
		return errors.New("evidence has incomplete identity or query window")
	}

	coordinates := map[string]bool{}
	workflows := map[string]bool{}
	workflowPaths := map[string]string{}
	coordinateJobs := map[string]Job{}

	for _, workflow := range inventory.Workflows {
		workflows[workflow.ID] = true
		workflowPaths[workflow.ID] = workflow.Path

		for _, job := range workflow.Jobs {
			for _, coordinate := range job.Coordinates {
				coordinates[workflow.ID+"/"+coordinate] = true
				coordinateJobs[workflow.ID+"/"+coordinate] = job
			}
		}
	}

	if e.ExpectedWorkflowRuns < 0 || e.ExpectedRunAttempts < 0 || e.ExpectedCaches < 0 ||
		e.ExpectedCaches != len(e.Caches) {
		return errors.New("evidence has invalid authoritative counts")
	}

	seen := map[string]bool{}
	runIDs := map[int64]bool{}
	runAttempts := map[string]RunAttemptEvidence{}
	runAttemptsByRun := map[int64][]RunAttemptEvidence{}
	observationGroups := map[string][]RunEvidence{}
	observationCountByAttempt := map[string]int{}
	jobObservations := map[string][]RunEvidence{}
	jobIDLocations := map[int64]string{}

	for _, runAttempt := range e.RunAttempts {
		key := fmt.Sprintf("%d/%d", runAttempt.RunID, runAttempt.Attempt)

		normalizedOutcome, outcomeErr := normalizeOutcome("completed", runAttempt.Conclusion)
		if runAttempt.RunID <= 0 || runAttempt.Attempt < 1 || runAttempt.WorkflowID == "" || runAttempt.WorkflowPath == "" ||
			normalizeWorkflowPathReference(runAttempt.WorkflowPath) != runAttempt.WorkflowPath ||
			workflowFilePath(runAttempt.WorkflowPath) != workflowPaths[runAttempt.WorkflowID] ||
			!workflows[runAttempt.WorkflowID] || runAttempt.HeadSHA == "" ||
			strings.TrimSpace(runAttempt.Event) == "" || runAttempt.PullRequest < 0 ||
			runAttempt.CreatedAt.IsZero() || runAttempt.UpdatedAt.IsZero() ||
			runAttempt.UpdatedAt.Before(runAttempt.CreatedAt) ||
			(!runAttempt.StartedAt.IsZero() &&
				(runAttempt.StartedAt.Before(runAttempt.CreatedAt) || runAttempt.StartedAt.After(runAttempt.UpdatedAt))) ||
			runAttempt.ExpectedJobs < 0 || outcomeErr != nil || normalizedOutcome != runAttempt.Outcome {
			return fmt.Errorf("invalid run attempt identity %s", key)
		}

		if _, exists := runAttempts[key]; exists {
			return fmt.Errorf("duplicate run attempt %s", key)
		}

		if runAttempt.RunDisposition == "superseded" {
			if runAttempt.Outcome != "cancelled" || runAttempt.SupersededBy == 0 || runAttempt.SupersessionBasis == "" {
				return fmt.Errorf("inconsistent run supersession metadata for %s", key)
			}
		} else if runAttempt.SupersededBy != 0 || runAttempt.SupersessionBasis != "" {
			return fmt.Errorf("unlabelled run supersession metadata for %s", key)
		}

		runAttempts[key] = runAttempt
		runAttemptsByRun[runAttempt.RunID] = append(runAttemptsByRun[runAttempt.RunID], runAttempt)
		runIDs[runAttempt.RunID] = true
	}

	for _, observation := range e.Observations {
		if !oneOf(observation.Outcome, "passed", "failed", "cancelled", "skipped", "neutral") ||
			!oneOf(observation.FirstOutcome, "passed", "failed", "cancelled", "skipped", "neutral") {
			return fmt.Errorf("invalid outcome for %s", observation.Coordinate)
		}

		normalizedOutcome, outcomeErr := normalizeOutcome(observation.JobStatus, observation.JobConclusion)
		if observation.RunID <= 0 || observation.JobID <= 0 || observation.Attempt < 1 || observation.WorkflowID == "" ||
			observation.HeadSHA == "" || observation.RunCreatedAt.IsZero() ||
			!coordinates[observation.WorkflowID+"/"+observation.Coordinate] ||
			observation.JobStatus != "completed" || outcomeErr != nil || normalizedOutcome != observation.Outcome {
			return fmt.Errorf("invalid observation identity for %s", observation.Coordinate)
		}

		key := fmt.Sprintf("%d/%d/%s", observation.RunID, observation.Attempt, observation.Coordinate)
		if seen[key] {
			return fmt.Errorf("duplicate evidence observation %s", key)
		}

		seen[key] = true

		runAttempt, exists := runAttempts[fmt.Sprintf("%d/%d", observation.RunID, observation.Attempt)]
		if !exists || observation.WorkflowID != runAttempt.WorkflowID || observation.Event != runAttempt.Event ||
			observation.HeadSHA != runAttempt.HeadSHA || observation.HeadBranch != runAttempt.HeadBranch ||
			observation.PullRequest != runAttempt.PullRequest || !observation.RunCreatedAt.Equal(runAttempt.CreatedAt) {
			return fmt.Errorf("observation %s does not match its run attempt", key)
		}

		groupKey := fmt.Sprintf("%d/%s", observation.RunID, observation.Coordinate)
		observationGroups[groupKey] = append(observationGroups[groupKey], observation)
		attemptKey := fmt.Sprintf("%d/%d", observation.RunID, observation.Attempt)
		observationCountByAttempt[attemptKey]++
		jobKey := fmt.Sprintf("%s/%d", attemptKey, observation.JobID)
		jobObservations[jobKey] = append(jobObservations[jobKey], observation)

		if location, exists := jobIDLocations[observation.JobID]; exists && location != jobKey {
			return fmt.Errorf("job ID %d is reused outside one raw row", observation.JobID)
		}

		jobIDLocations[observation.JobID] = jobKey

		if observation.Attempt == 1 && observation.Outcome == "passed" && observation.FirstOutcome == "skipped" {
			return fmt.Errorf("skipped result represented as passed for %s", observation.Coordinate)
		}

		invalidTimestamps := observation.QueueMillis != 0 || observation.ExecutionMillis != 0
		if observation.Outcome != "skipped" {
			invalidTimestamps = observation.CreatedAt.IsZero() || observation.StartedAt.IsZero() ||
				observation.CompletedAt.IsZero() ||
				observation.StartedAt.Before(observation.CreatedAt) ||
				observation.CompletedAt.Before(observation.StartedAt) ||
				observation.QueueMillis != observation.StartedAt.Sub(observation.CreatedAt).Milliseconds() ||
				observation.ExecutionMillis != observation.CompletedAt.Sub(observation.StartedAt).Milliseconds()
		}

		if observation.QueueMillis < 0 || observation.ExecutionMillis < 0 || invalidTimestamps {
			return fmt.Errorf("inconsistent duration for %s", observation.Coordinate)
		}

		recovered := observation.Retry && observation.Outcome == "passed" && observation.FirstOutcome == "failed"
		if observation.Retry != (observation.Attempt > 1) || observation.RecoveredAfterRetry != recovered {
			return fmt.Errorf("inconsistent retry metadata for %s", observation.Coordinate)
		}

		if observation.RunDisposition != runAttempt.RunDisposition ||
			observation.SupersededBy != runAttempt.SupersededBy ||
			observation.SupersessionBasis != runAttempt.SupersessionBasis {
			return fmt.Errorf("observation supersession does not match run attempt for %s", observation.Coordinate)
		}
	}

	for groupKey, group := range observationGroups {
		sort.Slice(group, func(i, j int) bool { return group[i].Attempt < group[j].Attempt })

		for attempt := 1; attempt < group[0].Attempt; attempt++ {
			attemptKey := fmt.Sprintf("%d/%d", group[0].RunID, attempt)
			if _, exists := runAttempts[attemptKey]; !exists || observationCountByAttempt[attemptKey] != 0 {
				return fmt.Errorf("missing zero-job prior attempt for %s", groupKey)
			}
		}

		firstOutcome := group[0].Outcome
		for _, observation := range group {
			if observation.FirstOutcome != firstOutcome {
				return fmt.Errorf("inconsistent first outcome for %s", groupKey)
			}
		}
	}

	jobCountsByAttempt := map[string]int{}

	for jobKey, group := range jobObservations {
		firstObservation := group[0]
		attemptKey := fmt.Sprintf("%d/%d", firstObservation.RunID, firstObservation.Attempt)
		jobCountsByAttempt[attemptKey]++

		if len(group) == 1 && !firstObservation.SyntheticFanout {
			continue
		}

		inventoryJob := coordinateJobs[firstObservation.WorkflowID+"/"+firstObservation.Coordinate]
		seenCoordinates := map[string]bool{}

		for _, observation := range group {
			if observation.WorkflowID != firstObservation.WorkflowID ||
				!observation.SyntheticFanout ||
				observation.JobStatus != "completed" ||
				!oneOf(observation.JobConclusion, "cancelled", "skipped") ||
				!oneOf(observation.Outcome, "cancelled", "skipped") {
				return fmt.Errorf("job %s has invalid synthetic matrix fan-out", jobKey)
			}

			job := coordinateJobs[observation.WorkflowID+"/"+observation.Coordinate]
			if job.ID != inventoryJob.ID || seenCoordinates[observation.Coordinate] {
				return fmt.Errorf("job %s has invalid synthetic matrix coordinate reuse", jobKey)
			}

			seenCoordinates[observation.Coordinate] = true
		}

		if len(seenCoordinates) != len(inventoryJob.Coordinates) {
			return fmt.Errorf("job %s has incomplete synthetic matrix fan-out", jobKey)
		}

		for _, coordinate := range inventoryJob.Coordinates {
			if !seenCoordinates[coordinate] {
				return fmt.Errorf("job %s has incomplete synthetic matrix fan-out", jobKey)
			}
		}
	}

	for runID, attempts := range runAttemptsByRun {
		sort.Slice(attempts, func(i, j int) bool { return attempts[i].Attempt < attempts[j].Attempt })

		for index, runAttempt := range attempts {
			if runAttempt.Attempt != index+1 {
				return fmt.Errorf("non-contiguous run attempts for run %d", runID)
			}

			if index > 0 && !sameRunIdentity(attempts[0], runAttempt) {
				return fmt.Errorf("run %d changes identity across attempts", runID)
			}

			attemptKey := fmt.Sprintf("%d/%d", runID, runAttempt.Attempt)
			if jobCountsByAttempt[attemptKey] != runAttempt.ExpectedJobs {
				return fmt.Errorf(
					"run %d attempt %d job count mismatch: expected %d got %d",
					runID, runAttempt.Attempt, runAttempt.ExpectedJobs, jobCountsByAttempt[attemptKey],
				)
			}

			if runAttempt.ExpectedJobs == 0 && !oneOf(runAttempt.Conclusion, "startup_failure", "cancelled") {
				return fmt.Errorf("run attempt %s has ineligible zero-job conclusion", attemptKey)
			}

			if runAttempt.RunDisposition != "superseded" {
				continue
			}

			if !validSupersessionTarget(runAttempt, runAttemptsByRun[runAttempt.SupersededBy]) {
				return fmt.Errorf("unproven supersession target %d for run %d", runAttempt.SupersededBy, runID)
			}
		}
	}

	if e.ExpectedRunAttempts != len(runAttempts) || e.ExpectedWorkflowRuns != len(runAttemptsByRun) {
		return fmt.Errorf(
			"evidence run count mismatch: expected %d workflows/%d attempts, got %d/%d",
			e.ExpectedWorkflowRuns, e.ExpectedRunAttempts, len(runAttemptsByRun), len(runAttempts),
		)
	}

	resourceRuns := map[int64]bool{}
	artifactIDs := map[int64]bool{}

	for _, resource := range e.RunResources {
		if resource.RunID == 0 || resourceRuns[resource.RunID] {
			return fmt.Errorf("invalid or duplicate run resource %d", resource.RunID)
		}

		resourceRuns[resource.RunID] = true

		if !runIDs[resource.RunID] {
			return fmt.Errorf("run resource %d has no run attempt", resource.RunID)
		}

		if resource.ExpectedArtifacts < 0 || resource.ExpectedArtifacts != len(resource.Artifacts) {
			return fmt.Errorf("run %d artifact count mismatch", resource.RunID)
		}

		if err := validateArtifacts(resource.Artifacts, artifactIDs); err != nil {
			return fmt.Errorf("run %d: %w", resource.RunID, err)
		}

		if err := validateCost(resource.Cost); err != nil {
			return fmt.Errorf("run %d: %w", resource.RunID, err)
		}
	}

	if len(resourceRuns) != len(runIDs) {
		return fmt.Errorf("run resource count mismatch: got %d resources for %d runs", len(resourceRuns), len(runIDs))
	}

	if err := validateCaches(e.Caches); err != nil {
		return err
	}

	return nil
}

func sameRunIdentity(first, next RunAttemptEvidence) bool {
	return first.WorkflowID == next.WorkflowID &&
		first.WorkflowPath == next.WorkflowPath &&
		first.Event == next.Event &&
		first.HeadSHA == next.HeadSHA &&
		first.HeadBranch == next.HeadBranch &&
		first.PullRequest == next.PullRequest
}

func validateArtifacts(artifacts []Artifact, seen map[int64]bool) error {
	if seen == nil {
		seen = map[int64]bool{}
	}

	for _, artifact := range artifacts {
		if artifact.ID <= 0 || strings.TrimSpace(artifact.Name) == "" || artifact.SizeBytes < 0 ||
			(artifact.Digest != "" && !artifactDigestPattern.MatchString(artifact.Digest)) {
			return fmt.Errorf("invalid artifact %d", artifact.ID)
		}

		if invalidOptionalTime(artifact.CreatedAt) || invalidOptionalTime(artifact.UpdatedAt) ||
			invalidOptionalTime(artifact.ExpiresAt) {
			return fmt.Errorf("invalid artifact %d timestamps", artifact.ID)
		}

		if artifact.CreatedAt != nil && artifact.UpdatedAt != nil &&
			artifact.UpdatedAt.Before(*artifact.CreatedAt) {
			return fmt.Errorf("invalid artifact %d timestamps", artifact.ID)
		}

		if artifact.CreatedAt != nil && artifact.ExpiresAt != nil &&
			!artifact.ExpiresAt.After(*artifact.CreatedAt) {
			return fmt.Errorf("invalid artifact %d timestamps", artifact.ID)
		}

		if artifact.UpdatedAt != nil && artifact.ExpiresAt != nil &&
			!artifact.ExpiresAt.After(*artifact.UpdatedAt) {
			return fmt.Errorf("invalid artifact %d timestamps", artifact.ID)
		}

		if seen[artifact.ID] {
			return fmt.Errorf("duplicate artifact %d", artifact.ID)
		}

		seen[artifact.ID] = true
	}

	return nil
}

func invalidOptionalTime(value *time.Time) bool {
	return value != nil && value.IsZero()
}

func validateCaches(caches []Cache) error {
	seen := map[int64]bool{}

	for _, cache := range caches {
		if cache.ID <= 0 || strings.TrimSpace(cache.Key) == "" || strings.TrimSpace(cache.Ref) == "" ||
			cache.SizeBytes < 0 || cache.CreatedAt.IsZero() || cache.LastAccessedAt.IsZero() ||
			cache.LastAccessedAt.Before(cache.CreatedAt) {
			return fmt.Errorf("invalid cache %d", cache.ID)
		}

		if seen[cache.ID] {
			return fmt.Errorf("duplicate cache %d", cache.ID)
		}

		seen[cache.ID] = true
	}

	return nil
}

func validateCost(cost CostInput) error {
	if cost.Available {
		if strings.TrimSpace(cost.Source) == "" || cost.UnavailableReason != "" {
			return errors.New("available cost has inconsistent source or unavailable reason")
		}
	} else if strings.TrimSpace(cost.UnavailableReason) == "" || len(cost.BillableMinutes) != 0 {
		return errors.New("unavailable cost has no reason or includes billable inputs")
	}

	seenLabels := map[string]bool{}
	for _, label := range cost.RunnerLabels {
		if strings.TrimSpace(label) == "" || seenLabels[label] {
			return errors.New("cost has invalid runner labels")
		}

		seenLabels[label] = true
	}

	for platform, minutes := range cost.BillableMinutes {
		if strings.TrimSpace(platform) == "" || minutes < 0 {
			return errors.New("cost has invalid billable input")
		}
	}

	return nil
}

func emptyCostInput(cost CostInput) bool {
	return !cost.Available && cost.Source == "" && cost.UnavailableReason == "" &&
		len(cost.BillableMinutes) == 0 && len(cost.RunnerLabels) == 0
}

func validSupersessionTarget(source RunAttemptEvidence, targets []RunAttemptEvidence) bool {
	if source.SupersessionBasis != "inferred-later-run-same-workflow-event-and-change-coordinate" {
		return false
	}

	for _, target := range targets {
		if target.WorkflowID != source.WorkflowID || target.Event != source.Event ||
			!target.CreatedAt.After(source.CreatedAt) {
			continue
		}

		if source.PullRequest != 0 && source.PullRequest == target.PullRequest {
			return true
		}

		if source.PullRequest == 0 && source.HeadBranch != "" && source.HeadBranch == target.HeadBranch {
			return true
		}
	}

	return false
}

func normalizeOutcome(status, conclusion string) (string, error) {
	switch conclusion {
	case "success":
		return "passed", nil
	case "failure", "timed_out", "action_required", "startup_failure":
		return "failed", nil
	case "cancelled":
		return "cancelled", nil
	case "skipped":
		return "skipped", nil
	case "neutral":
		return "neutral", nil
	}

	return "", fmt.Errorf("unsupported status/conclusion %q/%q", status, conclusion)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}

	return false
}
