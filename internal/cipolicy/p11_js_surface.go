package cipolicy

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/cibaseline"
	yaml "go.yaml.in/yaml/v3"
)

const (
	P11JSSurfaceDirectory       = ".github/workflows/scripts"
	P11RegenAuthReplacementPath = "internal/cipolicy/p11_js_surface_test.go"
)

type P11JSHelperContract struct {
	Path                 string
	Owner                string
	Kind                 string
	Callers              []string
	PolicyInputs         []string
	PolicyOutputs        []string
	Disposition          string
	ExecutableContract   string
	DeletionCriterion    string
	CompatibilitySamples []P11CompatibilitySampleRequirement
	Tranche              string
}

type P11CompatibilitySampleRequirement struct {
	ID          string
	Description string
}

type P11CompatibilitySample struct {
	ID                         string
	HelperPath                 string
	Description                string
	PlanOptions                PlanOptions
	ExpectedTrustTier          string
	ExpectedCapabilities       []string
	ExpectedSupersetReasons    []string
	ExpectedManifestModes      []P11ManifestModeExpectation
	ExpectedPresentCoordinates []string
	ExpectedAbsentCoordinates  []string
	CredentialExpectations     []P11CredentialExpectation
}

type P11KnownFile struct {
	Path    string
	SHA256  string
	Content []byte
}

type P11ManifestModeExpectation struct {
	ID           string
	Capability   string
	Requiredness string
	TrustTiers   []string
	Coordinates  []P11ManifestCoordinateExpectation
}

type P11ManifestCoordinateExpectation struct {
	ID           string
	Requiredness string
}

type P11CredentialExpectation struct {
	Operation       CredentialOperation
	Allowed         bool
	BindToPlan      bool
	WantErrorSubstr string
}

type P11CompatibilityComparison struct {
	ID              string
	HelperPath      string
	PlanDigest      string
	TrustTier       string
	Capabilities    []string
	RequiredModes   []string
	Coordinates     []string
	Superset        bool
	SupersetReasons []string
}

type P11WorkflowSummary struct {
	Name                  string
	Events                []string
	Permissions           map[string]string
	PermissionsExpression string
	Env                   map[string]string
	Jobs                  map[string]P11WorkflowJob
	Scalars               []string
}

type P11WorkflowJob struct {
	Name                  string
	If                    string
	Needs                 []string
	RunsOn                string
	Permissions           map[string]string
	PermissionsExpression string
	Env                   map[string]string
	Steps                 []P11WorkflowStep
}

type P11WorkflowStep struct {
	Name string
	Uses string
	If   string
	Env  map[string]string
	With map[string]string
	Run  string
}

func P11JSSurfaceContracts() []P11JSHelperContract {
	contracts := []P11JSHelperContract{
		{
			Path:         ".github/workflows/scripts/docs-diff-run.js",
			Owner:        "graith-maintainers",
			Kind:         "workflow-helper",
			Callers:      []string{".github/workflows/docs-preview.yml: diff rendered docs screenshots"},
			PolicyInputs: []string{"pages.json", "base screenshot directory", "head screenshot directory", "pngjs-decoded PNG files"},
			PolicyOutputs: []string{
				"flat docs-preview screenshot directory",
				"manifest.json entries classified as diff, same, new, or deleted",
				"docs-diff count summary on stdout",
			},
			Disposition:        "retain",
			ExecutableContract: "batch docs screenshot comparisons without changing publish semantics",
			DeletionCriterion:  "a Go or retained-JS adapter produces byte-equivalent manifests and image outputs for added, deleted, same, single-edit, and divergent screenshot samples",
			CompatibilitySamples: []P11CompatibilitySampleRequirement{
				{ID: "docs-diff-added-page", Description: "page exists only at head and copies the head PNG"},
				{ID: "docs-diff-deleted-page", Description: "page exists only at base and copies the base PNG"},
				{ID: "docs-diff-same-page", Description: "base and head rows match and no PNG is emitted"},
				{ID: "docs-diff-row-change", Description: "row-level diff emits one composite PNG and stable manifest kind"},
			},
			Tranche: "retain with docs-diff.js until image-output parity can be sampled",
		},
		{
			Path:               ".github/workflows/scripts/docs-diff.js",
			Owner:              "graith-maintainers",
			Kind:               "workflow-helper",
			Callers:            []string{".github/workflows/scripts/docs-diff-run.js", "manual CLI: node docs-diff.js <base.png> <head.png> <out.png>", ".github/workflows/scripts/docs-diff.test.js"},
			PolicyInputs:       []string{"base PNG", "head PNG", "row hash sequence", "hunk padding and denoise thresholds"},
			PolicyOutputs:      []string{"exit code 0 with diff PNG", "exit code 3 with no output for identical render", "exit code 2 for invalid CLI arguments"},
			Disposition:        "retain",
			ExecutableContract: "row-align docs screenshots and render base/head composite hunks while retaining pngjs for ecosystem-specific PNG decoding",
			DeletionCriterion:  "all pure row-diff functions and PNG render output match the existing helper over synthetic and captured docs-preview images; pngjs remains allowed unless a separate image-decoding decision replaces it",
			CompatibilitySamples: []P11CompatibilitySampleRequirement{
				{ID: "row-identical", Description: "identical rows return no diff and CLI exit 3"},
				{ID: "mid-page-insertion", Description: "Myers alignment realigns after inserted rows"},
				{ID: "global-divergence", Description: "large divergence falls back to one all-covering hunk"},
				{ID: "png-composite", Description: "rendered base/head PNG composite is byte-equivalent"},
			},
			Tranche: "retain because pngjs is the explicit P11 exception; pure row logic can be split later",
		},
		{
			Path:               ".github/workflows/scripts/docs-diff.test.js",
			Owner:              "graith-maintainers",
			Kind:               "workflow-contract-test",
			Callers:            []string{".github/workflows/workflow-lint.yml: workflow scripts job"},
			PolicyInputs:       []string{"docs-diff.js pure row-diff API", "synthetic RGBA rows"},
			PolicyOutputs:      []string{"Node test pass/fail for docs-preview visual diff contract"},
			Disposition:        "port",
			ExecutableContract: "preserve row hashing, Myers alignment, denoise, hunk merge, and render geometry assertions",
			DeletionCriterion:  "Go tests cover the same row-diff sample matrix and docs-preview retains PNG parity evidence",
			CompatibilitySamples: []P11CompatibilitySampleRequirement{
				{ID: "hash-row-stability", Description: "equal rows hash equal and different rows do not"},
				{ID: "hunk-padding", Description: "padding clamps and merges exactly as current tests assert"},
				{ID: "render-geometry", Description: "base-left/head-right/gutter pixels match expected RGBA output"},
			},
			Tranche: "after docs-diff.js pure logic is wrapped or ported",
		},
		{
			Path:               ".github/workflows/scripts/docs-preview.js",
			Owner:              "graith-maintainers",
			Kind:               "workflow-helper",
			Callers:            []string{".github/workflows/docs-preview.yml: publish screenshots", ".github/workflows/docs-preview.yml: cleanup closed PR screenshots", ".github/workflows/docs-preview.yml: prune stale screenshots", ".github/workflows/scripts/docs-preview.test.js"},
			PolicyInputs:       []string{"GitHub pull_request context", "screenshots branch ref/tree", "issue comments", "current wall clock for prune"},
			PolicyOutputs:      []string{"screenshots branch create/update commits", "sticky PR comment updates", "fork PR write no-op", "fail-closed truncated-tree errors"},
			Disposition:        "wrap",
			ExecutableContract: "preserve same-repository write boundary, full-tree rewrite safety, empty-tree handling, and compare-and-retry branch updates",
			DeletionCriterion:  "GitHub API fixture and Go policy fixture agree on fork skip, same-repo publish, truncated tree rejection, empty-tree commit, and retry outcomes with zero unexplained disagreement",
			CompatibilitySamples: []P11CompatibilitySampleRequirement{
				{ID: "docs-preview-fork-skip", Description: "fork PR never reads or writes screenshots branch"},
				{ID: "docs-preview-truncated-tree", Description: "cleanup and prune reject partial tree listings before rewrite"},
				{ID: "docs-preview-empty-tree", Description: "last screenshot deletion commits the empty tree SHA"},
				{ID: "docs-preview-ref-race", Description: "lost create/update ref race re-reads and rebuilds on the winner tip"},
			},
			Tranche: "after regen-auth semantic replacement because this helper mutates a write-token branch",
		},
		{
			Path:               ".github/workflows/scripts/docs-preview.test.js",
			Owner:              "graith-maintainers",
			Kind:               "workflow-contract-test",
			Callers:            []string{".github/workflows/workflow-lint.yml: workflow scripts job"},
			PolicyInputs:       []string{"docs-preview.js API", "fake GitHub git/issues clients", "synthetic PR contexts"},
			PolicyOutputs:      []string{"Node test pass/fail for docs-preview write-boundary contract"},
			Disposition:        "port",
			ExecutableContract: "preserve destructive branch rewrite, fork no-op, sticky comment, and stale-prune assertions",
			DeletionCriterion:  "Go semantic tests cover the same GitHub API state transitions and P2/P3 credential-operation boundaries",
			CompatibilitySamples: []P11CompatibilitySampleRequirement{
				{ID: "cleanup-same-repo", Description: "same-repo cleanup drops only the closed PR prefix"},
				{ID: "cleanup-fork", Description: "fork cleanup does not touch branch or comments"},
				{ID: "prune-stale", Description: "prune removes only dated run directories older than 30 days"},
			},
			Tranche: "paired with docs-preview.js wrap",
		},
		{
			Path:               ".github/workflows/scripts/package-lock.json",
			Owner:              "graith-maintainers",
			Kind:               "workflow-script-dependency-lock",
			Callers:            []string{".github/workflows/docs-preview.yml: npm ci --prefix .github/workflows/scripts --ignore-scripts"},
			PolicyInputs:       []string{"pinned npm dependency graph for docs-preview pngjs PNG decoding"},
			PolicyOutputs:      []string{"integrity-locked pngjs install"},
			Disposition:        "retain",
			ExecutableContract: "pin pngjs with an npm integrity lock and no install scripts",
			DeletionCriterion:  "only delete if the docs-preview PNG decode/encode path no longer needs npm dependencies or an owner-approved replacement lock exists",
			CompatibilitySamples: []P11CompatibilitySampleRequirement{
				{ID: "pngjs-integrity", Description: "lockfile pins pngjs 7.0.0 with integrity and no transitive packages"},
			},
			Tranche: "explicit P11 retained exception for pngjs",
		},
		{
			Path:               ".github/workflows/scripts/package.json",
			Owner:              "graith-maintainers",
			Kind:               "workflow-script-dependency-manifest",
			Callers:            []string{".github/workflows/docs-preview.yml: npm ci --prefix .github/workflows/scripts --ignore-scripts"},
			PolicyInputs:       []string{"docs-preview script dependency declaration"},
			PolicyOutputs:      []string{"declares pngjs as the only workflow-script package dependency"},
			Disposition:        "retain",
			ExecutableContract: "keep pngjs as an explicit ecosystem-specific dependency exception",
			DeletionCriterion:  "only delete if docs-preview no longer uses pngjs or the exception is replaced by an owner-approved image decoding contract",
			CompatibilitySamples: []P11CompatibilitySampleRequirement{
				{ID: "pngjs-only-dependency", Description: "package manifest keeps pngjs as the sole dependency"},
			},
			Tranche: "explicit P11 retained exception for pngjs",
		},
	}

	sort.Slice(contracts, func(i, j int) bool { return contracts[i].Path < contracts[j].Path })

	return contracts
}

func P11RegenAuthCompatibilityRequirements() []P11CompatibilitySampleRequirement {
	return []P11CompatibilitySampleRequirement{
		{ID: "regen-same-repository-agent", Description: "same-repository PR preserves the regen workflow guard and cannot obtain maintainer credentials in repository-controlled runners"},
		{ID: "regen-fork-untrusted", Description: "fork PR selects fork-untrusted trust and never runs the credentialed regen jobs"},
		{ID: "regen-trusted-base", Description: "trusted-base PR replay keeps regen jobs behind the same generated-metadata capability"},
		{ID: "regen-push-boundary", Description: "fresh-runner push uses only the verified generated commit and a non-force branch update"},
		{ID: "regen-non-superset-negative", Description: "non-superset docs-only plan rejects regeneration credential binding because generated-metadata is absent"},
	}
}

func ValidateP11JSSurfaceInventory(repoRoot string, inventory cibaseline.Inventory, contracts []P11JSHelperContract) error {
	if err := inventory.Validate(); err != nil {
		return fmt.Errorf("validate P0 inventory: %w", err)
	}

	current, err := currentP11ScriptPaths(repoRoot)
	if err != nil {
		return err
	}

	byPath := map[string]P11JSHelperContract{}

	for _, contract := range contracts {
		if err := validateP11Contract(contract); err != nil {
			return err
		}

		if _, exists := byPath[contract.Path]; exists {
			return fmt.Errorf("duplicate P11 JS helper contract for %s", contract.Path)
		}

		byPath[contract.Path] = contract
	}

	for _, path := range current {
		if _, exists := byPath[path]; !exists {
			return fmt.Errorf("missing P11 JS helper contract for %s", path)
		}
	}

	currentSet := map[string]bool{}
	for _, path := range current {
		currentSet[path] = true
	}

	for path := range byPath {
		if !currentSet[path] {
			return fmt.Errorf("P11 JS helper contract references non-current helper %s", path)
		}
	}

	surfaces := map[string]cibaseline.Surface{}

	for _, surface := range inventory.Surfaces {
		if strings.HasPrefix(surface.Path, P11JSSurfaceDirectory+"/") {
			surfaces[surface.Path] = surface
		}
	}

	for _, path := range current {
		surface, exists := surfaces[path]
		if !exists {
			return fmt.Errorf("P0 inventory is missing P11 JS helper surface %s", path)
		}

		contract := byPath[path]
		if surface.Owner != contract.Owner {
			return fmt.Errorf("P11 JS helper %s owner = %s, P0 inventory owner = %s", path, contract.Owner, surface.Owner)
		}
	}

	for path := range surfaces {
		if !currentSet[path] {
			return fmt.Errorf("P0 inventory references non-current P11 JS helper surface %s", path)
		}
	}

	return nil
}

func CompareP11CompatibilitySamples(manifest Manifest, knownFiles []P11KnownFile, samples []P11CompatibilitySample, now time.Time) ([]P11CompatibilityComparison, error) {
	if now.IsZero() {
		return nil, errors.New("P11 compatibility comparison requires a deterministic time")
	}

	var (
		comparisons []P11CompatibilityComparison
		seenSamples = map[string]bool{}
	)

	for _, sample := range samples {
		if sample.ID == "" || sample.HelperPath == "" || sample.Description == "" {
			return nil, fmt.Errorf("P11 compatibility sample has incomplete identity metadata: %#v", sample)
		}

		if seenSamples[sample.ID] {
			return nil, fmt.Errorf("duplicate P11 compatibility sample %s", sample.ID)
		}

		seenSamples[sample.ID] = true

		options := sample.PlanOptions
		options.Now = now

		if options.CreatedAt.IsZero() {
			options.CreatedAt = now.Add(-10 * time.Minute)
		}

		plan, err := BuildP11CompatibilityPlan(manifest, knownFiles, options)
		if err != nil {
			return nil, fmt.Errorf("%s: build P11 compatibility plan: %w", sample.ID, err)
		}

		comparison := P11CompatibilityComparison{
			ID:              sample.ID,
			HelperPath:      sample.HelperPath,
			PlanDigest:      plan.PlanDigest,
			TrustTier:       plan.TrustTier,
			Capabilities:    append([]string(nil), plan.Capabilities...),
			RequiredModes:   append([]string(nil), plan.RequiredModes...),
			Coordinates:     p11PlanCoordinates(plan),
			Superset:        plan.Superset,
			SupersetReasons: append([]string(nil), plan.SupersetReasons...),
		}

		if sample.ExpectedTrustTier != "" && plan.TrustTier != sample.ExpectedTrustTier {
			return nil, fmt.Errorf("%s: trust tier = %s, want %s", sample.ID, plan.TrustTier, sample.ExpectedTrustTier)
		}

		for _, capability := range sample.ExpectedCapabilities {
			if !containsString(plan.Capabilities, capability) {
				return nil, fmt.Errorf("%s: capability %s missing from Go policy plan", sample.ID, capability)
			}
		}

		for _, reason := range sample.ExpectedSupersetReasons {
			if !containsString(plan.SupersetReasons, reason) {
				return nil, fmt.Errorf("%s: superset reason %s missing from Go policy plan", sample.ID, reason)
			}
		}

		for _, expectation := range sample.ExpectedManifestModes {
			if err := validateP11ManifestModeExpectation(manifest, sample.ID, expectation); err != nil {
				return nil, err
			}

			if expectation.Requiredness != "required" && containsString(plan.RequiredModes, expectation.ID) {
				return nil, fmt.Errorf("%s: soft manifest mode %s unexpectedly entered the required plan", sample.ID, expectation.ID)
			}

			for _, coordinate := range expectation.Coordinates {
				if coordinate.Requiredness != "required" && containsString(comparison.Coordinates, coordinate.ID) {
					return nil, fmt.Errorf("%s: soft manifest coordinate %s unexpectedly entered the required plan", sample.ID, coordinate.ID)
				}
			}
		}

		for _, coordinate := range sample.ExpectedPresentCoordinates {
			if !containsString(comparison.Coordinates, coordinate) {
				return nil, fmt.Errorf("%s: coordinate %s missing from Go policy plan", sample.ID, coordinate)
			}
		}

		for _, coordinate := range sample.ExpectedAbsentCoordinates {
			if containsString(comparison.Coordinates, coordinate) {
				return nil, fmt.Errorf("%s: coordinate %s unexpectedly present in Go policy plan", sample.ID, coordinate)
			}
		}

		for _, expectation := range sample.CredentialExpectations {
			if !expectation.Allowed && expectation.WantErrorSubstr == "" {
				return nil, fmt.Errorf("%s: denied credential operation %s requires an expected error binding", sample.ID, expectation.Operation.Operation)
			}

			err := ValidateCredentialOperation(expectation.Operation)

			bindToPlan := expectation.BindToPlan || expectation.Allowed
			if bindToPlan {
				if planErr := validateP11CredentialPlanExpectation(sample.ID, sample.ExpectedTrustTier, expectation, plan); planErr != nil {
					return nil, planErr
				}
			}

			if err == nil && bindToPlan {
				policy := credentialOperationPolicies[expectation.Operation.Operation]

				err = validateCredentialOperationPlanBinding(expectation.Operation, policy, plan)
				if err == nil {
					err = validateP11CredentialTrustAllowance(sample.ID, expectation, plan)
				}
			}

			switch {
			case expectation.Allowed && err != nil:
				return nil, fmt.Errorf("%s: credential operation %s rejected: %w", sample.ID, expectation.Operation.Operation, err)
			case !expectation.Allowed && err == nil:
				return nil, fmt.Errorf("%s: credential operation %s unexpectedly allowed", sample.ID, expectation.Operation.Operation)
			case !expectation.Allowed && expectation.WantErrorSubstr != "" && !strings.Contains(err.Error(), expectation.WantErrorSubstr):
				return nil, fmt.Errorf("%s: credential operation %s error = %w, want %q", sample.ID, expectation.Operation.Operation, err, expectation.WantErrorSubstr)
			}
		}

		comparisons = append(comparisons, comparison)
	}

	return comparisons, nil
}

func validateP11CredentialPlanExpectation(sampleID, expectedPlanTrust string, expectation P11CredentialExpectation, plan RunPlan) error {
	if strings.TrimSpace(expectedPlanTrust) == "" {
		return fmt.Errorf("%s: credential operation %s requires explicit plan trust tier binding", sampleID, expectation.Operation.Operation)
	}

	if strings.TrimSpace(expectation.Operation.TrustTier) == "" {
		return fmt.Errorf("%s: credential operation %s requires explicit credential trust tier binding", sampleID, expectation.Operation.Operation)
	}

	return nil
}

type p11CredentialTrustAllowance struct {
	PlanTrustTier       string
	CredentialTrustTier string
}

var p11CredentialTrustAllowlist = map[string][]p11CredentialTrustAllowance{
	"coverage-comment": {
		{PlanTrustTier: "same-repository-agent", CredentialTrustTier: "same-repository-agent"},
	},
	"docs-preview-write": {
		{PlanTrustTier: "same-repository-agent", CredentialTrustTier: "same-repository-agent"},
	},
	"regeneration-push": {
		{PlanTrustTier: "trusted-base", CredentialTrustTier: "trusted-publication"},
	},
	"dev-release-publish": {
		{PlanTrustTier: "trusted-base", CredentialTrustTier: "trusted-publication"},
	},
	"stable-release-publish": {
		{PlanTrustTier: "trusted-base", CredentialTrustTier: "trusted-publication"},
	},
}

func validateP11CredentialTrustAllowance(sampleID string, expectation P11CredentialExpectation, plan RunPlan) error {
	for _, allowance := range p11CredentialTrustAllowlist[expectation.Operation.Operation] {
		if allowance.PlanTrustTier == plan.TrustTier && allowance.CredentialTrustTier == expectation.Operation.TrustTier {
			return nil
		}
	}

	return fmt.Errorf("%s: credential operation %s credential trust tier %s is not allowed for plan trust tier %s", sampleID, expectation.Operation.Operation, expectation.Operation.TrustTier, plan.TrustTier)
}

func BuildP11CompatibilityPlan(manifest Manifest, knownFiles []P11KnownFile, options PlanOptions) (RunPlan, error) {
	now := options.Now
	if now.IsZero() {
		return RunPlan{}, errors.New("P11 compatibility plan requires a deterministic validation time")
	}

	if err := manifest.ValidateAt(now); err != nil {
		return RunPlan{}, err
	}

	if err := ValidateP11ChangedFiles(knownFiles, options.ChangedFiles, options.ExactFileList); err != nil {
		return RunPlan{}, err
	}

	if err := ValidateP11ManifestWorkflowBindings(manifest, knownFiles); err != nil {
		return RunPlan{}, err
	}

	return BuildPlan(manifest, options)
}

func ValidateP11ChangedFiles(knownFiles []P11KnownFile, changedFiles []string, exact bool) error {
	if !exact {
		return errors.New("P11 compatibility plan requires an exact changed-file list")
	}

	known := map[string]bool{}

	for _, file := range knownFiles {
		path, err := validateP11KnownFile(file)
		if err != nil {
			return err
		}

		if known[path] {
			return fmt.Errorf("P11 compatibility fixture contains duplicate known path %s", path)
		}

		known[path] = true
	}

	for _, path := range canonicalChangedFiles(changedFiles) {
		if path == "" || invalidChangedPath(path) {
			return fmt.Errorf("changed file %q is not a valid P11 fixture path", path)
		}

		if !known[path] {
			return fmt.Errorf("changed file %q is missing from the P11 compatibility fixture", path)
		}

		if !p11DetectorKnowsPath(path) {
			return fmt.Errorf("changed file %q is unknown to the P11 compatibility detector", path)
		}
	}

	return nil
}

func ValidateP11ManifestWorkflowBindings(manifest Manifest, knownFiles []P11KnownFile) error {
	known := map[string]P11KnownFile{}

	for _, file := range knownFiles {
		path, err := validateP11KnownFile(file)
		if err != nil {
			return err
		}

		if _, exists := known[path]; exists {
			return fmt.Errorf("P11 compatibility fixture contains duplicate known path %s", path)
		}

		known[path] = file
	}

	bindings, err := p11ManifestWorkflowBindings(manifest)
	if err != nil {
		return err
	}

	for path, digest := range bindings {
		file, ok := known[path]
		if !ok {
			return fmt.Errorf("manifest workflow %s is missing from the P11 compatibility fixture", path)
		}

		if file.Content == nil {
			return fmt.Errorf("manifest workflow %s requires P11 fixture file content", path)
		}

		if file.SHA256 != digest {
			return fmt.Errorf("manifest workflow %s digest mismatch: fixture has %s manifest has %s", path, file.SHA256, digest)
		}
	}

	return nil
}

func P11KnownFilesFromRepository(repoRoot string, manifest Manifest, extraPaths []string) ([]P11KnownFile, error) {
	paths := map[string]bool{}

	for _, mode := range manifest.Modes {
		if mode.Trace.WorkflowPath != "" {
			normalized, err := normalizeP11RepoPath(mode.Trace.WorkflowPath)
			if err != nil {
				return nil, err
			}

			paths[normalized] = true
		}

		for _, coordinate := range mode.Coordinates {
			if coordinate.Trace.WorkflowPath != "" {
				normalized, err := normalizeP11RepoPath(coordinate.Trace.WorkflowPath)
				if err != nil {
					return nil, err
				}

				paths[normalized] = true
			}
		}
	}

	for _, path := range extraPaths {
		normalized, err := normalizeP11RepoPath(path)
		if err != nil {
			return nil, err
		}

		paths[normalized] = true
	}

	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}

	sort.Strings(ordered)

	files := make([]P11KnownFile, 0, len(ordered))
	for _, path := range ordered {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read fixture file %s: %w", path, err)
		}

		files = append(files, P11KnownFile{
			Path:    path,
			SHA256:  sha256Hex(data),
			Content: append([]byte(nil), data...),
		})
	}

	return files, nil
}

func validateP11KnownFile(file P11KnownFile) (string, error) {
	path := normalizeChangedPath(file.Path)
	if path == "" || invalidChangedPath(path) {
		return "", fmt.Errorf("P11 compatibility fixture contains invalid known path %q", file.Path)
	}

	if !digestPattern.MatchString(file.SHA256) {
		return "", fmt.Errorf("P11 compatibility fixture known path %s has invalid digest", path)
	}

	if file.Content != nil && sha256Hex(file.Content) != file.SHA256 {
		return "", fmt.Errorf("P11 compatibility fixture known path %s content digest does not match declared digest", path)
	}

	return path, nil
}

func p11DetectorKnowsPath(path string) bool {
	return isCIPolicyPath(path) ||
		isGeneratedInputPath(path) ||
		isLockfilePath(path) ||
		isReleaseMetadataPath(path) ||
		len(capabilitiesForPath(path)) > 0
}

func p11ManifestWorkflowBindings(manifest Manifest) (map[string]string, error) {
	bindings := map[string]string{}

	for _, mode := range manifest.Modes {
		if err := addP11ManifestWorkflowBinding(bindings, mode.Trace); err != nil {
			return nil, err
		}

		for _, coordinate := range mode.Coordinates {
			if err := addP11ManifestWorkflowBinding(bindings, coordinate.Trace); err != nil {
				return nil, err
			}
		}
	}

	return bindings, nil
}

func addP11ManifestWorkflowBinding(bindings map[string]string, trace LegacyTrace) error {
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

func ReadP11WorkflowSummary(path string) (P11WorkflowSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return P11WorkflowSummary{}, err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return P11WorkflowSummary{}, fmt.Errorf("decode workflow %s: %w", path, err)
	}

	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return P11WorkflowSummary{}, fmt.Errorf("workflow %s is not a mapping", path)
	}

	node := root.Content[0]
	permissions, permissionsExpression := p11StringMapOrExpression(p11MappingValue(node, "permissions"))
	summary := P11WorkflowSummary{
		Name:                  p11Scalar(p11MappingValue(node, "name")),
		Events:                p11EventNames(p11MappingValue(node, "on")),
		Permissions:           permissions,
		PermissionsExpression: permissionsExpression,
		Env:                   p11StringMap(p11MappingValue(node, "env")),
		Jobs:                  map[string]P11WorkflowJob{},
		Scalars:               p11ScalarValues(node),
	}

	jobs := p11MappingValue(node, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return P11WorkflowSummary{}, fmt.Errorf("workflow %s has no jobs mapping", path)
	}

	for index := 0; index < len(jobs.Content); index += 2 {
		id := jobs.Content[index].Value
		body := jobs.Content[index+1]

		if body.Kind != yaml.MappingNode {
			return P11WorkflowSummary{}, fmt.Errorf("workflow job %s is not a mapping", id)
		}

		permissions, permissionsExpression := p11StringMapOrExpression(p11MappingValue(body, "permissions"))
		summary.Jobs[id] = P11WorkflowJob{
			Name:                  p11Scalar(p11MappingValue(body, "name")),
			If:                    p11Scalar(p11MappingValue(body, "if")),
			Needs:                 p11StringList(p11MappingValue(body, "needs")),
			RunsOn:                p11Scalar(p11MappingValue(body, "runs-on")),
			Permissions:           permissions,
			PermissionsExpression: permissionsExpression,
			Env:                   p11StringMap(p11MappingValue(body, "env")),
			Steps:                 p11Steps(p11MappingValue(body, "steps")),
		}
	}

	return summary, nil
}

func validateP11Contract(contract P11JSHelperContract) error {
	if !strings.HasPrefix(contract.Path, P11JSSurfaceDirectory+"/") {
		return fmt.Errorf("P11 JS helper contract path %s is outside %s", contract.Path, P11JSSurfaceDirectory)
	}

	if contract.Owner == "" || contract.Kind == "" || contract.ExecutableContract == "" ||
		contract.DeletionCriterion == "" || contract.Tranche == "" {
		return fmt.Errorf("P11 JS helper contract %s has incomplete ownership or contract metadata", contract.Path)
	}

	if len(contract.Callers) == 0 || len(contract.PolicyInputs) == 0 ||
		len(contract.PolicyOutputs) == 0 || len(contract.CompatibilitySamples) == 0 {
		return fmt.Errorf("P11 JS helper contract %s has incomplete policy surface metadata", contract.Path)
	}

	switch contract.Disposition {
	case "port", "retain", "wrap":
	default:
		return fmt.Errorf("P11 JS helper contract %s has invalid disposition %q", contract.Path, contract.Disposition)
	}

	sampleIDs := map[string]bool{}

	for _, sample := range contract.CompatibilitySamples {
		if sample.ID == "" || sample.Description == "" {
			return fmt.Errorf("P11 JS helper contract %s has incomplete compatibility sample", contract.Path)
		}

		if sampleIDs[sample.ID] {
			return fmt.Errorf("P11 JS helper contract %s has duplicate compatibility sample %s", contract.Path, sample.ID)
		}

		sampleIDs[sample.ID] = true
	}

	return nil
}

func currentP11ScriptPaths(repoRoot string) ([]string, error) {
	// #nosec G204 -- repoRoot is a local repository path and arguments are passed without a shell.
	cmd := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "--", P11JSSurfaceDirectory)
	cmd.Env = p11IsolatedGitEnv()

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError

		details := ""
		if errors.As(err, &exitErr) {
			details = strings.TrimSpace(string(exitErr.Stderr))
		}

		if details != "" {
			return nil, fmt.Errorf("list P11 JS helpers from git index: %w: %s", err, details)
		}

		return nil, fmt.Errorf("list P11 JS helpers from git index: %w", err)
	}

	var paths []string

	for _, path := range strings.Split(string(output), "\x00") {
		if path == "" {
			continue
		}

		normalized, err := normalizeP11RepoPath(path)
		if err != nil {
			return nil, err
		}

		if !strings.HasPrefix(normalized, P11JSSurfaceDirectory+"/") {
			return nil, fmt.Errorf("git index returned path outside P11 JS surface: %s", normalized)
		}

		paths = append(paths, normalized)
	}

	sort.Strings(paths)

	return paths, nil
}

func p11IsolatedGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+2)

	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "GIT_DIR=") ||
			strings.HasPrefix(item, "GIT_WORK_TREE=") ||
			strings.HasPrefix(item, "GIT_INDEX_FILE=") ||
			strings.HasPrefix(item, "GIT_CONFIG_GLOBAL=") ||
			strings.HasPrefix(item, "GIT_CONFIG_SYSTEM=") ||
			strings.HasPrefix(item, "GIT_CONFIG_COUNT=") ||
			strings.HasPrefix(item, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(item, "GIT_CONFIG_VALUE_") {
			continue
		}

		env = append(env, item)
	}

	env = append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)

	return env
}

func normalizeP11RepoPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("repository path %s must be relative", path)
	}

	normalized := filepath.ToSlash(filepath.Clean(path))
	if normalized == "." || strings.HasPrefix(normalized, "../") || normalized == ".." {
		return "", fmt.Errorf("repository path %s escapes the repository", path)
	}

	return normalized, nil
}

func validateP11ManifestModeExpectation(manifest Manifest, sampleID string, expectation P11ManifestModeExpectation) error {
	mode, ok := p11ManifestModeByID(manifest, expectation.ID)
	if !ok {
		return fmt.Errorf("%s: manifest mode %s not found", sampleID, expectation.ID)
	}

	if expectation.Capability != "" && mode.Capability != expectation.Capability {
		return fmt.Errorf("%s: manifest mode %s capability = %s, want %s", sampleID, expectation.ID, mode.Capability, expectation.Capability)
	}

	if expectation.Requiredness != "" && mode.Requiredness != expectation.Requiredness {
		return fmt.Errorf("%s: manifest mode %s requiredness = %s, want %s", sampleID, expectation.ID, mode.Requiredness, expectation.Requiredness)
	}

	if len(expectation.TrustTiers) > 0 && !p11EqualStringSets(mode.TrustTiers, expectation.TrustTiers) {
		return fmt.Errorf("%s: manifest mode %s trust tiers = %v, want %v", sampleID, expectation.ID, mode.TrustTiers, expectation.TrustTiers)
	}

	for _, coordinateExpectation := range expectation.Coordinates {
		coordinate, ok := p11ManifestCoordinateByID(mode, coordinateExpectation.ID)
		if !ok {
			return fmt.Errorf("%s: manifest mode %s coordinate %s not found", sampleID, expectation.ID, coordinateExpectation.ID)
		}

		if coordinateExpectation.Requiredness != "" && coordinate.Requiredness != coordinateExpectation.Requiredness {
			return fmt.Errorf("%s: manifest coordinate %s requiredness = %s, want %s", sampleID, coordinateExpectation.ID, coordinate.Requiredness, coordinateExpectation.Requiredness)
		}
	}

	return nil
}

func p11ManifestModeByID(manifest Manifest, id string) (Mode, bool) {
	for _, mode := range manifest.Modes {
		if mode.ID == id {
			return mode, true
		}
	}

	return Mode{}, false
}

func p11ManifestCoordinateByID(mode Mode, id string) (Coordinate, bool) {
	for _, coordinate := range mode.Coordinates {
		if coordinate.ID == id {
			return coordinate, true
		}
	}

	return Coordinate{}, false
}

func p11EqualStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	left = append([]string(nil), left...)
	right = append([]string(nil), right...)

	sort.Strings(left)
	sort.Strings(right)

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func p11PlanCoordinates(plan RunPlan) []string {
	coordinates := make([]string, 0, len(plan.Jobs))
	for _, job := range plan.Jobs {
		coordinates = append(coordinates, job.Coordinate)
	}

	sort.Strings(coordinates)

	return coordinates
}

func p11MappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}

	return nil
}

func p11Scalar(node *yaml.Node) string {
	if node == nil {
		return ""
	}

	return node.Value
}

func p11ScalarValues(node *yaml.Node) []string {
	var values []string

	var walk func(*yaml.Node, map[*yaml.Node]bool)

	walk = func(current *yaml.Node, resolvingAliases map[*yaml.Node]bool) {
		if current == nil {
			return
		}

		if current.Kind == yaml.AliasNode {
			if current.Alias == nil || resolvingAliases[current.Alias] {
				return
			}

			resolvingAliases[current.Alias] = true
			walk(current.Alias, resolvingAliases)
			delete(resolvingAliases, current.Alias)

			return
		}

		if current.Kind == yaml.ScalarNode {
			values = append(values, current.Value)
		}

		for _, child := range current.Content {
			walk(child, resolvingAliases)
		}
	}

	walk(node, map[*yaml.Node]bool{})

	return values
}

func p11StringMap(node *yaml.Node) map[string]string {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	result := map[string]string{}
	for index := 0; index < len(node.Content); index += 2 {
		result[node.Content[index].Value] = node.Content[index+1].Value
	}

	return result
}

func p11StringMapOrExpression(node *yaml.Node) (map[string]string, string) {
	if node == nil {
		return nil, ""
	}

	if node.Kind != yaml.MappingNode {
		if node.Value != "" {
			return nil, node.Value
		}

		return nil, node.ShortTag()
	}

	return p11StringMap(node), ""
}

func p11StringList(node *yaml.Node) []string {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return nil
		}

		return []string{node.Value}
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			values = append(values, item.Value)
		}

		return values
	default:
		return nil
	}
}

func p11EventNames(node *yaml.Node) []string {
	if node == nil {
		return nil
	}

	var events []string

	switch node.Kind {
	case yaml.ScalarNode:
		events = append(events, node.Value)
	case yaml.SequenceNode:
		for _, event := range node.Content {
			events = append(events, event.Value)
		}
	case yaml.MappingNode:
		for index := 0; index < len(node.Content); index += 2 {
			events = append(events, node.Content[index].Value)
		}
	}

	sort.Strings(events)

	return events
}

func p11Steps(node *yaml.Node) []P11WorkflowStep {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}

	steps := make([]P11WorkflowStep, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}

		steps = append(steps, P11WorkflowStep{
			Name: p11Scalar(p11MappingValue(item, "name")),
			Uses: p11Scalar(p11MappingValue(item, "uses")),
			If:   p11Scalar(p11MappingValue(item, "if")),
			Env:  p11StringMap(p11MappingValue(item, "env")),
			With: p11StringMap(p11MappingValue(item, "with")),
			Run:  p11Scalar(p11MappingValue(item, "run")),
		})
	}

	return steps
}
