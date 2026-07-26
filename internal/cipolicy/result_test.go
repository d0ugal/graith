package cipolicy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFanInAcceptsCompleteSuccessfulPlanSet(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	results := successResults(t, plan)

	report, err := FanIn(manifest, plan, results, p2TestNow)
	if err != nil {
		t.Fatal(err)
	}

	if report.Status != "passed" {
		t.Fatalf("fan-in status = %s, want passed", report.Status)
	}

	if len(report.Accepted) != len(plan.Jobs) {
		t.Fatalf("accepted = %d, want %d", len(report.Accepted), len(plan.Jobs))
	}
}

func TestFanInRejectsMissingUnknownDuplicateAndExtraCoordinates(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	results := successResults(t, plan)

	tests := map[string]struct {
		edit func([]ResultRecord) []ResultRecord
		want string
	}{
		"missing coordinate": {
			edit: func(results []ResultRecord) []ResultRecord {
				return results[:len(results)-1]
			},
			want: "missing-result",
		},
		"unknown mode": {
			edit: func(results []ResultRecord) []ResultRecord {
				results[0].Mode = "legacy/dreich/blether"
				signResult(t, &results[0])

				return results
			},
			want: "unknown-or-extra-result",
		},
		"duplicate coordinate": {
			edit: func(results []ResultRecord) []ResultRecord {
				return append(results, results[0])
			},
			want: "duplicate-result",
		},
		"extra coordinate": {
			edit: func(results []ResultRecord) []ResultRecord {
				extra := results[0]
				extra.Coordinate = "bothy/blether"
				signResult(t, &extra)

				return append(results, extra)
			},
			want: "unknown-or-extra-result",
		},
		"count matches but coordinate missing": {
			edit: func(results []ResultRecord) []ResultRecord {
				extra := results[0]
				extra.Coordinate = "strath/blether"
				signResult(t, &extra)
				results[len(results)-1] = extra

				return results
			},
			want: "missing-result",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := cloneResults(results)

			report, err := FanIn(manifest, plan, test.edit(mutated), p2TestNow)
			if err == nil {
				t.Fatalf("FanIn() succeeded, want rejection")
			}

			if !decisionReasonsContain(report.Rejected, test.want) {
				t.Fatalf("rejected decisions = %#v, want reason %s", report.Rejected, test.want)
			}
		})
	}
}

func TestFanInRejectsNonGreenStatuses(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	results := successResults(t, plan)

	tests := map[string]struct {
		status       string
		failureClass string
		supersededBy string
	}{
		"failed": {
			status:       "failed",
			failureClass: "test",
		},
		"skipped": {
			status:       "skipped",
			failureClass: "policy",
		},
		"cancelled": {
			status:       "cancelled",
			failureClass: "cancelled",
		},
		"stale": {
			status:       "stale",
			failureClass: "stale",
		},
		"superseded": {
			status:       "superseded",
			failureClass: "superseded",
			supersededBy: strings.Repeat("5", 64),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := cloneResults(results)
			setFinalOutcome(t, &mutated[0], test.status, test.failureClass, test.supersededBy)

			report, err := FanIn(manifest, plan, mutated, p2TestNow)
			if err == nil {
				t.Fatalf("FanIn() succeeded, want non-green rejection")
			}

			if !decisionReasonsContain(report.Rejected, "result-status-not-green") {
				t.Fatalf("rejected decisions = %#v, want non-green reason", report.Rejected)
			}
		})
	}
}

func TestNewSupersededResultRecordProducesValidRecord(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	attempt := resultAttempt(1, "superseded", "superseded", p2TestNow)
	supersededBy := strings.Repeat("5", 64)

	result, err := NewSupersededResultRecord(plan, plan.Jobs[0], []ResultAttempt{attempt}, supersededBy)
	if err != nil {
		t.Fatal(err)
	}

	if result.SupersededBy != supersededBy {
		t.Fatalf("superseded_by = %s, want %s", result.SupersededBy, supersededBy)
	}

	if err := ValidateResultRecord(manifest, plan, result, p2TestNow); err != nil {
		t.Fatal(err)
	}
}

func TestNewResultRecordRejectsSupersededWithoutIdentity(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	attempt := resultAttempt(1, "superseded", "superseded", p2TestNow)

	if _, err := NewResultRecord(plan, plan.Jobs[0], []ResultAttempt{attempt}); err == nil ||
		!strings.Contains(err.Error(), "supersession identity") {
		t.Fatalf("NewResultRecord() error = %v, want supersession identity rejection", err)
	}
}

func TestResultValidationRejectsStaleBindings(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	result := successResults(t, plan)[0]

	tests := map[string]func(*ResultRecord){
		"commit mismatch": func(result *ResultRecord) {
			result.Source.Commit = strings.Repeat("6", 40)
		},
		"tree mismatch": func(result *ResultRecord) {
			result.Source.Tree = strings.Repeat("7", 40)
		},
		"policy mismatch": func(result *ResultRecord) {
			result.PolicyDigest = strings.Repeat("8", 64)
		},
		"detector mismatch": func(result *ResultRecord) {
			result.DetectorDigest = strings.Repeat("9", 64)
		},
		"event mismatch": func(result *ResultRecord) {
			result.Event.Event = "workflow-dispatch"
			result.Event.GitHubEvent = "workflow_dispatch"
		},
		"trust mismatch": func(result *ResultRecord) {
			result.TrustTier = "trusted-base"
		},
	}

	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := result
			edit(&mutated)
			signResult(t, &mutated)

			if err := ValidateResultRecord(manifest, plan, mutated, p2TestNow); err == nil ||
				!strings.Contains(err.Error(), "stale result binding") {
				t.Fatalf("ValidateResultRecord() error = %v, want stale binding rejection", err)
			}
		})
	}
}

func TestResultValidationPreservesAttemptHistory(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	attempts := []ResultAttempt{
		resultAttempt(1, "failed", "test", p2TestNow),
		resultAttempt(2, "success", "", p2TestNow.Add(time.Minute)),
	}

	result, err := NewResultRecord(plan, plan.Jobs[0], attempts)
	if err != nil {
		t.Fatal(err)
	}

	if err := ValidateResultRecord(manifest, plan, result, p2TestNow); err != nil {
		t.Fatal(err)
	}

	if result.FirstStatus != "failed" || result.Status != "success" {
		t.Fatalf("first/final statuses = %s/%s, want failed/success", result.FirstStatus, result.Status)
	}

	result.FirstStatus = "success"
	signResult(t, &result)

	if err := ValidateResultRecord(manifest, plan, result, p2TestNow); err == nil ||
		!strings.Contains(err.Error(), "first attempt outcome") {
		t.Fatalf("ValidateResultRecord() error = %v, want first-outcome rejection", err)
	}
}

func TestResultValidationRejectsInvalidDigests(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	result := successResults(t, plan)[0]

	tests := map[string]func(*ResultRecord){
		"missing evidence digest": func(result *ResultRecord) {
			result.Attempts[0].EvidenceDigest = ""
			result.EvidenceDigest = ""
		},
		"invalid artifact digest": func(result *ResultRecord) {
			result.Attempts[0].ArtifactDigest = "dreich"
			result.ArtifactDigest = "dreich"
		},
		"missing cache digest": func(result *ResultRecord) {
			result.Attempts[0].CacheDigest = ""
			result.CacheDigest = ""
		},
	}

	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := result
			edit(&mutated)
			signResult(t, &mutated)

			if err := ValidateResultRecord(manifest, plan, mutated, p2TestNow); err == nil ||
				!strings.Contains(err.Error(), "digest") {
				t.Fatalf("ValidateResultRecord() error = %v, want digest rejection", err)
			}
		})
	}
}

func TestResultValidationRejectsCommitShapedSupersessionIdentity(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	result := successResults(t, plan)[0]
	setFinalOutcome(t, &result, "superseded", "superseded", strings.Repeat("5", 40))

	if err := ValidateResultRecord(manifest, plan, result, p2TestNow); err == nil ||
		!strings.Contains(err.Error(), "supersession identity") {
		t.Fatalf("ValidateResultRecord() error = %v, want supersession identity rejection", err)
	}
}

func TestResultValidationCanonicalizesNilMatrix(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	result := successResults(t, plan)[0]

	result.Matrix = nil
	signResult(t, &result)

	if err := ValidateResultRecord(manifest, plan, result, p2TestNow); err != nil {
		t.Fatal(err)
	}
}

func TestResultCanonicalJSONAndDigestAreDeterministic(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	result := successResults(t, plan)[0]

	leftJSON, err := result.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	rightJSON, err := result.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("canonical JSON differs:\nleft=%s\nright=%s", leftJSON, rightJSON)
	}

	var decoded ResultRecord
	if err := json.Unmarshal(leftJSON, &decoded); err != nil {
		t.Fatal(err)
	}

	if err := ValidateResultRecord(manifest, plan, decoded, p2TestNow); err != nil {
		t.Fatal(err)
	}

	decoded.ResultDigest = strings.Repeat("0", 64)
	if err := ValidateResultRecord(manifest, plan, decoded, p2TestNow); err == nil ||
		!strings.Contains(err.Error(), "result digest mismatch") {
		t.Fatalf("ValidateResultRecord() error = %v, want result digest mismatch", err)
	}
}

func TestResultCanonicalizationNormalizesTimestampsToUTC(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	offset := time.FixedZone("BRAW", 3600)
	started := p2TestNow.In(offset)

	offsetResult, err := NewResultRecord(plan, plan.Jobs[0], []ResultAttempt{
		resultAttempt(1, "success", "", started),
	})
	if err != nil {
		t.Fatal(err)
	}

	utcResult, err := NewResultRecord(plan, plan.Jobs[0], []ResultAttempt{
		resultAttempt(1, "success", "", started.UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}

	if offsetResult.ResultDigest != utcResult.ResultDigest {
		t.Fatalf("result digest = %s, want %s", offsetResult.ResultDigest, utcResult.ResultDigest)
	}

	offsetJSON, err := offsetResult.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	utcJSON, err := utcResult.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	if string(offsetJSON) != string(utcJSON) {
		t.Fatalf("canonical JSON differs:\noffset=%s\nutc=%s", offsetJSON, utcJSON)
	}
}

func successResults(t *testing.T, plan RunPlan) []ResultRecord {
	t.Helper()

	results := make([]ResultRecord, 0, len(plan.Jobs))
	for index, job := range plan.Jobs {
		attempt := resultAttempt(1, "success", "", p2TestNow.Add(time.Duration(index)*time.Minute))

		result, err := NewResultRecord(plan, job, []ResultAttempt{attempt})
		if err != nil {
			t.Fatal(err)
		}

		results = append(results, result)
	}

	return results
}

func resultAttempt(attempt int, status, failureClass string, started time.Time) ResultAttempt {
	return ResultAttempt{
		Attempt:        attempt,
		Status:         status,
		FailureClass:   failureClass,
		StartedAt:      started,
		CompletedAt:    started.Add(time.Minute),
		EvidenceDigest: strings.Repeat("a", 64),
		ArtifactDigest: strings.Repeat("b", 64),
		CacheDigest:    strings.Repeat("c", 64),
	}
}

func setFinalOutcome(t *testing.T, result *ResultRecord, status, failureClass, supersededBy string) {
	t.Helper()

	final := result.Attempts[len(result.Attempts)-1]
	final.Status = status
	final.FailureClass = failureClass

	result.Attempts[len(result.Attempts)-1] = final
	if len(result.Attempts) == 1 {
		result.FirstStatus = status
		result.FirstFailureClass = failureClass
	}

	result.Status = status
	result.FailureClass = failureClass
	result.SupersededBy = supersededBy
	signResult(t, result)
}

func signResult(t *testing.T, result *ResultRecord) {
	t.Helper()

	digest, err := result.Digest()
	if err != nil {
		t.Fatal(err)
	}

	result.ResultDigest = digest
}

func cloneResults(results []ResultRecord) []ResultRecord {
	clone := append([]ResultRecord(nil), results...)
	for index := range clone {
		clone[index] = clone[index].copy()
	}

	return clone
}

func decisionReasonsContain(decisions []FanInDecision, wanted string) bool {
	return slices.ContainsFunc(decisions, func(decision FanInDecision) bool {
		return strings.Contains(decision.Reason, wanted)
	})
}
