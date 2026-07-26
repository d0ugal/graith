package cipolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/d0ugal/graith/internal/cibaseline"
)

var (
	matrixPattern                       = regexp.MustCompile(`^([^\[]+)(?:\[(.*)\])?$`)
	eventNameConditionPattern           = regexp.MustCompile(`github\.event_name\s*==\s*['"]([^'"]+)['"]`)
	eventNameNotEqualConditionPattern   = regexp.MustCompile(`github\.event_name\s*!=`)
	mainRefConditionPattern             = regexp.MustCompile(`github\.ref\s*==\s*['"]refs/heads/main['"]`)
	sameRepositoryPRConditionPattern    = regexp.MustCompile(`github\.event\.pull_request\.head\.repo\.full_name\s*==\s*github\.repository`)
	workflowDispatchInputGatePattern    = regexp.MustCompile(`github\.event_name\s*!=\s*['"]workflow_dispatch['"].*inputs\.include_linux|inputs\.include_linux.*github\.event_name\s*!=\s*['"]workflow_dispatch['"]`)
	policyIdentityConditionTokenPattern = regexp.MustCompile(`github\.event_name|github\.ref|github\.event\.pull_request\.head\.repo\.full_name|inputs\.`)
	inputReferencePattern               = regexp.MustCompile(`inputs\.([A-Za-z_][A-Za-z0-9_]*)`)
)

type conditionSupport struct {
	mainRef                   bool
	sameRepositoryPullRequest bool
	workflowDispatchInputGate bool
}

func FromInventory(inventory cibaseline.Inventory) (Manifest, error) {
	if err := inventory.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate P0 inventory: %w", err)
	}

	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		PolicyVersion: PolicyVersion,
		Source: SourceIdentity{
			ID: sourceID, Repository: DefaultRepository, DefaultBranch: DefaultDefaultBranch,
			PolicyPath: DefaultManifestPath,
			BaselineInventory: BaselineInventory{
				Path: DefaultInventoryPath, SchemaVersion: inventory.SchemaVersion,
				Digest: inventory.Digest, ObservationState: inventory.ObservationState,
			},
		},
		TrustTiers: []TrustTier{
			{
				ID: "fork-untrusted", Owner: "security-owners",
				Description: "Fork pull-request code and artifacts; no repository secrets or write credentials.",
			},
			{
				ID: "github-service", Owner: "security-owners",
				Description: "GitHub-created service activity observed outside repository-owned workflow files.",
			},
			{
				ID: "same-repository-agent", Owner: "security-owners",
				Description: "Same-repository agent-authored branches; repository location does not upgrade token, artifact, cache, or publication trust.",
			},
			{
				ID: "trusted-base", Owner: "graith-maintainers",
				Description: "Trusted default-branch policy and non-publication evaluation context.",
			},
			{
				ID: "trusted-publication", Owner: "release-owners",
				Description:            "Protected main, tag, environment, or publication path with credentials unavailable to pull-request code.",
				PublicationCredentials: true,
			},
		},
		Events: []EventIdentity{
			{
				ID: "dynamic", Source: sourceID, GitHubEvent: "dynamic",
				TrustTiers:  []string{"github-service"},
				Description: "GitHub-generated service activity not declared by repository-owned workflow YAML.",
			},
			{
				ID: "pull-request", Source: sourceID, GitHubEvent: "pull_request",
				TrustTiers:  []string{"fork-untrusted", "same-repository-agent", "trusted-base"},
				Description: "Pull-request proof, including fork and same-repository agent-authored branches.",
			},
			{
				ID: "push-main", Source: sourceID, GitHubEvent: "push", Refs: []string{"refs/heads/main"},
				TrustTiers:  []string{"trusted-base", "trusted-publication"},
				Description: "Default-branch push proof; only explicitly protected publication modes may use publication credentials.",
			},
			{
				ID: "push-tag", Source: sourceID, GitHubEvent: "push", Refs: []string{"refs/tags/v*"},
				TrustTiers:  []string{"trusted-base", "trusted-publication"},
				Description: "Version-tag release-candidate proof; only explicitly protected publication modes may use publication credentials.",
			},
			{
				ID: "schedule", Source: sourceID, GitHubEvent: "schedule",
				TrustTiers:  []string{"trusted-base"},
				Description: "Repository-owned scheduled maintenance and drift proof.",
			},
			{
				ID: "workflow-dispatch", Source: sourceID, GitHubEvent: "workflow_dispatch",
				TrustTiers:  []string{"trusted-base"},
				Description: "Maintainer-dispatched diagnostic or release-shaped proof.",
			},
		},
		Platforms: []Platform{
			{
				ID: "github-service", Owner: "security-owners", RunnerLabel: "github-service",
				OS: "github", Architecture: "service",
				Description: "GitHub service-side execution with no repository runner.",
			},
			{
				ID: "macos-26", Owner: "graith-maintainers", RunnerLabel: "macos-26",
				OS: "macos", Architecture: "arm64",
				Description: "Pinned GitHub-hosted macOS 26 Apple Silicon runner.",
			},
			{
				ID: "macos-latest", Owner: "graith-maintainers", RunnerLabel: "macos-latest",
				OS: "macos", Architecture: "hosted-default",
				Description: "GitHub-hosted macOS latest runner used by current proof.",
			},
			{
				ID: "ubuntu-24.04", Owner: "graith-maintainers", RunnerLabel: "ubuntu-24.04",
				OS: "linux", Architecture: "x64",
				Description: "Pinned GitHub-hosted Ubuntu 24.04 x64 runner.",
			},
			{
				ID: "ubuntu-24.04-arm", Owner: "graith-maintainers", RunnerLabel: "ubuntu-24.04-arm",
				OS: "linux", Architecture: "arm64",
				Description: "Pinned GitHub-hosted Ubuntu 24.04 arm64 runner.",
			},
			{
				ID: "ubuntu-latest", Owner: "graith-maintainers", RunnerLabel: "ubuntu-latest",
				OS: "linux", Architecture: "x64",
				Description: "GitHub-hosted Ubuntu latest runner used by current proof.",
			},
		},
		CostClasses: []CostClass{
			{ID: "external", Owner: "security-owners", Description: "GitHub service-side observation outside repository runner accounting."},
			{ID: "linux-fast", Owner: "graith-maintainers", Description: "Linux source-level or compile-only proof expected in the PR fast lane."},
			{ID: "linux-standard", Owner: "graith-maintainers", Description: "Linux runtime, package-consumer, or security proof with standard runner cost."},
			{ID: "macos", Owner: "gui-owners", Description: "macOS-hosted build, test, GUI, native, or sandbox proof."},
			{ID: "publication", Owner: "release-owners", Description: "Protected publication, release, Pages, or scheduled producer proof."},
		},
		Unsupported: []UnsupportedDecision{defaultExternalModeDecision()},
	}

	mappings := make(map[string]cibaseline.Mapping, len(inventory.Mappings))
	for _, mapping := range inventory.Mappings {
		mappings[mapping.LegacyCoordinate] = mapping
	}

	capabilityModes := map[string][]string{}

	for _, workflow := range inventory.Workflows {
		events, err := workflowEventIDs(workflow.Events)
		if err != nil {
			return Manifest{}, fmt.Errorf("workflow %s events: %w", workflow.ID, err)
		}

		for _, job := range workflow.Jobs {
			jobEvents, err := eventsForJob(events, job.Condition)
			if err != nil {
				return Manifest{}, fmt.Errorf("workflow %s job %s events: %w", workflow.ID, job.ID, err)
			}

			mode, err := modeFromJob(inventory, workflow, job, mappings, jobEvents)
			if err != nil {
				return Manifest{}, err
			}

			manifest.Modes = append(manifest.Modes, mode)
			capabilityModes[mode.Capability] = append(capabilityModes[mode.Capability], mode.ID)
		}
	}

	for capabilityID, modeIDs := range capabilityModes {
		manifest.Capabilities = append(manifest.Capabilities, Capability{
			ID: capabilityID, Owner: capabilityOwner(capabilityID),
			Description: capabilityDescription(capabilityID),
			Modes:       modeIDs,
		})
	}

	return manifest.Canonical(), nil
}

func eventsForJob(workflowEvents []string, condition string) ([]string, error) {
	matches := eventNameConditionPattern.FindAllStringSubmatch(condition, -1)
	if len(matches) == 0 {
		switch {
		case strings.Contains(condition, "github.event.action"):
			if err := validateConditionSupport(condition, conditionSupport{}); err != nil {
				return nil, err
			}

			return intersectEvents(workflowEvents, "pull-request")
		case sameRepositoryPRConditionPattern.MatchString(condition):
			if err := validateConditionSupport(condition, conditionSupport{sameRepositoryPullRequest: true}); err != nil {
				return nil, err
			}

			return intersectEvents(workflowEvents, "pull-request")
		case workflowDispatchInputGatePattern.MatchString(condition):
			if err := validateConditionSupport(condition, conditionSupport{workflowDispatchInputGate: true}); err != nil {
				return nil, err
			}

			return workflowEvents, nil
		case policyIdentityConditionTokenPattern.MatchString(condition):
			return nil, fmt.Errorf("unsupported policy condition %q", condition)
		}

		return workflowEvents, nil
	}

	wanted := map[string]bool{}

	for _, match := range matches {
		switch match[1] {
		case "pull_request":
			wanted["pull-request"] = true
		case "schedule":
			wanted["schedule"] = true
		case "push":
			wanted["push-main"] = true
			wanted["push-tag"] = true
		case "workflow_dispatch":
			wanted["workflow-dispatch"] = true
		default:
			return nil, fmt.Errorf("unsupported github.event_name %q", match[1])
		}
	}

	if mainRefConditionPattern.MatchString(condition) {
		wanted["push-main"] = true
		delete(wanted, "push-tag")
	}

	if err := validateConditionSupport(condition, conditionSupport{
		mainRef:                   mainRefConditionPattern.MatchString(condition),
		sameRepositoryPullRequest: sameRepositoryPRConditionPattern.MatchString(condition),
	}); err != nil {
		return nil, err
	}

	events := make([]string, 0, len(wanted))
	for event := range wanted {
		events = append(events, event)
	}

	sort.Strings(events)

	return intersectEvents(workflowEvents, events...)
}

func validateConditionSupport(condition string, support conditionSupport) error {
	if eventNameNotEqualConditionPattern.MatchString(condition) && !support.workflowDispatchInputGate {
		return fmt.Errorf("unsupported policy condition %q", condition)
	}

	if strings.Contains(condition, "github.ref") {
		if !mainRefConditionPattern.MatchString(condition) || !support.mainRef {
			return fmt.Errorf("unsupported policy condition %q", condition)
		}
	}

	if strings.Contains(condition, "github.event.pull_request.head.repo.full_name") {
		if !sameRepositoryPRConditionPattern.MatchString(condition) || !support.sameRepositoryPullRequest {
			return fmt.Errorf("unsupported policy condition %q", condition)
		}
	}

	for _, match := range inputReferencePattern.FindAllStringSubmatch(condition, -1) {
		if !support.workflowDispatchInputGate || match[1] != "include_linux" {
			return fmt.Errorf("unsupported policy condition %q", condition)
		}
	}

	return nil
}

func intersectEvents(workflowEvents []string, wanted ...string) ([]string, error) {
	allowed := map[string]bool{}
	for _, event := range wanted {
		allowed[event] = true
	}

	var result []string

	for _, event := range workflowEvents {
		if allowed[event] {
			result = append(result, event)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("condition event identity %v does not intersect workflow events %v", wanted, workflowEvents)
	}

	return result, nil
}

func defaultExternalModeDecision() UnsupportedDecision {
	return UnsupportedDecision{
		Mode: "dynamic/dependabot/update-graph", Coordinate: "dynamic/dependabot/update-graph",
		Source: sourceID, Event: "dynamic", Platform: "github-service", TrustTier: "github-service",
		Requiredness: "unsupported", Owner: "graith-maintainers",
		Rationale: "GitHub-generated Dependabot dependency-graph update outside the 18 repo-owned workflow paths; latency policy no-latency-target; dual-run eligibility false until P1/P2 gives it an explicit mode or retirement decision.",
		Expires:   "2026-08-31",
		EvidenceRefs: []string{
			"github-actions:run/30152132020/job/89664191125",
			"p0-acceptance:gap-external-dependabot-update-graph-30152132020",
		},
	}
}

func modeFromJob(
	inventory cibaseline.Inventory,
	workflow cibaseline.Workflow,
	job cibaseline.Job,
	mappings map[string]cibaseline.Mapping,
	events []string,
) (Mode, error) {
	capabilityID := capabilityForWorkflow(workflow.ID)
	modeID := "legacy/" + workflow.ID + "/" + job.ID
	mode := Mode{
		ID: modeID, Capability: capabilityID, Owner: job.Owner,
		Requiredness: job.Requiredness, ProofType: job.ProofType,
		EvidenceRefs: []string{fmt.Sprintf("p0-inventory:%s#%s/%s", inventory.Digest, workflow.ID, job.ID)},
		Trace: LegacyTrace{
			InventoryMapping: "job:" + workflow.ID + "/" + job.ID,
			LegacyWorkflow:   workflow.ID, LegacyJob: job.ID,
			WorkflowPath: workflow.Path, WorkflowSHA256: workflow.FileSHA256,
			LegacyCondition: job.Condition, SkipSemantics: job.SkipSemantics,
		},
	}
	hasMacOS := false

	for _, event := range events {
		mode.SourceEvents = append(mode.SourceEvents, SourceEvent{Source: sourceID, Event: event})
		mode.TrustTiers = append(mode.TrustTiers, trustTiersForEvent(event, workflow.ID, job.ID, job.Condition)...)
	}

	for index, coordinateID := range job.Coordinates {
		mapping, exists := mappings[coordinateID]
		if !exists {
			return Mode{}, fmt.Errorf("missing P0 mapping for %s", coordinateID)
		}

		matrix, err := matrixFromCoordinate(coordinateID)
		if err != nil {
			return Mode{}, fmt.Errorf("parse coordinate %s: %w", coordinateID, err)
		}

		platform, err := platformForJob(job, matrix)
		if err != nil {
			return Mode{}, fmt.Errorf("platform for %s: %w", coordinateID, err)
		}

		if strings.HasPrefix(platform, "macos-") {
			hasMacOS = true
		}

		if index >= len(job.GitHubNames) {
			return Mode{}, fmt.Errorf("missing GitHub name for coordinate %s", coordinateID)
		}

		githubName := job.GitHubNames[index]

		mode.Coordinates = append(mode.Coordinates, Coordinate{
			ID: coordinateID, Platform: platform, Matrix: matrix,
			Requiredness: job.Requiredness, GitHubName: githubName,
			EvidenceRefs: []string{
				fmt.Sprintf("p0-inventory:%s#%s", inventory.Digest, coordinateID),
				"github-check-name:" + githubName,
			},
			Trace: LegacyTrace{
				InventoryMapping: mapping.LegacyCoordinate,
				LegacyWorkflow:   workflow.ID, LegacyJob: job.ID,
				WorkflowPath: workflow.Path, WorkflowSHA256: workflow.FileSHA256,
				LegacyCondition: mapping.LegacyCondition, SkipSemantics: mapping.SkipSemantics,
			},
		})
	}

	mode.CostClass = costClass(job, hasMacOS)

	return mode, nil
}

func workflowEventIDs(raw json.RawMessage) ([]string, error) {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}

	var events []string

	for name, rawEvent := range decoded {
		switch name {
		case "pull_request":
			events = append(events, "pull-request")
		case "schedule":
			events = append(events, "schedule")
		case "workflow_dispatch":
			events = append(events, "workflow-dispatch")
		case "push":
			pushEvents, err := pushEventIDs(rawEvent)
			if err != nil {
				return nil, err
			}

			events = append(events, pushEvents...)
		default:
			return nil, fmt.Errorf("unsupported workflow event %q", name)
		}
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("no supported policy event in %s", string(raw))
	}

	sort.Strings(events)

	return events, nil
}

func pushEventIDs(raw json.RawMessage) ([]string, error) {
	if string(raw) == "null" {
		return nil, errors.New("unsupported unrestricted push identity")
	}

	var rawKeys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawKeys); err != nil {
		return nil, err
	}

	for key := range rawKeys {
		switch key {
		case "branches", "tags", "paths":
		default:
			return nil, fmt.Errorf("unsupported push identity key %q", key)
		}
	}

	var decoded struct {
		Branches []string `json:"branches"`
		Tags     []string `json:"tags"`
	}

	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	for _, branch := range decoded.Branches {
		switch branch {
		case "main", "release-please--branches--main":
			seen["push-main"] = true
		default:
			return nil, fmt.Errorf("unsupported push branch identity %q", branch)
		}
	}

	for _, tag := range decoded.Tags {
		switch tag {
		case "v*":
			seen["push-tag"] = true
		default:
			return nil, fmt.Errorf("unsupported push tag identity %q", tag)
		}
	}

	var events []string
	for event := range seen {
		events = append(events, event)
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("unsupported push identity %s", string(raw))
	}

	sort.Strings(events)

	return events, nil
}

func matrixFromCoordinate(coordinate string) (map[string]string, error) {
	matches := matrixPattern.FindStringSubmatch(coordinate)
	if matches == nil {
		return nil, errors.New("invalid coordinate")
	}

	if !validID(matches[1]) {
		return nil, fmt.Errorf("invalid coordinate mode %q", matches[1])
	}

	result := map[string]string{}
	if matches[2] == "" {
		return result, nil
	}

	for _, part := range strings.Split(matches[2], ",") {
		key, value, found := strings.Cut(part, "=")
		if !found || key == "" || value == "" {
			return nil, fmt.Errorf("invalid matrix term %q", part)
		}

		if !validID(key) {
			return nil, fmt.Errorf("invalid matrix key %q", key)
		}

		if strings.TrimSpace(value) != value || strings.ContainsAny(value, "[]=,") {
			return nil, fmt.Errorf("invalid matrix value %q for %s", value, key)
		}

		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate matrix key %q", key)
		}

		result[key] = value
	}

	return result, nil
}

func platformForJob(job cibaseline.Job, matrix map[string]string) (string, error) {
	if runner, exists := matrix["runner"]; exists {
		if !knownRunnerPlatform(runner) {
			return "", fmt.Errorf("unknown runner platform %q", runner)
		}

		return runner, nil
	}

	var runner string
	if err := json.Unmarshal(job.Runner, &runner); err != nil || runner == "" {
		return "", fmt.Errorf("unsupported runner identity %s", string(job.Runner))
	}

	if runner == "${{ matrix.runner }}" {
		return "", errors.New("matrix runner is missing from coordinate")
	}

	if !knownRunnerPlatform(runner) {
		return "", fmt.Errorf("unknown runner platform %q", runner)
	}

	return runner, nil
}

func knownRunnerPlatform(runner string) bool {
	switch runner {
	case "ubuntu-latest", "ubuntu-24.04", "ubuntu-24.04-arm", "macos-latest", "macos-26":
		return true
	default:
		return false
	}
}

func trustTiersForEvent(event, workflowID, jobID, condition string) []string {
	switch event {
	case "pull-request":
		if sameRepositoryPRConditionPattern.MatchString(condition) {
			return []string{"same-repository-agent", "trusted-base"}
		}

		return []string{"fork-untrusted", "same-repository-agent", "trusted-base"}
	case "push-main":
		if isProtectedPublicationMode(workflowID, jobID) {
			return []string{"trusted-base", "trusted-publication"}
		}

		return []string{"trusted-base"}
	case "push-tag":
		if isProtectedPublicationMode(workflowID, jobID) {
			return []string{"trusted-publication"}
		}

		return []string{"trusted-base"}
	case "schedule", "workflow-dispatch":
		return []string{"trusted-base"}
	default:
		return []string{"trusted-base"}
	}
}

func isProtectedPublicationMode(workflowID, jobID string) bool {
	switch workflowID + "/" + jobID {
	case "dev-release/publish-dev",
		"docs/deploy",
		"goreleaser/publish-stable",
		"libghostty-native-publish/publish":
		return true
	default:
		return false
	}
}

func costClass(job cibaseline.Job, hasMacOS bool) string {
	if hasMacOS {
		return "macos"
	}

	switch job.ProofType {
	case "compile-only", "source-level", "soft":
		return "linux-fast"
	case "scheduled":
		return "publication"
	case "package-consumer":
		if job.Owner == "release-owners" {
			return "publication"
		}

		return "linux-standard"
	default:
		return "linux-standard"
	}
}

func capabilityForWorkflow(workflowID string) string {
	switch workflowID {
	case "ci":
		return "go-core"
	case "coverage":
		return "coverage"
	case "gui-ci":
		return "gui"
	case "libghostty-native":
		return "native"
	case "libghostty-native-publish":
		return "native-publication"
	case "regen":
		return "generated-metadata"
	case "docs-preview":
		return "docs-preview"
	case "dev-release":
		return "dev-release"
	case "release-please":
		return "release-automation"
	case "goreleaser":
		return "stable-release"
	case "sandbox":
		return "sandbox"
	case "dependency-review":
		return "security-dependency-review"
	case "codeql":
		return "security-codeql"
	case "scorecard":
		return "security-scorecard"
	case "secret-scan":
		return "security-secret-scan"
	case "workflow-lint":
		return "workflow-policy"
	case "docs":
		return "docs-publication"
	case "commits":
		return "commit-policy"
	default:
		return "workflow-" + workflowID
	}
}

func capabilityOwner(capabilityID string) string {
	switch capabilityID {
	case "gui":
		return "gui-owners"
	case "native", "native-publication":
		return "native-owners"
	case "dev-release", "stable-release", "release-automation":
		return "release-owners"
	case "docs-preview", "docs-publication":
		return "docs-owners"
	case "security-codeql", "security-dependency-review", "security-scorecard", "security-secret-scan":
		return "security-owners"
	default:
		return "graith-maintainers"
	}
}

func capabilityDescription(capabilityID string) string {
	switch capabilityID {
	case "commit-policy":
		return "Commit message proof for pull-request merge hygiene."
	case "coverage":
		return "Current informational coverage collection and publication proof."
	case "dev-release":
		return "Development release-shaped package and publication proof."
	case "docs-preview":
		return "Pull-request documentation preview and screenshot publication proof."
	case "docs-publication":
		return "Main-branch documentation build and Pages publication proof."
	case "generated-metadata":
		return "Generated-file regeneration validation and credential separation proof."
	case "go-core":
		return "Go build, lint, test, vulnerability, and integration proof."
	case "gui":
		return "macOS and iOS GUI build and test proof."
	case "native":
		return "Native libghostty producer, adapter, and consumer proof."
	case "native-publication":
		return "Main-only native dependency publication proof."
	case "release-automation":
		return "Release Please automation proof."
	case "sandbox":
		return "Linux and macOS sandbox enforcement proof."
	case "security-codeql":
		return "CodeQL security analysis proof."
	case "security-dependency-review":
		return "Dependency review supply-chain policy proof."
	case "security-scorecard":
		return "OpenSSF scorecard posture proof."
	case "security-secret-scan":
		return "Secret scanning proof."
	case "stable-release":
		return "Stable release-shaped package, attestation, and publication proof."
	case "workflow-policy":
		return "Workflow, shell, Renovate, and helper policy proof."
	default:
		return "Current CI proof derived from the P0 baseline inventory."
	}
}
