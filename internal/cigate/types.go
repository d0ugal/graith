package cigate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/d0ugal/graith/internal/cipolicy"
)

const (
	SchemaVersion       = 1
	CheckName           = "graith-ci-gate"
	GitHubWebhookSource = "github-webhook"
)

var (
	digestPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitDigestPattern    = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	githubScopePattern  = regexp.MustCompile(`^[a-z_-]+:(read|write)$`)
	forbiddenLocalTerms = []string{"", "default", "local", "localhost", "pending", "tbd", "todo", "unset"}
)

type EvaluationInput struct {
	SchemaVersion int                     `json:"schema_version"`
	Config        Config                  `json:"config"`
	Delivery      DeliveryContext         `json:"delivery"`
	Event         EventContext            `json:"event"`
	Policy        cipolicy.Manifest       `json:"policy"`
	Plan          cipolicy.RunPlan        `json:"plan"`
	Results       []cipolicy.ResultRecord `json:"results"`
	Evidence      []CoordinateEvidence    `json:"evidence"`
	Evaluator     EvaluatorIdentity       `json:"evaluator"`
	Now           time.Time               `json:"now,omitempty"`
}

type TrustAnchors struct {
	Config               Config            `json:"config"`
	Policy               cipolicy.Manifest `json:"policy"`
	ExpectedPolicyDigest string            `json:"expected_policy_digest"`
	Evaluator            EvaluatorIdentity `json:"evaluator"`
}

type Config struct {
	SchemaVersion int                `json:"schema_version"`
	Repository    string             `json:"repository"`
	DefaultBranch string             `json:"default_branch"`
	App           AppContract        `json:"app"`
	Deployment    DeploymentContract `json:"deployment"`
	Retention     RetentionContract  `json:"retention"`
	LiveProof     LiveProofContract  `json:"live_proof"`
	Operators     []string           `json:"operators"`
}

type AppContract struct {
	Slug              string            `json:"slug"`
	ID                int64             `json:"id"`
	Owner             string            `json:"owner"`
	InstallationOwner string            `json:"installation_owner"`
	CheckName         string            `json:"check_name"`
	Permissions       map[string]string `json:"permissions"`
	Events            []string          `json:"events"`
}

type DeploymentContract struct {
	Runtime            string             `json:"runtime"`
	ReleaseDigest      string             `json:"release_digest"`
	EvaluatorDigest    string             `json:"evaluator_digest"`
	AttestationKey     AttestationKey     `json:"attestation_key"`
	Rotation           RotationContract   `json:"rotation"`
	IncidentRevocation RevocationContract `json:"incident_revocation"`
}

type AttestationKey struct {
	Service    string `json:"service"`
	KeyID      string `json:"key_id"`
	TrustModel string `json:"trust_model"`
}

type RotationContract struct {
	Owner   string `json:"owner"`
	Cadence string `json:"cadence"`
	Runbook string `json:"runbook"`
}

type RevocationContract struct {
	Owner   string `json:"owner"`
	Runbook string `json:"runbook"`
}

type RetentionContract struct {
	Owner    string `json:"owner"`
	Location string `json:"location"`
	Duration string `json:"duration"`
}

type LiveProofContract struct {
	FixtureRepository string `json:"fixture_repository"`
}

type DeliveryContext struct {
	ID                 string `json:"id"`
	Event              string `json:"event"`
	SignatureValidated bool   `json:"signature_validated"`
	BodyDigest         string `json:"body_digest"`
}

type EventContext struct {
	Source              string `json:"source"`
	GitHubEvent         string `json:"github_event"`
	PolicyGitHubEvent   string `json:"policy_github_event"`
	Action              string `json:"action"`
	Repository          string `json:"repository"`
	BaseRepository      string `json:"base_repository"`
	HeadRepository      string `json:"head_repository"`
	Ref                 string `json:"ref"`
	BaseRef             string `json:"base_ref"`
	HeadRef             string `json:"head_ref"`
	IntendedSHA         string `json:"intended_sha"`
	HeadSHA             string `json:"head_sha"`
	BaseSHA             string `json:"base_sha"`
	PolicyDigest        string `json:"policy_digest"`
	TrustTier           string `json:"trust_tier"`
	PullRequestFork     bool   `json:"pull_request_fork"`
	SameRepositoryAgent bool   `json:"same_repository_agent"`
	TrustedBase         bool   `json:"trusted_base"`
}

type EvaluatorIdentity struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	ReleaseDigest string `json:"release_digest"`
	SourceDigest  string `json:"source_digest"`
}

type CoordinateEvidence struct {
	Mode       string             `json:"mode"`
	Coordinate string             `json:"coordinate"`
	Run        RunProvenance      `json:"run"`
	Artifact   ArtifactProvenance `json:"artifact"`
}

type RunProvenance struct {
	ID              int64     `json:"id"`
	Attempt         int       `json:"attempt"`
	Repository      string    `json:"repository"`
	Event           string    `json:"event"`
	WorkflowName    string    `json:"workflow_name"`
	WorkflowPath    string    `json:"workflow_path"`
	WorkflowBlobSHA string    `json:"workflow_blob_sha"`
	HeadSHA         string    `json:"head_sha"`
	BaseSHA         string    `json:"base_sha"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
}

type ArtifactProvenance struct {
	Name               string    `json:"name"`
	Digest             string    `json:"digest"`
	Repository         string    `json:"repository"`
	WorkflowPath       string    `json:"workflow_path"`
	WorkflowBlobSHA    string    `json:"workflow_blob_sha"`
	ProducerRunID      int64     `json:"producer_run_id"`
	ProducerRunAttempt int       `json:"producer_run_attempt"`
	HeadSHA            string    `json:"head_sha"`
	BaseSHA            string    `json:"base_sha"`
	PlanDigest         string    `json:"plan_digest"`
	PolicyDigest       string    `json:"policy_digest"`
	UploadedAt         time.Time `json:"uploaded_at"`
}

type Evaluation struct {
	SchemaVersion int                  `json:"schema_version"`
	Check         CheckRun             `json:"check"`
	Report        cipolicy.FanInReport `json:"report,omitempty"`
	Evidence      RetainedEvidence     `json:"evidence"`
	Reasons       []string             `json:"reasons,omitempty"`
}

type CheckRun struct {
	Name        string    `json:"name"`
	HeadSHA     string    `json:"head_sha"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	CompletedAt time.Time `json:"completed_at"`
}

type RetainedEvidence struct {
	SchemaVersion       int      `json:"schema_version"`
	EventDeliveryID     string   `json:"event_delivery_id"`
	WebhookBodyDigest   string   `json:"webhook_body_digest"`
	IntendedSHA         string   `json:"intended_sha"`
	BaseSHA             string   `json:"base_sha"`
	PolicyDigest        string   `json:"policy_digest"`
	PlanDigest          string   `json:"plan_digest"`
	EvaluatorDigest     string   `json:"evaluator_digest"`
	ReleaseDigest       string   `json:"release_digest"`
	BundleDigest        string   `json:"bundle_digest"`
	ProducerRunAttempts []string `json:"producer_run_attempts"`
	WorkflowIdentities  []string `json:"workflow_identities"`
	ArtifactDigests     []string `json:"artifact_digests"`
}

func DecodeEvaluationInput(name string, data []byte) (EvaluationInput, error) {
	var input EvaluationInput
	if err := decodeStrict(name, data, &input); err != nil {
		return EvaluationInput{}, err
	}

	return input, nil
}

func DecodeConfig(name string, data []byte) (Config, error) {
	var config Config
	if err := decodeStrict(name, data, &config); err != nil {
		return Config{}, err
	}

	return config, nil
}

func EncodeCanonical(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}

func decodeStrict(name string, data []byte, target any) error {
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

func (config Config) Validate() error {
	if config.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported config schema version %d", config.SchemaVersion)
	}

	if config.Repository != cipolicy.DefaultRepository {
		return fmt.Errorf("config repository %q does not match policy repository %q", config.Repository, cipolicy.DefaultRepository)
	}

	if config.DefaultBranch != cipolicy.DefaultDefaultBranch {
		return fmt.Errorf("config default branch %q does not match policy branch %q", config.DefaultBranch, cipolicy.DefaultDefaultBranch)
	}

	if len(config.Operators) == 0 {
		return errors.New("config requires at least one operator owner")
	}

	for _, owner := range config.Operators {
		if invalidDecisionString(owner) {
			return errors.New("config contains a blank or placeholder operator owner")
		}
	}

	if err := config.App.Validate(); err != nil {
		return err
	}

	if err := config.Deployment.Validate(); err != nil {
		return err
	}

	if err := config.Retention.Validate(); err != nil {
		return err
	}

	return config.LiveProof.Validate(config.Repository)
}

func (app AppContract) Validate() error {
	if strings.TrimSpace(app.Slug) != CheckName {
		return fmt.Errorf("app slug %q must be %q", app.Slug, CheckName)
	}

	if app.ID <= 0 {
		return errors.New("app id is required")
	}

	if invalidDecisionString(app.Owner) || invalidDecisionString(app.InstallationOwner) {
		return errors.New("app owner and installation owner are required")
	}

	if app.CheckName != CheckName {
		return fmt.Errorf("app check name %q must be %q", app.CheckName, CheckName)
	}

	want := map[string]string{
		"metadata":      "read",
		"contents":      "read",
		"actions":       "read",
		"pull_requests": "read",
		"checks":        "write",
	}
	for permission, access := range want {
		if got := app.Permissions[permission]; got != access {
			return fmt.Errorf("app permission %s = %q, want %q", permission, got, access)
		}
	}

	if got := app.Permissions["statuses"]; got != "" && got != "write" {
		return fmt.Errorf("app statuses permission = %q, want omitted or write", got)
	}

	for permission, access := range app.Permissions {
		if !githubScopePattern.MatchString(permission + ":" + access) {
			return fmt.Errorf("app permission %s:%s is invalid", permission, access)
		}

		if _, required := want[permission]; !required && permission != "statuses" {
			return fmt.Errorf("app permission %s is outside the P4 least-privilege contract", permission)
		}
	}

	events := sortedStrings(app.Events)
	if !equalStrings(events, []string{"merge_group", "pull_request"}) {
		return fmt.Errorf("app events = %v, want [merge_group pull_request]", events)
	}

	return nil
}

func (deployment DeploymentContract) Validate() error {
	if invalidDecisionString(deployment.Runtime) {
		return errors.New("deployment runtime must be an explicit hosted runtime, not a local or pending default")
	}

	if err := validateDigest("release", deployment.ReleaseDigest); err != nil {
		return err
	}

	if err := validateDigest("evaluator", deployment.EvaluatorDigest); err != nil {
		return err
	}

	if deployment.AttestationKey.Service == "" || invalidDecisionString(deployment.AttestationKey.Service) {
		return errors.New("attestation key service must be explicitly selected")
	}

	if invalidDecisionString(deployment.AttestationKey.KeyID) {
		return errors.New("attestation key id is required")
	}

	if strings.TrimSpace(deployment.AttestationKey.TrustModel) == "" {
		return errors.New("attestation trust model is required")
	}

	if invalidDecisionString(deployment.AttestationKey.TrustModel) {
		return errors.New("attestation trust model must be explicit")
	}

	if err := deployment.Rotation.Validate("rotation"); err != nil {
		return err
	}

	return deployment.IncidentRevocation.Validate()
}

func (rotation RotationContract) Validate(kind string) error {
	if invalidDecisionString(rotation.Owner) {
		return fmt.Errorf("%s owner is required", kind)
	}

	if invalidDecisionString(rotation.Cadence) {
		return fmt.Errorf("%s cadence is required", kind)
	}

	if invalidDecisionString(rotation.Runbook) {
		return fmt.Errorf("%s runbook is required", kind)
	}

	return nil
}

func (revocation RevocationContract) Validate() error {
	if invalidDecisionString(revocation.Owner) {
		return errors.New("incident revocation owner is required")
	}

	if invalidDecisionString(revocation.Runbook) {
		return errors.New("incident revocation runbook is required")
	}

	return nil
}

func (retention RetentionContract) Validate() error {
	if invalidDecisionString(retention.Owner) {
		return errors.New("retention owner is required")
	}

	if invalidDecisionString(retention.Location) {
		return errors.New("retention location is required")
	}

	duration, err := time.ParseDuration(retention.Duration)
	if err != nil {
		return fmt.Errorf("retention duration %q: %w", retention.Duration, err)
	}

	if duration < 90*24*time.Hour {
		return fmt.Errorf("retention duration %s is shorter than the P4 90-day evidence floor", duration)
	}

	return nil
}

func (proof LiveProofContract) Validate(repository string) error {
	if invalidDecisionString(proof.FixtureRepository) {
		return errors.New("live proof fixture repository is required")
	}

	if strings.EqualFold(proof.FixtureRepository, repository) {
		return errors.New("live proof fixture repository must be separate from the protected repository")
	}

	return nil
}

func validateDigest(kind, digest string) error {
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("%s digest %q is invalid", kind, digest)
	}

	return nil
}

func invalidDecisionString(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return true
	}

	if decisionURLIsLocal(normalized) {
		return true
	}

	for _, token := range strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		for _, forbidden := range forbiddenLocalTerms {
			if token == forbidden {
				return true
			}
		}
	}

	return false
}

func decisionURLIsLocal(normalized string) bool {
	parsed, err := url.Parse(normalized)
	if err != nil {
		return false
	}

	if parsed.Scheme == "file" || parsed.Scheme == "unix" {
		return true
	}

	host := parsed.Hostname()
	if host == "" {
		return false
	}

	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}

	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}

	return address.IsLoopback()
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)

	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}
