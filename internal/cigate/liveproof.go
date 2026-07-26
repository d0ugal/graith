package cigate

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const LiveProofSource = "github-live-fixture"

var requiredLiveProofCases = []string{
	"app-source-restriction",
	"artifact-run-provenance",
	"check-freshness",
	"fork-permissions",
	"merge-group-trigger",
	"missing-evidence",
	"pr-yaml-rewrite",
	"replay",
	"same-repository-agent-permissions",
	"stale-head-sha",
	"zero-job",
}

func RequiredLiveProofCases() []string {
	return append([]string(nil), requiredLiveProofCases...)
}

type LiveProofBundle struct {
	SchemaVersion     int               `json:"schema_version"`
	Source            string            `json:"source"`
	FixtureRepository string            `json:"fixture_repository"`
	AppID             int64             `json:"app_id"`
	AppSlug           string            `json:"app_slug"`
	RulesetCheckAppID int64             `json:"ruleset_check_app_id"`
	NoBypassActors    bool              `json:"no_bypass_actors"`
	MergeQueueEnabled bool              `json:"merge_queue_enabled"`
	CollectedAt       time.Time         `json:"collected_at"`
	Cases             []LiveProofCase   `json:"cases"`
	ExternalDecisions ExternalDecisions `json:"external_decisions"`
}

type LiveProofCase struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	EventDeliveryID string `json:"event_delivery_id"`
	HeadSHA         string `json:"head_sha"`
	BaseSHA         string `json:"base_sha"`
	ArtifactDigest  string `json:"artifact_digest"`
	RequiredCheckID string `json:"required_check_id"`
	EvidenceURI     string `json:"evidence_uri"`
}

type ExternalDecisions struct {
	Runtime               string `json:"runtime"`
	AttestationKeyService string `json:"attestation_key_service"`
	AttestationKeyID      string `json:"attestation_key_id"`
	TrustModel            string `json:"trust_model"`
	RotationOwner         string `json:"rotation_owner"`
	RetentionOwner        string `json:"retention_owner"`
	RevocationOwner       string `json:"revocation_owner"`
	OperatorOwner         string `json:"operator_owner"`
}

func DecodeLiveProofBundle(name string, data []byte) (LiveProofBundle, error) {
	var bundle LiveProofBundle
	if err := decodeStrict(name, data, &bundle); err != nil {
		return LiveProofBundle{}, err
	}

	return bundle, nil
}

func ValidateLiveProofBundle(bundle LiveProofBundle, config Config, now time.Time) error {
	if now.IsZero() {
		return errors.New("live proof validation time is required")
	}

	if err := config.Validate(); err != nil {
		return fmt.Errorf("trusted config: %w", err)
	}

	if bundle.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported live proof schema version %d", bundle.SchemaVersion)
	}

	if bundle.Source != LiveProofSource {
		return fmt.Errorf("live proof source %q cannot satisfy GitHub fixture claims", bundle.Source)
	}

	if invalidDecisionString(bundle.FixtureRepository) {
		return errors.New("live proof fixture repository is required")
	}

	if strings.EqualFold(bundle.FixtureRepository, config.Repository) {
		return errors.New("live proof fixture repository must be separate from the protected repository")
	}

	if bundle.FixtureRepository != config.LiveProof.FixtureRepository {
		return fmt.Errorf("live proof fixture repository %q does not match trusted config %q", bundle.FixtureRepository, config.LiveProof.FixtureRepository)
	}

	if bundle.AppID <= 0 || bundle.RulesetCheckAppID <= 0 {
		return errors.New("live proof must include positive App ids")
	}

	if bundle.AppID != bundle.RulesetCheckAppID {
		return errors.New("live proof must bind the required check to the installed App id")
	}

	if bundle.AppID != config.App.ID {
		return fmt.Errorf("live proof App id %d does not match trusted config App id %d", bundle.AppID, config.App.ID)
	}

	if bundle.AppSlug != config.App.Slug {
		return fmt.Errorf("live proof app slug %q must match trusted config app slug %q", bundle.AppSlug, config.App.Slug)
	}

	if !bundle.NoBypassActors {
		return errors.New("live proof must show no bypass actors")
	}

	if !bundle.MergeQueueEnabled {
		return errors.New("live proof must show merge queue enabled")
	}

	if bundle.CollectedAt.IsZero() || bundle.CollectedAt.After(now.Add(5*time.Minute)) {
		return errors.New("live proof collection time is invalid")
	}

	if now.Sub(bundle.CollectedAt) > 30*24*time.Hour {
		return errors.New("live proof is older than the 30-day fixture freshness window")
	}

	if err := bundle.ExternalDecisions.Validate(); err != nil {
		return err
	}

	if err := validateExternalDecisionAnchors(bundle.ExternalDecisions, config); err != nil {
		return err
	}

	seen := map[string]LiveProofCase{}
	seenArtifactDigests := map[string]string{}
	seenCheckIDs := map[string]string{}
	seenDeliveryIDs := map[string]string{}
	seenEvidenceURIs := map[string]string{}

	for _, proofCase := range bundle.Cases {
		if proofCase.ID == "" {
			return errors.New("live proof contains a case without an id")
		}

		if seen[proofCase.ID].ID != "" {
			return fmt.Errorf("duplicate live proof case %s", proofCase.ID)
		}

		if proofCase.Status != "passed" {
			return fmt.Errorf("live proof case %s status = %q, want passed", proofCase.ID, proofCase.Status)
		}

		if invalidDecisionString(proofCase.EventDeliveryID) {
			return fmt.Errorf("live proof case %s is missing event delivery id", proofCase.ID)
		}

		if !gitDigestPattern.MatchString(proofCase.HeadSHA) || !gitDigestPattern.MatchString(proofCase.BaseSHA) {
			return fmt.Errorf("live proof case %s has invalid head/base SHA", proofCase.ID)
		}

		if err := validateDigest("live proof artifact", proofCase.ArtifactDigest); err != nil {
			return fmt.Errorf("live proof case %s: %w", proofCase.ID, err)
		}

		if invalidDecisionString(proofCase.RequiredCheckID) || invalidDecisionString(proofCase.EvidenceURI) {
			return fmt.Errorf("live proof case %s is missing retained check/evidence identity", proofCase.ID)
		}

		for field, check := range map[string]struct {
			value string
			seen  map[string]string
		}{
			"artifact digest":   {value: proofCase.ArtifactDigest, seen: seenArtifactDigests},
			"event delivery":    {value: proofCase.EventDeliveryID, seen: seenDeliveryIDs},
			"evidence URI":      {value: proofCase.EvidenceURI, seen: seenEvidenceURIs},
			"required check id": {value: proofCase.RequiredCheckID, seen: seenCheckIDs},
		} {
			if prior := check.seen[check.value]; prior != "" {
				return fmt.Errorf("live proof case %s reuses %s from case %s", proofCase.ID, field, prior)
			}

			check.seen[check.value] = proofCase.ID
		}

		seen[proofCase.ID] = proofCase
	}

	var missing []string

	for _, required := range requiredLiveProofCases {
		if seen[required].ID == "" {
			missing = append(missing, required)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("live proof is missing required case(s): %s", strings.Join(missing, ", "))
	}

	return nil
}

func validateExternalDecisionAnchors(decisions ExternalDecisions, config Config) error {
	expected := map[string][2]string{
		"runtime":                 {decisions.Runtime, config.Deployment.Runtime},
		"attestation key service": {decisions.AttestationKeyService, config.Deployment.AttestationKey.Service},
		"attestation key id":      {decisions.AttestationKeyID, config.Deployment.AttestationKey.KeyID},
		"trust model":             {decisions.TrustModel, config.Deployment.AttestationKey.TrustModel},
		"rotation owner":          {decisions.RotationOwner, config.Deployment.Rotation.Owner},
		"retention owner":         {decisions.RetentionOwner, config.Retention.Owner},
		"revocation owner":        {decisions.RevocationOwner, config.Deployment.IncidentRevocation.Owner},
	}

	for name, pair := range expected {
		if pair[0] != pair[1] {
			return fmt.Errorf("live proof external decision %s %q does not match trusted config %q", name, pair[0], pair[1])
		}
	}

	if !contains(config.Operators, decisions.OperatorOwner) {
		return fmt.Errorf("live proof external decision operator owner %q is not in trusted config operators", decisions.OperatorOwner)
	}

	return nil
}

func (decisions ExternalDecisions) Validate() error {
	required := map[string]string{
		"runtime":                 decisions.Runtime,
		"attestation key service": decisions.AttestationKeyService,
		"attestation key id":      decisions.AttestationKeyID,
		"trust model":             decisions.TrustModel,
		"rotation owner":          decisions.RotationOwner,
		"retention owner":         decisions.RetentionOwner,
		"revocation owner":        decisions.RevocationOwner,
		"operator owner":          decisions.OperatorOwner,
	}
	for name, value := range required {
		if invalidDecisionString(value) {
			return fmt.Errorf("external decision %s must be explicit", name)
		}
	}

	return nil
}
