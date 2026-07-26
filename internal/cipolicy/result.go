package cipolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

const ResultSchemaVersion = 1

type ResultRecord struct {
	SchemaVersion     int               `json:"schema_version"`
	ResultDigest      string            `json:"result_digest"`
	PlanDigest        string            `json:"plan_digest"`
	PolicyVersion     string            `json:"policy_version"`
	PolicyDigest      string            `json:"policy_digest"`
	DetectorVersion   string            `json:"detector_version"`
	DetectorDigest    string            `json:"detector_digest"`
	Source            SourceRevision    `json:"source"`
	Event             EventSelection    `json:"event"`
	TrustTier         string            `json:"trust_tier"`
	Mode              string            `json:"mode"`
	Coordinate        string            `json:"coordinate"`
	Capability        string            `json:"capability"`
	Platform          string            `json:"platform"`
	CostClass         string            `json:"cost_class"`
	Requiredness      string            `json:"requiredness"`
	Owner             string            `json:"owner"`
	Matrix            map[string]string `json:"matrix"`
	Attempts          []ResultAttempt   `json:"attempts"`
	FirstStatus       string            `json:"first_status"`
	FirstFailureClass string            `json:"first_failure_class"`
	Status            string            `json:"status"`
	FailureClass      string            `json:"failure_class"`
	StartedAt         time.Time         `json:"started_at"`
	CompletedAt       time.Time         `json:"completed_at"`
	EvidenceDigest    string            `json:"evidence_digest"`
	ArtifactDigest    string            `json:"artifact_digest"`
	CacheDigest       string            `json:"cache_digest"`
	SupersededBy      string            `json:"superseded_by"`
}

type ResultAttempt struct {
	Attempt        int       `json:"attempt"`
	Status         string    `json:"status"`
	FailureClass   string    `json:"failure_class"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at"`
	EvidenceDigest string    `json:"evidence_digest"`
	ArtifactDigest string    `json:"artifact_digest"`
	CacheDigest    string    `json:"cache_digest"`
}

type FanInReport struct {
	SchemaVersion int             `json:"schema_version"`
	PlanDigest    string          `json:"plan_digest"`
	PolicyDigest  string          `json:"policy_digest"`
	Source        SourceRevision  `json:"source"`
	Event         EventSelection  `json:"event"`
	TrustTier     string          `json:"trust_tier"`
	Status        string          `json:"status"`
	Accepted      []FanInDecision `json:"accepted"`
	Rejected      []FanInDecision `json:"rejected"`
}

type FanInDecision struct {
	Mode       string `json:"mode"`
	Coordinate string `json:"coordinate"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

func NewResultRecord(plan RunPlan, job PlanJob, attempts []ResultAttempt) (ResultRecord, error) {
	return newResultRecord(plan, job, attempts, "")
}

func NewSupersededResultRecord(plan RunPlan, job PlanJob, attempts []ResultAttempt, supersededBy string) (ResultRecord, error) {
	return newResultRecord(plan, job, attempts, supersededBy)
}

func newResultRecord(plan RunPlan, job PlanJob, attempts []ResultAttempt, supersededBy string) (ResultRecord, error) {
	if len(attempts) == 0 {
		return ResultRecord{}, errors.New("result requires at least one attempt")
	}

	first := attempts[0]
	final := attempts[len(attempts)-1]

	result := ResultRecord{
		SchemaVersion:     ResultSchemaVersion,
		PlanDigest:        plan.PlanDigest,
		PolicyVersion:     plan.PolicyVersion,
		PolicyDigest:      plan.PolicyDigest,
		DetectorVersion:   plan.DetectorVersion,
		DetectorDigest:    plan.DetectorDigest,
		Source:            plan.Source,
		Event:             plan.Event,
		TrustTier:         plan.TrustTier,
		Mode:              job.Mode,
		Coordinate:        job.Coordinate,
		Capability:        job.Capability,
		Platform:          job.Platform,
		CostClass:         job.CostClass,
		Requiredness:      job.Requiredness,
		Owner:             job.Owner,
		Matrix:            cloneStringMap(job.Matrix),
		Attempts:          append([]ResultAttempt(nil), attempts...),
		FirstStatus:       first.Status,
		FirstFailureClass: first.FailureClass,
		Status:            final.Status,
		FailureClass:      final.FailureClass,
		StartedAt:         first.StartedAt,
		CompletedAt:       final.CompletedAt,
		EvidenceDigest:    final.EvidenceDigest,
		ArtifactDigest:    final.ArtifactDigest,
		CacheDigest:       final.CacheDigest,
		SupersededBy:      supersededBy,
	}

	if err := validateResultOutcome(result.Status, result.FailureClass, result.SupersededBy); err != nil {
		return ResultRecord{}, err
	}

	digest, err := result.Digest()
	if err != nil {
		return ResultRecord{}, err
	}

	result.ResultDigest = digest

	return result, nil
}

func FanIn(manifest Manifest, plan RunPlan, results []ResultRecord, now time.Time) (FanInReport, error) {
	if err := plan.ValidateAt(manifest, now); err != nil {
		return FanInReport{}, err
	}

	report := FanInReport{
		SchemaVersion: ResultSchemaVersion,
		PlanDigest:    plan.PlanDigest,
		PolicyDigest:  plan.PolicyDigest,
		Source:        plan.Source,
		Event:         plan.Event,
		TrustTier:     plan.TrustTier,
		Status:        "passed",
	}

	expected := make(map[string]PlanJob, len(plan.Jobs))
	for _, job := range plan.Jobs {
		expected[planCoordinateKey(job.Mode, job.Coordinate)] = job
	}

	seen := map[string]bool{}

	for _, result := range results {
		key := planCoordinateKey(result.Mode, result.Coordinate)

		job, ok := expected[key]
		if !ok {
			report.Status = "failed"
			report.Rejected = append(report.Rejected, FanInDecision{
				Mode:       result.Mode,
				Coordinate: result.Coordinate,
				Status:     result.Status,
				Reason:     "unknown-or-extra-result",
			})

			continue
		}

		if seen[key] {
			report.Status = "failed"
			report.Rejected = append(report.Rejected, FanInDecision{
				Mode:       result.Mode,
				Coordinate: result.Coordinate,
				Status:     result.Status,
				Reason:     "duplicate-result",
			})

			continue
		}

		seen[key] = true

		if err := validateResultForJob(plan, job, result); err != nil {
			report.Status = "failed"
			report.Rejected = append(report.Rejected, FanInDecision{
				Mode:       result.Mode,
				Coordinate: result.Coordinate,
				Status:     result.Status,
				Reason:     err.Error(),
			})

			continue
		}

		if result.Status != "success" {
			report.Status = "failed"
			report.Rejected = append(report.Rejected, FanInDecision{
				Mode:       result.Mode,
				Coordinate: result.Coordinate,
				Status:     result.Status,
				Reason:     "result-status-not-green",
			})

			continue
		}

		report.Accepted = append(report.Accepted, FanInDecision{
			Mode:       result.Mode,
			Coordinate: result.Coordinate,
			Status:     result.Status,
		})
	}

	for _, job := range plan.Jobs {
		if seen[planCoordinateKey(job.Mode, job.Coordinate)] {
			continue
		}

		report.Status = "failed"
		report.Rejected = append(report.Rejected, FanInDecision{
			Mode:       job.Mode,
			Coordinate: job.Coordinate,
			Status:     "missing",
			Reason:     "missing-result",
		})
	}

	sortFanInDecisions(report.Accepted)
	sortFanInDecisions(report.Rejected)

	if report.Status != "passed" {
		return report, fmt.Errorf("fan-in rejected %d result row(s)", len(report.Rejected))
	}

	return report, nil
}

func ValidateResultRecord(manifest Manifest, plan RunPlan, result ResultRecord, now time.Time) error {
	if err := plan.ValidateAt(manifest, now); err != nil {
		return err
	}

	job, ok := planJobByResult(plan, result)
	if !ok {
		return fmt.Errorf("result references unknown mode/coordinate %s %s", result.Mode, result.Coordinate)
	}

	return validateResultForJob(plan, job, result)
}

func (result ResultRecord) Canonical() ResultRecord {
	clone := result.copy()
	clone.StartedAt = clone.StartedAt.UTC()
	clone.CompletedAt = clone.CompletedAt.UTC()

	if clone.Matrix == nil {
		clone.Matrix = map[string]string{}
	}

	for index := range clone.Attempts {
		clone.Attempts[index].StartedAt = clone.Attempts[index].StartedAt.UTC()
		clone.Attempts[index].CompletedAt = clone.Attempts[index].CompletedAt.UTC()
	}

	return clone
}

func (result ResultRecord) Digest() (string, error) {
	canonical := result.Canonical()
	canonical.ResultDigest = ""

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:]), nil
}

func (result ResultRecord) MarshalCanonical() ([]byte, error) {
	canonical := result.Canonical()

	digest, err := canonical.Digest()
	if err != nil {
		return nil, err
	}

	canonical.ResultDigest = digest

	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}

func (result ResultRecord) copy() ResultRecord {
	clone := result
	clone.Matrix = cloneStringMap(result.Matrix)
	clone.Attempts = append([]ResultAttempt(nil), result.Attempts...)

	return clone
}

func validateResultForJob(plan RunPlan, job PlanJob, result ResultRecord) error {
	if result.SchemaVersion != ResultSchemaVersion {
		return fmt.Errorf("unsupported result schema version %d", result.SchemaVersion)
	}

	canonicalResult := result.Canonical()

	digest, err := result.Digest()
	if err != nil {
		return err
	}

	if result.ResultDigest != digest {
		return fmt.Errorf("result digest mismatch: got %s want %s", result.ResultDigest, digest)
	}

	if result.PlanDigest != plan.PlanDigest ||
		result.PolicyVersion != plan.PolicyVersion ||
		result.PolicyDigest != plan.PolicyDigest ||
		result.DetectorVersion != plan.DetectorVersion ||
		result.DetectorDigest != plan.DetectorDigest ||
		!reflect.DeepEqual(result.Source, plan.Source) ||
		!reflect.DeepEqual(result.Event, plan.Event) ||
		result.TrustTier != plan.TrustTier {
		return errors.New("stale result binding does not match plan identity")
	}

	if result.Mode != job.Mode ||
		result.Coordinate != job.Coordinate ||
		result.Capability != job.Capability ||
		result.Platform != job.Platform ||
		result.CostClass != job.CostClass ||
		result.Requiredness != job.Requiredness ||
		result.Owner != job.Owner ||
		!reflect.DeepEqual(canonicalResult.Matrix, job.Matrix) {
		return fmt.Errorf("result row %s/%s does not match plan coordinate identity", result.Mode, result.Coordinate)
	}

	if len(result.Attempts) == 0 {
		return fmt.Errorf("result %s/%s has no attempts", result.Mode, result.Coordinate)
	}

	if result.StartedAt.IsZero() || result.CompletedAt.IsZero() || result.StartedAt.After(result.CompletedAt) {
		return fmt.Errorf("result %s/%s has invalid timestamps", result.Mode, result.Coordinate)
	}

	for index, attempt := range result.Attempts {
		if attempt.Attempt != index+1 {
			return fmt.Errorf("result %s/%s attempt history is not contiguous", result.Mode, result.Coordinate)
		}

		if err := validateAttempt(result.Mode, result.Coordinate, attempt); err != nil {
			return err
		}
	}

	first := result.Attempts[0]
	final := result.Attempts[len(result.Attempts)-1]

	if result.FirstStatus != first.Status || result.FirstFailureClass != first.FailureClass {
		return fmt.Errorf("result %s/%s does not preserve first attempt outcome", result.Mode, result.Coordinate)
	}

	if result.Status != final.Status || result.FailureClass != final.FailureClass {
		return fmt.Errorf("result %s/%s final outcome does not match final attempt", result.Mode, result.Coordinate)
	}

	if !result.StartedAt.Equal(first.StartedAt) || !result.CompletedAt.Equal(final.CompletedAt) {
		return fmt.Errorf("result %s/%s does not bind aggregate timestamps to attempts", result.Mode, result.Coordinate)
	}

	if result.EvidenceDigest != final.EvidenceDigest ||
		result.ArtifactDigest != final.ArtifactDigest ||
		result.CacheDigest != final.CacheDigest {
		return fmt.Errorf("result %s/%s aggregate digests do not match final attempt", result.Mode, result.Coordinate)
	}

	if err := validateResultOutcome(result.Status, result.FailureClass, result.SupersededBy); err != nil {
		return fmt.Errorf("result %s/%s: %w", result.Mode, result.Coordinate, err)
	}

	return nil
}

func validateAttempt(mode, coordinate string, attempt ResultAttempt) error {
	if !validResultStatus(attempt.Status) {
		return fmt.Errorf("result %s/%s has unrecognized attempt status %q", mode, coordinate, attempt.Status)
	}

	if attempt.StartedAt.IsZero() || attempt.CompletedAt.IsZero() || attempt.StartedAt.After(attempt.CompletedAt) {
		return fmt.Errorf("result %s/%s attempt %d has invalid timestamps", mode, coordinate, attempt.Attempt)
	}

	if err := validateDigest("evidence", attempt.EvidenceDigest); err != nil {
		return fmt.Errorf("result %s/%s attempt %d: %w", mode, coordinate, attempt.Attempt, err)
	}

	if err := validateDigest("artifact", attempt.ArtifactDigest); err != nil {
		return fmt.Errorf("result %s/%s attempt %d: %w", mode, coordinate, attempt.Attempt, err)
	}

	if err := validateDigest("cache", attempt.CacheDigest); err != nil {
		return fmt.Errorf("result %s/%s attempt %d: %w", mode, coordinate, attempt.Attempt, err)
	}

	if err := validateAttemptOutcome(attempt.Status, attempt.FailureClass); err != nil {
		return fmt.Errorf("result %s/%s attempt %d: %w", mode, coordinate, attempt.Attempt, err)
	}

	return nil
}

func validateResultOutcome(status, failureClass, supersededBy string) error {
	if !validResultStatus(status) {
		return fmt.Errorf("unrecognized status %q", status)
	}

	if status == "success" {
		if failureClass != "" {
			return errors.New("successful result cannot have a failure class")
		}

		if supersededBy != "" {
			return errors.New("successful result cannot be superseded")
		}

		return nil
	}

	if failureClass == "" {
		return fmt.Errorf("status %s requires a failure class", status)
	}

	if status == "superseded" && !digestPattern.MatchString(supersededBy) {
		return errors.New("superseded result requires a supersession identity")
	}

	if status != "superseded" && supersededBy != "" {
		return fmt.Errorf("status %s cannot carry a supersession identity", status)
	}

	return nil
}

func validateAttemptOutcome(status, failureClass string) error {
	if !validResultStatus(status) {
		return fmt.Errorf("unrecognized status %q", status)
	}

	if status == "success" {
		if failureClass != "" {
			return errors.New("successful attempt cannot have a failure class")
		}

		return nil
	}

	if failureClass == "" {
		return fmt.Errorf("status %s requires a failure class", status)
	}

	return nil
}

func validateDigest(kind, digest string) error {
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("%s digest %q is invalid", kind, digest)
	}

	return nil
}

func validResultStatus(status string) bool {
	switch status {
	case "success", "failed", "skipped", "cancelled", "stale", "superseded":
		return true
	default:
		return false
	}
}

func planJobByResult(plan RunPlan, result ResultRecord) (PlanJob, bool) {
	for _, job := range plan.Jobs {
		if planCoordinateKey(job.Mode, job.Coordinate) == planCoordinateKey(result.Mode, result.Coordinate) {
			return job, true
		}
	}

	return PlanJob{}, false
}

func planCoordinateKey(mode, coordinate string) string {
	return strings.Join([]string{mode, coordinate}, "\x00")
}

func sortFanInDecisions(decisions []FanInDecision) {
	sort.Slice(decisions, func(i, j int) bool {
		left := strings.Join([]string{decisions[i].Mode, decisions[i].Coordinate, decisions[i].Status, decisions[i].Reason}, "\x00")
		right := strings.Join([]string{decisions[j].Mode, decisions[j].Coordinate, decisions[j].Status, decisions[j].Reason}, "\x00")

		return left < right
	})
}
