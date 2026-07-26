package cipolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

const CacheSchemaVersion = 1

const cacheKeyPrefix = "graith-ci-cache-v1:"

type CacheProvenance struct {
	Workflow       string `json:"workflow"`
	WorkflowSHA256 string `json:"workflow_sha256"`
	RunID          int64  `json:"run_id"`
	RunAttempt     int    `json:"run_attempt"`
	JobID          string `json:"job_id"`
	JobName        string `json:"job_name"`
	ProducerStatus string `json:"producer_status"`
	UploadComplete bool   `json:"upload_complete"`
	CacheKey       string `json:"cache_key"`
	CacheDigest    string `json:"cache_digest"`
}

type CacheManifest struct {
	SchemaVersion  int    `json:"schema_version"`
	ManifestDigest string `json:"manifest_digest"`
	CacheKey       string `json:"cache_key"`
	CacheKeyDigest string `json:"cache_key_digest"`
	CacheFormat    string `json:"cache_format"`
	CacheDigest    string `json:"cache_digest"`

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

	Dependencies []IdentityDigest `json:"dependencies"`
	Toolchains   []IdentityDigest `json:"toolchains"`
	BuildFlags   []BuildFlag      `json:"build_flags"`
	Files        []ArtifactFile   `json:"files"`
	Provenance   CacheProvenance  `json:"provenance"`
}

type CacheManifestInput struct {
	CacheFormat  string
	CacheDigest  string
	Dependencies []IdentityDigest
	Toolchains   []IdentityDigest
	BuildFlags   []BuildFlag
	Files        []ArtifactFile
	Provenance   CacheProvenance
}

type CacheReadOptions struct {
	Dependencies []IdentityDigest
	Toolchains   []IdentityDigest
	BuildFlags   []BuildFlag
}

type CacheKeyMaterial struct {
	SchemaVersion int              `json:"schema_version"`
	PolicyVersion string           `json:"policy_version"`
	PolicyDigest  string           `json:"policy_digest"`
	Source        SourceRevision   `json:"source"`
	Mode          string           `json:"mode"`
	Coordinate    string           `json:"coordinate"`
	Capability    string           `json:"capability"`
	Platform      string           `json:"platform"`
	OS            string           `json:"os"`
	Architecture  string           `json:"architecture"`
	Dependencies  []IdentityDigest `json:"dependencies"`
	Toolchains    []IdentityDigest `json:"toolchains"`
	BuildFlags    []BuildFlag      `json:"build_flags"`
}

func ReadCacheManifest(path string) (CacheManifest, error) {
	var cache CacheManifest
	if err := readStrictJSON(path, &cache); err != nil {
		return CacheManifest{}, err
	}

	return cache, nil
}

func DecodeCacheManifest(name string, data []byte) (CacheManifest, error) {
	var cache CacheManifest
	if err := decodeStrictJSON(name, data, &cache); err != nil {
		return CacheManifest{}, err
	}

	return cache, nil
}

func NewCacheManifest(policy Manifest, plan RunPlan, result ResultRecord, input CacheManifestInput) (CacheManifest, error) {
	format := input.CacheFormat
	if format == "" {
		format = ArtifactFormatTar
	}

	platform, ok := platformByID(policy, result.Platform)
	if !ok {
		return CacheManifest{}, fmt.Errorf("cache platform %s is not declared by policy", result.Platform)
	}

	cache := CacheManifest{
		SchemaVersion:   CacheSchemaVersion,
		CacheFormat:     format,
		CacheDigest:     input.CacheDigest,
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
		return CacheManifest{}, errors.New("cache result does not match plan identity")
	}

	keyDigest, err := cache.ExpectedKeyDigest()
	if err != nil {
		return CacheManifest{}, err
	}

	cache.CacheKeyDigest = keyDigest
	cache.CacheKey = cacheKeyPrefix + keyDigest

	if cache.Provenance.CacheKey == "" || cache.Provenance.CacheDigest == "" {
		return CacheManifest{}, errors.New("cache provenance cache key and digest are required")
	}

	digest, err := cache.Digest()
	if err != nil {
		return CacheManifest{}, err
	}

	cache = cache.Canonical()
	cache.ManifestDigest = digest

	return cache, nil
}

func ValidateCacheWrite(policy Manifest, plan RunPlan, result ResultRecord, cache CacheManifest, payload []byte, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := validateCacheWriteAt(policy, plan, result, cache, payload, now); err != nil {
		return err
	}

	return validateProducerNotInFuture("cache", result, now)
}

func validateCacheWriteAt(policy Manifest, plan RunPlan, result ResultRecord, cache CacheManifest, payload []byte, now time.Time) error {
	if err := validateCacheWriteMetadataAt(policy, plan, result, cache, now); err != nil {
		return err
	}

	return validateCachePayload(cache, payload)
}

func validateCacheWriteMetadataAt(policy Manifest, plan RunPlan, result ResultRecord, cache CacheManifest, now time.Time) error {
	if err := ValidateResultRecord(policy, plan, result, now); err != nil {
		return err
	}

	if result.Status != producerStatusSuccess {
		return fmt.Errorf("cache producer result status %q is not success", result.Status)
	}

	if err := validateCacheManifestStructure(policy, cache); err != nil {
		return err
	}

	if cache.PolicyVersion != plan.PolicyVersion ||
		cache.PolicyDigest != plan.PolicyDigest ||
		cache.PlanDigest != plan.PlanDigest ||
		cache.DetectorVersion != plan.DetectorVersion ||
		cache.DetectorDigest != plan.DetectorDigest ||
		!reflect.DeepEqual(cache.Source, plan.Source) ||
		!reflect.DeepEqual(cache.Event, plan.Event) ||
		cache.TrustTier != plan.TrustTier {
		return errors.New("cache binding does not match producer plan identity")
	}

	if cache.ResultDigest != result.ResultDigest {
		return errors.New("cache binding does not match producer result identity")
	}

	if cache.CacheDigest != result.CacheDigest {
		return errors.New("cache digest does not match result cache digest")
	}

	if err := validateCacheProvenance(policy, cache); err != nil {
		return err
	}

	if err := validateCacheProducerAttempt(result, cache); err != nil {
		return err
	}

	if cache.Mode != result.Mode ||
		cache.Coordinate != result.Coordinate ||
		cache.Capability != result.Capability ||
		cache.Platform != result.Platform ||
		cache.CostClass != result.CostClass ||
		cache.Requiredness != result.Requiredness ||
		!reflect.DeepEqual(cache.Matrix, result.Canonical().Matrix) {
		return errors.New("cache coordinate identity does not match result")
	}

	return nil
}

func validateCachePayload(cache CacheManifest, payload []byte) error {
	if got := sha256Hex(payload); got != cache.CacheDigest {
		return fmt.Errorf("cache checksum mismatch: got %s want %s", got, cache.CacheDigest)
	}

	if err := VerifyCachePayload(cache, payload); err != nil {
		return err
	}

	return nil
}

func VerifyCachePayload(cache CacheManifest, payload []byte) error {
	artifact := ArtifactContractManifest{
		ArtifactFormat: cache.CacheFormat,
		ArtifactDigest: cache.CacheDigest,
		Files:          append([]ArtifactFile(nil), cache.Files...),
	}

	if _, err := verifiedArtifactEntries(artifact, payload, false); err != nil {
		return fmt.Errorf("cache payload: %w", err)
	}

	return nil
}

func ValidateCacheRead(
	policy Manifest,
	producerPlan RunPlan,
	producerResult ResultRecord,
	consumerPlan RunPlan,
	consumerJob PlanJob,
	cache CacheManifest,
	payload []byte,
	options CacheReadOptions,
	now time.Time,
) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := validateProducerNotInFuture("cache", producerResult, now); err != nil {
		return err
	}

	if err := validateCacheWriteMetadataAt(policy, producerPlan, producerResult, cache, producerValidationTime(producerResult, now)); err != nil {
		return err
	}

	if err := consumerPlan.ValidateAt(policy, now); err != nil {
		return err
	}

	if !planContainsJob(consumerPlan, consumerJob) {
		return fmt.Errorf("cache consumer job %s/%s is not in the consumer plan", consumerJob.Mode, consumerJob.Coordinate)
	}

	if cache.TrustTier != consumerPlan.TrustTier {
		return fmt.Errorf("cache trust tier %s cannot satisfy read tier %s", cache.TrustTier, consumerPlan.TrustTier)
	}

	if !reflect.DeepEqual(cache.Event, consumerPlan.Event) {
		return errors.New("cache consumer event identity does not match cache")
	}

	if err := validateIdentityDigests("dependency", options.Dependencies); err != nil {
		return err
	}

	if err := validateIdentityDigests("toolchain", options.Toolchains); err != nil {
		return err
	}

	if err := validateBuildFlags(options.BuildFlags); err != nil {
		return err
	}

	platform, ok := platformByID(policy, consumerJob.Platform)
	if !ok {
		return fmt.Errorf("cache consumer platform %s is not declared by policy", consumerJob.Platform)
	}

	material := CacheKeyMaterial{
		SchemaVersion: CacheSchemaVersion,
		PolicyVersion: consumerPlan.PolicyVersion,
		PolicyDigest:  consumerPlan.PolicyDigest,
		Source:        consumerPlan.Source,
		Mode:          consumerJob.Mode,
		Coordinate:    consumerJob.Coordinate,
		Capability:    consumerJob.Capability,
		Platform:      consumerJob.Platform,
		OS:            platform.OS,
		Architecture:  platform.Architecture,
		Dependencies:  canonicalIdentityDigests(options.Dependencies),
		Toolchains:    canonicalIdentityDigests(options.Toolchains),
		BuildFlags:    canonicalBuildFlags(options.BuildFlags),
	}

	expectedDigest, err := CacheKeyDigest(material)
	if err != nil {
		return err
	}

	if cache.CacheKeyDigest != expectedDigest || cache.CacheKey != cacheKeyPrefix+expectedDigest {
		return errors.New("cache key does not match consumer identity")
	}

	return validateCachePayload(cache, payload)
}

func (cache CacheManifest) ExpectedKeyDigest() (string, error) {
	material := CacheKeyMaterial{
		SchemaVersion: CacheSchemaVersion,
		PolicyVersion: cache.PolicyVersion,
		PolicyDigest:  cache.PolicyDigest,
		Source:        cache.Source,
		Mode:          cache.Mode,
		Coordinate:    cache.Coordinate,
		Capability:    cache.Capability,
		Platform:      cache.Platform,
		OS:            cache.OS,
		Architecture:  cache.Architecture,
		Dependencies:  canonicalIdentityDigests(cache.Dependencies),
		Toolchains:    canonicalIdentityDigests(cache.Toolchains),
		BuildFlags:    canonicalBuildFlags(cache.BuildFlags),
	}

	return CacheKeyDigest(material)
}

func CacheKeyDigest(material CacheKeyMaterial) (string, error) {
	canonical := material
	canonical.Dependencies = canonicalIdentityDigests(material.Dependencies)
	canonical.Toolchains = canonicalIdentityDigests(material.Toolchains)
	canonical.BuildFlags = canonicalBuildFlags(material.BuildFlags)

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}

	return sha256Hex(data), nil
}

func (cache CacheManifest) Canonical() CacheManifest {
	clone := cache.copy()

	if clone.CacheFormat == "" {
		clone.CacheFormat = ArtifactFormatTar
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

func (cache CacheManifest) Digest() (string, error) {
	canonical := cache.Canonical()
	canonical.ManifestDigest = ""

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}

	return sha256Hex(data), nil
}

func (cache CacheManifest) MarshalCanonical() ([]byte, error) {
	canonical := cache.Canonical()

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

func (cache CacheManifest) copy() CacheManifest {
	clone := cache
	clone.Matrix = cloneStringMap(cache.Matrix)
	clone.Dependencies = append([]IdentityDigest(nil), cache.Dependencies...)
	clone.Toolchains = append([]IdentityDigest(nil), cache.Toolchains...)
	clone.BuildFlags = append([]BuildFlag(nil), cache.BuildFlags...)
	clone.Files = append([]ArtifactFile(nil), cache.Files...)

	return clone
}

func validateCacheManifestStructure(policy Manifest, cache CacheManifest) error {
	if cache.SchemaVersion != CacheSchemaVersion {
		return fmt.Errorf("unsupported cache schema version %d", cache.SchemaVersion)
	}

	digest, err := cache.Digest()
	if err != nil {
		return err
	}

	if cache.ManifestDigest != digest {
		return fmt.Errorf("cache manifest digest mismatch: got %s want %s", cache.ManifestDigest, digest)
	}

	if !reflect.DeepEqual(cache, cache.Canonical()) {
		return errors.New("cache manifest is not canonical")
	}

	if err := validateDigest("cache", cache.CacheDigest); err != nil {
		return err
	}

	if !oneOf(cache.CacheFormat, ArtifactFormatTar, ArtifactFormatTarGzip) {
		return fmt.Errorf("unsupported cache format %q", cache.CacheFormat)
	}

	if err := validateArtifactFiles(cache.Files); err != nil {
		return fmt.Errorf("cache file contract: %w", err)
	}

	if cache.PolicyVersion != policy.PolicyVersion || cache.PolicyDigest != policy.PolicyDigest {
		return errors.New("cache policy identity does not match manifest")
	}

	platform, ok := platformByID(policy, cache.Platform)
	if !ok {
		return fmt.Errorf("cache platform %s is not declared by policy", cache.Platform)
	}

	if cache.OS != platform.OS || cache.Architecture != platform.Architecture {
		return fmt.Errorf("cache platform identity %s/%s does not match policy platform %s/%s", cache.OS, cache.Architecture, platform.OS, platform.Architecture)
	}

	if err := validateIdentityDigests("dependency", cache.Dependencies); err != nil {
		return err
	}

	if err := validateIdentityDigests("toolchain", cache.Toolchains); err != nil {
		return err
	}

	if err := validateBuildFlags(cache.BuildFlags); err != nil {
		return err
	}

	keyDigest, err := cache.ExpectedKeyDigest()
	if err != nil {
		return err
	}

	if cache.CacheKeyDigest != keyDigest {
		return fmt.Errorf("cache key digest mismatch: got %s want %s", cache.CacheKeyDigest, keyDigest)
	}

	if cache.CacheKey != cacheKeyPrefix+keyDigest {
		return fmt.Errorf("cache key %q does not match identity digest %s", cache.CacheKey, keyDigest)
	}

	return nil
}

func validateCacheProvenance(policy Manifest, cache CacheManifest) error {
	provenance := cache.Provenance

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

	if err := validatePolicyProducerProvenance(policy, "cache", cache.Mode, cache.Coordinate, identity); err != nil {
		return err
	}

	if provenance.CacheKey != cache.CacheKey || provenance.CacheDigest != cache.CacheDigest {
		return errors.New("cache provenance does not match cache identity")
	}

	return nil
}

func validateCacheProducerAttempt(result ResultRecord, cache CacheManifest) error {
	attempt := result.Attempts[len(result.Attempts)-1]
	if cache.Provenance.RunAttempt != attempt.Attempt {
		return fmt.Errorf("cache provenance run attempt %d does not match result attempt %d", cache.Provenance.RunAttempt, attempt.Attempt)
	}

	return nil
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
