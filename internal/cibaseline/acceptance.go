package cibaseline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

const AcceptanceSchemaVersion = 1
const p0CollectedInventoryDigest = "5b03df0aa114f60b8e6d0883886fe609440bb20c1cc7ca3681a19d28f64cd9cd"
const p0ReboundInventoryDigest = "90fc3b97b8a8c4ca8bff4c55b475a0b3b3ec9dec90f1212a1539f8b396748f21"
const p0ModeMatrixDigest = "ad551fcf9d7a5fe1f96a88fd25644ca687b88e67a82cc3b4a1ef73f8399ac63e"
const p0InventoryRebindSource = "origin/main 549b13e chore: cache docker lint runs"
const p0InventoryRebindDerivation = "canonical inventory JSON is identical after deleting digest and replacing policy_surfaces[path=Makefile].sha256 with <ignored>"
const p0OldMakefileSHA256 = "5e73ab72b853251ddcd27c599c4524961e723aa426409130ead7847360906384"
const p0ReboundMakefileSHA256 = "fad6702e29ec45d0031da82ead1234ca7b17e567637451fc086d4e25688add86"
const p0SignOffApprovalSource = "graith message msg_e9c209629cb97781"

var hexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var (
	approvedP0Requests = map[string]struct {
		since time.Time
		until time.Time
	}{
		"p0-2026-07-25T060500Z-2026-07-25T120500Z": {
			since: time.Date(2026, 7, 25, 6, 5, 0, 0, time.UTC),
			until: time.Date(2026, 7, 25, 12, 5, 0, 0, time.UTC),
		},
		"p0-2026-07-25T084200Z-2026-07-25T144200Z": {
			since: time.Date(2026, 7, 25, 8, 42, 0, 0, time.UTC),
			until: time.Date(2026, 7, 25, 14, 42, 0, 0, time.UTC),
		},
	}
)

type AcceptanceValidationOptions struct {
	AllowIncomplete bool
	ManifestPath    string
}

type AcceptanceManifest struct {
	SchemaVersion         int                          `json:"schema_version"`
	CollectionRequest     CollectionRequestManifest    `json:"collection_request"`
	InventoryDigest       string                       `json:"inventory_digest"`
	InventoryRebind       *InventoryRebindManifest     `json:"inventory_rebind,omitempty"`
	EvidencePackage       EvidencePackageManifest      `json:"evidence_package"`
	LatencyPolicies       []LatencyPolicyRow           `json:"latency_policies"`
	ModeMatrix            []ModeMatrixRow              `json:"mode_matrix"`
	ChangeClassifications []ChangeClassificationRow    `json:"change_classifications"`
	ObservedCells         []ObservedCellRow            `json:"observed_cells"`
	GapRows               []AcceptanceGapRow           `json:"gap_rows,omitempty"`
	RepresentativeReplay  RepresentativeReplayManifest `json:"representative_replay"`
	SignOff               AcceptanceSignOff            `json:"sign_off"`
	Result                AcceptanceResult             `json:"result"`
	Digest                string                       `json:"digest"`
}

type CollectionRequestManifest struct {
	ID                           string    `json:"id"`
	Owner                        string    `json:"owner"`
	ApprovedBy                   string    `json:"approved_by"`
	ApprovalSource               string    `json:"approval_source"`
	RequestedSince               time.Time `json:"requested_since"`
	RequestedUntil               time.Time `json:"requested_until"`
	FixedContiguous              bool      `json:"fixed_contiguous"`
	AbsoluteCeilingRunnerMinutes int64     `json:"absolute_ceiling_runner_minutes"`
}

type EvidencePackageManifest struct {
	Location                            string   `json:"location"`
	ManifestKind                        string   `json:"manifest_kind"`
	WindowBundlePath                    string   `json:"window_bundle_path,omitempty"`
	WindowBundleDigest                  string   `json:"window_bundle_digest,omitempty"`
	RepoEvidencePath                    string   `json:"repo_evidence_path,omitempty"`
	RepoEvidenceDigest                  string   `json:"repo_evidence_digest,omitempty"`
	ExternalRunDigests                  []string `json:"external_run_digests,omitempty"`
	ModeMatrixDigest                    string   `json:"mode_matrix_digest"`
	ObservedCellCount                   int      `json:"observed_cell_count"`
	GapRowCount                         int      `json:"gap_row_count"`
	MeasurementState                    string   `json:"measurement_state"`
	MeasurementIncompleteReason         string   `json:"measurement_incomplete_reason,omitempty"`
	CurrentGraphRunnerMinutes           *int64   `json:"current_graph_runner_minutes,omitempty"`
	CurrentGraphRunnerMinutesSource     string   `json:"current_graph_runner_minutes_source,omitempty"`
	CurrentGraphRunnerMinutesDerivation string   `json:"current_graph_runner_minutes_derivation,omitempty"`
	ObservedCellRunnerMinutes           *int64   `json:"observed_cell_runner_minutes,omitempty"`
	ObservedCellRunnerMinutesSource     string   `json:"observed_cell_runner_minutes_source,omitempty"`
	ObservedCellRunnerMinutesDerivation string   `json:"observed_cell_runner_minutes_derivation,omitempty"`
	RepoOwnedExecutionMillis            *int64   `json:"repo_owned_execution_millis,omitempty"`
	RepoOwnedAggregateDurationMinutes   *int64   `json:"repo_owned_aggregate_duration_minutes,omitempty"`
}

type InventoryRebindManifest struct {
	FromDigest              string                  `json:"from_digest"`
	ToDigest                string                  `json:"to_digest"`
	Source                  string                  `json:"source"`
	Derivation              string                  `json:"derivation"`
	WorkflowDelta           string                  `json:"workflow_delta"`
	LegacyMappingDelta      string                  `json:"legacy_mapping_delta"`
	RequiredContextsDelta   string                  `json:"required_contexts_delta"`
	ModeMatrixDelta         string                  `json:"mode_matrix_delta"`
	ModeMatrixDigest        string                  `json:"mode_matrix_digest"`
	EvidenceEffect          string                  `json:"evidence_effect"`
	ChangedPolicySurfaces   []InventorySurfaceDelta `json:"changed_policy_surfaces"`
	OwnerReviewRequired     bool                    `json:"owner_review_required"`
	DualRunSamplingEligible bool                    `json:"dual_run_sampling_eligible"`
}

type InventorySurfaceDelta struct {
	Path        string `json:"path"`
	Owner       string `json:"owner"`
	Kind        string `json:"kind"`
	GitMode     string `json:"git_mode"`
	OldSHA256   string `json:"old_sha256"`
	NewSHA256   string `json:"new_sha256"`
	Contract    string `json:"executable_contract"`
	Disposition string `json:"disposition"`
	Retirement  string `json:"retirement_criterion"`
}

type LatencyPolicyRow struct {
	ID                    string `json:"id"`
	EventShape            string `json:"event_shape"`
	Policy                string `json:"policy"`
	TargetMinutes         int    `json:"target_minutes,omitempty"`
	Owner                 string `json:"owner"`
	ApprovalSource        string `json:"approval_source"`
	PreCollectionApproved bool   `json:"pre_collection_approved"`
	ExpiresOn             string `json:"expires_on,omitempty"`
	Rationale             string `json:"rationale"`
}

type ModeMatrixRow struct {
	WorkflowID       string `json:"workflow_id"`
	ModeCoordinate   string `json:"mode_coordinate"`
	Capability       string `json:"capability"`
	ProofType        string `json:"proof_type"`
	Requiredness     string `json:"requiredness"`
	Owner            string `json:"owner"`
	LegacyCoordinate string `json:"legacy_coordinate"`
	LegacyCondition  string `json:"legacy_condition,omitempty"`
	SkipSemantics    string `json:"skip_semantics"`
	NewMode          string `json:"new_mode"`
	NewObligation    bool   `json:"new_obligation"`
	Justification    string `json:"justification,omitempty"`
}

type ObservedCellRow struct {
	ID                      string `json:"id"`
	ChangeID                string `json:"change_id"`
	Event                   string `json:"event"`
	Ref                     string `json:"ref"`
	EventShape              string `json:"event_shape"`
	WorkflowID              string `json:"workflow_id,omitempty"`
	WorkflowPath            string `json:"workflow_path,omitempty"`
	ModeCoordinate          string `json:"mode_coordinate,omitempty"`
	LatencyPolicyID         string `json:"latency_policy_id"`
	EvidenceState           string `json:"evidence_state"`
	MatchedRunnerMinutes    bool   `json:"matched_runner_minutes"`
	RunnerMinutes           *int64 `json:"runner_minutes,omitempty"`
	RunnerMinutesSource     string `json:"runner_minutes_source,omitempty"`
	RunnerMinutesDerivation string `json:"runner_minutes_derivation,omitempty"`
	RunID                   int64  `json:"run_id,omitempty"`
	RunAttempt              int    `json:"run_attempt,omitempty"`
	JobID                   int64  `json:"job_id,omitempty"`
	GapRowID                string `json:"gap_row_id,omitempty"`
}

type AcceptanceGapRow struct {
	ID                        string    `json:"id"`
	Classification            string    `json:"classification"`
	Owner                     string    `json:"owner"`
	ApprovalSource            string    `json:"approval_source"`
	ExpiresOn                 string    `json:"expires_on"`
	Rationale                 string    `json:"rationale"`
	WorkflowID                string    `json:"workflow_id,omitempty"`
	WorkflowPath              string    `json:"workflow_path,omitempty"`
	Event                     string    `json:"event"`
	Ref                       string    `json:"ref,omitempty"`
	EventShape                string    `json:"event_shape"`
	ChangeID                  string    `json:"change_id"`
	ModeCoordinate            string    `json:"mode_coordinate,omitempty"`
	LatencyPolicyID           string    `json:"latency_policy_id"`
	RunID                     int64     `json:"run_id,omitempty"`
	RunAttempt                int       `json:"run_attempt,omitempty"`
	JobID                     int64     `json:"job_id,omitempty"`
	RawCreatedAt              time.Time `json:"raw_created_at,omitempty"`
	RawStartedAt              time.Time `json:"raw_started_at,omitempty"`
	RawCompletedAt            time.Time `json:"raw_completed_at,omitempty"`
	RawConclusion             string    `json:"raw_conclusion,omitempty"`
	TimingEndpointRunnerClass string    `json:"timing_endpoint_runner_class,omitempty"`
	TimingEndpointTotalMillis *int64    `json:"timing_endpoint_total_millis,omitempty"`
	DerivedRunnerMinutes      *int64    `json:"derived_runner_minutes,omitempty"`
	RunnerMinutesSource       string    `json:"runner_minutes_source,omitempty"`
	RunnerMinutesDerivation   string    `json:"runner_minutes_derivation,omitempty"`
	MatchedRunnerMinutes      bool      `json:"matched_runner_minutes"`
	DualRunEligible           bool      `json:"dual_run_eligible"`
	BlocksP0                  bool      `json:"blocks_p0"`
}

type ChangeClassificationRow struct {
	ID              string   `json:"id"`
	ChangeID        string   `json:"change_id"`
	Event           string   `json:"event"`
	Ref             string   `json:"ref"`
	PullRequest     int64    `json:"pull_request,omitempty"`
	EventShape      string   `json:"event_shape"`
	LatencyPolicyID string   `json:"latency_policy_id"`
	Owner           string   `json:"owner"`
	Source          string   `json:"source"`
	Basis           string   `json:"basis"`
	Files           []string `json:"files,omitempty"`
}

type P0WindowEvidenceBundle struct {
	SchemaVersion      int                         `json:"schema_version"`
	RequestID          string                      `json:"request_id"`
	Repository         string                      `json:"repository"`
	RequestedSince     time.Time                   `json:"requested_since"`
	RequestedUntil     time.Time                   `json:"requested_until"`
	CollectedAt        time.Time                   `json:"collected_at"`
	InventoryDigest    string                      `json:"inventory_digest"`
	InventoryRebind    *InventoryRebindManifest    `json:"inventory_rebind,omitempty"`
	EvidenceDigest     string                      `json:"evidence_digest"`
	ExternalRunDigests []string                    `json:"external_run_digests"`
	Evidence           Evidence                    `json:"evidence"`
	ExternalRuns       []GitHubExternalRunEvidence `json:"external_runs"`
	Digest             string                      `json:"digest"`
}

type RepresentativeReplayManifest struct {
	Status          string                    `json:"status"`
	ObservedModeSet string                    `json:"observed_mode_set"`
	Changes         []RepresentativeChangeRow `json:"changes"`
	BlockedReason   string                    `json:"blocked_reason,omitempty"`
}

type RepresentativeChangeRow struct {
	ChangeID        string   `json:"change_id"`
	Source          string   `json:"source"`
	EventShapes     []string `json:"event_shapes"`
	ModeCoordinates []string `json:"mode_coordinates"`
	ReplayStatus    string   `json:"replay_status"`
}

type AcceptanceSignOff struct {
	OwnerReviewed             bool      `json:"owner_reviewed"`
	Owner                     string    `json:"owner,omitempty"`
	ReviewedAt                time.Time `json:"reviewed_at,omitempty"`
	ApprovalSource            string    `json:"approval_source,omitempty"`
	ReviewedManifestDigest    string    `json:"reviewed_manifest_digest,omitempty"`
	InventoryDigest           string    `json:"inventory_digest,omitempty"`
	InventoryRebindFromDigest string    `json:"inventory_rebind_from_digest,omitempty"`
	InventoryRebindToDigest   string    `json:"inventory_rebind_to_digest,omitempty"`
	ModeMatrixDigest          string    `json:"mode_matrix_digest,omitempty"`
	WindowBundleDigest        string    `json:"window_bundle_digest,omitempty"`
	RepoEvidenceDigest        string    `json:"repo_evidence_digest,omitempty"`
	ExternalRunDigests        []string  `json:"external_run_digests,omitempty"`
	ObservedCellCount         int       `json:"observed_cell_count,omitempty"`
	GapRowCount               int       `json:"gap_row_count,omitempty"`
	Rationale                 string    `json:"rationale,omitempty"`
}

type AcceptanceResult struct {
	P0ExitSatisfied  bool     `json:"p0_exit_satisfied"`
	UnsatisfiedCells []string `json:"unsatisfied_cells,omitempty"`
	Reasons          []string `json:"reasons,omitempty"`
}

func FinalizeAcceptanceManifest(manifest AcceptanceManifest) (AcceptanceManifest, error) {
	manifest.Digest = ""

	data, err := json.Marshal(manifest)
	if err != nil {
		return AcceptanceManifest{}, err
	}

	manifest.Digest = sum(data)

	return manifest, nil
}

func BuildModeMatrix(inventory Inventory) ([]ModeMatrixRow, error) {
	expected, err := expectedModeMatrix(inventory)
	if err != nil {
		return nil, err
	}

	rows := make([]ModeMatrixRow, 0, len(expected))
	for _, row := range expected {
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].ModeCoordinate < rows[j].ModeCoordinate })

	return rows, nil
}

func ValidateAcceptanceManifest(manifest AcceptanceManifest, inventory Inventory, opts AcceptanceValidationOptions) error {
	if manifest.SchemaVersion != AcceptanceSchemaVersion {
		return fmt.Errorf("unsupported acceptance schema %d", manifest.SchemaVersion)
	}

	if err := inventory.Validate(); err != nil {
		return fmt.Errorf("validate inventory for acceptance: %w", err)
	}

	signed, err := FinalizeAcceptanceManifest(manifest)
	if err != nil {
		return err
	}

	if manifest.Digest == "" || signed.Digest != manifest.Digest {
		return errors.New("acceptance manifest digest mismatch")
	}

	if err := validateCollectionRequest(manifest.CollectionRequest); err != nil {
		return err
	}

	if manifest.InventoryDigest == "" || manifest.InventoryDigest != inventory.Digest {
		return fmt.Errorf("acceptance inventory digest mismatch: got %s want %s", manifest.InventoryDigest, inventory.Digest)
	}

	latencyPolicies, err := validateLatencyPolicies(manifest.LatencyPolicies, inventory, manifest.CollectionRequest.RequestedUntil)
	if err != nil {
		return err
	}

	modeRows, matrixDigest, err := validateModeMatrix(manifest.ModeMatrix, inventory)
	if err != nil {
		return err
	}

	if err := validateInventoryRebind(manifest.InventoryRebind, inventory, matrixDigest, manifest.CollectionRequest.ID); err != nil {
		return err
	}

	if err := validateEvidencePackage(manifest.EvidencePackage, matrixDigest, len(manifest.ObservedCells), len(manifest.GapRows), manifest.CollectionRequest, manifest.Result.P0ExitSatisfied); err != nil {
		return err
	}

	gaps, err := validateGapRows(manifest.GapRows, modeRows, latencyPolicies, manifest.CollectionRequest.RequestedUntil)
	if err != nil {
		return err
	}

	classifications, err := validateChangeClassifications(manifest.ChangeClassifications, latencyPolicies, gaps)
	if err != nil {
		return err
	}

	if err := validateObservedCells(manifest.ObservedCells, modeRows, latencyPolicies, gaps, classifications); err != nil {
		return err
	}

	if err := validateModeCoverage(manifest.ObservedCells, gaps, modeRows, manifest.Result.P0ExitSatisfied); err != nil {
		return err
	}

	if err := validateRepresentativeReplay(manifest.RepresentativeReplay, modeRows, manifest.Result.P0ExitSatisfied); err != nil {
		return err
	}

	if err := validateSignOff(manifest, matrixDigest); err != nil {
		return err
	}

	if err := validateAcceptanceResult(manifest.Result, manifest.GapRows, manifest.ObservedCells, manifest.EvidencePackage); err != nil {
		return err
	}

	if manifest.EvidencePackage.MeasurementState == "complete" && opts.ManifestPath == "" {
		return errors.New("complete acceptance requires manifest path for retained evidence replay")
	}

	if opts.ManifestPath != "" {
		if err := validateRetainedEvidencePackage(manifest, inventory, opts.ManifestPath); err != nil {
			return err
		}
	}

	if !manifest.Result.P0ExitSatisfied && !opts.AllowIncomplete {
		return fmt.Errorf("p0 acceptance unsatisfied: %s", strings.Join(manifest.Result.Reasons, "; "))
	}

	return nil
}

func validateCollectionRequest(request CollectionRequestManifest) error {
	approved, exists := approvedP0Requests[request.ID]
	if !exists ||
		request.Owner == "" || request.ApprovedBy == "" || request.ApprovalSource == "" ||
		!request.RequestedSince.Equal(approved.since) ||
		!request.RequestedUntil.Equal(approved.until) ||
		!request.FixedContiguous ||
		request.AbsoluteCeilingRunnerMinutes != 1000 {
		return errors.New("unexpected collection request identity or bounds")
	}

	return nil
}

func validateLatencyPolicies(rows []LatencyPolicyRow, inventory Inventory, until time.Time) (map[string]LatencyPolicyRow, error) {
	seen := map[string]LatencyPolicyRow{}
	eventShapes := map[string]bool{
		"pull_request/go-only":                true,
		"pull_request/gui-or-native-touching": true,
		"push/main":                           true,
		"push/release-please-branch":          true,
		"push/release-candidate":              true,
		"dynamic/dependabot/update-graph":     true,
	}

	for _, workflow := range inventory.Workflows {
		if strings.Contains(string(workflow.Events), `"workflow_dispatch"`) {
			eventShapes["workflow_dispatch/"+workflow.ID] = true
		}

		if strings.Contains(string(workflow.Events), `"schedule"`) {
			eventShapes["schedule/"+workflow.ID] = true
		}
	}

	for _, row := range rows {
		if row.ID == "" || row.EventShape == "" || row.Owner == "" || row.ApprovalSource == "" ||
			row.Rationale == "" || !row.PreCollectionApproved {
			return nil, fmt.Errorf("latency policy %s has incomplete approval metadata", row.ID)
		}

		if _, exists := seen[row.ID]; exists {
			return nil, fmt.Errorf("duplicate latency policy %q", row.ID)
		}

		if !eventShapes[row.EventShape] {
			return nil, fmt.Errorf("latency policy %s references unexpected event shape %q", row.ID, row.EventShape)
		}

		switch row.Policy {
		case "target":
			if row.TargetMinutes <= 0 {
				return nil, fmt.Errorf("latency policy %s has no target", row.ID)
			}
		case "no-latency-target":
			if row.TargetMinutes != 0 || row.Owner != "graith-maintainers" || row.ExpiresOn == "" {
				return nil, fmt.Errorf("latency policy %s has invalid no-latency-target approval", row.ID)
			}

			expiry, err := parseOwnerExpiry(row.ExpiresOn)
			if err != nil {
				return nil, fmt.Errorf("latency policy %s: %w", row.ID, err)
			}

			if expiry.Before(until) {
				return nil, fmt.Errorf("latency policy %s expired before collection end", row.ID)
			}
		default:
			return nil, fmt.Errorf("latency policy %s has unsupported policy %q", row.ID, row.Policy)
		}

		seen[row.ID] = row
		delete(eventShapes, row.EventShape)
	}

	if len(eventShapes) != 0 {
		var missing []string
		for eventShape := range eventShapes {
			missing = append(missing, eventShape)
		}

		sort.Strings(missing)

		return nil, fmt.Errorf("missing latency policy rows for %s", strings.Join(missing, ", "))
	}

	return seen, nil
}

func validateModeMatrix(rows []ModeMatrixRow, inventory Inventory) (map[string]ModeMatrixRow, string, error) {
	expected, err := expectedModeMatrix(inventory)
	if err != nil {
		return nil, "", err
	}

	seen := map[string]ModeMatrixRow{}

	for _, row := range rows {
		if row.ModeCoordinate == "" {
			return nil, "", errors.New("mode matrix row has no coordinate")
		}

		if _, exists := seen[row.ModeCoordinate]; exists {
			return nil, "", fmt.Errorf("duplicate mode matrix row %q", row.ModeCoordinate)
		}

		want, exists := expected[row.ModeCoordinate]
		if !exists {
			return nil, "", fmt.Errorf("orphan mode matrix row %q", row.ModeCoordinate)
		}

		if row != want {
			return nil, "", fmt.Errorf("mode matrix row %q does not match inventory", row.ModeCoordinate)
		}

		seen[row.ModeCoordinate] = row
	}

	if len(seen) != len(expected) {
		var missing []string

		for coordinate := range expected {
			if _, exists := seen[coordinate]; !exists {
				missing = append(missing, coordinate)
			}
		}

		sort.Strings(missing)

		return nil, "", fmt.Errorf("missing mode matrix rows for %s", strings.Join(missing, ", "))
	}

	digest := digestJSON(rows)

	return seen, digest, nil
}

func validateInventoryRebind(rebind *InventoryRebindManifest, inventory Inventory, matrixDigest, requestID string) error {
	_, isP0Request := approvedP0Requests[requestID]

	requiresRebind := isP0Request && inventory.Digest != p0CollectedInventoryDigest
	if requiresRebind && inventory.Digest != p0ReboundInventoryDigest {
		return errors.New("inventory rebind target is not the owner-approved P0 inventory digest")
	}

	if rebind == nil {
		if requiresRebind {
			return errors.New("inventory rebind metadata is required after baseline inventory drift")
		}

		return nil
	}

	if rebind.FromDigest != p0CollectedInventoryDigest ||
		rebind.ToDigest != p0ReboundInventoryDigest ||
		rebind.ToDigest != inventory.Digest ||
		!hexDigestPattern.MatchString(rebind.FromDigest) ||
		!hexDigestPattern.MatchString(rebind.ToDigest) {
		return errors.New("inventory rebind has unexpected digest endpoints")
	}

	if matrixDigest != p0ModeMatrixDigest {
		return errors.New("inventory rebind target mode matrix is not the owner-approved P0 matrix")
	}

	if rebind.Source != p0InventoryRebindSource ||
		rebind.Derivation != p0InventoryRebindDerivation ||
		rebind.WorkflowDelta != "none" ||
		rebind.LegacyMappingDelta != "none" ||
		rebind.RequiredContextsDelta != "none" ||
		rebind.ModeMatrixDelta != "none" ||
		rebind.ModeMatrixDigest != p0ModeMatrixDigest ||
		rebind.EvidenceEffect != "offline-replay-only; no workflow/event/permission/coordinate/required-context claim changed" ||
		!rebind.OwnerReviewRequired ||
		rebind.DualRunSamplingEligible {
		return errors.New("inventory rebind proof is incomplete")
	}

	if len(rebind.ChangedPolicySurfaces) != 1 {
		return errors.New("inventory rebind must identify exactly one policy surface delta")
	}

	delta := rebind.ChangedPolicySurfaces[0]
	if delta.Path != "Makefile" || delta.OldSHA256 != p0OldMakefileSHA256 ||
		delta.NewSHA256 != p0ReboundMakefileSHA256 {
		return errors.New("inventory rebind policy surface delta is not the approved Makefile checksum change")
	}

	for _, surface := range inventory.Surfaces {
		if surface.Path != delta.Path {
			continue
		}

		if delta.Owner != surface.Owner ||
			delta.Kind != surface.Kind ||
			delta.GitMode != surface.GitMode ||
			delta.NewSHA256 != surface.SHA256 ||
			delta.Contract != surface.Contract ||
			delta.Disposition != surface.Disposition ||
			delta.Retirement != surface.Retirement {
			return errors.New("inventory rebind policy surface delta does not match current inventory")
		}

		return nil
	}

	return errors.New("inventory rebind policy surface is missing from current inventory")
}

func expectedModeMatrix(inventory Inventory) (map[string]ModeMatrixRow, error) {
	expected := map[string]ModeMatrixRow{}
	mappings := map[string]Mapping{}

	for _, mapping := range inventory.Mappings {
		mappings[mapping.LegacyCoordinate] = mapping
	}

	for _, workflow := range inventory.Workflows {
		for _, job := range workflow.Jobs {
			for _, coordinate := range job.Coordinates {
				mapping, exists := mappings[coordinate]
				if !exists {
					return nil, fmt.Errorf("inventory coordinate %s has no legacy mapping", coordinate)
				}

				expected[coordinate] = ModeMatrixRow{
					WorkflowID:       workflow.ID,
					ModeCoordinate:   coordinate,
					Capability:       job.Capability,
					ProofType:        job.ProofType,
					Requiredness:     job.Requiredness,
					Owner:            job.Owner,
					LegacyCoordinate: mapping.LegacyCoordinate,
					LegacyCondition:  mapping.LegacyCondition,
					SkipSemantics:    mapping.SkipSemantics,
					NewMode:          mapping.NewMode,
					NewObligation:    mapping.NewObligation,
					Justification:    mapping.Justification,
				}
			}
		}
	}

	return expected, nil
}

func validateEvidencePackage(pkg EvidencePackageManifest, matrixDigest string, observedCells, gapRows int, request CollectionRequestManifest, satisfied bool) error {
	if pkg.Location == "" || pkg.ManifestKind != "p0-acceptance" {
		return errors.New("evidence package has incomplete identity")
	}

	if pkg.ModeMatrixDigest == "" || pkg.ModeMatrixDigest != matrixDigest {
		return errors.New("evidence package mode matrix digest mismatch")
	}

	if pkg.ObservedCellCount != observedCells || pkg.GapRowCount != gapRows {
		return errors.New("evidence package counts do not match manifest rows")
	}

	switch pkg.MeasurementState {
	case "complete":
		if pkg.CurrentGraphRunnerMinutes == nil {
			return errors.New("complete evidence package has no runner-minute total")
		}

		if filepath.Clean(pkg.Location) != filepath.Dir(pkg.WindowBundlePath) ||
			filepath.Clean(pkg.Location) != filepath.Dir(pkg.RepoEvidencePath) {
			return errors.New("evidence package location does not match retained evidence paths")
		}

		if pkg.WindowBundlePath == "" || pkg.WindowBundleDigest == "" ||
			pkg.RepoEvidencePath == "" || pkg.RepoEvidenceDigest == "" ||
			!hexDigestPattern.MatchString(pkg.WindowBundleDigest) ||
			!hexDigestPattern.MatchString(pkg.RepoEvidenceDigest) ||
			len(pkg.ExternalRunDigests) == 0 ||
			pkg.CurrentGraphRunnerMinutesSource == "" ||
			pkg.CurrentGraphRunnerMinutesDerivation == "" ||
			pkg.ObservedCellRunnerMinutes == nil ||
			pkg.ObservedCellRunnerMinutesSource == "" ||
			pkg.ObservedCellRunnerMinutesDerivation == "" ||
			pkg.RepoOwnedExecutionMillis == nil ||
			pkg.RepoOwnedAggregateDurationMinutes == nil {
			return errors.New("complete evidence package has incomplete retained-evidence accounting")
		}

		for _, digest := range pkg.ExternalRunDigests {
			if !hexDigestPattern.MatchString(digest) {
				return fmt.Errorf("complete evidence package has invalid external run digest %q", digest)
			}
		}

		if *pkg.CurrentGraphRunnerMinutes > request.AbsoluteCeilingRunnerMinutes {
			return fmt.Errorf("runner-minute measurement %d exceeds ceiling %d", *pkg.CurrentGraphRunnerMinutes, request.AbsoluteCeilingRunnerMinutes)
		}
	case "incomplete":
		if pkg.MeasurementIncompleteReason == "" {
			return errors.New("incomplete evidence package has no measurement reason")
		}

		if satisfied {
			return errors.New("p0 cannot be satisfied with incomplete runner-minute measurements")
		}
	default:
		return fmt.Errorf("evidence package has unsupported measurement state %q", pkg.MeasurementState)
	}

	return nil
}

func validateChangeClassifications(rows []ChangeClassificationRow, policies map[string]LatencyPolicyRow, gaps map[string]AcceptanceGapRow) (map[string]ChangeClassificationRow, error) {
	seen := map[string]ChangeClassificationRow{}

	for _, row := range rows {
		if row.ID == "" || row.ChangeID == "" || row.Event == "" || row.Ref == "" ||
			row.EventShape == "" || row.LatencyPolicyID == "" || row.Owner == "" ||
			row.Source == "" || row.Basis == "" {
			return nil, fmt.Errorf("change classification %s has incomplete identity", row.ID)
		}

		key := classificationKey(row.ChangeID, row.Event, row.Ref)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate change classification %q", key)
		}

		policy, exists := policies[row.LatencyPolicyID]
		if !exists {
			return nil, fmt.Errorf("change classification %s references unknown latency policy %q", row.ID, row.LatencyPolicyID)
		}

		if policy.EventShape != row.EventShape {
			return nil, fmt.Errorf("change classification %s has policy/event-shape mismatch", row.ID)
		}

		if !strings.HasPrefix(row.EventShape, row.Event+"/") && !hasApprovedEventShapeException(row, gaps) {
			return nil, fmt.Errorf("change classification %s event shape %q does not match event %q", row.ID, row.EventShape, row.Event)
		}

		if row.Event == "pull_request" && row.PullRequest == 0 && len(row.Files) == 0 {
			return nil, fmt.Errorf("pull request change classification %s has no PR or file evidence", row.ID)
		}

		if !sort.StringsAreSorted(row.Files) {
			return nil, fmt.Errorf("change classification %s files are not sorted", row.ID)
		}

		for index, file := range row.Files {
			if strings.TrimSpace(file) == "" || (index > 0 && row.Files[index-1] == file) {
				return nil, fmt.Errorf("change classification %s has invalid file evidence", row.ID)
			}
		}

		seen[key] = row
	}

	return seen, nil
}

func hasApprovedEventShapeException(row ChangeClassificationRow, gaps map[string]AcceptanceGapRow) bool {
	for _, gap := range gaps {
		if gap.Classification == "provider-timestamp-anomaly" &&
			gap.ChangeID == row.ChangeID &&
			gap.Event == row.Event &&
			gap.Ref == row.Ref &&
			gap.EventShape == row.EventShape &&
			gap.LatencyPolicyID == row.LatencyPolicyID {
			return true
		}
	}

	return false
}

func validateGapRows(rows []AcceptanceGapRow, modes map[string]ModeMatrixRow, policies map[string]LatencyPolicyRow, until time.Time) (map[string]AcceptanceGapRow, error) {
	seen := map[string]AcceptanceGapRow{}

	for _, row := range rows {
		if row.ID == "" || row.Classification == "" || row.Owner == "" || row.ApprovalSource == "" ||
			row.ExpiresOn == "" || row.Rationale == "" {
			return nil, fmt.Errorf("gap row %s has incomplete approval metadata", row.ID)
		}

		if _, exists := seen[row.ID]; exists {
			return nil, fmt.Errorf("duplicate gap row %q", row.ID)
		}

		expiry, err := parseOwnerExpiry(row.ExpiresOn)
		if err != nil {
			return nil, fmt.Errorf("gap row %s: %w", row.ID, err)
		}

		if expiry.Before(until) {
			return nil, fmt.Errorf("gap row %s expired before collection end", row.ID)
		}

		switch row.Classification {
		case "external-workflow":
			if row.Event == "" || row.EventShape == "" || row.ChangeID == "" || row.LatencyPolicyID == "" {
				return nil, fmt.Errorf("gap row %s has incomplete approval metadata", row.ID)
			}

			if _, exists := policies[row.LatencyPolicyID]; !exists {
				return nil, fmt.Errorf("gap row %s references unknown latency policy %q", row.ID, row.LatencyPolicyID)
			}

			if row.Owner != "graith-maintainers" || row.WorkflowPath != "dynamic/dependabot/update-graph" ||
				row.Event != "dynamic" || row.EventShape != "dynamic/dependabot/update-graph" ||
				row.RunID != 30152132020 || row.JobID != 89664191125 ||
				!row.MatchedRunnerMinutes || row.DerivedRunnerMinutes == nil ||
				*row.DerivedRunnerMinutes <= 0 || row.RunnerMinutesSource == "" ||
				row.RunnerMinutesDerivation == "" || row.DualRunEligible || row.BlocksP0 ||
				row.ModeCoordinate != "" || row.WorkflowID != "" {
				return nil, fmt.Errorf("gap row %s does not match approved external workflow gap", row.ID)
			}
		case "provider-timestamp-anomaly":
			if row.Event == "" || row.EventShape == "" || row.ChangeID == "" || row.LatencyPolicyID == "" {
				return nil, fmt.Errorf("gap row %s has incomplete approval metadata", row.ID)
			}

			if _, exists := policies[row.LatencyPolicyID]; !exists {
				return nil, fmt.Errorf("gap row %s references unknown latency policy %q", row.ID, row.LatencyPolicyID)
			}

			if row.WorkflowID != "goreleaser" || row.WorkflowPath != ".github/workflows/goreleaser.yml" ||
				row.Event != "pull_request" || row.ModeCoordinate != "goreleaser/release-context" ||
				row.RunID != 30151461867 || row.JobID != 89662641145 ||
				row.RawConclusion != "cancelled" || row.MatchedRunnerMinutes ||
				row.DualRunEligible || !row.BlocksP0 ||
				row.RawCreatedAt.IsZero() || row.RawStartedAt.IsZero() || row.RawCompletedAt.IsZero() ||
				!row.RawCompletedAt.Before(row.RawStartedAt) {
				return nil, fmt.Errorf("gap row %s does not match approved provider timestamp anomaly", row.ID)
			}

			if _, exists := modes[row.ModeCoordinate]; !exists {
				return nil, fmt.Errorf("gap row %s references unknown mode %q", row.ID, row.ModeCoordinate)
			}
		case "mode-not-exercised":
			mode, exists := modes[row.ModeCoordinate]
			if !exists {
				return nil, fmt.Errorf("gap row %s references unknown mode %q", row.ID, row.ModeCoordinate)
			}

			if row.Owner != mode.Owner ||
				row.ApprovalSource != "graith message msg_5c3d2768f6e92272" ||
				row.WorkflowID != mode.WorkflowID ||
				row.WorkflowPath != "" ||
				row.Event != "not-observed" ||
				row.Ref != "" ||
				row.EventShape != "not-observed/mode" ||
				row.ChangeID != "not-observed" ||
				row.LatencyPolicyID != "" ||
				row.RunID != 0 ||
				row.RunAttempt != 0 ||
				row.JobID != 0 ||
				row.MatchedRunnerMinutes ||
				row.DerivedRunnerMinutes != nil ||
				row.RunnerMinutesSource != "" ||
				row.RunnerMinutesDerivation != "" ||
				row.DualRunEligible ||
				row.BlocksP0 {
				return nil, fmt.Errorf("gap row %s does not match approved mode-not-exercised gap", row.ID)
			}
		default:
			return nil, fmt.Errorf("gap row %s has unsupported classification %q", row.ID, row.Classification)
		}

		seen[row.ID] = row
	}

	return seen, nil
}

func validateObservedCells(
	rows []ObservedCellRow,
	modes map[string]ModeMatrixRow,
	policies map[string]LatencyPolicyRow,
	gaps map[string]AcceptanceGapRow,
	classifications map[string]ChangeClassificationRow,
) error {
	seen := map[string]bool{}

	for _, row := range rows {
		if row.ID == "" || row.ChangeID == "" || row.Event == "" || row.Ref == "" || row.EventShape == "" ||
			row.LatencyPolicyID == "" || row.EvidenceState == "" {
			return fmt.Errorf("observed cell %s has incomplete identity", row.ID)
		}

		if seen[row.ID] {
			return fmt.Errorf("duplicate observed cell %q", row.ID)
		}

		seen[row.ID] = true

		if _, exists := policies[row.LatencyPolicyID]; !exists {
			return fmt.Errorf("observed cell %s references unknown latency policy %q", row.ID, row.LatencyPolicyID)
		}

		classification, exists := classifications[classificationKey(row.ChangeID, row.Event, row.Ref)]
		if !exists {
			return fmt.Errorf("observed cell %s has no change classification", row.ID)
		}

		if classification.EventShape != row.EventShape || classification.LatencyPolicyID != row.LatencyPolicyID {
			return fmt.Errorf("observed cell %s does not match change classification", row.ID)
		}

		switch row.EvidenceState {
		case "observed":
			if row.WorkflowID == "" || row.ModeCoordinate == "" || row.RunID <= 0 || row.RunAttempt <= 0 || row.JobID <= 0 {
				return fmt.Errorf("observed cell %s has incomplete retained evidence identity", row.ID)
			}

			if _, exists := modes[row.ModeCoordinate]; !exists {
				return fmt.Errorf("observed cell %s references unknown mode %q", row.ID, row.ModeCoordinate)
			}

			if !row.MatchedRunnerMinutes || row.RunnerMinutes == nil ||
				row.RunnerMinutesSource == "" || row.RunnerMinutesDerivation == "" {
				return fmt.Errorf("observed cell %s is missing matched runner-minute measurement", row.ID)
			}
		case "gap", "anomaly":
			gap, exists := gaps[row.GapRowID]
			if row.GapRowID == "" || !exists {
				return fmt.Errorf("observed cell %s references unknown gap row %q", row.ID, row.GapRowID)
			}

			if row.EvidenceState == "anomaly" && gap.Classification != "provider-timestamp-anomaly" {
				return fmt.Errorf("observed cell %s has mismatched anomaly gap %q", row.ID, row.GapRowID)
			}

			if row.EvidenceState == "gap" && gap.Classification != "external-workflow" {
				return fmt.Errorf("observed cell %s has mismatched gap row %q", row.ID, row.GapRowID)
			}

			if gap.ChangeID != row.ChangeID || gap.Event != row.Event || gap.EventShape != row.EventShape ||
				gap.LatencyPolicyID != row.LatencyPolicyID || (gap.Ref != "" && gap.Ref != row.Ref) {
				return fmt.Errorf("observed cell %s does not match gap row %q", row.ID, row.GapRowID)
			}

			if row.MatchedRunnerMinutes && (row.RunnerMinutes == nil ||
				row.RunnerMinutesSource == "" || row.RunnerMinutesDerivation == "") {
				return fmt.Errorf("observed cell %s is missing matched runner-minute measurement", row.ID)
			}
		default:
			return fmt.Errorf("observed cell %s has unsupported evidence state %q", row.ID, row.EvidenceState)
		}
	}

	return nil
}

func validateModeCoverage(rows []ObservedCellRow, gaps map[string]AcceptanceGapRow, modes map[string]ModeMatrixRow, satisfied bool) error {
	if !satisfied {
		return nil
	}

	covered := map[string]string{}

	for _, row := range rows {
		if row.EvidenceState == "observed" && row.ModeCoordinate != "" {
			covered[row.ModeCoordinate] = row.ID
		}
	}

	for _, gap := range gaps {
		if gap.Classification != "mode-not-exercised" {
			continue
		}

		if _, exists := covered[gap.ModeCoordinate]; exists {
			return fmt.Errorf("mode %s has both observed evidence and gap %s", gap.ModeCoordinate, gap.ID)
		}

		covered[gap.ModeCoordinate] = gap.ID
	}

	var missing []string

	for coordinate := range modes {
		if _, exists := covered[coordinate]; !exists {
			missing = append(missing, coordinate)
		}
	}

	if len(missing) != 0 {
		sort.Strings(missing)

		return fmt.Errorf("uncovered mode matrix rows for %s", strings.Join(missing, ", "))
	}

	return nil
}

func classificationKey(changeID, event, ref string) string {
	return changeID + "\x00" + event + "\x00" + ref
}

func validateRepresentativeReplay(replay RepresentativeReplayManifest, modes map[string]ModeMatrixRow, satisfied bool) error {
	if replay.ObservedModeSet == "" || !oneOf(replay.Status, "passed", "blocked") {
		return errors.New("representative replay has incomplete status")
	}

	if replay.Status == "blocked" {
		if replay.BlockedReason == "" {
			return errors.New("representative replay is blocked without a reason")
		}

		if satisfied {
			return errors.New("p0 cannot be satisfied with blocked representative replay")
		}

		return nil
	}

	if len(replay.Changes) == 0 {
		return errors.New("representative replay has no merged changes")
	}

	for _, change := range replay.Changes {
		if change.ChangeID == "" || change.Source == "" || change.ReplayStatus != "passed" ||
			len(change.EventShapes) == 0 || len(change.ModeCoordinates) == 0 {
			return fmt.Errorf("representative replay change %s is not representative", change.ChangeID)
		}

		for _, coordinate := range change.ModeCoordinates {
			if _, exists := modes[coordinate]; !exists {
				return fmt.Errorf("representative replay change %s references unexplained mode %q", change.ChangeID, coordinate)
			}
		}
	}

	return nil
}

func validateSignOff(manifest AcceptanceManifest, matrixDigest string) error {
	if !manifest.Result.P0ExitSatisfied {
		return nil
	}

	signoff := manifest.SignOff
	if !signoff.OwnerReviewed || signoff.Owner == "" || signoff.ReviewedAt.IsZero() {
		return errors.New("p0 acceptance requires owner sign-off")
	}

	reviewedDigest, err := preSignOffManifestDigest(manifest)
	if err != nil {
		return err
	}

	if signoff.Owner != "ci-north-star-rollout" ||
		signoff.ApprovalSource != p0SignOffApprovalSource ||
		signoff.ReviewedManifestDigest != reviewedDigest ||
		signoff.InventoryDigest != manifest.InventoryDigest ||
		signoff.ModeMatrixDigest != matrixDigest ||
		signoff.ModeMatrixDigest != manifest.EvidencePackage.ModeMatrixDigest ||
		signoff.WindowBundleDigest != manifest.EvidencePackage.WindowBundleDigest ||
		signoff.RepoEvidenceDigest != manifest.EvidencePackage.RepoEvidenceDigest ||
		!reflect.DeepEqual(signoff.ExternalRunDigests, manifest.EvidencePackage.ExternalRunDigests) ||
		signoff.ObservedCellCount != manifest.EvidencePackage.ObservedCellCount ||
		signoff.GapRowCount != manifest.EvidencePackage.GapRowCount {
		return errors.New("p0 acceptance owner sign-off is not bound to retained evidence")
	}

	if manifest.InventoryRebind == nil {
		if signoff.InventoryRebindFromDigest != "" || signoff.InventoryRebindToDigest != "" {
			return errors.New("p0 acceptance owner sign-off has unexpected inventory rebind binding")
		}

		return nil
	}

	if signoff.InventoryRebindFromDigest != manifest.InventoryRebind.FromDigest ||
		signoff.InventoryRebindToDigest != manifest.InventoryRebind.ToDigest {
		return errors.New("p0 acceptance owner sign-off is not bound to inventory rebind")
	}

	return nil
}

func preSignOffManifestDigest(manifest AcceptanceManifest) (string, error) {
	pending := manifest
	pending.SignOff = AcceptanceSignOff{OwnerReviewed: false}
	pending.Result = AcceptanceResult{
		P0ExitSatisfied: false,
		Reasons:         []string{"unsatisfied-owner-signoff"},
	}

	finalized, err := FinalizeAcceptanceManifest(pending)
	if err != nil {
		return "", err
	}

	return finalized.Digest, nil
}

func validateAcceptanceResult(result AcceptanceResult, gaps []AcceptanceGapRow, cells []ObservedCellRow, pkg EvidencePackageManifest) error {
	if result.P0ExitSatisfied {
		if len(result.Reasons) != 0 || len(result.UnsatisfiedCells) != 0 {
			return errors.New("satisfied p0 result still records unsatisfied evidence")
		}

		for _, gap := range gaps {
			if gap.BlocksP0 {
				return fmt.Errorf("satisfied p0 result has blocking gap %s", gap.ID)
			}
		}

		for _, cell := range cells {
			if !cell.MatchedRunnerMinutes && cell.EvidenceState != "gap" {
				return fmt.Errorf("satisfied p0 result has unmatched cell %s", cell.ID)
			}
		}

		return nil
	}

	if len(result.Reasons) == 0 {
		return errors.New("unsatisfied p0 result has no reason")
	}

	if pkg.MeasurementState == "incomplete" && pkg.MeasurementIncompleteReason == "" {
		return errors.New("unsatisfied p0 result has incomplete measurements without a reason")
	}

	return nil
}

func validateRetainedEvidencePackage(manifest AcceptanceManifest, inventory Inventory, manifestPath string) error {
	if manifest.EvidencePackage.MeasurementState != "complete" {
		return nil
	}

	bundlePath, err := resolveRetainedManifestPath(manifestPath, manifest.EvidencePackage.WindowBundlePath)
	if err != nil {
		return fmt.Errorf("window bundle path: %w", err)
	}

	repoEvidencePath, err := resolveRetainedManifestPath(manifestPath, manifest.EvidencePackage.RepoEvidencePath)
	if err != nil {
		return fmt.Errorf("repo evidence path: %w", err)
	}

	var bundle P0WindowEvidenceBundle
	if err := readStrictRetainedJSON(bundlePath, &bundle); err != nil {
		return err
	}

	if bundle.SchemaVersion != 1 {
		return fmt.Errorf("unsupported window bundle schema %d", bundle.SchemaVersion)
	}

	bundleDigest, err := p0LogicalDigest(P0WindowEvidenceBundle{
		SchemaVersion:      bundle.SchemaVersion,
		RequestID:          bundle.RequestID,
		Repository:         bundle.Repository,
		RequestedSince:     bundle.RequestedSince,
		RequestedUntil:     bundle.RequestedUntil,
		CollectedAt:        bundle.CollectedAt,
		InventoryDigest:    bundle.InventoryDigest,
		InventoryRebind:    bundle.InventoryRebind,
		EvidenceDigest:     bundle.EvidenceDigest,
		ExternalRunDigests: bundle.ExternalRunDigests,
		Evidence:           bundle.Evidence,
		ExternalRuns:       bundle.ExternalRuns,
	})
	if err != nil {
		return err
	}

	if bundle.Digest == "" || bundle.Digest != bundleDigest || bundle.Digest != manifest.EvidencePackage.WindowBundleDigest {
		return errors.New("window bundle digest mismatch")
	}

	if !strings.Contains(filepath.Base(bundlePath), bundle.Digest) {
		return errors.New("window bundle path is not content-addressed by its digest")
	}

	if bundle.RequestID != manifest.CollectionRequest.ID ||
		bundle.Repository == "" ||
		!bundle.RequestedSince.Equal(manifest.CollectionRequest.RequestedSince) ||
		!bundle.RequestedUntil.Equal(manifest.CollectionRequest.RequestedUntil) ||
		bundle.InventoryDigest != inventory.Digest ||
		bundle.InventoryDigest != manifest.InventoryDigest ||
		!inventoryRebindEqual(bundle.InventoryRebind, manifest.InventoryRebind) ||
		!inventoryRebindEqual(bundle.Evidence.InventoryRebind, manifest.InventoryRebind) {
		return errors.New("window bundle identity does not match acceptance request")
	}

	var repoEvidence Evidence
	if err := readStrictRetainedJSON(repoEvidencePath, &repoEvidence); err != nil {
		return err
	}

	if repoEvidence.Digest == "" || repoEvidence.Digest != manifest.EvidencePackage.RepoEvidenceDigest ||
		repoEvidence.Digest != bundle.EvidenceDigest || repoEvidence.Digest != bundle.Evidence.Digest {
		return errors.New("repo evidence digest mismatch")
	}

	if !inventoryRebindEqual(repoEvidence.InventoryRebind, manifest.InventoryRebind) {
		return errors.New("repo evidence inventory rebind does not match acceptance manifest")
	}

	if !strings.Contains(filepath.Base(repoEvidencePath), repoEvidence.Digest) {
		return errors.New("repo evidence path is not content-addressed by its digest")
	}

	if err := repoEvidence.Replay(inventory); err != nil {
		return fmt.Errorf("replay retained repo evidence: %w", err)
	}

	if err := bundle.Evidence.Replay(inventory); err != nil {
		return fmt.Errorf("replay bundled repo evidence: %w", err)
	}

	if !repoEvidence.RequestedSince.Equal(manifest.CollectionRequest.RequestedSince) ||
		!repoEvidence.RequestedUntil.Equal(manifest.CollectionRequest.RequestedUntil) ||
		bundle.Evidence.ExpectedWorkflowRuns != repoEvidence.ExpectedWorkflowRuns ||
		bundle.Evidence.ExpectedRunAttempts != repoEvidence.ExpectedRunAttempts ||
		bundle.Evidence.ExpectedCaches != repoEvidence.ExpectedCaches ||
		len(bundle.Evidence.Observations) != len(repoEvidence.Observations) {
		return errors.New("retained repo evidence identity or counts do not match bundle")
	}

	if len(bundle.ExternalRunDigests) != len(bundle.ExternalRuns) ||
		len(manifest.EvidencePackage.ExternalRunDigests) != len(bundle.ExternalRuns) {
		return errors.New("external run digest count mismatch")
	}

	for index, external := range bundle.ExternalRuns {
		digest, err := p0LogicalDigest(external)
		if err != nil {
			return err
		}

		if digest != bundle.ExternalRunDigests[index] ||
			digest != manifest.EvidencePackage.ExternalRunDigests[index] {
			return fmt.Errorf("external run %d digest mismatch", external.RunID)
		}

		if err := validateRetainedExternalRun(external); err != nil {
			return err
		}
	}

	return validateObservedCellsAgainstRetainedEvidence(manifest, repoEvidence, bundle.ExternalRuns)
}

func validateObservedCellsAgainstRetainedEvidence(
	manifest AcceptanceManifest,
	evidence Evidence,
	externalRuns []GitHubExternalRunEvidence,
) error {
	retained := map[string]RunEvidence{}

	for _, observation := range evidence.Observations {
		key := retainedObservationKey(observation.RunID, observation.Attempt, observation.JobID, observation.Coordinate)
		if _, exists := retained[key]; exists {
			return fmt.Errorf("duplicate retained observation key %s", key)
		}

		retained[key] = observation
	}

	externalByJob := map[string]GitHubExternalRunEvidence{}
	for _, external := range externalRuns {
		externalByJob[fmt.Sprintf("%d/%d/%d", external.RunID, external.Attempt, external.Job.ID)] = external
	}

	var (
		repoOwnedExecutionMillis int64
		repoOwnedRunnerMinutes   int64
		matchedRows              int
	)

	seenRetained := map[string]string{}
	seenExternal := map[string]string{}
	modeCoverage := map[string]bool{}

	for _, row := range manifest.ObservedCells {
		if !row.MatchedRunnerMinutes {
			continue
		}

		if row.RunnerMinutes == nil || *row.RunnerMinutes < 0 {
			return fmt.Errorf("observed cell %s has invalid runner-minute measurement", row.ID)
		}

		matchedRows++

		switch row.EvidenceState {
		case "observed":
			key := retainedObservationKey(row.RunID, row.RunAttempt, row.JobID, row.ModeCoordinate)
			observation, exists := retained[key]

			if !exists {
				return fmt.Errorf("observed cell %s is missing from retained repo evidence", row.ID)
			}

			if observation.HeadSHA != row.ChangeID || observation.Event != row.Event ||
				observation.HeadBranch != row.Ref || observation.WorkflowID != row.WorkflowID {
				return fmt.Errorf("observed cell %s does not match retained repo evidence identity", row.ID)
			}

			expectedMinutes := runnerMinutesFromMillis(observation.ExecutionMillis)
			if *row.RunnerMinutes != expectedMinutes {
				return fmt.Errorf("observed cell %s runner minutes = %d, want %d", row.ID, *row.RunnerMinutes, expectedMinutes)
			}

			if firstRow, exists := seenRetained[key]; exists {
				return fmt.Errorf("observed cells %s and %s reference retained observation %s", firstRow, row.ID, key)
			}

			repoOwnedRunnerMinutes += *row.RunnerMinutes
			repoOwnedExecutionMillis += observation.ExecutionMillis
			seenRetained[key] = row.ID

			if retainedObservationCoversMode(observation) {
				modeCoverage[row.ModeCoordinate] = true
			}
		case "gap":
			key := fmt.Sprintf("%d/%d/%d", row.RunID, row.RunAttempt, row.JobID)

			external, exists := externalByJob[key]
			if !exists {
				return fmt.Errorf("observed gap cell %s is missing from retained external evidence", row.ID)
			}

			if external.HeadSHA != row.ChangeID || external.Event != row.Event ||
				external.HeadBranch != row.Ref {
				return fmt.Errorf("observed gap cell %s does not match retained external evidence identity", row.ID)
			}

			expectedMinutes := runnerMinutesFromMillis(external.Job.ExecutionMillis)
			if *row.RunnerMinutes != expectedMinutes {
				return fmt.Errorf("observed gap cell %s runner minutes = %d, want %d", row.ID, *row.RunnerMinutes, expectedMinutes)
			}

			if firstRow, exists := seenExternal[key]; exists {
				return fmt.Errorf("observed cells %s and %s reference retained external run %s", firstRow, row.ID, key)
			}

			seenExternal[key] = row.ID
		}
	}

	if len(seenRetained) != len(retained) {
		return fmt.Errorf("retained repo evidence has %d observations but manifest accounts for %d", len(retained), len(seenRetained))
	}

	if len(seenExternal) != len(externalRuns) {
		return fmt.Errorf("retained external evidence has %d runs but manifest accounts for %d", len(externalRuns), len(seenExternal))
	}

	if manifest.EvidencePackage.RepoOwnedExecutionMillis == nil ||
		*manifest.EvidencePackage.RepoOwnedExecutionMillis != repoOwnedExecutionMillis {
		return fmt.Errorf("repo-owned execution millis = %d, want %d", valueOrZero(manifest.EvidencePackage.RepoOwnedExecutionMillis), repoOwnedExecutionMillis)
	}

	repoOwnedAggregateMinutes := runnerMinutesFromMillis(repoOwnedExecutionMillis)
	if manifest.EvidencePackage.RepoOwnedAggregateDurationMinutes == nil ||
		*manifest.EvidencePackage.RepoOwnedAggregateDurationMinutes != repoOwnedAggregateMinutes {
		return fmt.Errorf("repo-owned aggregate duration minutes = %d, want %d", valueOrZero(manifest.EvidencePackage.RepoOwnedAggregateDurationMinutes), repoOwnedAggregateMinutes)
	}

	if manifest.EvidencePackage.CurrentGraphRunnerMinutes == nil ||
		*manifest.EvidencePackage.CurrentGraphRunnerMinutes != repoOwnedRunnerMinutes {
		return fmt.Errorf("current graph runner minutes = %d, want %d", valueOrZero(manifest.EvidencePackage.CurrentGraphRunnerMinutes), repoOwnedRunnerMinutes)
	}

	if manifest.EvidencePackage.ObservedCellRunnerMinutes == nil ||
		*manifest.EvidencePackage.ObservedCellRunnerMinutes != repoOwnedRunnerMinutes {
		return fmt.Errorf("observed cell runner minutes = %d, want %d", valueOrZero(manifest.EvidencePackage.ObservedCellRunnerMinutes), repoOwnedRunnerMinutes)
	}

	if matchedRows == 0 {
		return errors.New("retained evidence has no matched runner-minute cells")
	}

	if err := validateRetainedModeCoverage(manifest, modeCoverage); err != nil {
		return err
	}

	return nil
}

func retainedObservationCoversMode(observation RunEvidence) bool {
	return !observation.SyntheticFanout ||
		observation.JobStatus != "completed" ||
		observation.JobConclusion != "cancelled" ||
		observation.Outcome != "cancelled"
}

func validateRetainedModeCoverage(manifest AcceptanceManifest, covered map[string]bool) error {
	if !manifest.Result.P0ExitSatisfied {
		return nil
	}

	gapModes := map[string]bool{}

	for _, gap := range manifest.GapRows {
		if gap.Classification == "mode-not-exercised" {
			gapModes[gap.ModeCoordinate] = true
		}
	}

	for _, mode := range manifest.ModeMatrix {
		if covered[mode.ModeCoordinate] || gapModes[mode.ModeCoordinate] {
			continue
		}

		return fmt.Errorf("mode %s has no retained coverage-eligible observation", mode.ModeCoordinate)
	}

	return nil
}

func validateRetainedExternalRun(external GitHubExternalRunEvidence) error {
	billableMinutes, hasBillableMinutes := external.Cost.BillableMinutes["UBUNTU"]
	timingMillis, hasTimingMillis := external.TimingBillableMillis["UBUNTU"]

	if external.RunID != 30152132020 || external.Attempt != 1 ||
		external.WorkflowPath != "dynamic/dependabot/update-graph" ||
		external.Event != "dynamic" ||
		external.HeadSHA != "63f89267ebd0a858e22782416e2905fa2fcd43b8" ||
		external.HeadBranch != "main" ||
		external.Status != "completed" || external.Conclusion != "success" ||
		external.ExpectedJobs != 1 ||
		external.Job.ID != 89664191125 ||
		external.Job.Name != "update-go_modules-graph" ||
		external.Job.ExecutionMillis != 24_000 ||
		!external.Cost.Available ||
		!hasBillableMinutes ||
		!hasTimingMillis ||
		billableMinutes != 0 ||
		timingMillis != 0 {
		return errors.New("retained external run does not match approved Dependabot evidence")
	}

	return nil
}

func resolveRetainedManifestPath(manifestPath, relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" {
		return "", errors.New("path is required")
	}

	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("absolute path %q is not allowed", relativePath)
	}

	clean := filepath.Clean(relativePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the manifest directory", relativePath)
	}

	base := filepath.Clean(filepath.Dir(manifestPath))
	full := filepath.Clean(filepath.Join(base, clean))

	rel, err := filepath.Rel(base, full)
	if err != nil {
		return "", err
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the manifest directory", relativePath)
	}

	return full, nil
}

func readStrictRetainedJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("decode %s: trailing JSON value", path)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s: %w", path, err)
	}

	return nil
}

func inventoryRebindEqual(a, b *InventoryRebindManifest) bool {
	return reflect.DeepEqual(a, b)
}

func p0LogicalDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}

	return sum(data), nil
}

func retainedObservationKey(runID int64, attempt int, jobID int64, coordinate string) string {
	return fmt.Sprintf("%d/%d/%d/%s", runID, attempt, jobID, coordinate)
}

func runnerMinutesFromMillis(millis int64) int64 {
	if millis <= 0 {
		return 0
	}

	return (millis + 59_999) / 60_000
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}

	return *value
}

func parseOwnerExpiry(value string) (time.Time, error) {
	expiry, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid expiry %q", value)
	}

	return expiry.Add(24*time.Hour - time.Nanosecond), nil
}

func digestJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return sum(data)
}
