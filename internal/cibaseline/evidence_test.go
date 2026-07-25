package cibaseline

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOfflineCollectAndReplay(t *testing.T) {
	data, err := os.ReadFile("testdata/braw-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}

	var snapshot GitHubSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}

	evidence, err := Collect(snapshot, loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	inventory := loadInventory(t)
	if err := evidence.Replay(inventory); err != nil {
		t.Fatal(err)
	}

	if len(evidence.Observations) != 6 {
		t.Fatalf("observations = %d, want 6", len(evidence.Observations))
	}

	first, retry := evidence.Observations[0], evidence.Observations[1]
	if first.Outcome != "failed" || retry.Outcome != "passed" || retry.FirstOutcome != "failed" {
		t.Fatalf("attempt outcomes = %#v, %#v; want failure then pass retaining failure", first, retry)
	}

	if first.QueueMillis != 30_000 || first.ExecutionMillis != 90_000 {
		t.Fatalf("queue/execution = %d/%d, want 30000/90000", first.QueueMillis, first.ExecutionMillis)
	}

	outcomes := map[string]bool{}
	for _, observation := range evidence.Observations {
		outcomes[observation.Outcome] = true
		if observation.Outcome == "skipped" {
			if !observation.CompletedAt.Before(observation.StartedAt) ||
				observation.QueueMillis != 0 || observation.ExecutionMillis != 0 {
				t.Fatalf("skipped non-monotonic timestamps were not retained with zero durations: %#v", observation)
			}
		}
	}

	for _, want := range []string{"cancelled", "skipped"} {
		if !outcomes[want] {
			t.Errorf("missing %s outcome", want)
		}
	}

	invalidSkippedDuration := cloneEvidence(t, evidence)
	for index := range invalidSkippedDuration.Observations {
		if invalidSkippedDuration.Observations[index].Outcome == "skipped" {
			invalidSkippedDuration.Observations[index].ExecutionMillis = 1

			break
		}
	}

	resignEvidence(t, &invalidSkippedDuration)

	if err := invalidSkippedDuration.Replay(inventory); err == nil ||
		!strings.Contains(err.Error(), "inconsistent duration") {
		t.Fatalf("Replay(skipped nonzero duration) error = %v, want rejection", err)
	}

	if len(evidence.RunResources) == 0 || len(evidence.RunResources[0].Artifacts) != 1 ||
		evidence.RunResources[0].Artifacts[0].Digest == "" || len(evidence.Caches) != 1 {
		t.Fatalf("run resources/cache observations not retained: %#v", evidence)
	}

	if len(evidence.RunResources) != 5 || evidence.RunResources[0].RunID != 7001 ||
		evidence.RunResources[0].Cost.BillableMinutes["UBUNTU"] != 2 {
		t.Fatalf("run-scoped resources duplicated or misattributed: %#v", evidence.RunResources)
	}

	if evidence.Observations[4].Outcome != "cancelled" || evidence.Observations[4].RunDisposition != "superseded" {
		t.Fatalf("raw cancellation/supersession not retained separately: %#v", evidence.Observations[4])
	}

	failedBeforeSupersession := evidence
	failedBeforeSupersession.Observations = append([]RunEvidence(nil), evidence.Observations...)
	failedBeforeSupersession.Observations[4].Outcome = "failed"
	failedBeforeSupersession.Observations[4].JobConclusion = "failure"
	failedBeforeSupersession.Observations[4].FirstOutcome = "failed"
	resignEvidence(t, &failedBeforeSupersession)

	if err := failedBeforeSupersession.Replay(inventory); err != nil {
		t.Fatalf("Replay() erased a failure before supersession: %v", err)
	}

	second, err := Collect(snapshot, loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if evidence.Digest != second.Digest {
		t.Fatalf("non-deterministic evidence digest: %s != %s", evidence.Digest, second.Digest)
	}
}

func TestReplayRejectsSkippedAsPassed(t *testing.T) {
	inventory := loadInventory(t)
	evidence := Evidence{
		SchemaVersion:        EvidenceSchemaVersion,
		InventoryDigest:      inventory.Digest,
		Repository:           "d0ugal/graith",
		CollectedAt:          time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		RequestedSince:       time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		RequestedUntil:       time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		Complete:             true,
		ExpectedWorkflowRuns: 1,
		ExpectedRunAttempts:  1,
		RunAttempts: []RunAttemptEvidence{{
			RunID: 1, Attempt: 1, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
			Event: "push", HeadSHA: "braw", Outcome: "passed", Conclusion: "success", ExpectedJobs: 1,
			CreatedAt: time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 7, 25, 11, 1, 0, 0, time.UTC),
		}},
		Observations: []RunEvidence{{
			RunID: 1, JobID: 2, Attempt: 1, WorkflowID: "ci", Event: "push", HeadSHA: "braw",
			Coordinate: "ci/lint", Outcome: "passed", FirstOutcome: "skipped",
			JobStatus: "completed", JobConclusion: "success",
			RunCreatedAt: time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC),
			CreatedAt:    time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC),
			StartedAt:    time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC),
			CompletedAt:  time.Date(2026, 7, 25, 11, 1, 0, 0, time.UTC),
		}},
	}

	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}

	evidence.Digest = sum(data)
	if err := evidence.Replay(inventory); err == nil || !strings.Contains(err.Error(), "skipped result represented as passed") {
		t.Fatalf("Replay() error = %v, want skipped-as-passed rejection", err)
	}
}

func TestCollectRejectsMalformedGitHubData(t *testing.T) {
	inventory := loadInventory(t)

	base := GitHubSnapshot{
		SchemaVersion:  SnapshotSchemaVersion,
		Repository:     "d0ugal/graith",
		CollectedAt:    time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		RequestedSince: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		RequestedUntil: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		Complete:       true, ExpectedWorkflowRuns: 1, ExpectedRunAttempts: 1,
		Runs: []GitHubRun{{
			ID: 1, Attempt: 1, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
			Event: "push", HeadSHA: "braw", ExpectedJobs: 1,
			CreatedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 7, 25, 12, 1, 0, 0, time.UTC),
			Status:    "completed", Conclusion: "success",
			Jobs: []GitHubJob{{
				ID: 2, Key: "test", Attempt: 1, Status: "completed", Conclusion: "success",
				CreatedAt:   time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
				StartedAt:   time.Date(2026, 7, 25, 11, 59, 0, 0, time.UTC),
				CompletedAt: time.Date(2026, 7, 25, 12, 1, 0, 0, time.UTC),
			}},
		}},
	}
	if _, err := Collect(base, inventory); err == nil || !strings.Contains(err.Error(), "invalid timestamps") {
		t.Fatalf("Collect() error = %v, want timestamp rejection", err)
	}

	base.Runs[0].Jobs[0].StartedAt = base.Runs[0].Jobs[0].CreatedAt

	base.Runs[0].Jobs[0].Attempt = 2
	if _, err := Collect(base, inventory); err == nil || !strings.Contains(err.Error(), "does not match run attempt") {
		t.Fatalf("Collect() error = %v, want attempt mismatch rejection", err)
	}

	base.Runs[0].Jobs[0].Attempt = 1

	base.Runs[0].Jobs[0].Key = "thrawn"
	if _, err := Collect(base, inventory); err == nil || !strings.Contains(err.Error(), "unknown job coordinate") {
		t.Fatalf("Collect() error = %v, want coordinate rejection", err)
	}
}

func TestCollectRejectsIncompleteSnapshotAndUnknownMatrixCoordinate(t *testing.T) {
	inventory := loadInventory(t)

	snapshot := GitHubSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Repository:    "d0ugal/graith", CollectedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		RequestedSince:       time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		RequestedUntil:       time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		ExpectedWorkflowRuns: 1,
		ExpectedRunAttempts:  1,
	}
	if _, err := Collect(snapshot, inventory); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Collect() error = %v, want incomplete rejection", err)
	}

	snapshot.Complete = true

	snapshot.Runs = []GitHubRun{{
		ID: 1, Attempt: 1, WorkflowID: "dev-release", WorkflowPath: ".github/workflows/dev-release.yml",
		Event: "push", HeadSHA: "braw", ExpectedJobs: 1,
		CreatedAt: snapshot.RequestedSince,
		UpdatedAt: snapshot.RequestedSince,
		Status:    "completed", Conclusion: "success",
		Jobs: []GitHubJob{{
			ID: 2, Key: "build-linux", Coordinate: "dev-release/build-linux[goarch=thrawn,target=bothy]",
			Attempt: 1, Status: "completed", Conclusion: "success", CreatedAt: snapshot.RequestedSince,
			StartedAt: snapshot.RequestedSince, CompletedAt: snapshot.RequestedSince,
		}},
	}}
	if _, err := Collect(snapshot, inventory); err == nil || !strings.Contains(err.Error(), "unknown job coordinate") {
		t.Fatalf("Collect() error = %v, want matrix coordinate rejection", err)
	}
}

func TestCollectRetainsZeroJobRunAttempt(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	snapshot := GitHubSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Repository:    "d0ugal/graith", CollectedAt: now,
		RequestedSince: now.Add(-time.Hour), RequestedUntil: now,
		Complete: true, ExpectedWorkflowRuns: 2, ExpectedRunAttempts: 2,
		Runs: []GitHubRun{
			{
				ID: 1, Attempt: 1, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
				Event: "push", HeadSHA: "braw",
				CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute),
				Status: "completed", Conclusion: "startup_failure",
				Cost: CostInput{UnavailableReason: "offline fixture has no run usage"},
			},
			{
				ID: 2, Attempt: 1, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
				Event: "pull_request", HeadSHA: "canny",
				CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
				Status: "completed", Conclusion: "cancelled",
				Cost: CostInput{UnavailableReason: "offline fixture has no run usage"},
			},
		},
	}

	evidence, err := Collect(snapshot, loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(evidence.RunAttempts) != 2 || evidence.RunAttempts[0].Outcome != "failed" ||
		evidence.RunAttempts[1].Outcome != "cancelled" ||
		len(evidence.Observations) != 0 || len(evidence.RunResources) != 2 {
		t.Fatalf("zero-job evidence = %#v", evidence)
	}
}

func TestCollectAllowsSkippedJobToPassOnLaterAttempt(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	snapshot := GitHubSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Repository:    "d0ugal/graith", CollectedAt: now,
		RequestedSince: now.Add(-time.Hour), RequestedUntil: now,
		Complete: true, ExpectedWorkflowRuns: 1, ExpectedRunAttempts: 2,
		Runs: []GitHubRun{
			{
				ID: 1, Attempt: 1, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
				Event: "pull_request", HeadSHA: "canny",
				CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute),
				Status: "completed", Conclusion: "success",
				ExpectedJobs: 1,
				Jobs: []GitHubJob{{
					ID: 2, Key: "test-macos", Attempt: 1, Status: "completed", Conclusion: "skipped",
					CreatedAt: now.Add(-2 * time.Minute),
				}},
				Cost: CostInput{UnavailableReason: "offline fixture has no run usage"},
			},
			{
				ID: 1, Attempt: 2, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
				Event: "pull_request", HeadSHA: "canny",
				CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
				Status: "completed", Conclusion: "success",
				ExpectedJobs: 1,
				Jobs: []GitHubJob{{
					ID: 3, Key: "test-macos", Attempt: 2, Status: "completed", Conclusion: "success",
					CreatedAt: now.Add(-time.Minute), StartedAt: now.Add(-50 * time.Second),
					CompletedAt: now,
				}},
			},
		},
	}

	evidence, err := Collect(snapshot, loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(evidence.Observations) != 2 || evidence.Observations[1].Outcome != "passed" ||
		evidence.Observations[1].FirstOutcome != "skipped" {
		t.Fatalf("skipped then passed evidence = %#v", evidence.Observations)
	}
}

func TestCollectAllowsObservedJobAfterZeroJobAttempt(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	base := GitHubSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Repository:    "d0ugal/graith", CollectedAt: now,
		RequestedSince: now.Add(-time.Hour), RequestedUntil: now,
		Complete: true, ExpectedWorkflowRuns: 1, ExpectedRunAttempts: 2,
		Runs: []GitHubRun{
			{
				ID: 1, Attempt: 1, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
				Event: "pull_request", HeadSHA: "braw", HeadBranch: "canny",
				CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute),
				Status: "completed", Conclusion: "startup_failure",
				Cost: CostInput{UnavailableReason: "offline fixture has no run usage"},
			},
			{
				ID: 1, Attempt: 2, WorkflowID: "ci", WorkflowPath: ".github/workflows/ci.yml",
				Event: "pull_request", HeadSHA: "braw", HeadBranch: "canny",
				CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
				Status: "completed", Conclusion: "success",
				ExpectedJobs: 1,
				Jobs: []GitHubJob{{
					ID: 2, Key: "lint", Attempt: 2, Status: "completed", Conclusion: "success",
					CreatedAt: now.Add(-time.Minute), StartedAt: now.Add(-50 * time.Second), CompletedAt: now,
				}},
			},
		},
	}

	evidence, err := Collect(base, loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(evidence.Observations) != 1 || evidence.Observations[0].Attempt != 2 ||
		evidence.Observations[0].FirstOutcome != "passed" {
		t.Fatalf("zero-job retry evidence = %#v", evidence.Observations)
	}

	base.Runs[0].Jobs = []GitHubJob{{
		ID: 3, Key: "build", Attempt: 1, Status: "completed", Conclusion: "cancelled",
		CreatedAt: now.Add(-2 * time.Minute), StartedAt: now.Add(-110 * time.Second),
		CompletedAt: now.Add(-time.Minute),
	}}

	base.Runs[0].ExpectedJobs = 1
	if _, err := Collect(base, loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "missing zero-job prior attempt") {
		t.Fatalf("Collect() error = %v, want non-zero-job prior attempt rejection", err)
	}
}

func TestCollectAndReplayRejectMalformedResources(t *testing.T) {
	data, err := os.ReadFile("testdata/braw-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}

	var original GitHubSnapshot
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit func(*GitHubSnapshot)
		want string
	}{
		{"zero artifact id", func(s *GitHubSnapshot) { s.Runs[0].Artifacts[0].ID = 0 }, "invalid artifact"},
		{"blank artifact name", func(s *GitHubSnapshot) { s.Runs[0].Artifacts[0].Name = " " }, "invalid artifact"},
		{"negative artifact size", func(s *GitHubSnapshot) { s.Runs[0].Artifacts[0].SizeBytes = -1 }, "invalid artifact"},
		{"malformed artifact digest", func(s *GitHubSnapshot) {
			s.Runs[0].Artifacts[0].Digest = "sha256:dreich"
		}, "invalid artifact"},
		{"duplicate artifact", func(s *GitHubSnapshot) {
			s.Runs[0].Artifacts = append(s.Runs[0].Artifacts, s.Runs[0].Artifacts[0])
			s.Runs[0].ExpectedArtifacts++
		}, "duplicate artifact"},
		{"zero cache id", func(s *GitHubSnapshot) { s.Caches[0].ID = 0 }, "invalid cache"},
		{"blank cache key", func(s *GitHubSnapshot) { s.Caches[0].Key = " " }, "invalid cache"},
		{"blank cache ref", func(s *GitHubSnapshot) { s.Caches[0].Ref = "" }, "invalid cache"},
		{"negative cache size", func(s *GitHubSnapshot) { s.Caches[0].SizeBytes = -1 }, "invalid cache"},
		{"reversed cache timestamps", func(s *GitHubSnapshot) {
			s.Caches[0].LastAccessedAt = s.Caches[0].CreatedAt.Add(-time.Second)
		}, "invalid cache"},
		{"duplicate cache", func(s *GitHubSnapshot) {
			s.Caches = append(s.Caches, s.Caches[0])
			s.ExpectedCaches++
		}, "duplicate cache"},
	}

	inventory := loadInventory(t)

	valid, err := Collect(original, inventory)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var snapshot GitHubSnapshot
			if err := json.Unmarshal(data, &snapshot); err != nil {
				t.Fatal(err)
			}

			test.edit(&snapshot)

			if _, err := Collect(snapshot, inventory); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Collect() error = %v, want containing %q", err, test.want)
			}

			evidence := valid
			evidence.Caches = append([]Cache(nil), valid.Caches...)
			evidence.RunResources = append([]RunResourceEvidence(nil), valid.RunResources...)

			for index := range evidence.RunResources {
				evidence.RunResources[index].Artifacts =
					append([]Artifact(nil), valid.RunResources[index].Artifacts...)
			}

			switch {
			case strings.Contains(test.name, "artifact"):
				resourceSnapshot := GitHubSnapshot{Runs: []GitHubRun{{Artifacts: evidence.RunResources[0].Artifacts}}}
				test.edit(&resourceSnapshot)
				evidence.RunResources[0].Artifacts = resourceSnapshot.Runs[0].Artifacts
				evidence.RunResources[0].ExpectedArtifacts = len(evidence.RunResources[0].Artifacts)
			case strings.Contains(test.name, "cache"):
				resourceSnapshot := GitHubSnapshot{Caches: evidence.Caches}
				test.edit(&resourceSnapshot)
				evidence.Caches = resourceSnapshot.Caches
				evidence.ExpectedCaches = len(evidence.Caches)
			}

			resignEvidence(t, &evidence)

			if err := evidence.Replay(inventory); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Replay() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReplayRejectsDuplicateAndInconsistentObservations(t *testing.T) {
	data, err := os.ReadFile("testdata/braw-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}

	var snapshot GitHubSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}

	inventory := loadInventory(t)

	base, err := Collect(snapshot, inventory)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit func(*Evidence)
		want string
	}{
		{"duplicate", func(e *Evidence) {
			e.Observations = append(e.Observations, e.Observations[0])
		}, "duplicate evidence observation"},
		{"unknown coordinate", func(e *Evidence) {
			e.Observations[0].Coordinate = "croft/bothy"
		}, "invalid observation identity"},
		{"cross-workflow coordinate", func(e *Evidence) {
			e.Observations[0].WorkflowID = "goreleaser"
		}, "invalid observation identity"},
		{"duration contradiction", func(e *Evidence) {
			e.Observations[0].QueueMillis++
		}, "inconsistent duration"},
		{"missing timestamps", func(e *Evidence) {
			e.Observations[0].CreatedAt = time.Time{}
			e.Observations[0].StartedAt = time.Time{}
			e.Observations[0].CompletedAt = time.Time{}
			e.Observations[0].QueueMillis = 0
			e.Observations[0].ExecutionMillis = 0
		}, "inconsistent duration"},
		{"first outcome contradiction", func(e *Evidence) {
			e.Observations[1].FirstOutcome = "passed"
			e.Observations[1].RecoveredAfterRetry = false
		}, "inconsistent first outcome"},
		{"unproven supersession", func(e *Evidence) {
			e.Observations[4].SupersededBy = 9999
			for index := range e.RunAttempts {
				if e.RunAttempts[index].RunID == e.Observations[4].RunID {
					e.RunAttempts[index].SupersededBy = 9999
				}
			}
		}, "unproven supersession target"},
		{"retry contradiction", func(e *Evidence) {
			e.Observations[0].Retry = true
		}, "inconsistent retry metadata"},
		{"invalid outcome", func(e *Evidence) {
			e.Observations[0].Outcome = "blether"
		}, "invalid outcome"},
		{"cross-attempt identity", func(e *Evidence) {
			for index := range e.RunAttempts {
				if e.RunAttempts[index].RunID == 7001 && e.RunAttempts[index].Attempt == 2 {
					e.RunAttempts[index].HeadSHA = "dreich"
				}
			}

			for index := range e.Observations {
				if e.Observations[index].RunID == 7001 && e.Observations[index].Attempt == 2 {
					e.Observations[index].HeadSHA = "dreich"
				}
			}
		}, "changes identity across attempts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := base
			evidence.Observations = append([]RunEvidence(nil), base.Observations...)
			evidence.RunAttempts = append([]RunAttemptEvidence(nil), base.RunAttempts...)
			test.edit(&evidence)
			resignEvidence(t, &evidence)

			if err := evidence.Replay(inventory); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Replay() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReplayRejectsDifferentInventoryDigest(t *testing.T) {
	inventory := loadInventory(t)

	data, err := os.ReadFile("testdata/braw-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}

	var snapshot GitHubSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}

	evidence, err := Collect(snapshot, inventory)
	if err != nil {
		t.Fatal(err)
	}

	evidence.InventoryDigest = strings.Repeat("0", 64)
	resignEvidence(t, &evidence)

	if err := evidence.Replay(inventory); err == nil || !strings.Contains(err.Error(), "inventory digest mismatch") {
		t.Fatalf("Replay() error = %v, want inventory digest rejection", err)
	}
}

func TestAuthoritativeCountsFailClosedInCollectAndReplay(t *testing.T) {
	snapshot := loadSnapshot(t)
	inventory := loadInventory(t)

	type countCase struct {
		name         string
		editSnapshot func(*GitHubSnapshot)
		editEvidence func(*Evidence)
	}

	tests := []countCase{
		{"workflow runs extra", func(s *GitHubSnapshot) { s.ExpectedWorkflowRuns++ }, func(e *Evidence) { e.ExpectedWorkflowRuns++ }},
		{"workflow runs missing", func(s *GitHubSnapshot) { s.ExpectedWorkflowRuns-- }, func(e *Evidence) { e.ExpectedWorkflowRuns-- }},
		{"run attempts extra", func(s *GitHubSnapshot) { s.ExpectedRunAttempts++ }, func(e *Evidence) { e.ExpectedRunAttempts++ }},
		{"run attempts missing", func(s *GitHubSnapshot) { s.ExpectedRunAttempts-- }, func(e *Evidence) { e.ExpectedRunAttempts-- }},
		{"caches extra", func(s *GitHubSnapshot) { s.ExpectedCaches++ }, func(e *Evidence) { e.ExpectedCaches++ }},
		{"caches missing", func(s *GitHubSnapshot) { s.ExpectedCaches-- }, func(e *Evidence) { e.ExpectedCaches-- }},
		{"jobs extra", func(s *GitHubSnapshot) { s.Runs[0].ExpectedJobs++ }, func(e *Evidence) { e.RunAttempts[0].ExpectedJobs++ }},
		{"jobs missing", func(s *GitHubSnapshot) { s.Runs[0].ExpectedJobs-- }, func(e *Evidence) { e.RunAttempts[0].ExpectedJobs-- }},
		{"artifacts extra", func(s *GitHubSnapshot) { s.Runs[0].ExpectedArtifacts++ }, func(e *Evidence) { e.RunResources[0].ExpectedArtifacts++ }},
		{"artifacts missing", func(s *GitHubSnapshot) { s.Runs[0].ExpectedArtifacts-- }, func(e *Evidence) { e.RunResources[0].ExpectedArtifacts-- }},
	}

	valid, err := Collect(snapshot, inventory)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changedSnapshot := cloneSnapshot(t, snapshot)
			test.editSnapshot(&changedSnapshot)

			if _, err := Collect(changedSnapshot, inventory); err == nil ||
				!strings.Contains(err.Error(), "count") {
				t.Fatalf("Collect() error = %v, want authoritative count rejection", err)
			}

			changedEvidence := cloneEvidence(t, valid)
			test.editEvidence(&changedEvidence)
			resignEvidence(t, &changedEvidence)

			if err := changedEvidence.Replay(inventory); err == nil ||
				!strings.Contains(err.Error(), "count") {
				t.Fatalf("Replay() error = %v, want authoritative count rejection", err)
			}
		})
	}
}

func TestCollectRejectsMalformedZeroJobRunAndWorkflowAlias(t *testing.T) {
	snapshot := loadSnapshot(t)
	inventory := loadInventory(t)

	snapshot.Runs[0].Jobs = nil
	snapshot.Runs[0].ExpectedJobs = 0

	for _, conclusion := range []string{"success", "failure"} {
		snapshot.Runs[0].Conclusion = conclusion
		if _, err := Collect(snapshot, inventory); err == nil ||
			!strings.Contains(err.Error(), "ineligible zero-job conclusion") {
			t.Fatalf("Collect(%s zero-job) error = %v", conclusion, err)
		}
	}

	snapshot = loadSnapshot(t)

	snapshot.Runs[0].WorkflowPath = ".github/workflows/other/ci.yml"
	if _, err := Collect(snapshot, inventory); err == nil || !strings.Contains(err.Error(), "malformed run") {
		t.Fatalf("Collect(alias path) error = %v, want exact path rejection", err)
	}

	snapshot = loadSnapshot(t)

	snapshot.Runs[1].Cost = CostInput{UnavailableReason: "dreich"}
	if _, err := Collect(snapshot, inventory); err == nil ||
		!strings.Contains(err.Error(), "repeats run-scoped cost input") {
		t.Fatalf("Collect(retry cost) error = %v, want run-scoped cost rejection", err)
	}
}

func TestArtifactNullableTimestampsAndOrdering(t *testing.T) {
	snapshot := loadSnapshot(t)
	inventory := loadInventory(t)

	snapshot.Runs[0].Artifacts[0].CreatedAt = nil
	snapshot.Runs[0].Artifacts[0].UpdatedAt = nil
	snapshot.Runs[0].Artifacts[0].ExpiresAt = nil

	if _, err := Collect(snapshot, inventory); err != nil {
		t.Fatalf("Collect(nullable artifact timestamps): %v", err)
	}

	var missing Artifact
	if err := json.Unmarshal([]byte(`{"id":1,"name":"braw","size_in_bytes":1,"expired":false}`), &missing); err == nil ||
		!strings.Contains(err.Error(), "missing required nullable field") {
		t.Fatalf("UnmarshalJSON(missing artifact timestamps) error = %v, want required-field rejection", err)
	}

	tests := []struct {
		name string
		edit func(*Artifact)
	}{
		{"zero present", func(a *Artifact) { zero := time.Time{}; a.CreatedAt = &zero }},
		{"updated before created", func(a *Artifact) {
			value := a.CreatedAt.Add(-time.Second)
			a.UpdatedAt = &value
		}},
		{"expires before created", func(a *Artifact) {
			value := a.CreatedAt.Add(-time.Second)
			a.ExpiresAt = &value
		}},
		{"expires before updated", func(a *Artifact) {
			value := a.UpdatedAt.Add(-time.Second)
			a.ExpiresAt = &value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := loadSnapshot(t)
			test.edit(&changed.Runs[0].Artifacts[0])

			if _, err := Collect(changed, inventory); err == nil ||
				!strings.Contains(err.Error(), "invalid artifact") {
				t.Fatalf("Collect() error = %v, want artifact timestamp rejection", err)
			}
		})
	}
}

func TestSyntheticMatrixFanoutRequiresExactSingleRawRow(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	coordinates := []string{
		"goreleaser/execute-linux[goarch=amd64,runner=ubuntu-24.04]",
		"goreleaser/execute-linux[goarch=arm64,runner=ubuntu-24.04-arm]",
	}

	snapshot := GitHubSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Repository:    "d0ugal/graith", CollectedAt: now,
		RequestedSince: now.Add(-time.Hour), RequestedUntil: now,
		Complete: true, ExpectedWorkflowRuns: 1, ExpectedRunAttempts: 1,
		Runs: []GitHubRun{{
			ID: 81, Attempt: 1, WorkflowID: "goreleaser", WorkflowPath: ".github/workflows/goreleaser.yml",
			Event: "push", HeadSHA: "braw", CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
			Status: "completed", Conclusion: "success", ExpectedJobs: 1,
			Cost: CostInput{UnavailableReason: "offline fixture has no run usage"},
		}},
	}
	for _, coordinate := range coordinates {
		snapshot.Runs[0].Jobs = append(snapshot.Runs[0].Jobs, GitHubJob{
			ID: 82, Key: "execute-linux", Coordinate: coordinate, Name: "Execute stable Linux artifacts",
			Attempt: 1, Status: "completed", Conclusion: "skipped", CreatedAt: now.Add(-time.Minute),
			SyntheticFanout: true,
		})
	}

	inventory := loadInventory(t)
	if _, err := Collect(snapshot, inventory); err != nil {
		t.Fatalf("Collect(valid synthetic fan-out): %v", err)
	}

	tests := []struct {
		name string
		edit func(*GitHubSnapshot)
	}{
		{"non-skipped raw row", func(s *GitHubSnapshot) {
			for index := range s.Runs[0].Jobs {
				s.Runs[0].Jobs[index].Conclusion = "success"
				s.Runs[0].Jobs[index].StartedAt = now.Add(-30 * time.Second)
				s.Runs[0].Jobs[index].CompletedAt = now
			}
		}},
		{"incomplete coordinates", func(s *GitHubSnapshot) {
			s.Runs[0].Jobs = s.Runs[0].Jobs[:1]
		}},
		{"job ID reused for another row", func(s *GitHubSnapshot) {
			s.Runs[0].Jobs = append(s.Runs[0].Jobs, GitHubJob{
				ID: 82, Key: "changes", Coordinate: "goreleaser/changes", Name: "Changes",
				Attempt: 1, Status: "completed", Conclusion: "skipped", CreatedAt: now.Add(-time.Minute),
				SyntheticFanout: true,
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneSnapshot(t, snapshot)
			test.edit(&changed)

			if _, err := Collect(changed, inventory); err == nil ||
				!strings.Contains(err.Error(), "synthetic matrix") {
				t.Fatalf("Collect() error = %v, want synthetic matrix rejection", err)
			}
		})
	}
}

func loadSnapshot(t *testing.T) GitHubSnapshot {
	t.Helper()

	data, err := os.ReadFile("testdata/braw-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}

	var snapshot GitHubSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}

	return snapshot
}

func cloneSnapshot(t *testing.T, snapshot GitHubSnapshot) GitHubSnapshot {
	t.Helper()

	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	var clone GitHubSnapshot
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}

	return clone
}

func cloneEvidence(t *testing.T, evidence Evidence) Evidence {
	t.Helper()

	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}

	var clone Evidence
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}

	return clone
}

func resignEvidence(t *testing.T, evidence *Evidence) {
	t.Helper()

	evidence.Digest = ""

	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}

	evidence.Digest = sum(data)
}
