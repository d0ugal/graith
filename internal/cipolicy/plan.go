package cipolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	PlanSchemaVersion        = 1
	DetectorVersion          = "cipolicy-detector-v1"
	detectorAlgorithmVersion = "path-rules-v3"
)

const (
	defaultPlanTTL   = 24 * time.Hour
	maxPlanClockSkew = 5 * time.Minute
)

var gitDigestPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

type detectorPathRule struct {
	Paths        []string `json:"paths,omitempty"`
	Prefixes     []string `json:"prefixes,omitempty"`
	Suffixes     []string `json:"suffixes,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

var (
	ciPolicyPrefixes = []string{
		".github/actions/",
		".github/workflows/",
		"cmd/cipolicy/",
		"internal/cibaseline/",
		"internal/cipolicy/",
	}

	generatedInputRules = detectorPathRule{
		Paths: []string{
			"internal/architecture/manifest.json",
		},
		Prefixes: []string{
			"internal/capabilities/",
			"internal/protocol/",
			"website/data/",
		},
		Suffixes: []string{
			".generated.go",
			"_generated.go",
		},
	}

	lockfileRules = detectorPathRule{
		Paths: []string{
			"go.sum",
			"package-lock.json",
			"pnpm-lock.yaml",
			"yarn.lock",
			"Gemfile.lock",
			"Cargo.lock",
		},
		Suffixes: []string{".lock"},
	}

	releaseMetadataRules = detectorPathRule{
		Paths: []string{
			".goreleaser.yaml",
			".goreleaser.yml",
			".release-please-manifest.json",
			"release-please-config.json",
		},
		Prefixes: []string{
			".goreleaser/",
			"packaging/",
			"homebrew/",
			".github/release",
		},
	}

	websiteHugoBuildInputRules = detectorPathRule{
		Paths: []string{
			"website/go.mod",
			"website/go.sum",
			"website/hugo.toml",
		},
		Prefixes: []string{
			"website/.ci/",
			"website/archetypes/",
			"website/assets/",
			"website/cmd/",
			"website/config/",
			"website/content/",
			"website/data/",
			"website/i18n/",
			"website/layouts/",
			"website/static/",
			"website/tests/",
			"website/themes/",
		},
	}

	capabilityPathRules = []detectorPathRule{
		{
			Paths: []string{
				".github/workflows/dev-release.yml",
				".goreleaser-dev.yaml",
				"THIRD_PARTY_NOTICES.libghostty.md",
				"libghostty-native.lock.json",
				"libghostty-native.spdx.json",
				"scripts/dev-release-base-tag.sh",
				"scripts/dev-release-version.sh",
				"scripts/libghostty-native.sh",
			},
			Prefixes: []string{
				"internal/daemonservice/",
				"macos/notifier/",
				"macos/service/",
			},
			Capabilities: []string{"dev-release"},
		},
		{
			Paths: []string{
				"go.mod",
				"Makefile",
			},
			Prefixes: []string{
				"cmd/graith/",
				"cmd/covreport/",
				"internal/agent/",
				"internal/client/",
				"internal/cli/",
				"internal/config/",
				"internal/daemon/",
				"internal/headless/",
				"internal/pty/",
				"internal/store/",
				"internal/tools/",
				"internal/version/",
			},
			Capabilities: []string{"go-core"},
		},
		{
			Paths: []string{
				"go.mod",
			},
			Prefixes: []string{
				"internal/sandbox/",
			},
			Capabilities: []string{"sandbox"},
		},
		{
			Prefixes:     []string{"internal/sandbox/"},
			Capabilities: []string{"go-core", "sandbox"},
		},
		{
			Prefixes:     []string{"gui/"},
			Capabilities: []string{"gui"},
		},
		{
			Paths: []string{
				"go.mod",
				"gui/shared/Package.swift",
			},
			Prefixes: []string{
				"internal/cli/",
				"internal/daemon/",
				"internal/integration/",
				"internal/libghosttydeps/",
				"internal/pty/",
				"internal/release/",
				"gui/shared/Sources/CGhosttyVT/include/",
			},
			Capabilities: []string{"go-core", "native"},
		},
		{
			Prefixes:     []string{"docs/"},
			Capabilities: []string{"docs-preview"},
		},
		{
			Prefixes:     []string{"scripts/"},
			Capabilities: []string{"workflow-policy"},
		},
		{
			Paths: []string{
				".github/actionlint.yaml",
				"renovate.json",
				".renovaterc",
			},
			Prefixes:     []string{".github/renovate"},
			Capabilities: []string{"workflow-policy"},
		},
	}

	universalCapabilityRules = map[string][]string{
		"pull-request":      {"commit-policy", "go-core", "native", "sandbox"},
		"push-main":         {"go-core"},
		"workflow-dispatch": {"commit-policy", "go-core", "native", "sandbox"},
	}

	supersetReasonVocabulary = map[string]bool{
		"ci-policy-change":  true,
		"detector-error":    true,
		"empty-file-list":   true,
		"file-list-unknown": true,
		"generated-input":   true,
		"lockfile":          true,
		"release-metadata":  true,
		"unknown-path":      true,
	}
)

type PlanOptions struct {
	Event           EventInput
	ChangedFiles    []string
	ExactFileList   bool
	DetectorVersion string
	DetectorErrors  []string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	Now             time.Time
	PlanTTL         time.Duration
}

type EventInput struct {
	Source              string `json:"source"`
	GitHubEvent         string `json:"github_event"`
	Ref                 string `json:"ref"`
	BaseRef             string `json:"base_ref"`
	HeadRef             string `json:"head_ref"`
	BaseRepository      string `json:"base_repository"`
	HeadRepository      string `json:"head_repository"`
	Commit              string `json:"commit"`
	Tree                string `json:"tree"`
	PullRequestFork     bool   `json:"pull_request_fork"`
	SameRepositoryAgent bool   `json:"same_repository_agent"`
	TrustedBase         bool   `json:"trusted_base"`
	Publication         bool   `json:"publication"`
}

type SourceRevision struct {
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
	Commit         string `json:"commit"`
	Tree           string `json:"tree"`
	BaseRef        string `json:"base_ref"`
	HeadRef        string `json:"head_ref"`
	HeadRepository string `json:"head_repository"`
}

type EventSelection struct {
	Source              string `json:"source"`
	Event               string `json:"event"`
	GitHubEvent         string `json:"github_event"`
	Ref                 string `json:"ref"`
	BaseRef             string `json:"base_ref"`
	HeadRef             string `json:"head_ref"`
	BaseRepository      string `json:"base_repository"`
	HeadRepository      string `json:"head_repository"`
	PullRequestFork     bool   `json:"pull_request_fork"`
	SameRepositoryAgent bool   `json:"same_repository_agent"`
	TrustedBase         bool   `json:"trusted_base"`
	Publication         bool   `json:"publication"`
}

type Detection struct {
	DetectorVersion    string   `json:"detector_version"`
	DetectorDigest     string   `json:"detector_digest"`
	ExactFileList      bool     `json:"exact_file_list"`
	ChangedFilesDigest string   `json:"changed_files_digest"`
	Capabilities       []string `json:"capabilities"`
	Superset           bool     `json:"superset"`
	SupersetReasons    []string `json:"superset_reasons"`
	Errors             []string `json:"errors"`
}

type RunPlan struct {
	SchemaVersion        int                       `json:"schema_version"`
	PlanDigest           string                    `json:"plan_digest"`
	PolicyVersion        string                    `json:"policy_version"`
	PolicyDigest         string                    `json:"policy_digest"`
	DetectorVersion      string                    `json:"detector_version"`
	DetectorDigest       string                    `json:"detector_digest"`
	Source               SourceRevision            `json:"source"`
	Event                EventSelection            `json:"event"`
	TrustTier            string                    `json:"trust_tier"`
	DetectedCapabilities []string                  `json:"detected_capabilities"`
	Capabilities         []string                  `json:"capabilities"`
	RequiredModes        []string                  `json:"required_modes"`
	Jobs                 []PlanJob                 `json:"jobs"`
	Unsupported          []PlanUnsupportedDecision `json:"unsupported"`
	ExactFileList        bool                      `json:"exact_file_list"`
	ChangedFilesDigest   string                    `json:"changed_files_digest"`
	Superset             bool                      `json:"superset"`
	SupersetReasons      []string                  `json:"superset_reasons"`
	DetectorErrors       []string                  `json:"detector_errors"`
	CreatedAt            time.Time                 `json:"created_at"`
	ExpiresAt            time.Time                 `json:"expires_at"`
}

type PlanJob struct {
	Source       string            `json:"source"`
	Event        string            `json:"event"`
	TrustTier    string            `json:"trust_tier"`
	Mode         string            `json:"mode"`
	Coordinate   string            `json:"coordinate"`
	Capability   string            `json:"capability"`
	Platform     string            `json:"platform"`
	CostClass    string            `json:"cost_class"`
	Requiredness string            `json:"requiredness"`
	Owner        string            `json:"owner"`
	GitHubName   string            `json:"github_name"`
	Matrix       map[string]string `json:"matrix"`
	EvidenceRefs []string          `json:"evidence_refs"`
}

type PlanUnsupportedDecision struct {
	Mode              string   `json:"mode"`
	Coordinate        string   `json:"coordinate"`
	Source            string   `json:"source"`
	Event             string   `json:"event"`
	Platform          string   `json:"platform"`
	TrustTier         string   `json:"trust_tier"`
	Requiredness      string   `json:"requiredness"`
	Owner             string   `json:"owner"`
	Rationale         string   `json:"rationale"`
	SilentPassAllowed bool     `json:"silent_pass_allowed"`
	EvidenceRefs      []string `json:"evidence_refs"`
}

func BuildPlan(manifest Manifest, options PlanOptions) (RunPlan, error) {
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := manifest.ValidateAt(now); err != nil {
		return RunPlan{}, err
	}

	selection, trustTier, err := SelectEvent(manifest, options.Event)
	if err != nil {
		return RunPlan{}, err
	}

	detection := DetectCapabilities(options.ChangedFiles, options.ExactFileList, options.DetectorErrors)
	if options.DetectorVersion != "" && options.DetectorVersion != DetectorVersion {
		detection.Superset = true
		detection.Errors = append(detection.Errors, fmt.Sprintf("unsupported detector version %q", options.DetectorVersion))
		detection.SupersetReasons = append(detection.SupersetReasons, "detector-error")
		detection.Errors = sortedStrings(detection.Errors)
		detection.SupersetReasons = sortedStrings(detection.SupersetReasons)
	}

	selectedCapabilities := capabilitySet(detection.Capabilities)
	for _, capability := range universalCapabilities(selection.Event) {
		selectedCapabilities[capability] = true
	}

	if detection.Superset {
		selectedCapabilities = allCapabilitySet(manifest)
	}

	capabilities := sortedSet(selectedCapabilities)

	jobs, unsupported, err := expandPlanRows(manifest, selection.Event, trustTier, capabilities)
	if err != nil {
		return RunPlan{}, err
	}

	if len(jobs) == 0 {
		return RunPlan{}, fmt.Errorf("plan has zero required jobs for event %s trust tier %s", selection.Event, trustTier)
	}

	createdAt := options.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	expiresAt := options.ExpiresAt
	if expiresAt.IsZero() {
		ttl := options.PlanTTL
		if ttl == 0 {
			ttl = defaultPlanTTL
		}

		expiresAt = createdAt.Add(ttl)
	}

	plan := RunPlan{
		SchemaVersion:        PlanSchemaVersion,
		PolicyVersion:        manifest.PolicyVersion,
		PolicyDigest:         manifest.PolicyDigest,
		DetectorVersion:      DetectorVersion,
		DetectorDigest:       DetectorDigest(),
		Source:               sourceRevision(manifest, options.Event),
		Event:                selection,
		TrustTier:            trustTier,
		DetectedCapabilities: detection.Capabilities,
		Capabilities:         capabilities,
		RequiredModes:        requiredModes(jobs),
		Jobs:                 jobs,
		Unsupported:          unsupported,
		ExactFileList:        detection.ExactFileList,
		ChangedFilesDigest:   detection.ChangedFilesDigest,
		Superset:             detection.Superset,
		SupersetReasons:      detection.SupersetReasons,
		DetectorErrors:       detection.Errors,
		CreatedAt:            createdAt.UTC(),
		ExpiresAt:            expiresAt.UTC(),
	}

	digest, err := plan.Digest()
	if err != nil {
		return RunPlan{}, err
	}

	plan.PlanDigest = digest

	if err := plan.ValidateAt(manifest, now); err != nil {
		return RunPlan{}, err
	}

	return plan, nil
}

func SelectEvent(manifest Manifest, input EventInput) (EventSelection, string, error) {
	source := input.Source
	if source == "" {
		source = manifest.Source.ID
	}

	if source != manifest.Source.ID {
		return EventSelection{}, "", fmt.Errorf("event source %q does not match policy source %q", source, manifest.Source.ID)
	}

	githubEvent := input.GitHubEvent

	eventID, trustTier, err := classifyEvent(manifest, input)
	if err != nil {
		return EventSelection{}, "", err
	}

	event, ok := eventByID(manifest, eventID)
	if !ok {
		return EventSelection{}, "", fmt.Errorf("policy does not define event %s", eventID)
	}

	if event.GitHubEvent != githubEvent {
		return EventSelection{}, "", fmt.Errorf("event %s maps to GitHub event %s, got %s", eventID, event.GitHubEvent, githubEvent)
	}

	if !containsString(event.TrustTiers, trustTier) {
		return EventSelection{}, "", fmt.Errorf("trust tier %s is not authorized for event %s", trustTier, eventID)
	}

	pullRequest := eventID == "pull-request"
	selection := EventSelection{
		Source:              source,
		Event:               eventID,
		GitHubEvent:         githubEvent,
		Ref:                 input.Ref,
		BaseRef:             input.BaseRef,
		HeadRef:             input.HeadRef,
		BaseRepository:      defaultString(input.BaseRepository, manifest.Source.Repository),
		HeadRepository:      input.HeadRepository,
		PullRequestFork:     pullRequest && (input.PullRequestFork || isForkRepository(input, manifest)),
		SameRepositoryAgent: pullRequest && input.SameRepositoryAgent,
		TrustedBase:         pullRequest && input.TrustedBase,
		Publication:         trustTier == "trusted-publication",
	}

	return selection, trustTier, nil
}

func classifyEvent(manifest Manifest, input EventInput) (string, string, error) {
	baseRepository := defaultString(input.BaseRepository, manifest.Source.Repository)
	if baseRepository == "" {
		return "", "", errors.New("event requires a base repository")
	}

	if baseRepository != manifest.Source.Repository {
		return "", "", fmt.Errorf("event base repository %s does not match policy repository %s", baseRepository, manifest.Source.Repository)
	}

	switch input.GitHubEvent {
	case "dynamic":
		return classifiedEvent(manifest, "dynamic", "github-service", input.Ref)
	case "pull_request":
		if input.Ref == "" {
			return "", "", errors.New("pull_request event requires a ref")
		}

		if input.HeadRepository == "" {
			return "", "", errors.New("pull_request event requires a head repository")
		}

		fork := input.PullRequestFork || isForkRepository(input, manifest)
		contexts := 0

		if fork {
			contexts++
		}

		if input.SameRepositoryAgent {
			contexts++
		}

		if input.TrustedBase {
			contexts++
		}

		if contexts != 1 {
			return "", "", errors.New("pull_request event requires exactly one trust context")
		}

		if fork {
			return classifiedEvent(manifest, "pull-request", "fork-untrusted", input.Ref)
		}

		if input.SameRepositoryAgent {
			return classifiedEvent(manifest, "pull-request", "same-repository-agent", input.Ref)
		}

		if input.TrustedBase {
			return classifiedEvent(manifest, "pull-request", "trusted-base", input.Ref)
		}

		return "", "", errors.New("pull_request event requires fork, same-repository-agent, or trusted-base trust context")
	case "push":
		switch {
		case eventAllowsRef(manifest, "push-main", input.Ref):
			if input.Publication {
				return classifiedEvent(manifest, "push-main", "trusted-publication", input.Ref)
			}

			return classifiedEvent(manifest, "push-main", "trusted-base", input.Ref)
		case eventAllowsRef(manifest, "push-tag", input.Ref):
			return classifiedEvent(manifest, "push-tag", "trusted-publication", input.Ref)
		default:
			return "", "", fmt.Errorf("unsupported push ref %q", input.Ref)
		}
	case "schedule":
		if input.Publication {
			return "", "", errors.New("schedule event cannot select publication trust")
		}

		return classifiedEvent(manifest, "schedule", "trusted-base", input.Ref)
	case "workflow_dispatch":
		if input.Publication {
			return "", "", errors.New("workflow_dispatch event cannot select publication trust")
		}

		return classifiedEvent(manifest, "workflow-dispatch", "trusted-base", input.Ref)
	default:
		return "", "", fmt.Errorf("unsupported GitHub event %q", input.GitHubEvent)
	}
}

func classifiedEvent(manifest Manifest, eventID, trustTier, ref string) (string, string, error) {
	if !eventAllowsRef(manifest, eventID, ref) {
		return "", "", fmt.Errorf("event %s does not allow ref %q", eventID, ref)
	}

	return eventID, trustTier, nil
}

func eventAllowsRef(manifest Manifest, eventID, ref string) bool {
	event, ok := eventByID(manifest, eventID)
	if !ok {
		return false
	}

	if len(event.Refs) == 0 && event.GitHubEvent != "push" {
		return true
	}

	for _, pattern := range event.Refs {
		if refMatchesPattern(pattern, ref) {
			return true
		}
	}

	return false
}

func refMatchesPattern(pattern, ref string) bool {
	if pattern == "" || pattern == "*" {
		return false
	}

	if pattern == ref {
		return true
	}

	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		if prefix == "" {
			return false
		}

		return strings.HasPrefix(ref, prefix)
	}

	return false
}

func DetectCapabilities(changedFiles []string, exactFileList bool, detectorErrors []string) Detection {
	detection := Detection{
		DetectorVersion: DetectorVersion,
		DetectorDigest:  DetectorDigest(),
		ExactFileList:   exactFileList,
	}

	capabilities := map[string]bool{}
	reasons := map[string]bool{}

	normalizedFiles := canonicalChangedFiles(changedFiles)
	if exactFileList {
		detection.ChangedFilesDigest = changedFilesDigest(normalizedFiles)
		if len(normalizedFiles) == 0 {
			detection.Superset = true
			reasons["empty-file-list"] = true
		}
	}

	if !exactFileList {
		detection.Superset = true
		reasons["file-list-unknown"] = true
	}

	for _, detectorError := range detectorErrors {
		if strings.TrimSpace(detectorError) == "" {
			continue
		}

		detection.Superset = true
		detection.Errors = append(detection.Errors, detectorError)
		reasons["detector-error"] = true
	}

	for _, path := range normalizedFiles {
		if path == "" {
			detection.Superset = true
			reasons["detector-error"] = true

			detection.Errors = append(detection.Errors, "blank changed path")

			continue
		}

		if invalidChangedPath(path) {
			detection.Superset = true
			reasons["detector-error"] = true

			detection.Errors = append(detection.Errors, "invalid changed path "+path)

			continue
		}

		pathCapabilities := capabilitiesForPath(path)

		switch {
		case isCIPolicyPath(path):
			detection.Superset = true
			reasons["ci-policy-change"] = true

			for _, capability := range pathCapabilities {
				capabilities[capability] = true
			}

			continue
		case isGeneratedInputPath(path):
			detection.Superset = true
			reasons["generated-input"] = true

			continue
		case isLockfilePath(path):
			detection.Superset = true
			reasons["lockfile"] = true

			continue
		case isReleaseMetadataPath(path):
			detection.Superset = true
			reasons["release-metadata"] = true

			continue
		default:
			if len(pathCapabilities) == 0 {
				detection.Superset = true
				reasons["unknown-path"] = true

				continue
			}
		}

		for _, capability := range pathCapabilities {
			capabilities[capability] = true
		}
	}

	detection.Capabilities = sortedSet(capabilities)
	detection.SupersetReasons = sortedSet(reasons)
	detection.Errors = sortedStrings(detection.Errors)

	return detection
}

func DetectorDigest() string {
	rules := struct {
		Version          string              `json:"version"`
		Algorithm        string              `json:"algorithm"`
		Universal        map[string][]string `json:"universal"`
		CIPolicyPrefixes []string            `json:"ci_policy_prefixes"`
		GeneratedInputs  detectorPathRule    `json:"generated_inputs"`
		Lockfiles        detectorPathRule    `json:"lockfiles"`
		ReleaseMetadata  detectorPathRule    `json:"release_metadata"`
		WebsiteHugo      detectorPathRule    `json:"website_hugo"`
		CapabilityPaths  []detectorPathRule  `json:"capability_paths"`
	}{
		Version:          DetectorVersion,
		Algorithm:        detectorAlgorithmVersion,
		Universal:        universalCapabilityRules,
		CIPolicyPrefixes: ciPolicyPrefixes,
		GeneratedInputs:  generatedInputRules,
		Lockfiles:        lockfileRules,
		ReleaseMetadata:  releaseMetadataRules,
		WebsiteHugo:      websiteHugoBuildInputRules,
		CapabilityPaths:  capabilityPathRules,
	}

	data, err := json.Marshal(rules)
	if err != nil {
		panic(err)
	}

	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:])
}

func (plan RunPlan) Validate(manifest Manifest) error {
	return plan.ValidateAt(manifest, time.Now().UTC())
}

func (plan RunPlan) ValidateAt(manifest Manifest, now time.Time) error {
	if now.IsZero() {
		return errors.New("plan validation time is required")
	}

	if err := manifest.ValidateAt(now); err != nil {
		return err
	}

	if plan.SchemaVersion != PlanSchemaVersion {
		return fmt.Errorf("unsupported plan schema version %d", plan.SchemaVersion)
	}

	if plan.PolicyVersion != manifest.PolicyVersion {
		return fmt.Errorf("plan policy version %s does not match manifest %s", plan.PolicyVersion, manifest.PolicyVersion)
	}

	if plan.PolicyDigest != manifest.PolicyDigest {
		return fmt.Errorf("plan policy digest %s does not match manifest %s", plan.PolicyDigest, manifest.PolicyDigest)
	}

	if plan.DetectorVersion != DetectorVersion {
		return fmt.Errorf("plan detector version %s does not match %s", plan.DetectorVersion, DetectorVersion)
	}

	if plan.DetectorDigest != DetectorDigest() {
		return fmt.Errorf("plan detector digest %s does not match %s", plan.DetectorDigest, DetectorDigest())
	}

	digest, err := plan.Digest()
	if err != nil {
		return err
	}

	if plan.PlanDigest != digest {
		return fmt.Errorf("plan digest mismatch: got %s want %s", plan.PlanDigest, digest)
	}

	if !reflect.DeepEqual(plan, plan.Canonical()) {
		return errors.New("plan is not canonical")
	}

	if plan.CreatedAt.IsZero() || plan.ExpiresAt.IsZero() {
		return errors.New("plan requires created_at and expires_at")
	}

	if !plan.CreatedAt.Before(plan.ExpiresAt) {
		return errors.New("plan expiry must be after creation")
	}

	if plan.CreatedAt.After(now.Add(maxPlanClockSkew)) {
		return fmt.Errorf("plan created_at %s is too far in the future", plan.CreatedAt.Format(time.RFC3339))
	}

	if plan.ExpiresAt.Sub(plan.CreatedAt) > defaultPlanTTL {
		return fmt.Errorf("plan ttl %s exceeds maximum %s", plan.ExpiresAt.Sub(plan.CreatedAt), defaultPlanTTL)
	}

	if !now.Before(plan.ExpiresAt) {
		return fmt.Errorf("plan expired at %s", plan.ExpiresAt.Format(time.RFC3339))
	}

	if !gitDigestPattern.MatchString(plan.Source.Commit) {
		return fmt.Errorf("plan source commit %q is not a git digest", plan.Source.Commit)
	}

	if !gitDigestPattern.MatchString(plan.Source.Tree) {
		return fmt.Errorf("plan source tree %q is not a git digest", plan.Source.Tree)
	}

	if plan.Source.Repository != manifest.Source.Repository {
		return fmt.Errorf("plan source repository %s does not match manifest %s", plan.Source.Repository, manifest.Source.Repository)
	}

	if plan.Event.BaseRepository != manifest.Source.Repository {
		return fmt.Errorf("plan event base repository %s does not match manifest %s", plan.Event.BaseRepository, manifest.Source.Repository)
	}

	if plan.Source.Ref != plan.Event.Ref ||
		plan.Source.BaseRef != plan.Event.BaseRef ||
		plan.Source.HeadRef != plan.Event.HeadRef ||
		plan.Source.HeadRepository != plan.Event.HeadRepository {
		return errors.New("plan source revision does not match event refs")
	}

	event, ok := eventByID(manifest, plan.Event.Event)
	if !ok {
		return fmt.Errorf("plan references unknown event %s", plan.Event.Event)
	}

	if plan.Event.Source != manifest.Source.ID || plan.Event.GitHubEvent != event.GitHubEvent {
		return fmt.Errorf("plan event identity does not match manifest event %s", plan.Event.Event)
	}

	if !containsString(event.TrustTiers, plan.TrustTier) {
		return fmt.Errorf("plan trust tier %s is not authorized for event %s", plan.TrustTier, plan.Event.Event)
	}

	if err := validateRequiredCapabilitiesCoveredByUniversalFloor(manifest, plan.Event.Event); err != nil {
		return err
	}

	derivedEvent, derivedTrustTier, err := classifyEvent(manifest, EventInput{
		Source:              plan.Event.Source,
		GitHubEvent:         plan.Event.GitHubEvent,
		Ref:                 plan.Event.Ref,
		BaseRef:             plan.Event.BaseRef,
		HeadRef:             plan.Event.HeadRef,
		BaseRepository:      plan.Event.BaseRepository,
		HeadRepository:      plan.Event.HeadRepository,
		PullRequestFork:     plan.Event.PullRequestFork,
		SameRepositoryAgent: plan.Event.SameRepositoryAgent,
		TrustedBase:         plan.Event.TrustedBase,
		Publication:         plan.Event.Publication,
	})
	if err != nil {
		return err
	}

	if derivedEvent != plan.Event.Event || derivedTrustTier != plan.TrustTier {
		return fmt.Errorf("plan event identity derives event %s trust tier %s, got event %s trust tier %s", derivedEvent, derivedTrustTier, plan.Event.Event, plan.TrustTier)
	}

	derivedSelection, _, err := SelectEvent(manifest, EventInput{
		Source:              plan.Event.Source,
		GitHubEvent:         plan.Event.GitHubEvent,
		Ref:                 plan.Event.Ref,
		BaseRef:             plan.Event.BaseRef,
		HeadRef:             plan.Event.HeadRef,
		BaseRepository:      plan.Event.BaseRepository,
		HeadRepository:      plan.Event.HeadRepository,
		PullRequestFork:     plan.Event.PullRequestFork,
		SameRepositoryAgent: plan.Event.SameRepositoryAgent,
		TrustedBase:         plan.Event.TrustedBase,
		Publication:         plan.Event.Publication,
	})
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(derivedSelection, plan.Event) {
		return errors.New("plan event selection is not canonical")
	}

	knownCapabilities := allCapabilitySet(manifest)
	for _, capability := range plan.Capabilities {
		if !knownCapabilities[capability] {
			return fmt.Errorf("plan references unknown capability %s", capability)
		}
	}

	for _, capability := range plan.DetectedCapabilities {
		if !knownCapabilities[capability] {
			return fmt.Errorf("plan references unknown detected capability %s", capability)
		}
	}

	if !plan.ExactFileList && !plan.Superset {
		return errors.New("plan cannot narrow without an exact file list")
	}

	if plan.ExactFileList {
		if !digestPattern.MatchString(plan.ChangedFilesDigest) {
			return fmt.Errorf("plan changed files digest %q is invalid", plan.ChangedFilesDigest)
		}
	} else if plan.ChangedFilesDigest != "" {
		return errors.New("plan cannot bind a changed files digest without an exact file list")
	}

	if len(plan.DetectorErrors) > 0 && !plan.Superset {
		return errors.New("plan cannot narrow after detector errors")
	}

	if plan.Superset && len(plan.SupersetReasons) == 0 {
		return errors.New("safe-superset plan requires at least one reason")
	}

	if !plan.Superset && len(plan.SupersetReasons) > 0 {
		return errors.New("narrow plan cannot carry safe-superset reasons")
	}

	for _, reason := range plan.SupersetReasons {
		if !supersetReasonVocabulary[reason] {
			return fmt.Errorf("unknown safe-superset reason %s", reason)
		}
	}

	selectedCapabilities := capabilitySet(plan.Capabilities)
	if plan.Superset && !reflect.DeepEqual(plan.Capabilities, sortedSet(allCapabilitySet(manifest))) {
		return errors.New("safe-superset plan must select every capability")
	}

	for _, capability := range plan.DetectedCapabilities {
		if !selectedCapabilities[capability] {
			return fmt.Errorf("plan omits detected capability %s", capability)
		}
	}

	for _, capability := range universalCapabilities(plan.Event.Event) {
		if !selectedCapabilities[capability] {
			return fmt.Errorf("plan omits universal capability %s for event %s", capability, plan.Event.Event)
		}
	}

	jobs, unsupported, err := expandPlanRows(manifest, plan.Event.Event, plan.TrustTier, plan.Capabilities)
	if err != nil {
		return err
	}

	if len(jobs) == 0 {
		return fmt.Errorf("plan has zero required jobs for event %s trust tier %s", plan.Event.Event, plan.TrustTier)
	}

	if !reflect.DeepEqual(requiredModes(jobs), plan.RequiredModes) {
		return errors.New("plan required modes are stale")
	}

	if !reflect.DeepEqual(jobs, plan.Canonical().Jobs) {
		return errors.New("plan jobs are not canonical")
	}

	if !reflect.DeepEqual(jobs, plan.Jobs) {
		return errors.New("plan jobs do not match manifest expansion")
	}

	if !reflect.DeepEqual(unsupported, plan.Unsupported) {
		return errors.New("plan unsupported decisions do not match manifest expansion")
	}

	return nil
}

func (plan RunPlan) Canonical() RunPlan {
	clone := plan.copy()

	clone.CreatedAt = clone.CreatedAt.UTC()
	clone.ExpiresAt = clone.ExpiresAt.UTC()
	clone.DetectedCapabilities = sortedStrings(clone.DetectedCapabilities)
	clone.Capabilities = sortedStrings(clone.Capabilities)
	clone.RequiredModes = sortedStrings(clone.RequiredModes)
	clone.SupersetReasons = sortedStrings(clone.SupersetReasons)
	clone.DetectorErrors = sortedStrings(clone.DetectorErrors)

	sort.Slice(clone.Jobs, func(i, j int) bool {
		return planJobKey(clone.Jobs[i]) < planJobKey(clone.Jobs[j])
	})

	for index := range clone.Jobs {
		if clone.Jobs[index].Matrix == nil {
			clone.Jobs[index].Matrix = map[string]string{}
		}

		clone.Jobs[index].EvidenceRefs = sortedStrings(clone.Jobs[index].EvidenceRefs)
	}

	sort.Slice(clone.Unsupported, func(i, j int) bool {
		return planUnsupportedKey(clone.Unsupported[i]) < planUnsupportedKey(clone.Unsupported[j])
	})

	for index := range clone.Unsupported {
		clone.Unsupported[index].EvidenceRefs = sortedStrings(clone.Unsupported[index].EvidenceRefs)
	}

	return clone
}

func (plan RunPlan) Digest() (string, error) {
	canonical := plan.Canonical()
	canonical.PlanDigest = ""

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:]), nil
}

func (plan RunPlan) MarshalCanonical() ([]byte, error) {
	canonical := plan.Canonical()

	digest, err := canonical.Digest()
	if err != nil {
		return nil, err
	}

	canonical.PlanDigest = digest

	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}

func (plan RunPlan) copy() RunPlan {
	clone := plan
	clone.DetectedCapabilities = append([]string(nil), plan.DetectedCapabilities...)
	clone.Capabilities = append([]string(nil), plan.Capabilities...)
	clone.RequiredModes = append([]string(nil), plan.RequiredModes...)
	clone.SupersetReasons = append([]string(nil), plan.SupersetReasons...)
	clone.DetectorErrors = append([]string(nil), plan.DetectorErrors...)

	clone.Jobs = append([]PlanJob(nil), plan.Jobs...)
	for index := range clone.Jobs {
		clone.Jobs[index].EvidenceRefs = append([]string(nil), plan.Jobs[index].EvidenceRefs...)
		if plan.Jobs[index].Matrix != nil {
			clone.Jobs[index].Matrix = cloneStringMap(plan.Jobs[index].Matrix)
		}
	}

	clone.Unsupported = append([]PlanUnsupportedDecision(nil), plan.Unsupported...)
	for index := range clone.Unsupported {
		clone.Unsupported[index].EvidenceRefs = append([]string(nil), plan.Unsupported[index].EvidenceRefs...)
	}

	return clone
}

func expandPlanRows(
	manifest Manifest,
	eventID string,
	trustTier string,
	capabilities []string,
) ([]PlanJob, []PlanUnsupportedDecision, error) {
	capabilityAllowed := capabilitySet(capabilities)

	event, ok := eventByID(manifest, eventID)
	if !ok {
		return nil, nil, fmt.Errorf("unknown event %s", eventID)
	}

	if !containsString(event.TrustTiers, trustTier) {
		return nil, nil, fmt.Errorf("trust tier %s is not authorized for event %s", trustTier, eventID)
	}

	var jobs []PlanJob

	for _, mode := range manifest.Canonical().Modes {
		if !capabilityAllowed[mode.Capability] ||
			mode.Requiredness != "required" ||
			!modeAppliesToEvent(mode, eventID) ||
			!containsString(mode.TrustTiers, trustTier) {
			continue
		}

		for _, coordinate := range mode.Coordinates {
			if coordinate.Requiredness != "required" {
				continue
			}

			jobs = append(jobs, PlanJob{
				Source:       sourceID,
				Event:        eventID,
				TrustTier:    trustTier,
				Mode:         mode.ID,
				Coordinate:   coordinate.ID,
				Capability:   mode.Capability,
				Platform:     coordinate.Platform,
				CostClass:    mode.CostClass,
				Requiredness: coordinate.Requiredness,
				Owner:        mode.Owner,
				GitHubName:   coordinate.GitHubName,
				Matrix:       cloneStringMap(coordinate.Matrix),
				EvidenceRefs: append([]string(nil), coordinate.EvidenceRefs...),
			})
		}
	}

	var unsupported []PlanUnsupportedDecision

	for _, decision := range manifest.Canonical().Unsupported {
		if decision.Source != sourceID || decision.Event != eventID || decision.TrustTier != trustTier {
			continue
		}

		unsupported = append(unsupported, PlanUnsupportedDecision{
			Mode:              decision.Mode,
			Coordinate:        decision.Coordinate,
			Source:            decision.Source,
			Event:             decision.Event,
			Platform:          decision.Platform,
			TrustTier:         decision.TrustTier,
			Requiredness:      decision.Requiredness,
			Owner:             decision.Owner,
			Rationale:         decision.Rationale,
			SilentPassAllowed: decision.SilentPassAllowed,
			EvidenceRefs:      append([]string(nil), decision.EvidenceRefs...),
		})
	}

	sort.Slice(jobs, func(i, j int) bool { return planJobKey(jobs[i]) < planJobKey(jobs[j]) })

	for index := range jobs {
		jobs[index].EvidenceRefs = sortedStrings(jobs[index].EvidenceRefs)
		if jobs[index].Matrix == nil {
			jobs[index].Matrix = map[string]string{}
		}
	}

	sort.Slice(unsupported, func(i, j int) bool {
		return planUnsupportedKey(unsupported[i]) < planUnsupportedKey(unsupported[j])
	})

	for index := range unsupported {
		unsupported[index].EvidenceRefs = sortedStrings(unsupported[index].EvidenceRefs)
	}

	return jobs, unsupported, nil
}

func sourceRevision(manifest Manifest, input EventInput) SourceRevision {
	return SourceRevision{
		Repository:     manifest.Source.Repository,
		Ref:            input.Ref,
		Commit:         input.Commit,
		Tree:           input.Tree,
		BaseRef:        input.BaseRef,
		HeadRef:        input.HeadRef,
		HeadRepository: input.HeadRepository,
	}
}

func requiredModes(jobs []PlanJob) []string {
	modes := map[string]bool{}
	for _, job := range jobs {
		modes[job.Mode] = true
	}

	return sortedSet(modes)
}

func modeAppliesToEvent(mode Mode, eventID string) bool {
	for _, sourceEvent := range mode.SourceEvents {
		if sourceEvent.Source == sourceID && sourceEvent.Event == eventID {
			return true
		}
	}

	return false
}

func eventByID(manifest Manifest, id string) (EventIdentity, bool) {
	for _, event := range manifest.Events {
		if event.ID == id {
			return event, true
		}
	}

	return EventIdentity{}, false
}

func universalCapabilities(eventID string) []string {
	return append([]string(nil), universalCapabilityRules[eventID]...)
}

func validateRequiredCapabilitiesCoveredByUniversalFloor(manifest Manifest, eventID string) error {
	universal := capabilitySet(universalCapabilities(eventID))

	for _, mode := range manifest.Canonical().Modes {
		if mode.Requiredness != "required" || !modeAppliesToEvent(mode, eventID) {
			continue
		}

		if !universal[mode.Capability] {
			return fmt.Errorf("required mode %s capability %s is not in universal capability floor for event %s", mode.ID, mode.Capability, eventID)
		}
	}

	return nil
}

func allCapabilitySet(manifest Manifest) map[string]bool {
	result := map[string]bool{}
	for _, capability := range manifest.Capabilities {
		result[capability.ID] = true
	}

	return result
}

func capabilitySet(capabilities []string) map[string]bool {
	result := map[string]bool{}
	for _, capability := range capabilities {
		result[capability] = true
	}

	return result
}

func capabilitiesForPath(path string) []string {
	capabilities := map[string]bool{}

	if detectorRuleMatches(websiteHugoBuildInputRules, path) {
		capabilities["docs-preview"] = true
		capabilities["docs-publication"] = true
	}

	for _, rule := range capabilityPathRules {
		if detectorRuleMatches(rule, path) {
			for _, capability := range rule.Capabilities {
				capabilities[capability] = true
			}
		}
	}

	return sortedSet(capabilities)
}

func canonicalChangedFiles(changedFiles []string) []string {
	normalized := make([]string, 0, len(changedFiles))
	for _, file := range changedFiles {
		normalized = append(normalized, normalizeChangedPath(file))
	}

	return sortedStrings(normalized)
}

func changedFilesDigest(changedFiles []string) string {
	var data strings.Builder
	for _, path := range changedFiles {
		data.WriteString(path)
		data.WriteByte('\n')
	}

	digest := sha256.Sum256([]byte(data.String()))

	return hex.EncodeToString(digest[:])
}

func normalizeChangedPath(path string) string {
	return filepath.ToSlash(strings.TrimSuffix(path, "\r"))
}

func invalidChangedPath(path string) bool {
	return strings.TrimSpace(path) != path ||
		strings.HasPrefix(path, "/") ||
		path == "." ||
		path == ".." ||
		strings.HasPrefix(path, "../") ||
		strings.Contains(path, "/../")
}

func isCIPolicyPath(path string) bool {
	for _, prefix := range ciPolicyPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}

func isGeneratedInputPath(path string) bool {
	return detectorRuleMatches(generatedInputRules, path)
}

func isLockfilePath(path string) bool {
	return detectorRuleMatches(lockfileRules, path)
}

func isReleaseMetadataPath(path string) bool {
	return detectorRuleMatches(releaseMetadataRules, path)
}

func detectorRuleMatches(rule detectorPathRule, path string) bool {
	for _, rulePath := range rule.Paths {
		if path == rulePath {
			return true
		}
	}

	for _, prefix := range rule.Prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	for _, suffix := range rule.Suffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}

	return false
}

func isForkRepository(input EventInput, manifest Manifest) bool {
	base := input.BaseRepository
	if base == "" {
		base = manifest.Source.Repository
	}

	return input.HeadRepository != "" && base != "" && input.HeadRepository != base
}

func planJobKey(job PlanJob) string {
	return strings.Join([]string{job.Mode, job.Coordinate, job.Platform, job.TrustTier}, "\x00")
}

func planUnsupportedKey(decision PlanUnsupportedDecision) string {
	return strings.Join([]string{decision.Mode, decision.Coordinate, decision.Platform, decision.TrustTier}, "\x00")
}

func sortedSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}

	sort.Strings(result)

	if result == nil {
		return []string{}
	}

	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}

	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}

	return clone
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}

	return fallback
}
