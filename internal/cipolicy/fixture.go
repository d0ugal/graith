package cipolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	FixtureSchemaVersion          = 1
	FixtureCanonicalPath          = "/usr/bin:/bin"
	FixtureCanonicalLocale        = "C"
	FixtureCanonicalTimezone      = "UTC"
	fixtureDefaultArtifactMaxAge  = 24 * time.Hour
	syntheticReadToken            = "read"
	syntheticRepositoryWriteToken = "repository-write"
	syntheticMaintainerToken      = "maintainer"
)

type FixtureFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content []byte `json:"-"`
}

type GeneratedWorkflowData struct {
	SchemaVersion int                    `json:"schema_version"`
	PolicyVersion string                 `json:"policy_version"`
	PolicyDigest  string                 `json:"policy_digest"`
	PlanDigest    string                 `json:"plan_digest"`
	Jobs          []GeneratedWorkflowJob `json:"jobs"`
	Unsupported   []GeneratedUnsupported `json:"unsupported"`
}

type GeneratedWorkflowJob struct {
	Mode       string            `json:"mode"`
	Coordinate string            `json:"coordinate"`
	GitHubName string            `json:"github_name"`
	Display    string            `json:"display"`
	Platform   string            `json:"platform"`
	TrustTier  string            `json:"trust_tier"`
	Matrix     map[string]string `json:"matrix"`
}

type GeneratedUnsupported struct {
	Mode         string `json:"mode"`
	Coordinate   string `json:"coordinate"`
	Platform     string `json:"platform"`
	TrustTier    string `json:"trust_tier"`
	Requiredness string `json:"requiredness"`
	Status       string `json:"status,omitempty"`
}

type JobObservation struct {
	Mode           string
	Coordinate     string
	Display        string
	Status         string
	FailureClass   string
	StartedAt      time.Time
	CompletedAt    time.Time
	EvidenceDigest string
	ArtifactDigest string
	CacheDigest    string
	SupersededBy   string
	UploadComplete bool
}

type CacheRequest struct {
	Plan            RunPlan
	Job             PlanJob
	Key             string
	ToolchainDigest string
	Checksum        string
	TrustTier       string
	Now             time.Time
}

type CacheEntry struct {
	Key             string
	ToolchainDigest string
	Checksum        string
	TrustTier       string
	WrittenBy       string
	WriterDigest    string
	PlanDigest      string
	PolicyDigest    string
	SourceCommit    string
	SourceTree      string
	Mode            string
	Coordinate      string
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

type ArtifactExpectation struct {
	Plan          RunPlan
	Job           PlanJob
	Digest        string
	ProducerRunID string
	RunAttempt    int
	Now           time.Time
	MaxAge        time.Duration
}

type ArtifactManifest struct {
	Name           string
	Digest         string
	ContentDigest  string
	PlanDigest     string
	PolicyDigest   string
	SourceCommit   string
	SourceTree     string
	Mode           string
	Coordinate     string
	ProducerRunID  string
	RunAttempt     int
	CreatedAt      time.Time
	UploadComplete bool
}

type ArchiveSnapshot struct {
	Platform string
	Entries  []ArchiveEntry
}

type ArchiveEntry struct {
	Name       string
	Type       string
	Mode       int
	SHA256     string
	LineEnding string
	LinkTarget string
}

type SyntheticToken struct {
	Name         string
	TrustTier    string
	Class        string
	Scopes       []string
	AllowedRoots []string
}

type CredentialOperation struct {
	Operation  string
	TrustTier  string
	Capability string
	Token      SyntheticToken
	Target     string
}

type CacheRestoreCheck struct {
	Job     PlanJob
	Request CacheRequest
	Entry   CacheEntry
}

type ArtifactCheck struct {
	Expectation ArtifactExpectation
	Artifact    ArtifactManifest
}

type ArchiveComparison struct {
	Left  ArchiveSnapshot
	Right ArchiveSnapshot
}

type credentialOperationPolicy struct {
	TokenClasses   []string
	TrustTiers     []string
	Capabilities   []string
	RequiredScopes []string
	AllowedRoots   []string
}

type FixtureRun struct {
	Manifest             Manifest
	KnownFiles           []FixtureFile
	Environment          map[string]string
	PlanOptions          PlanOptions
	WorkflowData         GeneratedWorkflowData
	Observations         []JobObservation
	CacheRestores        []CacheRestoreCheck
	Artifacts            []ArtifactCheck
	ArchiveComparisons   []ArchiveComparison
	CredentialOperations []CredentialOperation
	Now                  time.Time
}

type FaultInjection struct {
	ID    string
	Apply func(*FixtureRun)
}

var credentialOperationPolicies = map[string]credentialOperationPolicy{
	"docs-preview-write": {
		TokenClasses:   []string{syntheticRepositoryWriteToken},
		TrustTiers:     []string{"same-repository-agent"},
		Capabilities:   []string{"docs-preview"},
		RequiredScopes: []string{"contents:write", "pull-requests:write"},
		AllowedRoots:   []string{"screenshots"},
	},
	"coverage-comment": {
		TokenClasses:   []string{syntheticRepositoryWriteToken},
		TrustTiers:     []string{"same-repository-agent"},
		Capabilities:   []string{"coverage"},
		RequiredScopes: []string{"pull-requests:write"},
		AllowedRoots:   []string{"comments"},
	},
	"regeneration-push": {
		TokenClasses:   []string{syntheticMaintainerToken},
		TrustTiers:     []string{"trusted-publication"},
		Capabilities:   []string{"generated-metadata"},
		RequiredScopes: []string{"contents:write"},
		AllowedRoots:   []string{"generated"},
	},
	"dev-release-publish": {
		TokenClasses:   []string{syntheticMaintainerToken},
		TrustTiers:     []string{"trusted-publication"},
		Capabilities:   []string{"dev-release"},
		RequiredScopes: []string{"contents:write"},
		AllowedRoots:   []string{"dist/dev-release"},
	},
	"stable-release-publish": {
		TokenClasses:   []string{syntheticMaintainerToken},
		TrustTiers:     []string{"trusted-publication"},
		Capabilities:   []string{"stable-release"},
		RequiredScopes: []string{"contents:write"},
		AllowedRoots:   []string{"dist/stable-release"},
	},
}

func BuildHermeticPlan(manifest Manifest, knownFiles []FixtureFile, options PlanOptions) (RunPlan, error) {
	now := options.Now
	if now.IsZero() {
		return RunPlan{}, errors.New("fixture requires a deterministic validation time")
	}

	if err := manifest.ValidateAt(now); err != nil {
		return RunPlan{}, err
	}

	if err := ValidateFixtureChangedFiles(knownFiles, options.ChangedFiles, options.ExactFileList); err != nil {
		return RunPlan{}, err
	}

	if err := ValidateManifestWorkflowBindings(manifest, knownFiles); err != nil {
		return RunPlan{}, err
	}

	return BuildPlan(manifest, options)
}

func ValidateFixtureChangedFiles(knownFiles []FixtureFile, changedFiles []string, exact bool) error {
	if !exact {
		return errors.New("fixture requires an exact changed-file list")
	}

	known := map[string]bool{}

	for _, file := range knownFiles {
		path, err := validateFixtureFile(file)
		if err != nil {
			return err
		}

		if known[path] {
			return fmt.Errorf("fixture contains duplicate known path %s", path)
		}

		known[path] = true
	}

	for _, path := range canonicalChangedFiles(changedFiles) {
		if path == "" || invalidChangedPath(path) {
			return fmt.Errorf("changed file %q is not a valid fixture path", path)
		}

		if !known[path] {
			return fmt.Errorf("changed file %q is missing from the hermetic fixture", path)
		}

		if !fixtureDetectorKnowsPath(path) {
			return fmt.Errorf("changed file %q is unknown to the hermetic fixture detector", path)
		}
	}

	return nil
}

func ValidateManifestWorkflowBindings(manifest Manifest, knownFiles []FixtureFile) error {
	known := map[string]FixtureFile{}

	for _, file := range knownFiles {
		path, err := validateFixtureFile(file)
		if err != nil {
			return err
		}

		if _, exists := known[path]; exists {
			return fmt.Errorf("fixture contains duplicate known path %s", path)
		}

		known[path] = file
	}

	bindings, err := manifestWorkflowBindings(manifest)
	if err != nil {
		return err
	}

	for path, digest := range bindings {
		file, ok := known[path]
		if !ok {
			return fmt.Errorf("manifest workflow %s is missing from the hermetic fixture", path)
		}

		if file.Content == nil {
			return fmt.Errorf("manifest workflow %s requires hermetic file content", path)
		}

		if file.SHA256 != digest {
			return fmt.Errorf("manifest workflow %s digest mismatch: fixture has %s manifest has %s", path, file.SHA256, digest)
		}
	}

	return nil
}

func ValidateHermeticEnvironment(env map[string]string) error {
	if env["PATH"] != FixtureCanonicalPath {
		return fmt.Errorf("polluted PATH %q, want %q", env["PATH"], FixtureCanonicalPath)
	}

	if env["LC_ALL"] != FixtureCanonicalLocale || env["LANG"] != FixtureCanonicalLocale {
		return fmt.Errorf("polluted locale LC_ALL=%q LANG=%q", env["LC_ALL"], env["LANG"])
	}

	if env["TZ"] != FixtureCanonicalTimezone {
		return fmt.Errorf("polluted timezone %q, want %q", env["TZ"], FixtureCanonicalTimezone)
	}

	for name := range env {
		if isAllowedFixtureEnvName(name) {
			continue
		}

		switch {
		case isCredentialEnvName(name):
			return fmt.Errorf("credential environment variable %s is not allowed in the hermetic fixture", name)
		case isNetworkEnvName(name):
			return fmt.Errorf("network environment variable %s is not allowed in the hermetic fixture", name)
		case isCompilerEnvName(name):
			return fmt.Errorf("compiler environment variable %s must be declared through the fixture toolchain, not ambient env", name)
		default:
			return fmt.Errorf("unexpected environment variable %s is not allowed in the hermetic fixture", name)
		}
	}

	return nil
}

func GenerateWorkflowData(plan RunPlan) GeneratedWorkflowData {
	data := GeneratedWorkflowData{
		SchemaVersion: FixtureSchemaVersion,
		PolicyVersion: plan.PolicyVersion,
		PolicyDigest:  plan.PolicyDigest,
		PlanDigest:    plan.PlanDigest,
	}

	for _, job := range plan.Jobs {
		data.Jobs = append(data.Jobs, GeneratedWorkflowJob{
			Mode:       job.Mode,
			Coordinate: job.Coordinate,
			GitHubName: job.GitHubName,
			Display:    job.GitHubName,
			Platform:   job.Platform,
			TrustTier:  job.TrustTier,
			Matrix:     cloneStringMap(job.Matrix),
		})
	}

	for _, decision := range plan.Unsupported {
		data.Unsupported = append(data.Unsupported, GeneratedUnsupported{
			Mode:         decision.Mode,
			Coordinate:   decision.Coordinate,
			Platform:     decision.Platform,
			TrustTier:    decision.TrustTier,
			Requiredness: decision.Requiredness,
		})
	}

	sortGeneratedWorkflowData(&data)

	return data
}

func (data GeneratedWorkflowData) ValidateAgainstPlan(plan RunPlan) error {
	if data.SchemaVersion != FixtureSchemaVersion {
		return fmt.Errorf("unsupported generated workflow schema version %d", data.SchemaVersion)
	}

	if data.PolicyVersion != plan.PolicyVersion || data.PolicyDigest != plan.PolicyDigest || data.PlanDigest != plan.PlanDigest {
		return errors.New("generated workflow data is not bound to the evaluated manifest and plan")
	}

	canonical := cloneGeneratedWorkflowData(data)
	sortGeneratedWorkflowData(&canonical)

	if !reflect.DeepEqual(data, canonical) {
		return errors.New("generated workflow data is not canonical")
	}

	expected := GenerateWorkflowData(plan)
	if !reflect.DeepEqual(data.Jobs, expected.Jobs) {
		return errors.New("generated workflow jobs do not match manifest plan expansion")
	}

	if !reflect.DeepEqual(data.Unsupported, expected.Unsupported) {
		return errors.New("generated workflow unsupported decisions do not match manifest plan expansion")
	}

	return nil
}

func FanInFixture(manifest Manifest, plan RunPlan, workflowData GeneratedWorkflowData, observations []JobObservation, now time.Time) (FanInReport, error) {
	if err := RejectUnsupportedPlatformPasses(plan, workflowData.Unsupported); err != nil {
		return FanInReport{}, err
	}

	if err := workflowData.ValidateAgainstPlan(plan); err != nil {
		return FanInReport{}, err
	}

	workflowJobs := map[string]GeneratedWorkflowJob{}

	for _, job := range workflowData.Jobs {
		key := planCoordinateKey(job.Mode, job.Coordinate)
		if workflowJobs[key].Mode != "" {
			return FanInReport{}, fmt.Errorf("generated workflow has duplicate coordinate %s/%s", job.Mode, job.Coordinate)
		}

		workflowJobs[key] = job
	}

	planJobs := map[string]PlanJob{}
	for _, job := range plan.Jobs {
		planJobs[planCoordinateKey(job.Mode, job.Coordinate)] = job
	}

	results := make([]ResultRecord, 0, len(observations))
	for _, observation := range observations {
		key := planCoordinateKey(observation.Mode, observation.Coordinate)

		workflowJob, ok := workflowJobs[key]
		if !ok {
			return FanInReport{}, fmt.Errorf("observation references unknown coordinate %s/%s", observation.Mode, observation.Coordinate)
		}

		if observation.Display != workflowJob.Display {
			return FanInReport{}, fmt.Errorf("observation for %s/%s has misleading display name %q, want %q", observation.Mode, observation.Coordinate, observation.Display, workflowJob.Display)
		}

		planJob, ok := planJobs[key]
		if !ok {
			return FanInReport{}, fmt.Errorf("observation references coordinate %s/%s missing from the evaluated plan", observation.Mode, observation.Coordinate)
		}

		if !observation.UploadComplete {
			return FanInReport{}, fmt.Errorf("observation for %s/%s has a partial upload", observation.Mode, observation.Coordinate)
		}

		if err := validateObservationTime(observation, plan, now); err != nil {
			return FanInReport{}, err
		}

		result, err := resultFromObservation(plan, planJob, observation)
		if err != nil {
			return FanInReport{}, err
		}

		results = append(results, result)
	}

	return FanIn(manifest, plan, results, now)
}

func RejectUnsupportedPlatformPasses(plan RunPlan, observations []GeneratedUnsupported) error {
	unsupported := map[string]bool{}
	for _, decision := range plan.Unsupported {
		unsupported[generatedUnsupportedKey(GeneratedUnsupported{
			Mode:       decision.Mode,
			Coordinate: decision.Coordinate,
			Platform:   decision.Platform,
			TrustTier:  decision.TrustTier,
		})] = true
		if decision.SilentPassAllowed {
			return fmt.Errorf("unsupported platform %s/%s allows a silent pass", decision.Mode, decision.Coordinate)
		}
	}

	for _, observation := range observations {
		if unsupported[generatedUnsupportedKey(observation)] && unsupportedReportedPassedStatus(observation.Status) {
			return fmt.Errorf("unsupported platform %s/%s was reported as passed", observation.Mode, observation.Coordinate)
		}
	}

	return nil
}

func RunHermeticFixture(run FixtureRun) (FanInReport, error) {
	if run.Now.IsZero() {
		return FanInReport{}, errors.New("fixture run requires a deterministic validation time")
	}

	if err := ValidateHermeticEnvironment(run.Environment); err != nil {
		return FanInReport{}, err
	}

	options := run.PlanOptions
	options.Now = run.Now

	plan, err := BuildHermeticPlan(run.Manifest, run.KnownFiles, options)
	if err != nil {
		return FanInReport{}, err
	}

	workflowData := run.WorkflowData
	if workflowData.SchemaVersion == 0 {
		return FanInReport{}, errors.New("fixture run requires generated workflow data")
	}

	if err := validateFixtureSideEffects(run, plan); err != nil {
		return FanInReport{}, err
	}

	report, err := FanInFixture(run.Manifest, plan, workflowData, run.Observations, run.Now)
	if err != nil {
		return report, err
	}

	if err := validateSideEffectsBoundToAcceptedResults(run, plan, report); err != nil {
		return report, err
	}

	return report, nil
}

func validateFixtureSideEffects(run FixtureRun, plan RunPlan) error {
	for _, check := range run.CacheRestores {
		request := check.Request
		if !request.Now.IsZero() && !request.Now.Equal(run.Now) {
			return fmt.Errorf("cache restore validation time %s does not match fixture run time %s", request.Now.Format(time.RFC3339), run.Now.Format(time.RFC3339))
		}

		request.Now = run.Now

		if request.TrustTier != "" && request.TrustTier != plan.TrustTier {
			return fmt.Errorf("cache restore trust tier %s does not match evaluated plan trust tier %s", request.TrustTier, plan.TrustTier)
		}

		if err := bindCacheRestoreCheckToPlan(&check, plan); err != nil {
			return err
		}

		if hasRunPlanIdentity(request.Plan) && !sameRunPlanIdentity(request.Plan, plan) {
			return errors.New("cache restore request plan does not match the evaluated plan")
		}

		if request.Job.Mode != "" || request.Job.Coordinate != "" {
			if !reflect.DeepEqual(request.Job, check.Job) {
				return fmt.Errorf("cache restore request job %s/%s does not match the evaluated plan", request.Job.Mode, request.Job.Coordinate)
			}
		}

		request.Plan = plan
		request.Job = check.Job

		if err := VerifyCacheRestore(request, check.Entry); err != nil {
			return err
		}
	}

	for _, check := range run.Artifacts {
		expectation := check.Expectation
		if !expectation.Now.IsZero() && !expectation.Now.Equal(run.Now) {
			return fmt.Errorf("artifact expectation validation time %s does not match fixture run time %s", expectation.Now.Format(time.RFC3339), run.Now.Format(time.RFC3339))
		}

		expectation.Now = run.Now

		if expectation.MaxAge > fixtureDefaultArtifactMaxAge {
			return fmt.Errorf("artifact expectation max age %s exceeds fixture maximum %s", expectation.MaxAge, fixtureDefaultArtifactMaxAge)
		}

		if err := bindArtifactExpectationToPlan(&expectation, plan); err != nil {
			return err
		}

		if err := VerifyArtifact(expectation, check.Artifact); err != nil {
			return err
		}
	}

	for _, comparison := range run.ArchiveComparisons {
		if err := validateArchiveComparisonPlanBinding(comparison, plan); err != nil {
			return err
		}

		if err := ComparePortableArchives(comparison.Left, comparison.Right); err != nil {
			return err
		}
	}

	for _, operation := range run.CredentialOperations {
		if operation.TrustTier != "" && operation.TrustTier != plan.TrustTier {
			return fmt.Errorf("credential operation trust tier %s does not match evaluated plan trust tier %s", operation.TrustTier, plan.TrustTier)
		}

		if err := ValidateCredentialOperation(operation); err != nil {
			return err
		}

		if err := validateCredentialOperationPlanBinding(operation, credentialOperationPolicies[operation.Operation], plan); err != nil {
			return err
		}
	}

	return nil
}

func bindCacheRestoreCheckToPlan(check *CacheRestoreCheck, plan RunPlan) error {
	if check.Job.Mode == "" || check.Job.Coordinate == "" {
		return errors.New("cache restore job coordinate identity is required")
	}

	job, ok := planJobByCoordinate(plan, check.Job.Mode, check.Job.Coordinate)
	if !ok {
		return fmt.Errorf("cache restore references job %s/%s missing from the evaluated plan", check.Job.Mode, check.Job.Coordinate)
	}

	if !reflect.DeepEqual(check.Job, job) {
		return fmt.Errorf("cache restore job %s/%s does not match the evaluated plan", check.Job.Mode, check.Job.Coordinate)
	}

	check.Job = job

	return nil
}

func cacheWriterDigest(plan RunPlan, job PlanJob, writtenBy string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		plan.PlanDigest,
		plan.PolicyDigest,
		plan.Source.Commit,
		plan.Source.Tree,
		plan.TrustTier,
		writtenBy,
		job.Mode,
		job.Coordinate,
		job.Capability,
		job.Platform,
	}, "\x00")))

	return hex.EncodeToString(sum[:])
}

func bindArtifactExpectationToPlan(expectation *ArtifactExpectation, plan RunPlan) error {
	if hasRunPlanIdentity(expectation.Plan) && !sameRunPlanIdentity(expectation.Plan, plan) {
		return errors.New("artifact expectation plan does not match the evaluated plan")
	}

	if expectation.Job.Mode == "" || expectation.Job.Coordinate == "" {
		return errors.New("artifact expectation job coordinate identity is required")
	}

	job, ok := planJobByCoordinate(plan, expectation.Job.Mode, expectation.Job.Coordinate)
	if !ok {
		return fmt.Errorf("artifact expectation references job %s/%s missing from the evaluated plan", expectation.Job.Mode, expectation.Job.Coordinate)
	}

	if !reflect.DeepEqual(expectation.Job, job) {
		return fmt.Errorf("artifact expectation job %s/%s does not match the evaluated plan", expectation.Job.Mode, expectation.Job.Coordinate)
	}

	expectation.Plan = plan
	expectation.Job = job

	return nil
}

func validateSideEffectsBoundToAcceptedResults(run FixtureRun, plan RunPlan, report FanInReport) error {
	accepted := acceptedCoordinateSet(report)
	observations := observationsByCoordinate(run.Observations)

	for _, check := range run.CacheRestores {
		if err := bindCacheRestoreCheckToPlan(&check, plan); err != nil {
			return err
		}

		observation, err := acceptedObservationForJob("cache restore", check.Job, accepted, observations)
		if err != nil {
			return err
		}

		if observation.CacheDigest != check.Entry.Checksum {
			return fmt.Errorf("cache restore digest for %s/%s does not match accepted result row: got %s want %s", check.Job.Mode, check.Job.Coordinate, observation.CacheDigest, check.Entry.Checksum)
		}
	}

	for _, check := range run.Artifacts {
		expectation := check.Expectation
		if err := bindArtifactExpectationToPlan(&expectation, plan); err != nil {
			return err
		}

		observation, err := acceptedObservationForJob("artifact", expectation.Job, accepted, observations)
		if err != nil {
			return err
		}

		if observation.ArtifactDigest != check.Artifact.Digest {
			return fmt.Errorf("artifact digest for %s/%s does not match accepted result row: got %s want %s", expectation.Job.Mode, expectation.Job.Coordinate, observation.ArtifactDigest, check.Artifact.Digest)
		}
	}

	return nil
}

func acceptedCoordinateSet(report FanInReport) map[string]bool {
	accepted := map[string]bool{}
	for _, decision := range report.Accepted {
		accepted[planCoordinateKey(decision.Mode, decision.Coordinate)] = true
	}

	return accepted
}

func observationsByCoordinate(observations []JobObservation) map[string]JobObservation {
	byCoordinate := map[string]JobObservation{}
	for _, observation := range observations {
		byCoordinate[planCoordinateKey(observation.Mode, observation.Coordinate)] = observation
	}

	return byCoordinate
}

func acceptedObservationForJob(kind string, job PlanJob, accepted map[string]bool, observations map[string]JobObservation) (JobObservation, error) {
	key := planCoordinateKey(job.Mode, job.Coordinate)
	if !accepted[key] {
		return JobObservation{}, fmt.Errorf("%s for %s/%s is not bound to an accepted result row", kind, job.Mode, job.Coordinate)
	}

	observation, ok := observations[key]
	if !ok {
		return JobObservation{}, fmt.Errorf("%s for %s/%s has no matching observation", kind, job.Mode, job.Coordinate)
	}

	return observation, nil
}

func hasRunPlanIdentity(plan RunPlan) bool {
	return plan.PlanDigest != "" ||
		plan.PolicyVersion != "" ||
		plan.PolicyDigest != "" ||
		plan.TrustTier != "" ||
		plan.Source != (SourceRevision{}) ||
		plan.Event != (EventSelection{})
}

func hasCompleteCachePlanIdentity(plan RunPlan) bool {
	return digestPattern.MatchString(plan.PlanDigest) &&
		strings.TrimSpace(plan.PolicyVersion) != "" &&
		digestPattern.MatchString(plan.PolicyDigest) &&
		strings.TrimSpace(plan.TrustTier) != "" &&
		strings.TrimSpace(plan.Source.Repository) != "" &&
		strings.TrimSpace(plan.Source.Ref) != "" &&
		gitDigestPattern.MatchString(plan.Source.Commit) &&
		gitDigestPattern.MatchString(plan.Source.Tree) &&
		strings.TrimSpace(plan.Event.Source) != "" &&
		strings.TrimSpace(plan.Event.Event) != "" &&
		strings.TrimSpace(plan.Event.GitHubEvent) != ""
}

func hasCompleteCacheJobIdentity(job PlanJob) bool {
	return strings.TrimSpace(job.Source) != "" &&
		strings.TrimSpace(job.Event) != "" &&
		strings.TrimSpace(job.TrustTier) != "" &&
		strings.TrimSpace(job.Mode) != "" &&
		strings.TrimSpace(job.Coordinate) != "" &&
		strings.TrimSpace(job.Capability) != "" &&
		strings.TrimSpace(job.Platform) != "" &&
		strings.TrimSpace(job.CostClass) != "" &&
		strings.TrimSpace(job.Requiredness) != "" &&
		strings.TrimSpace(job.Owner) != "" &&
		strings.TrimSpace(job.GitHubName) != ""
}

func sameRunPlanIdentity(actual, expected RunPlan) bool {
	return actual.PlanDigest == expected.PlanDigest &&
		actual.PolicyVersion == expected.PolicyVersion &&
		actual.PolicyDigest == expected.PolicyDigest &&
		actual.TrustTier == expected.TrustTier &&
		reflect.DeepEqual(actual.Source, expected.Source) &&
		reflect.DeepEqual(actual.Event, expected.Event)
}

func planJobByCoordinate(plan RunPlan, mode, coordinate string) (PlanJob, bool) {
	for _, job := range plan.Jobs {
		if job.Mode == mode && job.Coordinate == coordinate {
			return job, true
		}
	}

	return PlanJob{}, false
}

func validateCredentialOperationPlanBinding(operation CredentialOperation, policy credentialOperationPolicy, plan RunPlan) error {
	if operation.Capability == "" {
		return fmt.Errorf("%s credential operation requires plan capability identity", operation.Operation)
	}

	if !containsString(policy.Capabilities, operation.Capability) {
		return fmt.Errorf("%s credential operation cannot use plan capability %s", operation.Operation, operation.Capability)
	}

	if !containsString(plan.Capabilities, operation.Capability) {
		return fmt.Errorf("%s credential operation requires plan capability %s", operation.Operation, operation.Capability)
	}

	return nil
}

func validateArchiveComparisonPlanBinding(comparison ArchiveComparison, plan RunPlan) error {
	platforms := map[string]struct{}{}
	for _, job := range plan.Jobs {
		platforms[job.Platform] = struct{}{}
	}

	for _, platform := range []string{comparison.Left.Platform, comparison.Right.Platform} {
		if platform == "" {
			return errors.New("archive comparison requires platform identity")
		}

		if _, ok := platforms[platform]; !ok {
			return fmt.Errorf("archive comparison platform %s is missing from the evaluated plan", platform)
		}
	}

	return nil
}

func ApplyFault(run FixtureRun, fault FaultInjection) (FixtureRun, error) {
	if strings.TrimSpace(fault.ID) == "" {
		return FixtureRun{}, errors.New("fault injection requires an ID")
	}

	if fault.Apply == nil {
		return FixtureRun{}, fmt.Errorf("fault %s has no mutation", fault.ID)
	}

	mutated := cloneFixtureRun(run)
	fault.Apply(&mutated)

	return mutated, nil
}

func VerifyCacheRestore(request CacheRequest, entry CacheEntry) error {
	if request.Now.IsZero() {
		return errors.New("cache verification requires a deterministic time")
	}

	if !hasCompleteCachePlanIdentity(request.Plan) {
		return errors.New("cache request complete plan identity is required")
	}

	if !hasCompleteCacheJobIdentity(request.Job) {
		return errors.New("cache request complete job identity is required")
	}

	if request.Key == "" || entry.Key == "" {
		return errors.New("cache key is required")
	}

	if request.TrustTier == "" || entry.TrustTier == "" {
		return errors.New("cache trust tier is required")
	}

	if entry.Key != request.Key {
		return fmt.Errorf("cache key mismatch: got %s want %s", entry.Key, request.Key)
	}

	if !digestPattern.MatchString(request.ToolchainDigest) || !digestPattern.MatchString(entry.ToolchainDigest) {
		return errors.New("cache toolchain digest is invalid")
	}

	if entry.ToolchainDigest != request.ToolchainDigest {
		return fmt.Errorf("cache toolchain digest mismatch: got %s want %s", entry.ToolchainDigest, request.ToolchainDigest)
	}

	if !digestPattern.MatchString(request.Checksum) || !digestPattern.MatchString(entry.Checksum) {
		return errors.New("cache checksum is invalid")
	}

	if entry.Checksum != request.Checksum {
		return fmt.Errorf("cache checksum mismatch: got %s want %s", entry.Checksum, request.Checksum)
	}

	if entry.TrustTier != request.TrustTier {
		return fmt.Errorf("cache trust tier %s cannot satisfy %s", entry.TrustTier, request.TrustTier)
	}

	if request.Plan.TrustTier != request.TrustTier || request.Job.TrustTier != request.TrustTier {
		return fmt.Errorf("cache request trust tier %s does not match plan/job provenance", request.TrustTier)
	}

	if entry.CreatedAt.IsZero() || entry.ExpiresAt.IsZero() {
		return errors.New("cache creation and expiry times are required")
	}

	if entry.CreatedAt.After(request.Now) {
		return fmt.Errorf("cache entry created in the future at %s", entry.CreatedAt.Format(time.RFC3339))
	}

	if !entry.CreatedAt.Before(entry.ExpiresAt) {
		return errors.New("cache creation time must be before expiry")
	}

	if !request.Now.Before(entry.ExpiresAt) {
		return fmt.Errorf("cache entry expired at %s", entry.ExpiresAt.Format(time.RFC3339))
	}

	if entry.WrittenBy == "" {
		return errors.New("cache writer identity is required")
	}

	if !digestPattern.MatchString(entry.WriterDigest) {
		return errors.New("cache writer proof digest is required")
	}

	writerDigest := cacheWriterDigest(request.Plan, request.Job, entry.WrittenBy)
	if entry.WriterDigest != writerDigest {
		return fmt.Errorf("cache writer provenance mismatch: got %s want %s", entry.WriterDigest, writerDigest)
	}

	if entry.PlanDigest != request.Plan.PlanDigest ||
		entry.PolicyDigest != request.Plan.PolicyDigest ||
		entry.SourceCommit != request.Plan.Source.Commit ||
		entry.SourceTree != request.Plan.Source.Tree ||
		entry.Mode != request.Job.Mode ||
		entry.Coordinate != request.Job.Coordinate {
		return errors.New("cache writer provenance does not match request plan/job")
	}

	return nil
}

func VerifyArtifact(expect ArtifactExpectation, artifact ArtifactManifest) error {
	now := expect.Now
	if now.IsZero() {
		return errors.New("artifact verification requires a deterministic time")
	}

	maxAge := expect.MaxAge
	if maxAge == 0 {
		maxAge = fixtureDefaultArtifactMaxAge
	}

	if !artifact.UploadComplete {
		return fmt.Errorf("artifact %s is partially uploaded", artifact.Name)
	}

	if expect.Job.Mode == "" || expect.Job.Coordinate == "" || artifact.Mode == "" || artifact.Coordinate == "" {
		return fmt.Errorf("artifact %s job coordinate identity is required", artifact.Name)
	}

	if expect.ProducerRunID == "" || artifact.ProducerRunID == "" {
		return fmt.Errorf("artifact %s producer run identity is required", artifact.Name)
	}

	if expect.RunAttempt <= 0 || artifact.RunAttempt <= 0 {
		return fmt.Errorf("artifact %s run attempt must be positive", artifact.Name)
	}

	for _, digest := range []struct {
		kind  string
		value string
	}{
		{kind: "expected artifact", value: expect.Digest},
		{kind: "artifact", value: artifact.Digest},
		{kind: "content", value: artifact.ContentDigest},
	} {
		if !digestPattern.MatchString(digest.value) {
			return fmt.Errorf("%s digest %q is invalid", digest.kind, digest.value)
		}
	}

	if artifact.Digest != expect.Digest || artifact.ContentDigest != expect.Digest {
		return fmt.Errorf("artifact digest mismatch for %s", artifact.Name)
	}

	if artifact.PlanDigest != expect.Plan.PlanDigest ||
		artifact.PolicyDigest != expect.Plan.PolicyDigest ||
		artifact.SourceCommit != expect.Plan.Source.Commit ||
		artifact.SourceTree != expect.Plan.Source.Tree ||
		artifact.Mode != expect.Job.Mode ||
		artifact.Coordinate != expect.Job.Coordinate {
		return fmt.Errorf("artifact %s provenance does not match the evaluated plan coordinate", artifact.Name)
	}

	if artifact.ProducerRunID != expect.ProducerRunID || artifact.RunAttempt != expect.RunAttempt {
		return fmt.Errorf("artifact %s producer run identity does not match", artifact.Name)
	}

	if artifact.CreatedAt.IsZero() || artifact.CreatedAt.After(now) {
		return fmt.Errorf("artifact %s has invalid creation time", artifact.Name)
	}

	if now.Sub(artifact.CreatedAt) > maxAge {
		return fmt.Errorf("artifact %s is stale", artifact.Name)
	}

	return nil
}

func ComparePortableArchives(left ArchiveSnapshot, right ArchiveSnapshot) error {
	if left.Platform == "" || right.Platform == "" {
		return errors.New("archive snapshots require platform identity")
	}

	if left.Platform == right.Platform {
		return fmt.Errorf("archive comparison requires distinct platforms, got %s twice", left.Platform)
	}

	if err := validateArchiveSnapshot(left); err != nil {
		return fmt.Errorf("archive snapshot %s is invalid: %w", left.Platform, err)
	}

	if err := validateArchiveSnapshot(right); err != nil {
		return fmt.Errorf("archive snapshot %s is invalid: %w", right.Platform, err)
	}

	if len(left.Entries) != len(right.Entries) {
		return fmt.Errorf("archive member count differs: %d != %d", len(left.Entries), len(right.Entries))
	}

	for index := range left.Entries {
		leftEntry, rightEntry := left.Entries[index], right.Entries[index]
		if leftEntry.Name != rightEntry.Name {
			return fmt.Errorf("archive member order differs at %d: %s != %s", index, leftEntry.Name, rightEntry.Name)
		}

		if leftEntry.Type != rightEntry.Type {
			return fmt.Errorf("archive member %s type differs: %s != %s", leftEntry.Name, leftEntry.Type, rightEntry.Type)
		}

		if leftEntry.Mode != rightEntry.Mode {
			return fmt.Errorf("archive member %s mode differs: %#o != %#o", leftEntry.Name, leftEntry.Mode, rightEntry.Mode)
		}

		if leftEntry.LineEnding != rightEntry.LineEnding {
			return fmt.Errorf("archive member %s line ending differs: %s != %s", leftEntry.Name, leftEntry.LineEnding, rightEntry.LineEnding)
		}

		if leftEntry.LinkTarget != rightEntry.LinkTarget {
			return fmt.Errorf("archive member %s symlink target differs: %s != %s", leftEntry.Name, leftEntry.LinkTarget, rightEntry.LinkTarget)
		}

		if leftEntry.SHA256 != rightEntry.SHA256 {
			return fmt.Errorf("archive member %s digest differs", leftEntry.Name)
		}
	}

	return nil
}

func ValidateCredentialOperation(operation CredentialOperation) error {
	policy, ok := credentialOperationPolicies[operation.Operation]
	if !ok {
		return fmt.Errorf("unsupported credential operation %q", operation.Operation)
	}

	if operation.Token.Name == "" {
		return errors.New("synthetic token identity is required")
	}

	if operation.Token.TrustTier != operation.TrustTier {
		return fmt.Errorf("synthetic token trust tier %s does not match operation trust tier %s", operation.Token.TrustTier, operation.TrustTier)
	}

	if operation.TrustTier == "fork-untrusted" && operation.Token.Class != syntheticReadToken {
		return errors.New("fork pull requests may use only synthetic read tokens")
	}

	if operation.TrustTier == "same-repository-agent" && operation.Token.Class == syntheticMaintainerToken {
		return errors.New("same-repository agent branches cannot obtain maintainer credentials from repository location")
	}

	if !containsString(policy.TrustTiers, operation.TrustTier) {
		return fmt.Errorf("%s is not allowed for trust tier %s", operation.Operation, operation.TrustTier)
	}

	if !containsString(policy.TokenClasses, operation.Token.Class) {
		if containsString(policy.TokenClasses, syntheticMaintainerToken) {
			return fmt.Errorf("%s requires a maintainer credential", operation.Operation)
		}

		if operation.Token.Class == syntheticMaintainerToken {
			return fmt.Errorf("%s must not use maintainer credentials", operation.Operation)
		}

		return fmt.Errorf("%s cannot use synthetic %s tokens", operation.Operation, operation.Token.Class)
	}

	for _, scope := range policy.RequiredScopes {
		if !containsString(operation.Token.Scopes, scope) {
			return fmt.Errorf("%s token is missing required scope %s", operation.Operation, scope)
		}
	}

	for _, scope := range operation.Token.Scopes {
		if !containsString(policy.RequiredScopes, scope) {
			return fmt.Errorf("%s token has unsupported scope %s", operation.Operation, scope)
		}
	}

	for _, root := range operation.Token.AllowedRoots {
		if !targetWithinAllowedRoots(root, policy.AllowedRoots) {
			return fmt.Errorf("%s token root %q is outside the operation boundary", operation.Operation, root)
		}
	}

	if !targetWithinAllowedRoots(operation.Target, operation.Token.AllowedRoots) {
		return fmt.Errorf("%s target %q escapes synthetic token filesystem boundary", operation.Operation, operation.Target)
	}

	if !targetWithinAllowedRoots(operation.Target, policy.AllowedRoots) {
		return fmt.Errorf("%s target %q is not allowed for the operation boundary", operation.Operation, operation.Target)
	}

	return nil
}

func validateObservationTime(observation JobObservation, plan RunPlan, now time.Time) error {
	if observation.StartedAt.IsZero() || observation.CompletedAt.IsZero() || observation.StartedAt.After(observation.CompletedAt) {
		return fmt.Errorf("observation for %s/%s has invalid timestamps", observation.Mode, observation.Coordinate)
	}

	if observation.StartedAt.After(now) {
		return fmt.Errorf("observation for %s/%s starts in the future at %s", observation.Mode, observation.Coordinate, observation.StartedAt.Format(time.RFC3339))
	}

	if observation.CompletedAt.After(now) {
		return fmt.Errorf("observation for %s/%s completes in the future at %s", observation.Mode, observation.Coordinate, observation.CompletedAt.Format(time.RFC3339))
	}

	if observation.StartedAt.Before(plan.CreatedAt) {
		return fmt.Errorf("observation for %s/%s starts before evaluated plan at %s", observation.Mode, observation.Coordinate, observation.StartedAt.Format(time.RFC3339))
	}

	if observation.CompletedAt.After(plan.ExpiresAt) {
		return fmt.Errorf("observation for %s/%s completes after evaluated plan expiry at %s", observation.Mode, observation.Coordinate, observation.CompletedAt.Format(time.RFC3339))
	}

	return nil
}

func resultFromObservation(plan RunPlan, job PlanJob, observation JobObservation) (ResultRecord, error) {
	status, failureClass := normalizeObservationOutcome(observation.Status, observation.FailureClass)

	attempt := ResultAttempt{
		Attempt:        1,
		Status:         status,
		FailureClass:   failureClass,
		StartedAt:      observation.StartedAt,
		CompletedAt:    observation.CompletedAt,
		EvidenceDigest: observation.EvidenceDigest,
		ArtifactDigest: observation.ArtifactDigest,
		CacheDigest:    observation.CacheDigest,
	}

	return newResultRecord(plan, job, []ResultAttempt{attempt}, observation.SupersededBy)
}

func normalizeObservationOutcome(status, failureClass string) (string, string) {
	switch status {
	case "timed-out":
		return "failed", defaultString(failureClass, "runner")
	default:
		return status, failureClass
	}
}

func sortGeneratedWorkflowData(data *GeneratedWorkflowData) {
	sort.Slice(data.Jobs, func(i, j int) bool {
		return generatedWorkflowJobKey(data.Jobs[i]) < generatedWorkflowJobKey(data.Jobs[j])
	})

	for index := range data.Jobs {
		if data.Jobs[index].Matrix == nil {
			data.Jobs[index].Matrix = map[string]string{}
		}
	}

	sort.Slice(data.Unsupported, func(i, j int) bool {
		return generatedUnsupportedKey(data.Unsupported[i]) < generatedUnsupportedKey(data.Unsupported[j])
	})
}

func generatedWorkflowJobKey(job GeneratedWorkflowJob) string {
	return strings.Join([]string{job.Mode, job.Coordinate, job.Platform, job.TrustTier}, "\x00")
}

func generatedUnsupportedKey(decision GeneratedUnsupported) string {
	return strings.Join([]string{decision.Mode, decision.Coordinate, decision.Platform, decision.TrustTier}, "\x00")
}

func unsupportedReportedPassedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "passed", "success", "skipped", "neutral":
		return true
	default:
		return false
	}
}

func isAllowedFixtureEnvName(name string) bool {
	switch name {
	case "PATH", "LC_ALL", "LANG", "TZ":
		return true
	default:
		return false
	}
}

func isCredentialEnvName(name string) bool {
	upper := strings.ToUpper(name)

	return upper == "GH_TOKEN" ||
		upper == "GITHUB_TOKEN" ||
		upper == "TOKEN" ||
		upper == "SECRET" ||
		upper == "PASSWORD" ||
		upper == "RELEASE_TOKEN" ||
		upper == "ACTIONS_ID_TOKEN_REQUEST_TOKEN" ||
		upper == "ACTIONS_ID_TOKEN_REQUEST_URL" ||
		upper == "ACTIONS_RUNTIME_TOKEN" ||
		upper == "DOCKER_AUTH_CONFIG" ||
		upper == "GIT_ASKPASS" ||
		upper == "NETRC" ||
		upper == "SSH_AUTH_SOCK" ||
		strings.HasSuffix(upper, "_TOKEN") ||
		strings.HasSuffix(upper, "_SECRET") ||
		strings.HasSuffix(upper, "_PASSWORD") ||
		strings.HasSuffix(upper, "_CREDENTIALS") ||
		strings.HasSuffix(upper, "_API_KEY") ||
		strings.HasSuffix(upper, "_PRIVATE_KEY") ||
		strings.HasSuffix(upper, "_CLIENT_SECRET") ||
		strings.HasSuffix(upper, "_ACCESS_KEY_ID") ||
		strings.HasSuffix(upper, "_SECRET_ACCESS_KEY") ||
		strings.HasSuffix(upper, "_KEY_ID") ||
		strings.HasSuffix(upper, "_ACCESS_KEY")
}

func isNetworkEnvName(name string) bool {
	switch strings.ToUpper(name) {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"GOPROXY", "GOSUMDB", "GONOSUMDB", "GOPRIVATE", "GOINSECURE",
		"NPM_CONFIG_REGISTRY", "YARN_REGISTRY", "PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL",
		"CARGO_REGISTRIES_CRATES_IO_PROTOCOL", "ACTIONS_CACHE_URL", "ACTIONS_RESULTS_URL", "ACTIONS_RUNTIME_URL":
		return true
	default:
		return false
	}
}

func isCompilerEnvName(name string) bool {
	switch strings.ToUpper(name) {
	case "CC", "CXX", "CFLAGS", "CPPFLAGS", "CXXFLAGS", "LDFLAGS", "CGO_ENABLED", "CGO_CFLAGS", "CGO_CPPFLAGS", "CPATH", "C_INCLUDE_PATH", "CPLUS_INCLUDE_PATH", "LIBRARY_PATH", "PKG_CONFIG_PATH",
		"GOFLAGS", "GOTOOLCHAIN", "GOOS", "GOARCH", "GOARM", "GOAMD64", "GOEXPERIMENT", "GOPATH", "GOCACHE", "GOMODCACHE",
		"RUSTFLAGS", "SDKROOT", "MACOSX_DEPLOYMENT_TARGET":
		return true
	default:
		return false
	}
}

func fixtureDetectorKnowsPath(path string) bool {
	return isCIPolicyPath(path) ||
		isGeneratedInputPath(path) ||
		isLockfilePath(path) ||
		isReleaseMetadataPath(path) ||
		len(capabilitiesForPath(path)) > 0
}

func validateFixtureFile(file FixtureFile) (string, error) {
	path := normalizeChangedPath(file.Path)
	if path == "" || invalidChangedPath(path) {
		return "", fmt.Errorf("fixture contains invalid known path %q", file.Path)
	}

	if !digestPattern.MatchString(file.SHA256) {
		return "", fmt.Errorf("fixture known path %s has invalid digest", path)
	}

	if file.Content != nil {
		digest := sha256.Sum256(file.Content)
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			return "", fmt.Errorf("fixture known path %s content digest does not match declared digest", path)
		}
	}

	return path, nil
}

func manifestWorkflowBindings(manifest Manifest) (map[string]string, error) {
	bindings := map[string]string{}

	for _, mode := range manifest.Modes {
		if err := addManifestWorkflowBinding(bindings, mode.Trace); err != nil {
			return nil, err
		}

		for _, coordinate := range mode.Coordinates {
			if err := addManifestWorkflowBinding(bindings, coordinate.Trace); err != nil {
				return nil, err
			}
		}
	}

	return bindings, nil
}

func addManifestWorkflowBinding(bindings map[string]string, trace LegacyTrace) error {
	path := normalizeChangedPath(trace.WorkflowPath)
	if path == "" {
		return nil
	}

	if invalidChangedPath(path) {
		return fmt.Errorf("manifest trace contains invalid workflow path %q", trace.WorkflowPath)
	}

	if !digestPattern.MatchString(trace.WorkflowSHA256) {
		return fmt.Errorf("manifest trace for workflow %s has invalid digest", path)
	}

	if existing, ok := bindings[path]; ok && existing != trace.WorkflowSHA256 {
		return fmt.Errorf("manifest trace has conflicting digests for workflow %s", path)
	}

	bindings[path] = trace.WorkflowSHA256

	return nil
}

func validateArchiveSnapshot(snapshot ArchiveSnapshot) error {
	if len(snapshot.Entries) == 0 {
		return errors.New("archive snapshot must contain entries")
	}

	seenNames := map[string]struct{}{}

	for _, entry := range snapshot.Entries {
		if entry.Name == "" || invalidChangedPath(filepath.ToSlash(entry.Name)) {
			return fmt.Errorf("archive member %q has invalid name", entry.Name)
		}

		name := filepath.ToSlash(filepath.Clean(entry.Name))
		if _, ok := seenNames[name]; ok {
			return fmt.Errorf("archive member %s is duplicated", name)
		}

		seenNames[name] = struct{}{}

		switch entry.Type {
		case "file", "directory", "symlink":
		default:
			return fmt.Errorf("archive member %s has unsupported type %q", entry.Name, entry.Type)
		}

		if entry.Mode <= 0 {
			return fmt.Errorf("archive member %s has invalid mode %#o", entry.Name, entry.Mode)
		}

		if !digestPattern.MatchString(entry.SHA256) {
			return fmt.Errorf("archive member %s has invalid digest", entry.Name)
		}

		switch entry.LineEnding {
		case "binary", "crlf", "lf", "none":
		default:
			return fmt.Errorf("archive member %s has unsupported line ending %q", entry.Name, entry.LineEnding)
		}

		if entry.Type == "symlink" && entry.LinkTarget == "" {
			return fmt.Errorf("archive member %s has empty symlink target", entry.Name)
		}

		if entry.Type == "symlink" && (filepath.IsAbs(entry.LinkTarget) || invalidChangedPath(filepath.ToSlash(entry.LinkTarget))) {
			return fmt.Errorf("archive member %s symlink target %q escapes the archive root", entry.Name, entry.LinkTarget)
		}

		if entry.Type != "symlink" && entry.LinkTarget != "" {
			return fmt.Errorf("archive member %s has unexpected symlink target", entry.Name)
		}
	}

	return nil
}

func targetWithinAllowedRoots(target string, roots []string) bool {
	if target == "" || filepath.IsAbs(target) || invalidChangedPath(filepath.ToSlash(target)) {
		return false
	}

	cleanTarget := filepath.ToSlash(filepath.Clean(target))
	for _, root := range roots {
		cleanRoot := filepath.ToSlash(filepath.Clean(root))
		if cleanRoot == "." || invalidChangedPath(cleanRoot) {
			continue
		}

		if cleanTarget == cleanRoot || strings.HasPrefix(cleanTarget, cleanRoot+"/") {
			return true
		}
	}

	return false
}

func cloneFixtureRun(run FixtureRun) FixtureRun {
	clone := run
	clone.Manifest = run.Manifest.copy()
	clone.KnownFiles = cloneFixtureFiles(run.KnownFiles)
	clone.Environment = cloneStringMap(run.Environment)
	clone.PlanOptions.ChangedFiles = append([]string(nil), run.PlanOptions.ChangedFiles...)
	clone.PlanOptions.DetectorErrors = append([]string(nil), run.PlanOptions.DetectorErrors...)
	clone.WorkflowData = cloneGeneratedWorkflowData(run.WorkflowData)
	clone.Observations = append([]JobObservation(nil), run.Observations...)
	clone.CacheRestores = cloneCacheRestoreChecks(run.CacheRestores)
	clone.Artifacts = cloneArtifactChecks(run.Artifacts)
	clone.ArchiveComparisons = cloneArchiveComparisons(run.ArchiveComparisons)
	clone.CredentialOperations = cloneCredentialOperations(run.CredentialOperations)

	return clone
}

func cloneFixtureFiles(files []FixtureFile) []FixtureFile {
	clone := append([]FixtureFile(nil), files...)
	for index := range clone {
		clone[index].Content = append([]byte(nil), files[index].Content...)
	}

	return clone
}

func cloneCacheRestoreChecks(checks []CacheRestoreCheck) []CacheRestoreCheck {
	clone := append([]CacheRestoreCheck(nil), checks...)
	for index := range clone {
		clone[index].Request.Plan = checks[index].Request.Plan.copy()
		clone[index].Request.Job.Matrix = cloneStringMap(checks[index].Request.Job.Matrix)
		clone[index].Request.Job.EvidenceRefs = append([]string(nil), checks[index].Request.Job.EvidenceRefs...)
		clone[index].Job.Matrix = cloneStringMap(checks[index].Job.Matrix)
		clone[index].Job.EvidenceRefs = append([]string(nil), checks[index].Job.EvidenceRefs...)
	}

	return clone
}

func cloneArtifactChecks(checks []ArtifactCheck) []ArtifactCheck {
	clone := append([]ArtifactCheck(nil), checks...)
	for index := range clone {
		clone[index].Expectation.Plan = checks[index].Expectation.Plan.copy()
		clone[index].Expectation.Job.Matrix = cloneStringMap(checks[index].Expectation.Job.Matrix)
		clone[index].Expectation.Job.EvidenceRefs = append([]string(nil), checks[index].Expectation.Job.EvidenceRefs...)
	}

	return clone
}

func cloneArchiveComparisons(comparisons []ArchiveComparison) []ArchiveComparison {
	clone := append([]ArchiveComparison(nil), comparisons...)
	for index := range clone {
		clone[index].Left.Entries = append([]ArchiveEntry(nil), comparisons[index].Left.Entries...)
		clone[index].Right.Entries = append([]ArchiveEntry(nil), comparisons[index].Right.Entries...)
	}

	return clone
}

func cloneCredentialOperations(operations []CredentialOperation) []CredentialOperation {
	clone := append([]CredentialOperation(nil), operations...)
	for index := range clone {
		clone[index].Token.Scopes = append([]string(nil), operations[index].Token.Scopes...)
		clone[index].Token.AllowedRoots = append([]string(nil), operations[index].Token.AllowedRoots...)
	}

	return clone
}

func cloneGeneratedWorkflowData(data GeneratedWorkflowData) GeneratedWorkflowData {
	clone := data
	clone.Jobs = append([]GeneratedWorkflowJob(nil), data.Jobs...)

	for index := range clone.Jobs {
		clone.Jobs[index].Matrix = cloneStringMap(data.Jobs[index].Matrix)
	}

	clone.Unsupported = append([]GeneratedUnsupported(nil), data.Unsupported...)

	return clone
}
