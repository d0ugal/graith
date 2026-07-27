package cipolicy

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/cibaseline"
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9./_-]*[a-z0-9]$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	evidencePattern = regexp.MustCompile(`^(p0-inventory:[0-9a-f]{64}#[A-Za-z0-9./=_,\[\]-]+|github-check-name:.+|github-actions:run/[0-9]+/job/[0-9]+|p0-acceptance:[A-Za-z0-9./=_-]+)$`)
)

type ownedIdentity struct {
	id          string
	owner       string
	description string
}

func (manifest Manifest) Validate() error {
	return manifest.ValidateAt(time.Now().UTC())
}

func (manifest Manifest) ValidateAt(now time.Time) error {
	if now.IsZero() {
		return errors.New("validation time is required")
	}

	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema version %d", manifest.SchemaVersion)
	}

	if manifest.PolicyVersion != PolicyVersion {
		return fmt.Errorf("unsupported policy version %q", manifest.PolicyVersion)
	}

	if manifest.PolicyDigest == "" {
		return errors.New("policy digest is required")
	}

	digest, err := manifest.Digest()
	if err != nil {
		return err
	}

	if manifest.PolicyDigest != digest {
		return fmt.Errorf("policy digest mismatch: got %s want %s", manifest.PolicyDigest, digest)
	}

	baselineDigest, err := validateSource(manifest.Source)
	if err != nil {
		return err
	}

	trustTiers, err := validateTrustTiers(manifest.TrustTiers)
	if err != nil {
		return err
	}

	events, err := validateEvents(manifest.Events, trustTiers)
	if err != nil {
		return err
	}

	platforms, err := validatePlatforms(manifest.Platforms)
	if err != nil {
		return err
	}

	costClasses, err := validateCostClasses(manifest.CostClasses)
	if err != nil {
		return err
	}

	capabilities, capabilityModes, err := validateCapabilities(manifest.Capabilities)
	if err != nil {
		return err
	}

	modes, coordinates, err := validateModes(manifest.Modes, capabilities, capabilityModes, events, trustTiers, platforms, costClasses, baselineDigest)
	if err != nil {
		return err
	}

	return validateUnsupported(manifest.Unsupported, modes, coordinates, events, trustTiers, platforms, baselineDigest)
}

func validateSource(source SourceIdentity) (string, error) {
	if source.ID != sourceID {
		return "", fmt.Errorf("source identity %q is unsupported", source.ID)
	}

	if source.Repository != DefaultRepository || source.DefaultBranch != DefaultDefaultBranch ||
		source.PolicyPath != DefaultManifestPath {
		return "", fmt.Errorf("source identity must be %s %s %s", DefaultRepository, DefaultDefaultBranch, DefaultManifestPath)
	}

	baseline := source.BaselineInventory
	if baseline.Path != DefaultInventoryPath {
		return "", fmt.Errorf("source baseline inventory path must be %s", DefaultInventoryPath)
	}

	if baseline.SchemaVersion != cibaseline.SchemaVersion {
		return "", fmt.Errorf("source baseline inventory schema version must be %d", cibaseline.SchemaVersion)
	}

	if !digestPattern.MatchString(baseline.Digest) {
		return "", errors.New("source baseline inventory digest is invalid")
	}

	if baseline.ObservationState != "p0-observation-in-progress" {
		return "", fmt.Errorf("source baseline inventory observation state %q is unsupported", baseline.ObservationState)
	}

	return baseline.Digest, nil
}

func validateTrustTiers(values []TrustTier) (map[string]TrustTier, error) {
	return validateOwnedIdentities(
		"trust tier",
		"trust tiers",
		values,
		func(tier TrustTier) ownedIdentity {
			return ownedIdentity{id: tier.ID, owner: tier.Owner, description: tier.Description}
		},
		[]string{"fork-untrusted", "github-service", "same-repository-agent", "trusted-base", "trusted-publication"},
	)
}

func validateEvents(values []EventIdentity, trustTiers map[string]TrustTier) (map[string]EventIdentity, error) {
	if len(values) == 0 {
		return nil, errors.New("no events declared")
	}

	result := map[string]EventIdentity{}

	for _, event := range values {
		if !validID(event.ID) {
			return nil, fmt.Errorf("invalid event %q", event.ID)
		}

		if _, exists := result[event.ID]; exists {
			return nil, fmt.Errorf("duplicate event %q", event.ID)
		}

		if event.Source != sourceID || !oneOf(event.GitHubEvent, "dynamic", "pull_request", "push", "schedule", "workflow_dispatch") ||
			event.Description == "" {
			return nil, fmt.Errorf("event %s has invalid source identity", event.ID)
		}

		if len(event.TrustTiers) == 0 {
			return nil, fmt.Errorf("event %s has no trust tier", event.ID)
		}

		for _, tier := range event.TrustTiers {
			if _, exists := trustTiers[tier]; !exists {
				return nil, fmt.Errorf("event %s references unknown trust tier %s", event.ID, tier)
			}
		}

		result[event.ID] = event
	}

	for _, required := range []string{"dynamic", "pull-request", "push-main", "push-tag", "schedule", "workflow-dispatch"} {
		if _, exists := result[required]; !exists {
			return nil, fmt.Errorf("missing required event %q", required)
		}
	}

	return result, nil
}

func validatePlatforms(values []Platform) (map[string]Platform, error) {
	if len(values) == 0 {
		return nil, errors.New("no platforms declared")
	}

	result := map[string]Platform{}

	for _, platform := range values {
		if !validID(platform.ID) {
			return nil, fmt.Errorf("invalid platform %q", platform.ID)
		}

		if _, exists := result[platform.ID]; exists {
			return nil, fmt.Errorf("duplicate platform %q", platform.ID)
		}

		if platform.Owner == "" || platform.RunnerLabel == "" || platform.OS == "" || platform.Architecture == "" ||
			platform.Description == "" {
			return nil, fmt.Errorf("platform %s has incomplete identity", platform.ID)
		}

		result[platform.ID] = platform
	}

	for _, required := range []string{"github-service", "macos-26", "macos-latest", "ubuntu-24.04", "ubuntu-24.04-arm", "ubuntu-latest"} {
		if _, exists := result[required]; !exists {
			return nil, fmt.Errorf("missing required platform %q", required)
		}
	}

	return result, nil
}

func validateCostClasses(values []CostClass) (map[string]CostClass, error) {
	return validateOwnedIdentities(
		"cost class",
		"cost classes",
		values,
		func(costClass CostClass) ownedIdentity {
			return ownedIdentity{id: costClass.ID, owner: costClass.Owner, description: costClass.Description}
		},
		[]string{"external", "linux-fast", "linux-standard", "macos", "publication"},
	)
}

func validateCapabilities(values []Capability) (map[string]Capability, map[string]string, error) {
	if len(values) == 0 {
		return nil, nil, errors.New("no capabilities declared")
	}

	result := map[string]Capability{}
	modeOwners := map[string]string{}

	for _, capability := range values {
		if !validID(capability.ID) {
			return nil, nil, fmt.Errorf("invalid capability %q", capability.ID)
		}

		if _, exists := result[capability.ID]; exists {
			return nil, nil, fmt.Errorf("duplicate capability %q", capability.ID)
		}

		if capability.Owner == "" || capability.Description == "" || len(capability.Modes) == 0 {
			return nil, nil, fmt.Errorf("capability %s has incomplete ownership or modes", capability.ID)
		}

		for _, mode := range capability.Modes {
			if modeOwners[mode] != "" {
				return nil, nil, fmt.Errorf("mode %s is claimed by multiple capabilities", mode)
			}

			modeOwners[mode] = capability.ID
		}

		result[capability.ID] = capability
	}

	return result, modeOwners, nil
}

func validateModes(
	values []Mode,
	capabilities map[string]Capability,
	capabilityModes map[string]string,
	events map[string]EventIdentity,
	trustTiers map[string]TrustTier,
	platforms map[string]Platform,
	costClasses map[string]CostClass,
	baselineDigest string,
) (map[string]Mode, map[string]string, error) {
	if len(values) == 0 {
		return nil, nil, errors.New("no modes declared")
	}

	result := map[string]Mode{}
	coordinates := map[string]string{}
	githubNames := map[string]string{}

	for _, mode := range values {
		if !validID(mode.ID) {
			return nil, nil, fmt.Errorf("invalid mode %q", mode.ID)
		}

		if _, exists := result[mode.ID]; exists {
			return nil, nil, fmt.Errorf("duplicate mode ID %q", mode.ID)
		}

		if _, exists := capabilities[mode.Capability]; !exists {
			return nil, nil, fmt.Errorf("mode %s references unknown capability %s", mode.ID, mode.Capability)
		}

		if capabilityModes[mode.ID] != mode.Capability {
			return nil, nil, fmt.Errorf("mode %s is not listed by capability %s", mode.ID, mode.Capability)
		}

		if mode.Owner == "" {
			return nil, nil, fmt.Errorf("mode %s has no owner", mode.ID)
		}

		if !oneOf(mode.Requiredness, "required", "soft", "deferred") {
			return nil, nil, fmt.Errorf("mode %s has ambiguous requiredness %q", mode.ID, mode.Requiredness)
		}

		if _, exists := costClasses[mode.CostClass]; !exists {
			return nil, nil, fmt.Errorf("mode %s references unknown cost class %s", mode.ID, mode.CostClass)
		}

		if !oneOf(mode.ProofType, "source-level", "package-consumer", "compile-only", "runtime", "scheduled", "soft") {
			return nil, nil, fmt.Errorf("mode %s has unsupported proof type %q", mode.ID, mode.ProofType)
		}

		if len(mode.SourceEvents) == 0 {
			return nil, nil, fmt.Errorf("mode %s has no source event identity", mode.ID)
		}

		modeEventIDs := map[string]bool{}
		modeEvents := []EventIdentity{}
		allowedTrustTiers := map[string]bool{}

		for _, sourceEvent := range mode.SourceEvents {
			if sourceEvent.Source != sourceID {
				return nil, nil, fmt.Errorf("mode %s references invalid source %s", mode.ID, sourceEvent.Source)
			}

			event, exists := events[sourceEvent.Event]
			if !exists {
				return nil, nil, fmt.Errorf("mode %s references unknown event %s", mode.ID, sourceEvent.Event)
			}

			if !modeEventIDs[sourceEvent.Event] {
				modeEventIDs[sourceEvent.Event] = true

				modeEvents = append(modeEvents, event)
			}

			for _, tier := range event.TrustTiers {
				allowedTrustTiers[tier] = true
			}
		}

		if len(mode.TrustTiers) == 0 {
			return nil, nil, fmt.Errorf("mode %s has no trust tier", mode.ID)
		}

		modeTrustTiers := map[string]bool{}

		for _, tier := range mode.TrustTiers {
			if _, exists := trustTiers[tier]; !exists {
				return nil, nil, fmt.Errorf("mode %s references unknown trust tier %s", mode.ID, tier)
			}

			if !allowedTrustTiers[tier] {
				return nil, nil, fmt.Errorf("mode %s trust tier %s is not allowed by source events", mode.ID, tier)
			}

			modeTrustTiers[tier] = true
		}

		for _, event := range modeEvents {
			if !hasTrustTierIntersection(modeTrustTiers, event.TrustTiers) {
				return nil, nil, fmt.Errorf("mode %s has no trust tier allowed by event %s", mode.ID, event.ID)
			}
		}

		if len(mode.Coordinates) == 0 {
			return nil, nil, fmt.Errorf("mode %s has no coordinates", mode.ID)
		}

		if err := validateEvidenceRefs(mode.ID, mode.EvidenceRefs, baselineDigest); err != nil {
			return nil, nil, err
		}

		if err := validateLegacyTrace(mode.ID, mode.Trace); err != nil {
			return nil, nil, err
		}

		for _, coordinate := range mode.Coordinates {
			if coordinate.ID == "" {
				return nil, nil, fmt.Errorf("mode %s has blank coordinate", mode.ID)
			}

			if err := validateCoordinateIdentity(coordinate); err != nil {
				return nil, nil, fmt.Errorf("invalid coordinate %q: %w", coordinate.ID, err)
			}

			if owner, exists := coordinates[coordinate.ID]; exists {
				return nil, nil, fmt.Errorf("duplicate coordinate %q in modes %s and %s", coordinate.ID, owner, mode.ID)
			}

			if _, exists := platforms[coordinate.Platform]; !exists {
				return nil, nil, fmt.Errorf("coordinate %s references unknown platform %s", coordinate.ID, coordinate.Platform)
			}

			if !oneOf(coordinate.Requiredness, "required", "soft", "deferred") {
				return nil, nil, fmt.Errorf("coordinate %s has ambiguous requiredness %q", coordinate.ID, coordinate.Requiredness)
			}

			if coordinate.Requiredness != mode.Requiredness {
				return nil, nil, fmt.Errorf("coordinate %s requiredness %s does not match mode %s", coordinate.ID, coordinate.Requiredness, mode.Requiredness)
			}

			if coordinate.GitHubName == "" {
				return nil, nil, fmt.Errorf("coordinate %s has no GitHub name", coordinate.ID)
			}

			if owner, exists := githubNames[coordinate.GitHubName]; exists {
				return nil, nil, fmt.Errorf("duplicate GitHub name %q in coordinates %s and %s", coordinate.GitHubName, owner, coordinate.ID)
			}

			githubNames[coordinate.GitHubName] = coordinate.ID

			if err := validateCoordinateEvidenceRefs(coordinate, baselineDigest); err != nil {
				return nil, nil, err
			}

			if err := validateLegacyTrace(coordinate.ID, coordinate.Trace); err != nil {
				return nil, nil, err
			}

			coordinates[coordinate.ID] = mode.ID
		}

		result[mode.ID] = mode
	}

	for claimed := range capabilityModes {
		if _, exists := result[claimed]; !exists {
			return nil, nil, fmt.Errorf("capability references unknown mode %s", claimed)
		}
	}

	return result, coordinates, nil
}

func validateUnsupported(
	values []UnsupportedDecision,
	modes map[string]Mode,
	liveCoordinates map[string]string,
	events map[string]EventIdentity,
	trustTiers map[string]TrustTier,
	platforms map[string]Platform,
	baselineDigest string,
) error {
	seen := map[string]bool{}
	seenCoordinates := map[string]string{}

	for _, decision := range values {
		subject := unsupportedSubject(decision)

		if decision.Mode == "" && decision.Coordinate == "" {
			return errors.New("unsupported row has no mode or coordinate")
		}

		if decision.Mode != "" && !validID(decision.Mode) {
			return fmt.Errorf("unsupported %s has invalid mode %q", subject, decision.Mode)
		}

		if decision.Coordinate != "" {
			if _, err := matrixFromCoordinate(decision.Coordinate); err != nil {
				return fmt.Errorf("unsupported %s has invalid coordinate %q: %w", subject, decision.Coordinate, err)
			}
		}

		key := unsupportedKey(decision)
		if seen[key] {
			return fmt.Errorf("duplicate unsupported decision %q", key)
		}

		seen[key] = true

		if decision.Coordinate != "" {
			if owner, exists := seenCoordinates[decision.Coordinate]; exists {
				return fmt.Errorf("duplicate unsupported coordinate %q in %s and %s", decision.Coordinate, owner, subject)
			}

			seenCoordinates[decision.Coordinate] = subject
		}

		if decision.Owner == "" || decision.Rationale == "" {
			return fmt.Errorf("unsupported %s lacks owner or rationale", subject)
		}

		if decision.SilentPassAllowed {
			return fmt.Errorf("unsupported %s cannot allow a silent pass", subject)
		}

		if decision.Requiredness != "unsupported" && decision.Requiredness != "retired" {
			return fmt.Errorf("unsupported %s has invalid requiredness %q", subject, decision.Requiredness)
		}

		if decision.Source != sourceID {
			return fmt.Errorf("unsupported %s references invalid source %s", subject, decision.Source)
		}

		event, exists := events[decision.Event]
		if !exists {
			return fmt.Errorf("unsupported %s references unknown event %s", subject, decision.Event)
		}

		if _, exists := platforms[decision.Platform]; !exists {
			return fmt.Errorf("unsupported %s references unknown platform %s", subject, decision.Platform)
		}

		if _, exists := trustTiers[decision.TrustTier]; !exists {
			return fmt.Errorf("unsupported %s references unknown trust tier %s", subject, decision.TrustTier)
		}

		if !containsString(event.TrustTiers, decision.TrustTier) {
			return fmt.Errorf("unsupported %s trust tier %s is not allowed by event %s", subject, decision.TrustTier, decision.Event)
		}

		if decision.Mode != "" {
			if _, exists := modes[decision.Mode]; exists {
				return fmt.Errorf("unsupported %s collides with live mode %s", subject, decision.Mode)
			} else if !exists && !isKnownExternalUnsupported(decision) {
				return fmt.Errorf("unsupported %s references unknown mode %s", subject, decision.Mode)
			}
		}

		if owner := liveCoordinates[decision.Coordinate]; owner != "" {
			return fmt.Errorf("unsupported %s collides with live coordinate in mode %s", subject, owner)
		}

		if err := validateEvidenceRefs(subject, decision.EvidenceRefs, baselineDigest); err != nil {
			return err
		}
	}

	return nil
}

func isKnownExternalUnsupported(decision UnsupportedDecision) bool {
	known := defaultExternalModeDecision()

	return decision.Mode == known.Mode &&
		decision.Coordinate == known.Coordinate &&
		decision.Source == known.Source &&
		decision.Event == known.Event &&
		decision.Platform == known.Platform &&
		decision.TrustTier == known.TrustTier &&
		decision.Owner == known.Owner
}

func unsupportedSubject(decision UnsupportedDecision) string {
	if decision.Coordinate != "" {
		return decision.Coordinate
	}

	if decision.Mode != "" {
		return decision.Mode
	}

	return "unsupported row"
}

func validateEvidenceRefs(subject string, refs []string, baselineDigest string) error {
	if len(refs) == 0 {
		return fmt.Errorf("%s has no evidence references", subject)
	}

	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref] {
			return fmt.Errorf("%s has duplicate evidence reference %q", subject, ref)
		}

		seen[ref] = true

		if !evidencePattern.MatchString(ref) {
			return fmt.Errorf("%s has invalid evidence reference %q", subject, ref)
		}

		if strings.HasPrefix(ref, "p0-inventory:") {
			rest := strings.TrimPrefix(ref, "p0-inventory:")
			digest, _, _ := strings.Cut(rest, "#")

			if digest != baselineDigest {
				return fmt.Errorf("%s has P0 inventory digest %s, want %s", subject, digest, baselineDigest)
			}
		}
	}

	return nil
}

func validateCoordinateEvidenceRefs(coordinate Coordinate, baselineDigest string) error {
	if err := validateEvidenceRefs(coordinate.ID, coordinate.EvidenceRefs, baselineDigest); err != nil {
		return err
	}

	var hasGitHubCheckName bool

	for _, ref := range coordinate.EvidenceRefs {
		if !strings.HasPrefix(ref, "github-check-name:") {
			continue
		}

		hasGitHubCheckName = true
		name := strings.TrimPrefix(ref, "github-check-name:")

		if name == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("%s has invalid GitHub check-name evidence %q", coordinate.ID, ref)
		}

		if name != coordinate.GitHubName {
			return fmt.Errorf("%s GitHub check-name evidence %q does not match %q", coordinate.ID, name, coordinate.GitHubName)
		}
	}

	if !hasGitHubCheckName {
		return fmt.Errorf("%s has no GitHub check-name evidence", coordinate.ID)
	}

	return nil
}

func validateLegacyTrace(subject string, trace LegacyTrace) error {
	if trace.InventoryMapping == "" ||
		trace.LegacyWorkflow == "" ||
		trace.LegacyJob == "" ||
		trace.WorkflowPath == "" ||
		!digestPattern.MatchString(trace.WorkflowSHA256) ||
		trace.SkipSemantics != "github-skipped-is-distinct-from-passed" {
		return fmt.Errorf("%s has incomplete P0 trace", subject)
	}

	return nil
}

func validID(value string) bool {
	return idPattern.MatchString(value)
}

func validateOwnedIdentities[T any](
	singular string,
	plural string,
	values []T,
	identity func(T) ownedIdentity,
	required []string,
) (map[string]T, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("no %s declared", plural)
	}

	result := map[string]T{}

	for _, value := range values {
		item := identity(value)
		if !validID(item.id) {
			return nil, fmt.Errorf("invalid %s %q", singular, item.id)
		}

		if _, exists := result[item.id]; exists {
			return nil, fmt.Errorf("duplicate %s %q", singular, item.id)
		}

		if item.owner == "" || item.description == "" {
			return nil, fmt.Errorf("%s %s has no owner or description", singular, item.id)
		}

		result[item.id] = value
	}

	for _, id := range required {
		if _, exists := result[id]; !exists {
			return nil, fmt.Errorf("missing required %s %q", singular, id)
		}
	}

	return result, nil
}

func validateCoordinateIdentity(coordinate Coordinate) error {
	matrix, err := matrixFromCoordinate(coordinate.ID)
	if err != nil {
		return err
	}

	if !sameStringMap(matrix, coordinate.Matrix) {
		return fmt.Errorf("matrix %v does not match coordinate ID matrix %v", coordinate.Matrix, matrix)
	}

	for key, value := range coordinate.Matrix {
		if !validID(key) {
			return fmt.Errorf("invalid matrix key %q", key)
		}

		if strings.TrimSpace(value) != value || value == "" || strings.ContainsAny(value, "[]=,") {
			return fmt.Errorf("invalid matrix value %q for %s", value, key)
		}
	}

	if runner, exists := coordinate.Matrix["runner"]; exists && runner != coordinate.Platform {
		return fmt.Errorf("runner matrix platform %s does not match platform %s", runner, coordinate.Platform)
	}

	return nil
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}

	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}

	return true
}

func hasTrustTierIntersection(modeTrustTiers map[string]bool, eventTrustTiers []string) bool {
	for _, tier := range eventTrustTiers {
		if modeTrustTiers[tier] {
			return true
		}
	}

	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}

	return false
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}

	return false
}
