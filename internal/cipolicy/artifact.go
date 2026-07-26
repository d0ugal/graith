package cipolicy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ArtifactSchemaVersion = 1
	ArtifactResultVersion = 1
)

const (
	ArtifactTypeNativeLibghostty = "native-libghostty"
	ArtifactTypeRelease          = "release"

	ArtifactFormatTar     = "tar"
	ArtifactFormatTarGzip = "tar.gz"

	ArtifactFileRegular = "file"
	ArtifactFileSymlink = "symlink"

	producerStatusSuccess = "success"
)

const (
	tarBlockSize     = 512
	maxTarEOFPadding = 1 << 20
)

type IdentityDigest struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type BuildFlag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ArtifactFile struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Mode       int64  `json:"mode"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	LinkTarget string `json:"link_target,omitempty"`
}

type ArtifactProvenance struct {
	Workflow       string `json:"workflow"`
	WorkflowSHA256 string `json:"workflow_sha256"`
	RunID          int64  `json:"run_id"`
	RunAttempt     int    `json:"run_attempt"`
	JobID          string `json:"job_id"`
	JobName        string `json:"job_name"`
	ProducerStatus string `json:"producer_status"`
	UploadComplete bool   `json:"upload_complete"`
	ArtifactID     string `json:"artifact_id"`
	ArtifactDigest string `json:"artifact_digest"`
}

type ArtifactProducerResult struct {
	SchemaVersion     int                       `json:"schema_version"`
	ResultDigest      string                    `json:"result_digest"`
	PlanDigest        string                    `json:"plan_digest"`
	PolicyVersion     string                    `json:"policy_version"`
	PolicyDigest      string                    `json:"policy_digest"`
	DetectorVersion   string                    `json:"detector_version"`
	DetectorDigest    string                    `json:"detector_digest"`
	Source            SourceRevision            `json:"source"`
	Event             EventSelection            `json:"event"`
	TrustTier         string                    `json:"trust_tier"`
	Mode              string                    `json:"mode"`
	Coordinate        string                    `json:"coordinate"`
	Capability        string                    `json:"capability"`
	Platform          string                    `json:"platform"`
	CostClass         string                    `json:"cost_class"`
	Requiredness      string                    `json:"requiredness"`
	Owner             string                    `json:"owner"`
	Matrix            map[string]string         `json:"matrix"`
	Attempts          []ArtifactProducerAttempt `json:"attempts"`
	FirstStatus       string                    `json:"first_status"`
	FirstFailureClass string                    `json:"first_failure_class"`
	Status            string                    `json:"status"`
	FailureClass      string                    `json:"failure_class"`
	StartedAt         time.Time                 `json:"started_at"`
	CompletedAt       time.Time                 `json:"completed_at"`
	EvidenceDigest    string                    `json:"evidence_digest"`
	ArtifactDigest    string                    `json:"artifact_digest"`
	SupersededBy      string                    `json:"superseded_by"`
}

type ArtifactProducerAttempt struct {
	Attempt        int       `json:"attempt"`
	Status         string    `json:"status"`
	FailureClass   string    `json:"failure_class"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at"`
	EvidenceDigest string    `json:"evidence_digest"`
	ArtifactDigest string    `json:"artifact_digest"`
}

type ArtifactContractManifest struct {
	SchemaVersion  int    `json:"schema_version"`
	ManifestDigest string `json:"manifest_digest"`
	ArtifactType   string `json:"artifact_type"`
	ArtifactID     string `json:"artifact_id"`
	ArtifactFormat string `json:"artifact_format"`
	ArtifactDigest string `json:"artifact_digest"`

	PolicyVersion   string         `json:"policy_version"`
	PolicyDigest    string         `json:"policy_digest"`
	PlanDigest      string         `json:"plan_digest"`
	ResultDigest    string         `json:"result_digest"`
	DetectorVersion string         `json:"detector_version"`
	DetectorDigest  string         `json:"detector_digest"`
	Source          SourceRevision `json:"source"`
	Event           EventSelection `json:"event"`
	TrustTier       string         `json:"trust_tier"`

	Mode         string            `json:"mode"`
	Coordinate   string            `json:"coordinate"`
	Capability   string            `json:"capability"`
	Platform     string            `json:"platform"`
	OS           string            `json:"os"`
	Architecture string            `json:"architecture"`
	CostClass    string            `json:"cost_class"`
	Requiredness string            `json:"requiredness"`
	Matrix       map[string]string `json:"matrix"`

	Dependencies []IdentityDigest   `json:"dependencies"`
	Toolchains   []IdentityDigest   `json:"toolchains"`
	BuildFlags   []BuildFlag        `json:"build_flags"`
	Files        []ArtifactFile     `json:"files"`
	Provenance   ArtifactProvenance `json:"provenance"`
}

type ArtifactManifestInput struct {
	ArtifactType   string
	ArtifactID     string
	ArtifactFormat string
	ArtifactDigest string
	Dependencies   []IdentityDigest
	Toolchains     []IdentityDigest
	BuildFlags     []BuildFlag
	Files          []ArtifactFile
	Provenance     ArtifactProvenance
}

type ArtifactVerificationOptions struct {
	ArtifactType       string
	ArtifactID         string
	ArtifactDigest     string
	ProducerMode       string
	ProducerCoordinate string
	ConsumerPlan       RunPlan
	ConsumerJob        PlanJob
	Workflow           string
	RunID              int64
	RunAttempt         int
}

type producerProvenanceIdentity struct {
	Workflow       string
	WorkflowSHA256 string
	RunID          int64
	RunAttempt     int
	JobID          string
	JobName        string
	ProducerStatus string
	UploadComplete bool
}

type policyProducerTrace struct {
	Workflow       string
	WorkflowSHA256 string
	JobID          string
	JobName        string
}

type verifiedArchiveEntry struct {
	file ArtifactFile
	data []byte
}

type artifactTarStream struct {
	reader *tar.Reader
	finish func() error
	close  func()
}

func ReadArtifactManifest(path string) (ArtifactContractManifest, error) {
	var artifact ArtifactContractManifest
	if err := readStrictJSON(path, &artifact); err != nil {
		return ArtifactContractManifest{}, err
	}

	return artifact, nil
}

func DecodeArtifactManifest(name string, data []byte) (ArtifactContractManifest, error) {
	var artifact ArtifactContractManifest
	if err := decodeStrictJSON(name, data, &artifact); err != nil {
		return ArtifactContractManifest{}, err
	}

	return artifact, nil
}

func NewArtifactProducerResult(plan RunPlan, job PlanJob, attempts []ArtifactProducerAttempt) (ArtifactProducerResult, error) {
	if len(attempts) == 0 {
		return ArtifactProducerResult{}, errors.New("artifact producer result requires at least one attempt")
	}

	first := attempts[0]
	final := attempts[len(attempts)-1]

	result := ArtifactProducerResult{
		SchemaVersion:     ArtifactResultVersion,
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
		Attempts:          append([]ArtifactProducerAttempt(nil), attempts...),
		FirstStatus:       first.Status,
		FirstFailureClass: first.FailureClass,
		Status:            final.Status,
		FailureClass:      final.FailureClass,
		StartedAt:         first.StartedAt,
		CompletedAt:       final.CompletedAt,
		EvidenceDigest:    final.EvidenceDigest,
		ArtifactDigest:    final.ArtifactDigest,
	}

	if err := validateArtifactProducerOutcome(result.Status, result.FailureClass, result.SupersededBy); err != nil {
		return ArtifactProducerResult{}, err
	}

	digest, err := result.Digest()
	if err != nil {
		return ArtifactProducerResult{}, err
	}

	result.ResultDigest = digest

	return result, nil
}

func NewArtifactManifest(policy Manifest, plan RunPlan, result ArtifactProducerResult, input ArtifactManifestInput) (ArtifactContractManifest, error) {
	format := input.ArtifactFormat
	if format == "" {
		format = ArtifactFormatTar
	}

	platform, ok := platformByID(policy, result.Platform)
	if !ok {
		return ArtifactContractManifest{}, fmt.Errorf("artifact platform %s is not declared by policy", result.Platform)
	}

	artifact := ArtifactContractManifest{
		SchemaVersion:   ArtifactSchemaVersion,
		ArtifactType:    input.ArtifactType,
		ArtifactID:      input.ArtifactID,
		ArtifactFormat:  format,
		ArtifactDigest:  input.ArtifactDigest,
		PolicyVersion:   result.PolicyVersion,
		PolicyDigest:    result.PolicyDigest,
		PlanDigest:      result.PlanDigest,
		ResultDigest:    result.ResultDigest,
		DetectorVersion: result.DetectorVersion,
		DetectorDigest:  result.DetectorDigest,
		Source:          result.Source,
		Event:           result.Event,
		TrustTier:       result.TrustTier,
		Mode:            result.Mode,
		Coordinate:      result.Coordinate,
		Capability:      result.Capability,
		Platform:        result.Platform,
		OS:              platform.OS,
		Architecture:    platform.Architecture,
		CostClass:       result.CostClass,
		Requiredness:    result.Requiredness,
		Matrix:          cloneStringMap(result.Matrix),
		Dependencies:    append([]IdentityDigest(nil), input.Dependencies...),
		Toolchains:      append([]IdentityDigest(nil), input.Toolchains...),
		BuildFlags:      append([]BuildFlag(nil), input.BuildFlags...),
		Files:           append([]ArtifactFile(nil), input.Files...),
		Provenance:      input.Provenance,
	}

	if plan.PlanDigest != result.PlanDigest {
		return ArtifactContractManifest{}, errors.New("artifact result does not match plan identity")
	}

	digest, err := artifact.Digest()
	if err != nil {
		return ArtifactContractManifest{}, err
	}

	artifact = artifact.Canonical()
	artifact.ManifestDigest = digest

	return artifact, nil
}

func ValidateArtifactManifest(policy Manifest, plan RunPlan, result ArtifactProducerResult, artifact ArtifactContractManifest, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := validateArtifactManifestAt(policy, plan, result, artifact, now); err != nil {
		return err
	}

	return validateProducerNotInFuture("artifact", result, now)
}

func validateArtifactManifestAt(policy Manifest, plan RunPlan, result ArtifactProducerResult, artifact ArtifactContractManifest, now time.Time) error {
	if err := validateArtifactProducerResult(policy, plan, result, now); err != nil {
		return err
	}

	if result.Status != producerStatusSuccess {
		return fmt.Errorf("artifact producer result status %q is not success", result.Status)
	}

	if err := validateArtifactManifestStructure(policy, artifact); err != nil {
		return err
	}

	if artifact.PolicyVersion != plan.PolicyVersion ||
		artifact.PolicyDigest != plan.PolicyDigest ||
		artifact.PlanDigest != plan.PlanDigest ||
		artifact.DetectorVersion != plan.DetectorVersion ||
		artifact.DetectorDigest != plan.DetectorDigest ||
		!reflect.DeepEqual(artifact.Source, plan.Source) ||
		!reflect.DeepEqual(artifact.Event, plan.Event) ||
		artifact.TrustTier != plan.TrustTier {
		return errors.New("artifact binding does not match plan identity")
	}

	if artifact.ResultDigest != result.ResultDigest {
		return errors.New("artifact binding does not match result identity")
	}

	if artifact.ArtifactDigest != result.ArtifactDigest {
		return errors.New("artifact digest does not match result artifact digest")
	}

	if artifact.Mode != result.Mode ||
		artifact.Coordinate != result.Coordinate ||
		artifact.Capability != result.Capability ||
		artifact.Platform != result.Platform ||
		artifact.CostClass != result.CostClass ||
		artifact.Requiredness != result.Requiredness ||
		!reflect.DeepEqual(artifact.Matrix, result.Canonical().Matrix) {
		return errors.New("artifact coordinate identity does not match result")
	}

	if err := validateArtifactProvenance(policy, artifact); err != nil {
		return err
	}

	if err := validateArtifactProvenanceAttempt(result, artifact); err != nil {
		return err
	}

	return nil
}

func VerifyArtifactConsumer(
	policy Manifest,
	plan RunPlan,
	result ArtifactProducerResult,
	artifact ArtifactContractManifest,
	archive []byte,
	options ArtifactVerificationOptions,
	now time.Time,
) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := validateProducerNotInFuture("artifact", result, now); err != nil {
		return err
	}

	if err := validateArtifactManifestAt(policy, plan, result, artifact, producerValidationTime(result, now)); err != nil {
		return err
	}

	if err := validateArtifactExpectations(policy, artifact, options, now); err != nil {
		return err
	}

	return VerifyArtifactArchive(artifact, archive)
}

func VerifyArtifactArchive(artifact ArtifactContractManifest, archive []byte) error {
	_, err := verifiedArtifactEntries(artifact, archive, false)
	return err
}

func ExtractVerifiedArtifact(
	policy Manifest,
	plan RunPlan,
	result ArtifactProducerResult,
	artifact ArtifactContractManifest,
	archive []byte,
	destination string,
	options ArtifactVerificationOptions,
	now time.Time,
) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := validateProducerNotInFuture("artifact", result, now); err != nil {
		return err
	}

	if err := validateArtifactManifestAt(policy, plan, result, artifact, producerValidationTime(result, now)); err != nil {
		return err
	}

	if err := validateArtifactExpectations(policy, artifact, options, now); err != nil {
		return err
	}

	entries, err := verifiedArtifactEntries(artifact, archive, true)
	if err != nil {
		return err
	}

	return extractVerifiedEntries(destination, entries)
}

func producerValidationTime(result ArtifactProducerResult, fallback time.Time) time.Time {
	if !result.CompletedAt.IsZero() {
		return result.CompletedAt.UTC()
	}

	return fallback.UTC()
}

func validateProducerNotInFuture(kind string, result ArtifactProducerResult, now time.Time) error {
	if result.CompletedAt.IsZero() || !result.CompletedAt.After(now.Add(maxPlanClockSkew)) {
		return nil
	}

	return fmt.Errorf("%s producer result completed_at %s is after verification time %s", kind, result.CompletedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
}

func validateArtifactProducerResult(policy Manifest, plan RunPlan, result ArtifactProducerResult, now time.Time) error {
	if err := plan.ValidateAt(policy, now); err != nil {
		return err
	}

	job, ok := planJobByArtifactResult(plan, result)
	if !ok {
		return fmt.Errorf("artifact producer result references unknown mode/coordinate %s %s", result.Mode, result.Coordinate)
	}

	return validateArtifactProducerResultForJob(plan, job, result)
}

func (result ArtifactProducerResult) Canonical() ArtifactProducerResult {
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

func (result ArtifactProducerResult) Digest() (string, error) {
	canonical := result.Canonical()
	canonical.ResultDigest = ""

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}

	return sha256Hex(data), nil
}

func (result ArtifactProducerResult) copy() ArtifactProducerResult {
	clone := result
	clone.Matrix = cloneStringMap(result.Matrix)
	clone.Attempts = append([]ArtifactProducerAttempt(nil), result.Attempts...)

	return clone
}

func validateArtifactProducerResultForJob(plan RunPlan, job PlanJob, result ArtifactProducerResult) error {
	if result.SchemaVersion != ArtifactResultVersion {
		return fmt.Errorf("unsupported artifact producer result schema version %d", result.SchemaVersion)
	}

	canonicalResult := result.Canonical()

	digest, err := result.Digest()
	if err != nil {
		return err
	}

	if result.ResultDigest != digest {
		return fmt.Errorf("artifact producer result digest mismatch: got %s want %s", result.ResultDigest, digest)
	}

	if result.PlanDigest != plan.PlanDigest ||
		result.PolicyVersion != plan.PolicyVersion ||
		result.PolicyDigest != plan.PolicyDigest ||
		result.DetectorVersion != plan.DetectorVersion ||
		result.DetectorDigest != plan.DetectorDigest ||
		!reflect.DeepEqual(result.Source, plan.Source) ||
		!reflect.DeepEqual(result.Event, plan.Event) ||
		result.TrustTier != plan.TrustTier {
		return errors.New("stale artifact producer result binding does not match plan identity")
	}

	if result.Mode != job.Mode ||
		result.Coordinate != job.Coordinate ||
		result.Capability != job.Capability ||
		result.Platform != job.Platform ||
		result.CostClass != job.CostClass ||
		result.Requiredness != job.Requiredness ||
		result.Owner != job.Owner ||
		!reflect.DeepEqual(canonicalResult.Matrix, job.Matrix) {
		return fmt.Errorf("artifact producer result row %s/%s does not match plan coordinate identity", result.Mode, result.Coordinate)
	}

	if len(result.Attempts) == 0 {
		return fmt.Errorf("artifact producer result %s/%s has no attempts", result.Mode, result.Coordinate)
	}

	if result.StartedAt.IsZero() || result.CompletedAt.IsZero() || result.StartedAt.After(result.CompletedAt) {
		return fmt.Errorf("artifact producer result %s/%s has invalid timestamps", result.Mode, result.Coordinate)
	}

	for index, attempt := range result.Attempts {
		if attempt.Attempt != index+1 {
			return fmt.Errorf("artifact producer result %s/%s attempt history is not contiguous", result.Mode, result.Coordinate)
		}

		if err := validateArtifactProducerAttempt(result.Mode, result.Coordinate, attempt); err != nil {
			return err
		}
	}

	first := result.Attempts[0]
	final := result.Attempts[len(result.Attempts)-1]

	if result.FirstStatus != first.Status || result.FirstFailureClass != first.FailureClass {
		return fmt.Errorf("artifact producer result %s/%s does not preserve first attempt outcome", result.Mode, result.Coordinate)
	}

	if result.Status != final.Status || result.FailureClass != final.FailureClass {
		return fmt.Errorf("artifact producer result %s/%s final outcome does not match final attempt", result.Mode, result.Coordinate)
	}

	if !result.StartedAt.Equal(first.StartedAt) || !result.CompletedAt.Equal(final.CompletedAt) {
		return fmt.Errorf("artifact producer result %s/%s does not bind aggregate timestamps to attempts", result.Mode, result.Coordinate)
	}

	if result.EvidenceDigest != final.EvidenceDigest ||
		result.ArtifactDigest != final.ArtifactDigest {
		return fmt.Errorf("artifact producer result %s/%s aggregate digests do not match final attempt", result.Mode, result.Coordinate)
	}

	if err := validateArtifactProducerOutcome(result.Status, result.FailureClass, result.SupersededBy); err != nil {
		return fmt.Errorf("artifact producer result %s/%s: %w", result.Mode, result.Coordinate, err)
	}

	return nil
}

func validateArtifactProducerAttempt(mode, coordinate string, attempt ArtifactProducerAttempt) error {
	if !validArtifactProducerStatus(attempt.Status) {
		return fmt.Errorf("artifact producer result %s/%s has unrecognized attempt status %q", mode, coordinate, attempt.Status)
	}

	if attempt.StartedAt.IsZero() || attempt.CompletedAt.IsZero() || attempt.StartedAt.After(attempt.CompletedAt) {
		return fmt.Errorf("artifact producer result %s/%s attempt %d has invalid timestamps", mode, coordinate, attempt.Attempt)
	}

	if err := validateDigest("evidence", attempt.EvidenceDigest); err != nil {
		return fmt.Errorf("artifact producer result %s/%s attempt %d: %w", mode, coordinate, attempt.Attempt, err)
	}

	if err := validateDigest("artifact", attempt.ArtifactDigest); err != nil {
		return fmt.Errorf("artifact producer result %s/%s attempt %d: %w", mode, coordinate, attempt.Attempt, err)
	}

	if err := validateArtifactProducerAttemptOutcome(attempt.Status, attempt.FailureClass); err != nil {
		return fmt.Errorf("artifact producer result %s/%s attempt %d: %w", mode, coordinate, attempt.Attempt, err)
	}

	return nil
}

func validateArtifactProducerOutcome(status, failureClass, supersededBy string) error {
	if !validArtifactProducerStatus(status) {
		return fmt.Errorf("unrecognized status %q", status)
	}

	if status == "success" {
		if failureClass != "" {
			return errors.New("successful artifact producer result cannot have a failure class")
		}

		if supersededBy != "" {
			return errors.New("successful artifact producer result cannot be superseded")
		}

		return nil
	}

	if failureClass == "" {
		return fmt.Errorf("status %s requires a failure class", status)
	}

	if status == "superseded" && !digestPattern.MatchString(supersededBy) {
		return errors.New("superseded artifact producer result requires a supersession identity")
	}

	if status != "superseded" && supersededBy != "" {
		return fmt.Errorf("status %s cannot carry a supersession identity", status)
	}

	return nil
}

func validateArtifactProducerAttemptOutcome(status, failureClass string) error {
	if !validArtifactProducerStatus(status) {
		return fmt.Errorf("unrecognized status %q", status)
	}

	if status == "success" {
		if failureClass != "" {
			return errors.New("successful artifact producer attempt cannot have a failure class")
		}

		return nil
	}

	if failureClass == "" {
		return fmt.Errorf("status %s requires a failure class", status)
	}

	return nil
}

func validArtifactProducerStatus(status string) bool {
	switch status {
	case "success", "failed", "skipped", "cancelled", "stale", "superseded":
		return true
	default:
		return false
	}
}

func planJobByArtifactResult(plan RunPlan, result ArtifactProducerResult) (PlanJob, bool) {
	for _, job := range plan.Jobs {
		if job.Mode == result.Mode && job.Coordinate == result.Coordinate {
			return job, true
		}
	}

	return PlanJob{}, false
}

func (artifact ArtifactContractManifest) Canonical() ArtifactContractManifest {
	clone := artifact.copy()

	if clone.ArtifactFormat == "" {
		clone.ArtifactFormat = ArtifactFormatTar
	}

	if clone.Matrix == nil {
		clone.Matrix = map[string]string{}
	}

	clone.Dependencies = canonicalIdentityDigests(clone.Dependencies)
	clone.Toolchains = canonicalIdentityDigests(clone.Toolchains)
	clone.BuildFlags = canonicalBuildFlags(clone.BuildFlags)
	clone.Files = canonicalArtifactFiles(clone.Files)

	return clone
}

func (artifact ArtifactContractManifest) Digest() (string, error) {
	canonical := artifact.Canonical()
	canonical.ManifestDigest = ""

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}

	return sha256Hex(data), nil
}

func (artifact ArtifactContractManifest) MarshalCanonical() ([]byte, error) {
	canonical := artifact.Canonical()

	digest, err := canonical.Digest()
	if err != nil {
		return nil, err
	}

	canonical.ManifestDigest = digest

	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}

func (artifact ArtifactContractManifest) copy() ArtifactContractManifest {
	clone := artifact
	clone.Matrix = cloneStringMap(artifact.Matrix)
	clone.Dependencies = append([]IdentityDigest(nil), artifact.Dependencies...)
	clone.Toolchains = append([]IdentityDigest(nil), artifact.Toolchains...)
	clone.BuildFlags = append([]BuildFlag(nil), artifact.BuildFlags...)
	clone.Files = append([]ArtifactFile(nil), artifact.Files...)

	return clone
}

func readStrictJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return decodeStrictJSON(path, data, target)
}

func decodeStrictJSON(name string, data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(name, data); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: trailing JSON value", name)
		}

		return fmt.Errorf("decode %s: %w", name, err)
	}

	return nil
}

func rejectDuplicateJSONKeys(name string, data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValueForDuplicateKeys(decoder); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}

	return nil
}

func scanJSONValueForDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}

	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		seen := map[string]bool{}

		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}

			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}

			if seen[key] {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}

			seen[key] = true

			if err := scanJSONValueForDuplicateKeys(decoder); err != nil {
				return err
			}
		}

		end, err := decoder.Token()
		if err != nil {
			return err
		}

		if end != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValueForDuplicateKeys(decoder); err != nil {
				return err
			}
		}

		end, err := decoder.Token()
		if err != nil {
			return err
		}

		if end != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}

	return nil
}

func validateArtifactManifestStructure(policy Manifest, artifact ArtifactContractManifest) error {
	if artifact.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("unsupported artifact schema version %d", artifact.SchemaVersion)
	}

	if artifact.ManifestDigest == "" {
		return errors.New("artifact manifest digest is required")
	}

	digest, err := artifact.Digest()
	if err != nil {
		return err
	}

	if artifact.ManifestDigest != digest {
		return fmt.Errorf("artifact manifest digest mismatch: got %s want %s", artifact.ManifestDigest, digest)
	}

	if err := validateArtifactFiles(artifact.Files); err != nil {
		return err
	}

	if !reflect.DeepEqual(artifact, artifact.Canonical()) {
		return errors.New("artifact manifest is not canonical")
	}

	if !oneOf(artifact.ArtifactType, ArtifactTypeNativeLibghostty, ArtifactTypeRelease) {
		return fmt.Errorf("unsupported artifact type %q", artifact.ArtifactType)
	}

	if strings.TrimSpace(artifact.ArtifactID) != artifact.ArtifactID || artifact.ArtifactID == "" ||
		strings.ContainsAny(artifact.ArtifactID, "/\\\x00") {
		return fmt.Errorf("artifact ID %q is invalid", artifact.ArtifactID)
	}

	if !oneOf(artifact.ArtifactFormat, ArtifactFormatTar, ArtifactFormatTarGzip) {
		return fmt.Errorf("unsupported artifact format %q", artifact.ArtifactFormat)
	}

	if err := validateDigest("artifact", artifact.ArtifactDigest); err != nil {
		return err
	}

	if artifact.PolicyVersion != policy.PolicyVersion || artifact.PolicyDigest != policy.PolicyDigest {
		return errors.New("artifact policy identity does not match manifest")
	}

	platform, ok := platformByID(policy, artifact.Platform)
	if !ok {
		return fmt.Errorf("artifact platform %s is not declared by policy", artifact.Platform)
	}

	if artifact.OS != platform.OS || artifact.Architecture != platform.Architecture {
		return fmt.Errorf("artifact platform identity %s/%s does not match policy platform %s/%s", artifact.OS, artifact.Architecture, platform.OS, platform.Architecture)
	}

	if err := validateIdentityDigests("dependency", artifact.Dependencies); err != nil {
		return err
	}

	if err := validateIdentityDigests("toolchain", artifact.Toolchains); err != nil {
		return err
	}

	if err := validateBuildFlags(artifact.BuildFlags); err != nil {
		return err
	}

	return nil
}

func validateArtifactProvenance(policy Manifest, artifact ArtifactContractManifest) error {
	provenance := artifact.Provenance

	identity := producerIdentityFromParts(
		provenance.Workflow,
		provenance.WorkflowSHA256,
		provenance.RunID,
		provenance.RunAttempt,
		provenance.JobID,
		provenance.JobName,
		provenance.ProducerStatus,
		provenance.UploadComplete,
	)

	if err := validatePolicyProducerProvenance(policy, "artifact", artifact.Mode, artifact.Coordinate, identity); err != nil {
		return err
	}

	if provenance.ArtifactID != artifact.ArtifactID || provenance.ArtifactDigest != artifact.ArtifactDigest {
		return errors.New("artifact provenance does not match artifact identity")
	}

	return nil
}

func validateArtifactProvenanceAttempt(result ArtifactProducerResult, artifact ArtifactContractManifest) error {
	attempt := result.Attempts[len(result.Attempts)-1]
	if artifact.Provenance.RunAttempt != attempt.Attempt {
		return fmt.Errorf("artifact provenance run attempt %d does not match result attempt %d", artifact.Provenance.RunAttempt, attempt.Attempt)
	}

	return nil
}

func validateProducerProvenance(kind string, provenance producerProvenanceIdentity) error {
	if provenance.Workflow == "" || strings.TrimSpace(provenance.Workflow) != provenance.Workflow ||
		strings.HasPrefix(provenance.Workflow, "/") || strings.Contains(provenance.Workflow, "..") {
		return fmt.Errorf("%s provenance workflow %q is invalid", kind, provenance.Workflow)
	}

	if err := validateDigest("workflow", provenance.WorkflowSHA256); err != nil {
		return err
	}

	if provenance.RunID <= 0 {
		return fmt.Errorf("%s provenance requires a producer run ID", kind)
	}

	if provenance.RunAttempt <= 0 {
		return fmt.Errorf("%s provenance requires a producer run attempt", kind)
	}

	if provenance.JobID == "" || strings.TrimSpace(provenance.JobID) != provenance.JobID {
		return fmt.Errorf("%s provenance job ID %q is invalid", kind, provenance.JobID)
	}

	if provenance.JobName == "" || strings.TrimSpace(provenance.JobName) != provenance.JobName {
		return fmt.Errorf("%s provenance job name %q is invalid", kind, provenance.JobName)
	}

	if provenance.ProducerStatus != producerStatusSuccess {
		return fmt.Errorf("%s producer status %q is not success", kind, provenance.ProducerStatus)
	}

	if !provenance.UploadComplete {
		return fmt.Errorf("%s upload is incomplete", kind)
	}

	return nil
}

func producerIdentityFromParts(
	workflow string,
	workflowSHA256 string,
	runID int64,
	runAttempt int,
	jobID string,
	jobName string,
	producerStatus string,
	uploadComplete bool,
) producerProvenanceIdentity {
	return producerProvenanceIdentity{
		Workflow:       workflow,
		WorkflowSHA256: workflowSHA256,
		RunID:          runID,
		RunAttempt:     runAttempt,
		JobID:          jobID,
		JobName:        jobName,
		ProducerStatus: producerStatus,
		UploadComplete: uploadComplete,
	}
}

func validatePolicyProducerProvenance(policy Manifest, kind, modeID, coordinateID string, provenance producerProvenanceIdentity) error {
	if err := validateProducerProvenance(kind, provenance); err != nil {
		return err
	}

	return validateProducerTrace(policy, modeID, coordinateID, policyProducerTrace{
		Workflow:       provenance.Workflow,
		WorkflowSHA256: provenance.WorkflowSHA256,
		JobID:          provenance.JobID,
		JobName:        provenance.JobName,
	})
}

func validateArtifactExpectations(policy Manifest, artifact ArtifactContractManifest, options ArtifactVerificationOptions, now time.Time) error {
	if options.ArtifactType == "" {
		return errors.New("artifact consumer must declare an expected artifact type")
	}

	if options.ArtifactID == "" {
		return errors.New("artifact consumer must declare an expected artifact ID")
	}

	if options.ArtifactDigest == "" {
		return errors.New("artifact consumer must declare an expected artifact digest")
	}

	if options.ProducerMode == "" {
		return errors.New("artifact consumer must declare an expected producer mode")
	}

	if options.ProducerCoordinate == "" {
		return errors.New("artifact consumer must declare an expected producer coordinate")
	}

	if options.ConsumerPlan.PlanDigest == "" {
		return errors.New("artifact consumer plan is required")
	}

	if options.ConsumerJob.Mode == "" || options.ConsumerJob.Coordinate == "" {
		return errors.New("artifact consumer job is required")
	}

	if options.Workflow == "" {
		return errors.New("artifact consumer must declare an expected workflow")
	}

	if options.RunID == 0 {
		return errors.New("artifact consumer must declare an expected run ID")
	}

	if options.RunAttempt == 0 {
		return errors.New("artifact consumer must declare an expected run attempt")
	}

	if err := options.ConsumerPlan.ValidateAt(policy, now); err != nil {
		return err
	}

	if options.ArtifactType != artifact.ArtifactType {
		return fmt.Errorf("artifact type %s does not match expected %s", artifact.ArtifactType, options.ArtifactType)
	}

	if options.ArtifactID != artifact.ArtifactID {
		return fmt.Errorf("artifact ID %s does not match expected %s", artifact.ArtifactID, options.ArtifactID)
	}

	if options.ArtifactDigest != artifact.ArtifactDigest {
		return fmt.Errorf("artifact digest %s does not match expected %s", artifact.ArtifactDigest, options.ArtifactDigest)
	}

	if options.ProducerMode != artifact.Mode {
		return fmt.Errorf("artifact producer mode %s does not match expected %s", artifact.Mode, options.ProducerMode)
	}

	if options.ProducerCoordinate != artifact.Coordinate {
		return fmt.Errorf("artifact producer coordinate %s does not match expected %s", artifact.Coordinate, options.ProducerCoordinate)
	}

	if artifact.PolicyVersion != options.ConsumerPlan.PolicyVersion ||
		artifact.PolicyDigest != options.ConsumerPlan.PolicyDigest ||
		artifact.DetectorVersion != options.ConsumerPlan.DetectorVersion ||
		artifact.DetectorDigest != options.ConsumerPlan.DetectorDigest {
		return errors.New("artifact consumer policy identity does not match artifact")
	}

	if !reflect.DeepEqual(artifact.Source, options.ConsumerPlan.Source) {
		return errors.New("artifact consumer source identity does not match artifact")
	}

	if artifact.TrustTier != options.ConsumerPlan.TrustTier {
		return fmt.Errorf("artifact trust tier %s cannot satisfy consumer tier %s", artifact.TrustTier, options.ConsumerPlan.TrustTier)
	}

	if !reflect.DeepEqual(artifact.Event, options.ConsumerPlan.Event) {
		return errors.New("artifact consumer event identity does not match artifact")
	}

	if !planContainsJob(options.ConsumerPlan, options.ConsumerJob) {
		return fmt.Errorf("artifact consumer job %s/%s is not in the consumer plan", options.ConsumerJob.Mode, options.ConsumerJob.Coordinate)
	}

	if options.Workflow != artifact.Provenance.Workflow {
		return fmt.Errorf("artifact workflow %s does not match expected %s", artifact.Provenance.Workflow, options.Workflow)
	}

	if options.RunID != artifact.Provenance.RunID {
		return fmt.Errorf("artifact run ID %d does not match expected %d", artifact.Provenance.RunID, options.RunID)
	}

	if options.RunAttempt != artifact.Provenance.RunAttempt {
		return fmt.Errorf("artifact run attempt %d does not match expected %d", artifact.Provenance.RunAttempt, options.RunAttempt)
	}

	return nil
}

func validateProducerTrace(policy Manifest, modeID, coordinateID string, actual policyProducerTrace) error {
	expected, ok := producerTraceFor(policy, modeID, coordinateID)
	if !ok {
		return fmt.Errorf("producer trace for %s/%s is not declared by policy", modeID, coordinateID)
	}

	if actual.Workflow != expected.Workflow {
		return fmt.Errorf("producer workflow %s does not match policy workflow %s", actual.Workflow, expected.Workflow)
	}

	if actual.WorkflowSHA256 != expected.WorkflowSHA256 {
		return fmt.Errorf("producer workflow digest %s does not match policy digest %s", actual.WorkflowSHA256, expected.WorkflowSHA256)
	}

	if actual.JobID != expected.JobID {
		return fmt.Errorf("producer job ID %s does not match policy job ID %s", actual.JobID, expected.JobID)
	}

	if actual.JobName != expected.JobName {
		return fmt.Errorf("producer job name %s does not match policy job name %s", actual.JobName, expected.JobName)
	}

	return nil
}

func producerTraceFor(policy Manifest, modeID, coordinateID string) (policyProducerTrace, bool) {
	for _, mode := range policy.Canonical().Modes {
		if mode.ID != modeID {
			continue
		}

		for _, coordinate := range mode.Coordinates {
			if coordinate.ID != coordinateID {
				continue
			}

			trace := coordinate.Trace
			if trace.WorkflowPath == "" {
				trace = mode.Trace
			}

			return policyProducerTrace{
				Workflow:       trace.WorkflowPath,
				WorkflowSHA256: trace.WorkflowSHA256,
				JobID:          trace.LegacyJob,
				JobName:        coordinate.GitHubName,
			}, true
		}
	}

	return policyProducerTrace{}, false
}

func verifiedArtifactEntries(artifact ArtifactContractManifest, archive []byte, collectData bool) ([]verifiedArchiveEntry, error) {
	if err := validateArtifactFiles(artifact.Files); err != nil {
		return nil, err
	}

	if got := sha256Hex(archive); got != artifact.ArtifactDigest {
		return nil, fmt.Errorf("artifact archive digest mismatch: got %s want %s", got, artifact.ArtifactDigest)
	}

	files := artifact.Canonical().Files

	if err := validateArtifactTarPhysicalMembers(artifact.ArtifactFormat, archive, files); err != nil {
		return nil, err
	}

	stream, err := artifactTarReader(artifact.ArtifactFormat, archive)
	if err != nil {
		return nil, err
	}
	defer stream.close()

	seen := map[string]bool{}
	entries := make([]verifiedArchiveEntry, 0, len(files))

	for index := 0; ; index++ {
		header, err := stream.reader.Next()
		if errors.Is(err, io.EOF) {
			if index != len(files) {
				return nil, fmt.Errorf("artifact archive is missing %d member(s)", len(files)-index)
			}

			if err := stream.finish(); err != nil {
				return nil, err
			}

			return entries, nil
		}

		if err != nil {
			return nil, fmt.Errorf("read artifact archive member: %w", err)
		}

		if index >= len(files) {
			return nil, fmt.Errorf("artifact archive has extra member %q", header.Name)
		}

		expected := files[index]

		if err := validateArtifactPath(header.Name); err != nil {
			return nil, fmt.Errorf("artifact archive member %q is invalid: %w", header.Name, err)
		}

		if seen[header.Name] {
			return nil, fmt.Errorf("artifact archive has duplicate member %q", header.Name)
		}

		seen[header.Name] = true

		if header.Name != expected.Path {
			return nil, fmt.Errorf("artifact member order mismatch at %d: got %q want %q", index, header.Name, expected.Path)
		}

		entry, err := verifiedEntryFromHeader(stream.reader, header, expected, collectData)
		if err != nil {
			return nil, err
		}

		entries = append(entries, entry)
	}
}

func canonicalArtifactFiles(files []ArtifactFile) []ArtifactFile {
	clone := append([]ArtifactFile(nil), files...)

	sort.Slice(clone, func(i, j int) bool {
		return clone[i].Path < clone[j].Path
	})

	return clone
}

func artifactTarReader(format string, archive []byte) (*artifactTarStream, error) {
	switch format {
	case ArtifactFormatTar:
		raw := bytes.NewReader(archive)

		return &artifactTarStream{
			reader: tar.NewReader(raw),
			finish: func() error {
				return validateTarEOFPadding(raw)
			},
			close: func() {},
		}, nil
	case ArtifactFormatTarGzip:
		raw := bytes.NewReader(archive)

		gzipReader, err := gzip.NewReader(raw)
		if err != nil {
			return nil, fmt.Errorf("open gzip artifact: %w", err)
		}

		gzipReader.Multistream(false)

		return &artifactTarStream{
			reader: tar.NewReader(gzipReader),
			finish: func() error {
				if err := validateTarEOFPadding(gzipReader); err != nil {
					return err
				}

				if raw.Len() != 0 {
					return errors.New("artifact archive has trailing data after gzip stream")
				}

				return nil
			},
			close: func() { _ = gzipReader.Close() },
		}, nil
	default:
		return nil, fmt.Errorf("unsupported artifact format %q", format)
	}
}

func validateArtifactTarPhysicalMembers(format string, archive []byte, files []ArtifactFile) error {
	switch format {
	case ArtifactFormatTar:
		return validateTarPhysicalMembers(bytes.NewReader(archive), files)
	case ArtifactFormatTarGzip:
		raw := bytes.NewReader(archive)

		gzipReader, err := gzip.NewReader(raw)
		if err != nil {
			return fmt.Errorf("open gzip artifact: %w", err)
		}
		defer func() { _ = gzipReader.Close() }()

		gzipReader.Multistream(false)

		if err := validateTarPhysicalMembers(gzipReader, files); err != nil {
			return err
		}

		if raw.Len() != 0 {
			return errors.New("artifact archive has trailing data after gzip stream")
		}

		return nil
	default:
		return fmt.Errorf("unsupported artifact format %q", format)
	}
}

func validateTarPhysicalMembers(reader io.Reader, files []ArtifactFile) error {
	var block [tarBlockSize]byte

	for index := 0; ; index++ {
		if err := readTarBlock(reader, block[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return errors.New("artifact archive ended before end-of-archive marker")
			}

			return err
		}

		if isZeroTarBlock(block[:]) {
			if err := readTarBlock(reader, block[:]); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return errors.New("artifact archive ended before complete end-of-archive marker")
				}

				return err
			}

			if !isZeroTarBlock(block[:]) {
				return errors.New("artifact archive has trailing data after end-of-archive marker")
			}

			if index != len(files) {
				return fmt.Errorf("artifact archive is missing %d member(s)", len(files)-index)
			}

			return validateTarEOFPadding(reader)
		}

		if name, ok := unsupportedTarExtensionRecordName(block[156]); ok {
			return fmt.Errorf("artifact archive uses %s metadata record at physical member %d", name, index)
		}

		if index >= len(files) {
			return fmt.Errorf("artifact archive has extra member at physical member %d", index)
		}

		expected := files[index]

		name := parseTarPhysicalPath(block[:])
		if err := validateArtifactPath(name); err != nil {
			return fmt.Errorf("artifact archive member %q is invalid: %w", name, err)
		}

		if name != expected.Path {
			return fmt.Errorf("artifact member order mismatch at %d: got %q want %q", index, name, expected.Path)
		}

		switch block[156] {
		case tar.TypeReg, 0, tar.TypeSymlink:
		case tar.TypeLink:
			return fmt.Errorf("artifact member %s is a hardlink; hardlinks are not safe to extract", expected.Path)
		default:
			return fmt.Errorf("artifact member %s has unsupported type %q", expected.Path, string(block[156]))
		}

		size, err := parseTarPhysicalSize(block[124:136])
		if err != nil {
			return fmt.Errorf("artifact archive member %d has malformed size: %w", index, err)
		}

		if size != expected.Size {
			return fmt.Errorf("artifact member %s archive size mismatch: got %d want %d", expected.Path, size, expected.Size)
		}

		if err := skipTarPhysicalPayload(reader, size); err != nil {
			return fmt.Errorf("read artifact archive member %d payload: %w", index, err)
		}
	}
}

func readTarBlock(reader io.Reader, block []byte) error {
	if _, err := io.ReadFull(reader, block); err != nil {
		return err
	}

	return nil
}

func isZeroTarBlock(block []byte) bool {
	for _, value := range block {
		if value != 0 {
			return false
		}
	}

	return true
}

func parseTarPhysicalPath(block []byte) string {
	name := parseTarPhysicalString(block[0:100])
	if !hasUSTARPhysicalPrefix(block) {
		return name
	}

	prefix := parseTarPhysicalString(block[345:500])
	if prefix == "" {
		return name
	}

	return prefix + "/" + name
}

func hasUSTARPhysicalPrefix(block []byte) bool {
	return bytes.Equal(block[257:263], []byte("ustar\x00")) && bytes.Equal(block[263:265], []byte("00"))
}

func parseTarPhysicalString(field []byte) string {
	if index := bytes.IndexByte(field, 0); index >= 0 {
		field = field[:index]
	}

	return string(field)
}

func unsupportedTarExtensionRecordName(typeflag byte) (string, bool) {
	switch typeflag {
	case tar.TypeXHeader:
		return "PAX extended header", true
	case tar.TypeXGlobalHeader:
		return "PAX global header", true
	case tar.TypeGNULongName:
		return "GNU long name", true
	case tar.TypeGNULongLink:
		return "GNU long link", true
	case tar.TypeGNUSparse:
		return "GNU sparse file", true
	default:
		return "", false
	}
}

func parseTarPhysicalSize(field []byte) (int64, error) {
	if len(field) > 0 && field[0]&0x80 != 0 {
		var invert byte
		if field[0]&0x40 != 0 {
			invert = 0xff
		}

		var value uint64

		for index, digit := range field {
			digit ^= invert
			if index == 0 {
				digit &= 0x7f
			}

			if value>>56 != 0 {
				return 0, errors.New("size overflows int64")
			}

			value = value<<8 | uint64(digit)
		}

		if value>>63 != 0 {
			return 0, errors.New("size overflows int64")
		}

		if invert == 0xff {
			return 0, errors.New("negative size is not supported")
		}

		return int64(value), nil
	}

	trimmed := bytes.Trim(field, " \x00")
	if len(trimmed) == 0 {
		return 0, nil
	}

	size, err := strconv.ParseInt(string(trimmed), 8, 64)
	if err != nil {
		return 0, err
	}

	if size < 0 {
		return 0, errors.New("negative size is not supported")
	}

	return size, nil
}

func skipTarPhysicalPayload(reader io.Reader, size int64) error {
	if _, err := io.CopyN(io.Discard, reader, size); err != nil {
		return err
	}

	padding := (tarBlockSize - (size % tarBlockSize)) % tarBlockSize
	if padding == 0 {
		return nil
	}

	_, err := io.CopyN(io.Discard, reader, padding)

	return err
}

func validateTarEOFPadding(reader io.Reader) error {
	padding, err := io.ReadAll(io.LimitReader(reader, maxTarEOFPadding+1))
	if err != nil {
		return fmt.Errorf("finish artifact archive: %w", err)
	}

	if len(padding) > maxTarEOFPadding {
		return errors.New("artifact archive has excessive trailing padding after end-of-archive marker")
	}

	if len(padding) == 0 {
		return nil
	}

	if len(padding)%tarBlockSize != 0 {
		return errors.New("artifact archive has trailing data after end-of-archive marker")
	}

	for _, value := range padding {
		if value != 0 {
			return errors.New("artifact archive has trailing data after end-of-archive marker")
		}
	}

	return nil
}

func verifiedEntryFromHeader(reader io.Reader, header *tar.Header, expected ArtifactFile, collectData bool) (verifiedArchiveEntry, error) {
	if err := validateArtifactTarHeader(header, expected.Path); err != nil {
		return verifiedArchiveEntry{}, err
	}

	if header.Mode != expected.Mode {
		return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s mode mismatch: got %#o want %#o", expected.Path, header.Mode, expected.Mode)
	}

	switch header.Typeflag {
	case tar.TypeReg:
		if expected.Kind != ArtifactFileRegular {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s kind mismatch: got file want %s", expected.Path, expected.Kind)
		}

		if header.Size != expected.Size {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s size mismatch: got %d want %d", expected.Path, header.Size, expected.Size)
		}

		hash := sha256.New()

		var (
			writer io.Writer = hash
			buffer bytes.Buffer
		)

		if collectData {
			writer = io.MultiWriter(hash, &buffer)
		}

		written, err := io.Copy(writer, reader)
		if err != nil {
			return verifiedArchiveEntry{}, fmt.Errorf("read artifact member %s: %w", expected.Path, err)
		}

		if written != expected.Size {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s short read: got %d want %d", expected.Path, written, expected.Size)
		}

		if got := hex.EncodeToString(hash.Sum(nil)); got != expected.SHA256 {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s checksum mismatch: got %s want %s", expected.Path, got, expected.SHA256)
		}

		return verifiedArchiveEntry{file: expected, data: buffer.Bytes()}, nil
	case tar.TypeSymlink:
		if expected.Kind != ArtifactFileSymlink {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s kind mismatch: got symlink want %s", expected.Path, expected.Kind)
		}

		if header.Size != 0 {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact symlink %s size mismatch: got archive size %d want 0", expected.Path, header.Size)
		}

		if err := validateSymlinkTarget(expected.Path, header.Linkname); err != nil {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact symlink %s target is invalid: %w", expected.Path, err)
		}

		if header.Linkname != expected.LinkTarget {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact symlink %s target mismatch: got %q want %q", expected.Path, header.Linkname, expected.LinkTarget)
		}

		if expected.Size != 0 {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact symlink %s size mismatch: got manifest size %d want 0", expected.Path, expected.Size)
		}

		if got := sha256Hex([]byte(header.Linkname)); got != expected.SHA256 {
			return verifiedArchiveEntry{}, fmt.Errorf("artifact symlink %s checksum mismatch: got %s want %s", expected.Path, got, expected.SHA256)
		}

		return verifiedArchiveEntry{file: expected}, nil
	case tar.TypeLink:
		return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s is a hardlink; hardlinks are not safe to extract", expected.Path)
	default:
		return verifiedArchiveEntry{}, fmt.Errorf("artifact member %s has unsupported type %q", expected.Path, string(header.Typeflag))
	}
}

func validateArtifactTarHeader(header *tar.Header, path string) error {
	if len(header.PAXRecords) != 0 {
		return fmt.Errorf("artifact member %s uses PAX extended metadata", path)
	}

	if header.Format == tar.FormatPAX {
		return fmt.Errorf("artifact member %s uses PAX format", path)
	}

	return nil
}

func extractVerifiedEntries(destination string, entries []verifiedArchiveEntry) error {
	root, err := filepath.Abs(destination)
	if err != nil {
		return err
	}

	if err := validateExtractionRoot(root); err != nil {
		return err
	}

	if err := preflightVerifiedEntries(root, entries); err != nil {
		return err
	}

	if err := ensureDirectoryPath(root, "artifact extraction destination"); err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.file.Kind != ArtifactFileRegular {
			continue
		}

		if err := extractVerifiedRegular(root, entry); err != nil {
			return err
		}
	}

	for _, entry := range entries {
		if entry.file.Kind != ArtifactFileSymlink {
			continue
		}

		if err := extractVerifiedSymlink(root, entry.file); err != nil {
			return err
		}
	}

	return nil
}

func preflightVerifiedEntries(root string, entries []verifiedArchiveEntry) error {
	if err := validateExtractionPathCollisions(entries); err != nil {
		return err
	}

	for _, entry := range entries {
		target, err := safeDestinationPath(root, entry.file.Path)
		if err != nil {
			return err
		}

		if err := validateSafeParent(root, entry.file.Path); err != nil {
			return err
		}

		info, err := os.Lstat(target)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("artifact extraction target %q is a symlink", target)
			}

			return fmt.Errorf("artifact extraction target %q already exists", target)
		}

		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	return nil
}

func validateExtractionPathCollisions(entries []verifiedArchiveEntry) error {
	seenPaths := map[string]string{}
	seenPrefixes := map[string]string{}

	for _, entry := range entries {
		folded := foldArtifactPath(entry.file.Path)
		if previous, ok := seenPaths[folded]; ok && previous != entry.file.Path {
			return fmt.Errorf("artifact extraction paths %q and %q collide after case folding", previous, entry.file.Path)
		}

		if previous, ok := seenPrefixes[folded]; ok && previous != entry.file.Path {
			return fmt.Errorf("artifact extraction path %q conflicts with prefix path %q after case folding", entry.file.Path, previous)
		}

		seenPaths[folded] = entry.file.Path

		parts := strings.Split(entry.file.Path, "/")
		for i := 1; i < len(parts); i++ {
			prefixPath := strings.Join(parts[:i], "/")
			prefix := foldArtifactPath(prefixPath)

			if previous, ok := seenPaths[prefix]; ok && previous != prefixPath {
				return fmt.Errorf("artifact extraction path %q conflicts with prefix path %q after case folding", entry.file.Path, previous)
			}

			if previous, ok := seenPrefixes[prefix]; ok && previous != prefixPath {
				return fmt.Errorf("artifact extraction directory prefixes %q and %q collide after case folding", previous, prefixPath)
			}

			seenPrefixes[prefix] = prefixPath
		}
	}

	return nil
}

func foldArtifactPath(value string) string {
	return strings.ToLower(value)
}

func validateExtractionRoot(root string) error {
	info, err := os.Lstat(root)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact extraction destination %q is a symlink", root)
		}

		if !info.IsDir() {
			return fmt.Errorf("artifact extraction destination %q is not a directory", root)
		}

		if err := validateEmptyExtractionRoot(root); err != nil {
			return err
		}

		return validateExistingDestinationParent(root)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return validateExistingDestinationParent(root)
}

func validateEmptyExtractionRoot(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	if len(entries) != 0 {
		return fmt.Errorf("artifact extraction destination %q is not empty", root)
	}

	return nil
}

func validateExistingDestinationParent(path string) error {
	parent := filepath.Dir(filepath.Clean(path))
	if parent == filepath.Clean(path) {
		return nil
	}

	return validateExistingDirectoryComponents(parent, "artifact extraction destination parent")
}

func validateExistingDirectoryComponents(path, description string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	separator := string(filepath.Separator)
	rooted := filepath.IsAbs(clean)

	current := volume
	if strings.HasPrefix(rest, separator) {
		if current == "" {
			current = separator
		} else {
			current += separator
		}

		rest = strings.TrimPrefix(rest, separator)
	} else if current == "" {
		current = "."
	}

	depth := 0

	for _, part := range strings.Split(rest, separator) {
		if part == "" || part == "." {
			continue
		}

		current = filepath.Join(current, part)
		depth++

		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if rooted && depth == 1 {
					targetInfo, statErr := os.Stat(current)
					if statErr != nil {
						return statErr
					}

					if !targetInfo.IsDir() {
						return fmt.Errorf("%s %q is not a directory", description, current)
					}

					continue
				}

				return fmt.Errorf("%s %q is a symlink", description, current)
			}

			if !info.IsDir() {
				return fmt.Errorf("%s %q is not a directory", description, current)
			}

			continue
		}

		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}

	return nil
}

func extractVerifiedRegular(root string, entry verifiedArchiveEntry) error {
	target, err := safeDestinationPath(root, entry.file.Path)
	if err != nil {
		return err
	}

	if err := ensureSafeParent(root, entry.file.Path); err != nil {
		return err
	}

	mode, err := artifactFileMode(entry.file)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}

	_, writeErr := file.Write(entry.data)

	var chmodErr error
	if writeErr == nil {
		chmodErr = file.Chmod(mode)
	}

	closeErr := file.Close()

	if writeErr != nil {
		_ = os.Remove(target)
		return writeErr
	}

	if chmodErr != nil {
		_ = os.Remove(target)
		return chmodErr
	}

	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}

	return nil
}

func extractVerifiedSymlink(root string, file ArtifactFile) error {
	target, err := safeDestinationPath(root, file.Path)
	if err != nil {
		return err
	}

	if err := ensureSafeParent(root, file.Path); err != nil {
		return err
	}

	return os.Symlink(file.LinkTarget, target)
}

func validateSafeParent(root, relative string) error {
	parent := filepath.Dir(filepath.FromSlash(relative))
	if parent == "." {
		return nil
	}

	current := root

	for _, part := range strings.Split(parent, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}

		current = filepath.Join(current, part)

		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("artifact extraction parent %q is a symlink", current)
			}

			if !info.IsDir() {
				return fmt.Errorf("artifact extraction parent %q is not a directory", current)
			}

			continue
		}

		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}

	return nil
}

func ensureSafeParent(root, relative string) error {
	parent := filepath.Dir(filepath.FromSlash(relative))
	if parent == "." {
		return nil
	}

	current := root

	for _, part := range strings.Split(parent, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}

		current = filepath.Join(current, part)

		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("artifact extraction parent %q is a symlink", current)
			}

			if !info.IsDir() {
				return fmt.Errorf("artifact extraction parent %q is not a directory", current)
			}

			continue
		}

		if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		if err := os.Mkdir(current, 0o750); err != nil {
			if errors.Is(err, os.ErrExist) {
				info, statErr := os.Lstat(current)
				if statErr != nil {
					return statErr
				}

				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("artifact extraction parent %q is a symlink", current)
				}

				if !info.IsDir() {
					return fmt.Errorf("artifact extraction parent %q is not a directory", current)
				}

				continue
			}

			return err
		}
	}

	return nil
}

func ensureDirectoryPath(path, description string) error {
	if err := validateExistingDestinationParent(path); err != nil {
		return err
	}

	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s %q is a symlink", description, path)
		}

		if !info.IsDir() {
			return fmt.Errorf("%s %q is not a directory", description, path)
		}

		return nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parent := filepath.Dir(path)
	if parent == path {
		return err
	}

	if err := ensureDirectoryPath(parent, "artifact extraction destination parent"); err != nil {
		return err
	}

	if err := os.Mkdir(path, 0o750); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ensureDirectoryPath(path, description)
		}

		return err
	}

	return ensureDirectoryPath(path, description)
}

func safeDestinationPath(root, relative string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(relative))

	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}

	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("artifact extraction path %q escapes destination", relative)
	}

	return target, nil
}

func artifactFileMode(file ArtifactFile) (os.FileMode, error) {
	if file.Mode <= 0 || file.Mode&^int64(0o777) != 0 {
		return 0, fmt.Errorf("artifact file %s has invalid mode %#o", file.Path, file.Mode)
	}

	var mode os.FileMode
	for _, bit := range []struct {
		raw  int64
		mode os.FileMode
	}{
		{raw: 0o400, mode: 0o400},
		{raw: 0o200, mode: 0o200},
		{raw: 0o100, mode: 0o100},
		{raw: 0o040, mode: 0o040},
		{raw: 0o020, mode: 0o020},
		{raw: 0o010, mode: 0o010},
		{raw: 0o004, mode: 0o004},
		{raw: 0o002, mode: 0o002},
		{raw: 0o001, mode: 0o001},
	} {
		if file.Mode&bit.raw != 0 {
			mode |= bit.mode
		}
	}

	return mode, nil
}

func validateArtifactFiles(files []ArtifactFile) error {
	if len(files) == 0 {
		return errors.New("artifact manifest requires an exact file list")
	}

	kindByPath := map[string]string{}
	previous := ""

	for _, file := range files {
		if err := validateArtifactPath(file.Path); err != nil {
			return fmt.Errorf("artifact file %q is invalid: %w", file.Path, err)
		}

		if _, ok := kindByPath[file.Path]; ok {
			return fmt.Errorf("artifact file list has duplicate path %q", file.Path)
		}

		if previous != "" {
			if file.Path <= previous {
				return errors.New("artifact file list is not canonical")
			}
		}

		previous = file.Path

		if prefix, ok := declaredArtifactPathPrefix(file.Path, kindByPath); ok {
			return fmt.Errorf("artifact file %q conflicts with prefix path %q", file.Path, prefix)
		}

		kindByPath[file.Path] = file.Kind

		if file.Mode <= 0 || file.Mode&^int64(0o777) != 0 {
			return fmt.Errorf("artifact file %s has invalid mode %#o", file.Path, file.Mode)
		}

		if err := validateDigest("file", file.SHA256); err != nil {
			return fmt.Errorf("artifact file %s: %w", file.Path, err)
		}

		switch file.Kind {
		case ArtifactFileRegular:
			if file.Size < 0 {
				return fmt.Errorf("artifact file %s has negative size", file.Path)
			}

			if file.LinkTarget != "" {
				return fmt.Errorf("artifact file %s cannot have a link target", file.Path)
			}
		case ArtifactFileSymlink:
			if file.Size != 0 {
				return fmt.Errorf("artifact symlink %s must have size 0", file.Path)
			}

			if err := validateSymlinkTarget(file.Path, file.LinkTarget); err != nil {
				return fmt.Errorf("artifact symlink %s target is invalid: %w", file.Path, err)
			}
		default:
			return fmt.Errorf("artifact file %s has unsupported kind %q", file.Path, file.Kind)
		}
	}

	for _, file := range files {
		if file.Kind != ArtifactFileSymlink {
			continue
		}

		target, err := resolvedSymlinkTarget(file.Path, file.LinkTarget)
		if err != nil {
			return fmt.Errorf("artifact symlink %s target is invalid: %w", file.Path, err)
		}

		targetKind, ok := kindByPath[target]
		if !ok {
			return fmt.Errorf("artifact symlink %s target %q is not declared in the manifest", file.Path, target)
		}

		if targetKind != ArtifactFileRegular {
			return fmt.Errorf("artifact symlink %s target %q is not a regular file", file.Path, target)
		}
	}

	return nil
}

func declaredArtifactPathPrefix(value string, seen map[string]string) (string, bool) {
	parts := strings.Split(value, "/")
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		if _, ok := seen[prefix]; ok {
			return prefix, true
		}
	}

	return "", false
}

func validateArtifactPath(value string) error {
	if value == "" {
		return errors.New("path is empty")
	}

	if strings.ContainsAny(value, "\\\x00") {
		return errors.New("path contains an invalid character")
	}

	if path.IsAbs(value) || filepath.IsAbs(value) {
		return errors.New("absolute path")
	}

	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("path traverses outside the artifact root")
	}

	return nil
}

func validateSymlinkTarget(linkPath, target string) error {
	_, err := resolvedSymlinkTarget(linkPath, target)
	return err
}

func resolvedSymlinkTarget(linkPath, target string) (string, error) {
	if target == "" {
		return "", errors.New("target is empty")
	}

	if strings.ContainsAny(target, "\\\x00") {
		return "", errors.New("target contains an invalid character")
	}

	if path.IsAbs(target) || filepath.IsAbs(target) {
		return "", errors.New("absolute target")
	}

	clean := path.Clean(target)
	if clean != target || clean == "." {
		return "", errors.New("target traverses outside the artifact root")
	}

	resolved := path.Clean(path.Join(path.Dir(linkPath), clean))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", errors.New("target escapes the artifact root")
	}

	return resolved, nil
}

func validateIdentityDigests(kind string, values []IdentityDigest) error {
	if len(values) == 0 {
		return fmt.Errorf("%s identity list is required", kind)
	}

	seen := map[string]bool{}
	previous := ""

	for _, value := range values {
		if !validID(value.ID) {
			return fmt.Errorf("%s identity %q is invalid", kind, value.ID)
		}

		if value.Version == "" || strings.TrimSpace(value.Version) != value.Version {
			return fmt.Errorf("%s identity %s has invalid version %q", kind, value.ID, value.Version)
		}

		if err := validateDigest(kind, value.Digest); err != nil {
			return fmt.Errorf("%s identity %s: %w", kind, value.ID, err)
		}

		key := value.ID + "\x00" + value.Version
		if seen[key] {
			return fmt.Errorf("%s identity list has duplicate %s", kind, value.ID)
		}

		seen[key] = true

		if previous != "" && key <= previous {
			return fmt.Errorf("%s identity list is not canonical", kind)
		}

		previous = key
	}

	return nil
}

func validateBuildFlags(flags []BuildFlag) error {
	seen := map[string]bool{}
	previous := ""

	for _, flag := range flags {
		if !validID(flag.Name) {
			return fmt.Errorf("build flag %q is invalid", flag.Name)
		}

		if strings.TrimSpace(flag.Value) != flag.Value || strings.ContainsAny(flag.Value, "\x00") {
			return fmt.Errorf("build flag %s has invalid value %q", flag.Name, flag.Value)
		}

		if seen[flag.Name] {
			return fmt.Errorf("build flag list has duplicate %s", flag.Name)
		}

		seen[flag.Name] = true

		if previous != "" && flag.Name <= previous {
			return errors.New("build flag list is not canonical")
		}

		previous = flag.Name
	}

	if flags == nil {
		return errors.New("build flag list must be explicit")
	}

	return nil
}

func canonicalIdentityDigests(values []IdentityDigest) []IdentityDigest {
	canonical := append([]IdentityDigest(nil), values...)
	sort.Slice(canonical, func(i, j int) bool {
		left := canonical[i].ID + "\x00" + canonical[i].Version
		right := canonical[j].ID + "\x00" + canonical[j].Version

		return left < right
	})

	return canonical
}

func canonicalBuildFlags(values []BuildFlag) []BuildFlag {
	canonical := append([]BuildFlag(nil), values...)
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].Name < canonical[j].Name
	})

	if canonical == nil {
		return []BuildFlag{}
	}

	return canonical
}

func platformByID(manifest Manifest, id string) (Platform, bool) {
	for _, platform := range manifest.Platforms {
		if platform.ID == id {
			return platform, true
		}
	}

	return Platform{}, false
}

func planContainsJob(plan RunPlan, job PlanJob) bool {
	for _, candidate := range plan.Jobs {
		if candidate.Source == job.Source &&
			candidate.Event == job.Event &&
			candidate.TrustTier == job.TrustTier &&
			candidate.Mode == job.Mode &&
			candidate.Coordinate == job.Coordinate &&
			candidate.Capability == job.Capability &&
			candidate.Platform == job.Platform &&
			candidate.CostClass == job.CostClass &&
			candidate.Requiredness == job.Requiredness &&
			candidate.Owner == job.Owner &&
			candidate.GitHubName == job.GitHubName &&
			reflect.DeepEqual(candidate.Matrix, job.Matrix) &&
			reflect.DeepEqual(candidate.EvidenceRefs, job.EvidenceRefs) {
			return true
		}
	}

	return false
}

func validateDigest(kind, digest string) error {
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("%s digest %q is invalid", kind, digest)
	}

	return nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:])
}
