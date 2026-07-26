package cigate

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/cipolicy"
)

func TestEvaluateAcceptsDigestBoundEvidence(t *testing.T) {
	input := validEvaluationInput(t)

	evaluation, err := evaluateForTest(t, input, NewMemoryReplayStore())
	if err != nil {
		t.Fatal(err)
	}

	if got := evaluation.Check.Conclusion; got != "success" {
		t.Fatalf("check conclusion = %s, want success", got)
	}

	if len(evaluation.Reasons) != 0 {
		t.Fatalf("success reasons = %v, want empty", evaluation.Reasons)
	}

	if got := len(evaluation.Report.Accepted); got != len(input.Plan.Jobs) {
		t.Fatalf("accepted rows = %d, want %d", got, len(input.Plan.Jobs))
	}

	if evaluation.Evidence.EventDeliveryID != input.Delivery.ID {
		t.Fatalf("event delivery id = %s, want %s", evaluation.Evidence.EventDeliveryID, input.Delivery.ID)
	}

	if evaluation.Evidence.WebhookBodyDigest != input.Delivery.BodyDigest {
		t.Fatalf("webhook body digest = %s, want %s", evaluation.Evidence.WebhookBodyDigest, input.Delivery.BodyDigest)
	}

	if evaluation.Evidence.PlanDigest != input.Plan.PlanDigest {
		t.Fatalf("plan digest = %s, want %s", evaluation.Evidence.PlanDigest, input.Plan.PlanDigest)
	}

	if evaluation.Evidence.BundleDigest == "" {
		t.Fatal("bundle digest is empty")
	}

	if len(evaluation.Evidence.WorkflowIdentities) == 0 {
		t.Fatal("workflow identities are empty")
	}
}

func TestEvaluateAcceptsWebhookValidatedDeliveryWithSharedReplayStore(t *testing.T) {
	input := validEvaluationInput(t)
	secret := []byte("croft secret")
	body := []byte(`{"action":"synchronize"}`)
	store := NewMemoryReplayStore()

	delivery, err := ValidateWebhook(secret, WebhookRequest{
		Event:        input.Event.GitHubEvent,
		DeliveryID:   input.Delivery.ID,
		Signature256: webhookSignature(secret, body),
		Body:         body,
	}, store)
	if err != nil {
		t.Fatal(err)
	}

	input.Delivery = delivery

	if _, err := evaluateForTest(t, input, store); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateRequiresReplayStore(t *testing.T) {
	input := validEvaluationInput(t)

	evaluation, err := evaluateForTest(t, input, nil)
	if err == nil || !strings.Contains(err.Error(), "replay store") {
		t.Fatalf("Evaluate() error = %v, want replay store rejection", err)
	}

	if got := evaluation.Check.Conclusion; got != "failure" {
		t.Fatalf("check conclusion = %s, want failure", got)
	}
}

func TestEvaluateRejectsBundleSuppliedTime(t *testing.T) {
	input := validEvaluationInput(t)
	input.Now = evaluationTime(input)

	evaluation, err := EvaluateAt(input, trustAnchorsForTest(t), NewMemoryReplayStore(), evaluationTime(input))
	if err == nil || !strings.Contains(err.Error(), "cannot supply trusted time") {
		t.Fatalf("EvaluateAt() error = %v, want bundle time rejection", err)
	}

	if got := evaluation.Check.Summary; !strings.Contains(got, "cannot supply trusted time") {
		t.Fatalf("check summary = %q, want trusted time rejection", got)
	}
}

func TestEvaluateRejectsTypedNilReplayStore(t *testing.T) {
	input := validEvaluationInput(t)

	var store *MemoryReplayStore

	_, err := evaluateForTest(t, input, store)
	if err == nil || !strings.Contains(err.Error(), "not initialised") {
		t.Fatalf("EvaluateAt() error = %v, want typed nil replay rejection", err)
	}
}

func TestEvaluateAcceptsMergeGroupMappedToPullRequestTrustedBase(t *testing.T) {
	input := mergeGroupEvaluationInput(t)

	evaluation, err := evaluateForTest(t, input, NewMemoryReplayStore())
	if err != nil {
		t.Fatal(err)
	}

	if got := evaluation.Check.Conclusion; got != "success" {
		t.Fatalf("check conclusion = %s, want success", got)
	}
}

func TestEvaluateRejectsFalseGreenBoundaries(t *testing.T) {
	tests := map[string]struct {
		mutate func(*EvaluationInput)
		want   string
	}{
		"untrusted event source": {
			mutate: func(input *EvaluationInput) { input.Event.Source = "pull-request-payload" },
			want:   `event source "pull-request-payload" is untrusted`,
		},
		"missing delivery": {
			mutate: func(input *EvaluationInput) { input.Delivery.ID = "" },
			want:   "event delivery id is required",
		},
		"wrong head sha": {
			mutate: func(input *EvaluationInput) { input.Event.HeadSHA = strings.Repeat("4", 40) },
			want:   "intended SHA",
		},
		"wrong base sha": {
			mutate: func(input *EvaluationInput) { input.Event.BaseSHA = strings.Repeat("9", 40) },
			want:   "base SHA",
		},
		"stale policy epoch": {
			mutate: func(input *EvaluationInput) { input.Event.PolicyDigest = digestHex("dreich policy") },
			want:   "event policy digest",
		},
		"wrong workflow path": {
			mutate: func(input *EvaluationInput) { input.Evidence[0].Run.WorkflowPath = ".github/workflows/canny.yml" },
			want:   "workflow path",
		},
		"wrong workflow blob": {
			mutate: func(input *EvaluationInput) {
				input.Evidence[0].Artifact.WorkflowBlobSHA = digestHex("thrawn workflow")
			},
			want: "workflow blob SHA",
		},
		"wrong producer attempt": {
			mutate: func(input *EvaluationInput) { input.Evidence[0].Artifact.ProducerRunAttempt++ },
			want:   "producer run/attempt",
		},
		"missing evidence": {
			mutate: func(input *EvaluationInput) { input.Evidence = input.Evidence[1:] },
			want:   "missing coordinate evidence",
		},
		"extra evidence": {
			mutate: func(input *EvaluationInput) {
				extra := input.Evidence[0]
				extra.Coordinate = "bothy-extra"
				input.Evidence = append(input.Evidence, extra)
			},
			want: "extra coordinate evidence",
		},
		"zero job plan": {
			mutate: func(input *EvaluationInput) { input.Plan.Jobs = nil },
			want:   "zero-job plan cannot satisfy the gate",
		},
		"partial result": {
			mutate: func(input *EvaluationInput) {
				input.Results = input.Results[1:]
				input.Evidence = input.Evidence[1:]
			},
			want: "fan-in rejected",
		},
		"wrong artifact digest": {
			mutate: func(input *EvaluationInput) { input.Evidence[0].Artifact.Digest = digestHex("wrong artifact") },
			want:   "artifact digest",
		},
		"artifact uploaded before run": {
			mutate: func(input *EvaluationInput) {
				input.Evidence[0].Artifact.UploadedAt = input.Evidence[0].Run.StartedAt.Add(-time.Second)
			},
			want: "artifact upload time",
		},
		"artifact uploaded long after run": {
			mutate: func(input *EvaluationInput) {
				input.Evidence[0].Artifact.UploadedAt = input.Evidence[0].Run.CompletedAt.Add(artifactUploadSkew + time.Second)
			},
			want: "artifact upload time",
		},
		"future run provenance": {
			mutate: func(input *EvaluationInput) {
				now := evaluationTime(*input)
				input.Evidence[0].Run.StartedAt = now.Add(time.Hour)
				input.Evidence[0].Run.CompletedAt = now.Add(2 * time.Hour)
				input.Evidence[0].Artifact.UploadedAt = now.Add(2*time.Hour + time.Minute)
			},
			want: "run completion time",
		},
		"run provenance unrelated to result": {
			mutate: func(input *EvaluationInput) {
				input.Evidence[0].Run.StartedAt = input.Results[0].StartedAt.Add(-90 * 24 * time.Hour)
				input.Evidence[0].Run.CompletedAt = input.Results[0].CompletedAt.Add(-90 * 24 * time.Hour)
				input.Evidence[0].Artifact.UploadedAt = input.Evidence[0].Run.CompletedAt
			},
			want: "result timestamps",
		},
		"trust confusion": {
			mutate: func(input *EvaluationInput) { input.Event.PullRequestFork = true },
			want:   "exactly one trusted event context",
		},
		"tier flag confusion": {
			mutate: func(input *EvaluationInput) {
				input.Event.SameRepositoryAgent = false
				input.Event.PullRequestFork = true
			},
			want: "same-repository-agent tier requires a same-repository head",
		},
		"unsupported pull request action": {
			mutate: func(input *EvaluationInput) { input.Event.Action = "labeled-by-a-bot" },
			want:   `pull_request action "labeled-by-a-bot" is unsupported`,
		},
		"unsupported pull request policy event override": {
			mutate: func(input *EvaluationInput) { input.Event.PolicyGitHubEvent = "push" },
			want:   `pull_request policy event "push" is unsupported`,
		},
		"pr controlled success lacks provenance": {
			mutate: func(input *EvaluationInput) {
				input.Evidence[0].Run.WorkflowPath = ".github/workflows/pr-controlled.yml"
				input.Evidence[0].Artifact.WorkflowPath = ".github/workflows/pr-controlled.yml"
			},
			want: "workflow path",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			input := validEvaluationInput(t)
			test.mutate(&input)

			evaluation, err := evaluateForTest(t, input, NewMemoryReplayStore())
			if err == nil {
				t.Fatal("Evaluate() error = nil, want rejection")
			}

			if got := evaluation.Check.Conclusion; got != "failure" {
				t.Fatalf("check conclusion = %s, want failure", got)
			}

			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Evaluate() error = %v, want substring %q", err, test.want)
			}

			if !strings.Contains(evaluation.Check.Summary, test.want) {
				t.Fatalf("check summary = %q, want substring %q", evaluation.Check.Summary, test.want)
			}
		})
	}
}

func TestEvaluateRejectsBundlePolicyForgery(t *testing.T) {
	input := validEvaluationInput(t)
	input.Policy.Modes[0].Coordinates[0].Trace.WorkflowPath = ".github/workflows/pr-controlled.yml"

	digest, err := input.Policy.Digest()
	if err != nil {
		t.Fatal(err)
	}

	input.Policy.PolicyDigest = digest
	input.Event.PolicyDigest = input.Policy.PolicyDigest

	_, err = evaluateForTest(t, input, NewMemoryReplayStore())
	if err == nil || !strings.Contains(err.Error(), "bundle policy digest") {
		t.Fatalf("EvaluateAt() error = %v, want trusted policy anchor rejection", err)
	}
}

func TestEvaluateRejectsTrustedPolicyDigestMismatch(t *testing.T) {
	input := validEvaluationInput(t)
	anchors := trustAnchorsForTest(t)
	anchors.ExpectedPolicyDigest = digestHex("forged expected policy")

	_, err := EvaluateAt(input, anchors, NewMemoryReplayStore(), evaluationTime(input))
	if err == nil || !strings.Contains(err.Error(), "expected policy digest") {
		t.Fatalf("EvaluateAt() error = %v, want expected policy digest rejection", err)
	}
}

func TestEvaluateRejectsBundleEvaluatorForgery(t *testing.T) {
	input := validEvaluationInput(t)
	input.Evaluator.SourceDigest = digestHex("forged evaluator")

	_, err := evaluateForTest(t, input, NewMemoryReplayStore())
	if err == nil || !strings.Contains(err.Error(), "bundle evaluator identity") {
		t.Fatalf("EvaluateAt() error = %v, want trusted evaluator anchor rejection", err)
	}
}

func TestEvaluateRejectsPartialTrustedEchoes(t *testing.T) {
	tests := map[string]struct {
		mutate func(*EvaluationInput)
		want   string
	}{
		"partial config": {
			mutate: func(input *EvaluationInput) { input.Config = Config{Repository: "d0ugal/graith"} },
			want:   "bundle config echo is invalid",
		},
		"partial policy": {
			mutate: func(input *EvaluationInput) {
				input.Policy = cipolicy.Manifest{PolicyDigest: input.Policy.PolicyDigest}
			},
			want: "bundle policy echo is invalid",
		},
		"partial evaluator": {
			mutate: func(input *EvaluationInput) {
				input.Evaluator = EvaluatorIdentity{SourceDigest: input.Evaluator.SourceDigest}
			},
			want: "bundle evaluator identity",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			input := validEvaluationInput(t)
			test.mutate(&input)

			_, err := evaluateForTest(t, input, NewMemoryReplayStore())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EvaluateAt() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestEvaluateDoesNotBurnDeliveryOnFailedValidation(t *testing.T) {
	store := NewMemoryReplayStore()
	input := validEvaluationInput(t)
	invalid := input
	invalid.Evidence = invalid.Evidence[1:]

	if _, err := evaluateForTest(t, invalid, store); err == nil {
		t.Fatal("EvaluateAt() error = nil, want failed validation")
	}

	if _, err := evaluateForTest(t, input, store); err != nil {
		t.Fatalf("valid redelivery after failed validation: %v", err)
	}
}

func TestEvaluateRejectsMergeGroupTrustConfusion(t *testing.T) {
	input := mergeGroupEvaluationInput(t)
	input.Event.SameRepositoryAgent = true
	input.Event.TrustedBase = false

	_, err := evaluateForTest(t, input, NewMemoryReplayStore())
	if err == nil || !strings.Contains(err.Error(), "trusted-base policy context") {
		t.Fatalf("EvaluateAt() error = %v, want merge_group trust rejection", err)
	}
}

func TestEvaluateRejectsDuplicateDeliveryAndReplayedBundle(t *testing.T) {
	store := NewMemoryReplayStore()
	input := validEvaluationInput(t)

	if _, err := evaluateForTest(t, input, store); err != nil {
		t.Fatal(err)
	}

	_, err := evaluateForTest(t, input, store)
	if err == nil || !strings.Contains(err.Error(), "replayed delivery") {
		t.Fatalf("duplicate delivery error = %v, want replay rejection", err)
	}

	replayed := input
	replayed.Delivery.ID = "bairn-delivery"

	_, err = evaluateForTest(t, replayed, store)
	if err == nil || !strings.Contains(err.Error(), "replayed bundle") {
		t.Fatalf("replayed bundle error = %v, want bundle replay rejection", err)
	}
}

func TestEvaluateRejectsReplayedBundleWithFreshDeliveryEnvelope(t *testing.T) {
	tests := map[string]struct {
		mutate func(*EvaluationInput)
	}{
		"changed delivery body digest": {
			mutate: func(input *EvaluationInput) {
				input.Delivery.BodyDigest = digestHex("changed delivery body")
			},
		},
		"changed pull request action": {
			mutate: func(input *EvaluationInput) {
				input.Event.Action = "reopened"
			},
		},
		"explicit default policy event": {
			mutate: func(input *EvaluationInput) {
				input.Event.PolicyGitHubEvent = "pull_request"
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryReplayStore()
			input := validEvaluationInput(t)

			if _, err := evaluateForTest(t, input, store); err != nil {
				t.Fatal(err)
			}

			replayed := input
			replayed.Delivery.ID = "bairn-delivery"
			test.mutate(&replayed)

			_, err := evaluateForTest(t, replayed, store)
			if err == nil || !strings.Contains(err.Error(), "replayed bundle") {
				t.Fatalf("EvaluateAt() error = %v, want bundle replay despite fresh envelope change", err)
			}
		})
	}
}

func TestEvaluateRejectsReplayedBundleWithIgnoredDisplayFieldChanges(t *testing.T) {
	store := NewMemoryReplayStore()
	input := validEvaluationInput(t)

	if len(input.Results) < 2 || len(input.Evidence) < 2 {
		t.Fatal("fixture needs at least two results and evidence rows")
	}

	if _, err := evaluateForTest(t, input, store); err != nil {
		t.Fatal(err)
	}

	replayed := input
	replayed.Delivery.ID = "bairn-delivery"
	replayed.Evidence[0].Run.WorkflowName = "changed display name"
	replayed.Evidence[0].Artifact.Name = "changed-artifact-name"
	reverseResultRecords(replayed.Results)
	reverseCoordinateEvidence(replayed.Evidence)

	_, err := evaluateForTest(t, replayed, store)
	if err == nil || !strings.Contains(err.Error(), "replayed bundle") {
		t.Fatalf("EvaluateAt() error = %v, want normalized bundle replay", err)
	}
}

func TestFileReplayStorePersistsReplayState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.json")
	input := validEvaluationInput(t)

	if _, err := evaluateForTest(t, input, NewFileReplayStore(path)); err != nil {
		t.Fatal(err)
	}

	replayed := input
	replayed.Delivery.ID = "bairn-delivery"

	_, err := evaluateForTest(t, replayed, NewFileReplayStore(path))
	if err == nil || !strings.Contains(err.Error(), "replayed bundle") {
		t.Fatalf("EvaluateAt() error = %v, want persisted bundle replay", err)
	}
}

func evaluateForTest(t *testing.T, input EvaluationInput, store ReplayStore) (Evaluation, error) {
	t.Helper()

	return EvaluateAt(input, trustAnchorsForTest(t), store, evaluationTime(input))
}

func trustAnchorsForTest(t *testing.T) TrustAnchors {
	t.Helper()

	policy, err := cipolicy.ReadManifest(testManifestPath())
	if err != nil {
		t.Fatal(err)
	}

	return TrustAnchors{
		Config:               validConfig(),
		Policy:               policy,
		ExpectedPolicyDigest: policy.PolicyDigest,
		Evaluator:            validEvaluator(),
	}
}

func evaluationTime(input EvaluationInput) time.Time {
	if !input.Plan.CreatedAt.IsZero() {
		return input.Plan.CreatedAt.Add(time.Hour)
	}

	return time.Now().UTC().Truncate(time.Second)
}

func validEvaluationInput(t *testing.T) EvaluationInput {
	t.Helper()

	now := stableGateTime(t)

	manifest, err := cipolicy.ReadManifest(testManifestPath())
	if err != nil {
		t.Fatal(err)
	}

	headSHA := strings.Repeat("1", 40)
	baseSHA := strings.Repeat("2", 40)
	treeSHA := strings.Repeat("3", 40)

	plan, err := cipolicy.BuildPlan(manifest, cipolicy.PlanOptions{
		Event: cipolicy.EventInput{
			GitHubEvent:         "pull_request",
			Ref:                 "refs/pull/17/merge",
			BaseRef:             "refs/heads/main",
			HeadRef:             "refs/heads/braw",
			BaseRepository:      cipolicy.DefaultRepository,
			HeadRepository:      cipolicy.DefaultRepository,
			Commit:              headSHA,
			Tree:                treeSHA,
			SameRepositoryAgent: true,
		},
		ChangedFiles:  []string{"internal/daemon/session.go"},
		ExactFileList: true,
		CreatedAt:     now.Add(-time.Hour),
		ExpiresAt:     now.Add(time.Hour),
		Now:           now,
	})
	if err != nil {
		t.Fatal(err)
	}

	results, evidence := successfulResultsAndEvidence(t, manifest, plan, now, baseSHA)

	return EvaluationInput{
		SchemaVersion: SchemaVersion,
		Config:        validConfig(),
		Delivery: DeliveryContext{
			ID:                 "blether-delivery",
			Event:              "pull_request",
			SignatureValidated: true,
			BodyDigest:         digestHex("delivery body"),
		},
		Event: EventContext{
			Source:              GitHubWebhookSource,
			GitHubEvent:         "pull_request",
			Action:              "synchronize",
			Repository:          cipolicy.DefaultRepository,
			BaseRepository:      cipolicy.DefaultRepository,
			HeadRepository:      cipolicy.DefaultRepository,
			Ref:                 plan.Event.Ref,
			BaseRef:             plan.Event.BaseRef,
			HeadRef:             plan.Event.HeadRef,
			IntendedSHA:         headSHA,
			HeadSHA:             headSHA,
			BaseSHA:             baseSHA,
			PolicyDigest:        manifest.PolicyDigest,
			TrustTier:           "same-repository-agent",
			SameRepositoryAgent: true,
		},
		Policy:    manifest,
		Plan:      plan,
		Results:   results,
		Evidence:  evidence,
		Evaluator: validEvaluator(),
	}
}

func mergeGroupEvaluationInput(t *testing.T) EvaluationInput {
	t.Helper()

	now := stableGateTime(t)

	manifest, err := cipolicy.ReadManifest(testManifestPath())
	if err != nil {
		t.Fatal(err)
	}

	headSHA := strings.Repeat("5", 40)
	baseSHA := strings.Repeat("6", 40)
	treeSHA := strings.Repeat("7", 40)

	plan, err := cipolicy.BuildPlan(manifest, cipolicy.PlanOptions{
		Event: cipolicy.EventInput{
			GitHubEvent:    "pull_request",
			Ref:            "refs/heads/gh-readonly-queue/main/pr-17-braw",
			BaseRef:        "refs/heads/main",
			HeadRef:        "gh-readonly-queue/main/pr-17-braw",
			BaseRepository: cipolicy.DefaultRepository,
			HeadRepository: cipolicy.DefaultRepository,
			Commit:         headSHA,
			Tree:           treeSHA,
			TrustedBase:    true,
		},
		ChangedFiles:  []string{"internal/daemon/session.go"},
		ExactFileList: true,
		CreatedAt:     now.Add(-time.Hour),
		ExpiresAt:     now.Add(time.Hour),
		Now:           now,
	})
	if err != nil {
		t.Fatal(err)
	}

	results, evidence := successfulResultsAndEvidence(t, manifest, plan, now, baseSHA)
	for index := range evidence {
		evidence[index].Run.Event = "merge_group"
	}

	return EvaluationInput{
		SchemaVersion: SchemaVersion,
		Config:        validConfig(),
		Delivery: DeliveryContext{
			ID:                 "merge-group-delivery",
			Event:              "merge_group",
			SignatureValidated: true,
			BodyDigest:         digestHex("merge group delivery body"),
		},
		Event: EventContext{
			Source:            GitHubWebhookSource,
			GitHubEvent:       "merge_group",
			PolicyGitHubEvent: "pull_request",
			Action:            "checks_requested",
			Repository:        cipolicy.DefaultRepository,
			BaseRepository:    cipolicy.DefaultRepository,
			HeadRepository:    cipolicy.DefaultRepository,
			Ref:               plan.Event.Ref,
			BaseRef:           plan.Event.BaseRef,
			HeadRef:           plan.Event.HeadRef,
			IntendedSHA:       headSHA,
			HeadSHA:           headSHA,
			BaseSHA:           baseSHA,
			PolicyDigest:      manifest.PolicyDigest,
			TrustTier:         "trusted-base",
			TrustedBase:       true,
		},
		Policy:    manifest,
		Plan:      plan,
		Results:   results,
		Evidence:  evidence,
		Evaluator: validEvaluator(),
	}
}

func successfulResultsAndEvidence(t *testing.T, manifest cipolicy.Manifest, plan cipolicy.RunPlan, now time.Time, baseSHA string) ([]cipolicy.ResultRecord, []CoordinateEvidence) {
	t.Helper()

	traces, err := workflowTraces(manifest, plan)
	if err != nil {
		t.Fatal(err)
	}

	var (
		results  []cipolicy.ResultRecord
		evidence []CoordinateEvidence
	)

	for index, job := range plan.Jobs {
		artifactDigest := digestHex("artifact", job.Mode, job.Coordinate)
		attempt := cipolicy.ResultAttempt{
			Attempt:        1,
			Status:         "success",
			StartedAt:      now.Add(-20 * time.Minute).Add(time.Duration(index) * time.Second),
			CompletedAt:    now.Add(-10 * time.Minute).Add(time.Duration(index) * time.Second),
			EvidenceDigest: digestHex("evidence", job.Mode, job.Coordinate),
			ArtifactDigest: artifactDigest,
			CacheDigest:    digestHex("cache", job.Mode, job.Coordinate),
		}

		result, err := cipolicy.NewResultRecord(plan, job, []cipolicy.ResultAttempt{attempt})
		if err != nil {
			t.Fatal(err)
		}

		results = append(results, result)

		trace := traces[coordinateKey(job.Mode, job.Coordinate)]
		runID := int64(1700 + index)
		evidence = append(evidence, CoordinateEvidence{
			Mode:       job.Mode,
			Coordinate: job.Coordinate,
			Run: RunProvenance{
				ID:              runID,
				Attempt:         1,
				Repository:      cipolicy.DefaultRepository,
				Event:           "pull_request",
				WorkflowName:    job.GitHubName,
				WorkflowPath:    trace.WorkflowPath,
				WorkflowBlobSHA: trace.WorkflowSHA256,
				HeadSHA:         plan.Source.Commit,
				BaseSHA:         baseSHA,
				StartedAt:       attempt.StartedAt,
				CompletedAt:     attempt.CompletedAt,
			},
			Artifact: ArtifactProvenance{
				Name:               "canny-" + job.Coordinate,
				Digest:             artifactDigest,
				Repository:         cipolicy.DefaultRepository,
				WorkflowPath:       trace.WorkflowPath,
				WorkflowBlobSHA:    trace.WorkflowSHA256,
				ProducerRunID:      runID,
				ProducerRunAttempt: 1,
				HeadSHA:            plan.Source.Commit,
				BaseSHA:            baseSHA,
				PlanDigest:         plan.PlanDigest,
				PolicyDigest:       plan.PolicyDigest,
				UploadedAt:         attempt.CompletedAt.Add(-time.Minute),
			},
		})
	}

	return results, evidence
}

func validConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Repository:    cipolicy.DefaultRepository,
		DefaultBranch: cipolicy.DefaultDefaultBranch,
		App: AppContract{
			Slug:              CheckName,
			ID:                424242,
			Owner:             "graith-maintainers",
			InstallationOwner: "d0ugal",
			CheckName:         CheckName,
			Permissions: map[string]string{
				"metadata":      "read",
				"contents":      "read",
				"actions":       "read",
				"pull_requests": "read",
				"checks":        "write",
			},
			Events: []string{"pull_request", "merge_group"},
		},
		Deployment: DeploymentContract{
			Runtime:         "fixture-hosted-runtime",
			ReleaseDigest:   digestHex("release"),
			EvaluatorDigest: digestHex("evaluator"),
			AttestationKey: AttestationKey{
				Service:    "fixture-attestation-kms",
				KeyID:      "projects/braw/locations/global/keyRings/canny/cryptoKeys/gate",
				TrustModel: "reviewed-release-digest-signed-by-maintainer-key",
			},
			Rotation: RotationContract{
				Owner:   "graith-maintainers",
				Cadence: "90d",
				Runbook: "docs/runbooks/ci-gate-rotation.md",
			},
			IncidentRevocation: RevocationContract{
				Owner:   "graith-maintainers",
				Runbook: "docs/runbooks/ci-gate-revoke.md",
			},
		},
		Retention: RetentionContract{
			Owner:    "graith-maintainers",
			Location: "artifact-store://braw/graith-ci-gate",
			Duration: "2160h",
		},
		LiveProof: LiveProofContract{
			FixtureRepository: "d0ugal/graith-ci-gate-fixture",
		},
		Operators: []string{"graith-maintainers"},
	}
}

func validEvaluator() EvaluatorIdentity {
	config := validConfig()

	return EvaluatorIdentity{
		Name:          CheckName,
		Version:       "v0.0.0-test",
		ReleaseDigest: config.Deployment.ReleaseDigest,
		SourceDigest:  config.Deployment.EvaluatorDigest,
	}
}

func stableGateTime(t *testing.T) time.Time {
	t.Helper()

	manifest, err := cipolicy.ReadManifest(testManifestPath())
	if err != nil {
		t.Fatal(err)
	}

	var earliest time.Time

	for _, decision := range manifest.Unsupported {
		expires, err := time.Parse(time.DateOnly, decision.Expires)
		if err != nil {
			t.Fatal(err)
		}

		if earliest.IsZero() || expires.Before(earliest) {
			earliest = expires
		}
	}

	if earliest.IsZero() {
		return time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	}

	candidate := earliest.AddDate(0, -1, 0).Add(10 * time.Hour).UTC()
	current := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)

	if candidate.After(current) {
		return current
	}

	return candidate
}

func digestHex(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func testManifestPath() string {
	return filepath.Join("..", "cipolicy", "manifest.json")
}

func reverseResultRecords(results []cipolicy.ResultRecord) {
	for left, right := 0, len(results)-1; left < right; left, right = left+1, right-1 {
		results[left], results[right] = results[right], results[left]
	}
}

func reverseCoordinateEvidence(evidence []CoordinateEvidence) {
	for left, right := 0, len(evidence)-1; left < right; left, right = left+1, right-1 {
		evidence[left], evidence[right] = evidence[right], evidence[left]
	}
}
