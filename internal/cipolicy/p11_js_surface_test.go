package cipolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/cibaseline"
)

var p11TestNow = p2TestNow

const p11SameRepositoryGuard = "github.event.pull_request.head.repo.full_name == github.repository"

var (
	p11ReleaseTokenExpression             = regexp.MustCompile(`\$\{\{\s*secrets\s*(?:\.\s*RELEASE_TOKEN|\[\s*['"]RELEASE_TOKEN['"]\s*\])\s*\}\}`)
	p11GitHubTokenExpression              = regexp.MustCompile(`\$\{\{\s*github\s*(?:\.\s*token|\[\s*['"]token['"]\s*\])\s*\}\}`)
	p11RepositoryControlledCommandPattern = regexp.MustCompile(`(?:^|[[:space:];|&({"']|` + "`" + `)(?:(?:go|make|node|npm|npx|pnpm|python3?|sh|bash)(?:[[:space:]]|$)|(?:\./|\.\./)[^[:space:];|&)]*)`)
	p11RepositoryControlledScriptPattern  = regexp.MustCompile(`scripts/libghostty-native\.sh|\.github/workflows/scripts/`)
)

func TestP11JSSurfaceContractsCoverCurrentInventory(t *testing.T) {
	repoRoot := p11RepoRoot()

	inventory, err := ReadInventory(filepath.Join(repoRoot, DefaultInventoryPath))
	if err != nil {
		t.Fatal(err)
	}

	contracts := P11JSSurfaceContracts()
	if err := ValidateP11JSSurfaceInventory(repoRoot, inventory, contracts); err != nil {
		t.Fatal(err)
	}

	if len(contracts) != 0 {
		t.Fatalf("P11 retained JS contract count = %d, want none: %#v", len(contracts), contracts)
	}

	retiredPaths := append([]string{}, p11DocsDiffRetiredSurfacePaths...)
	retiredPaths = append(retiredPaths, p11DocsPreviewRetiredSurfacePaths...)

	for _, path := range retiredPaths {
		for _, contract := range contracts {
			if contract.Path == path {
				t.Fatalf("retired JS surface %s still has a retained JS contract", path)
			}
		}
	}
}

func TestP11DocsDiffReplacementSamplesCoverPortMatrix(t *testing.T) {
	repoRoot := p11RepoRoot()

	inventory, err := ReadInventory(filepath.Join(repoRoot, DefaultInventoryPath))
	if err != nil {
		t.Fatal(err)
	}

	requirements := P11DocsDiffCompatibilityRequirements()
	if err := ValidateP11DocsDiffReplacement(repoRoot, inventory, P11DocsDiffReplacementPath, requirements); err != nil {
		t.Fatal(err)
	}

	replacement := P11JSHelperContract{
		Path:                 P11DocsDiffReplacementPath,
		CompatibilitySamples: requirements,
	}

	for _, want := range p11DocsDiffRequiredSampleIDs() {
		if !p11HasSampleRequirement(replacement, want) {
			t.Fatalf("%s is missing compatibility sample %s", replacement.Path, want)
		}
	}
}

func TestP11DocsPreviewReplacementSamplesCoverPortMatrix(t *testing.T) {
	repoRoot := p11RepoRoot()

	inventory, err := ReadInventory(filepath.Join(repoRoot, DefaultInventoryPath))
	if err != nil {
		t.Fatal(err)
	}

	requirements := P11DocsPreviewCompatibilityRequirements()
	if err := ValidateP11DocsPreviewReplacement(repoRoot, inventory, P11DocsPreviewReplacementPath, requirements); err != nil {
		t.Fatal(err)
	}

	replacement := P11JSHelperContract{
		Path:                 P11DocsPreviewReplacementPath,
		CompatibilitySamples: requirements,
	}

	for _, want := range p11DocsPreviewRequiredSampleIDs() {
		if !p11HasSampleRequirement(replacement, want) {
			t.Fatalf("%s is missing compatibility sample %s", replacement.Path, want)
		}
	}
}

func TestP11ReplacementRejectsMissingMismatchedAndRetiredSurfaces(t *testing.T) {
	tests := map[string]p11ReplacementRejectionSuite{
		"docs-diff": {
			requirements:        P11DocsDiffCompatibilityRequirements(),
			replacementPath:     P11DocsDiffReplacementPath,
			missingPath:         "cmd/docsdiff/missing_test.go",
			missingWant:         "missing P11 docs-diff Go replacement surface cmd/docsdiff/missing_test.go",
			mismatchedPath:      "cmd/docsdiff/main.go",
			missingSampleID:     "png-composite",
			missingSampleWant:   "missing compatibility sample png-composite",
			withoutRequirement:  p11DocsDiffRequirementsWithout,
			validateReplacement: ValidateP11DocsDiffReplacement,
			retiredSurfaces: map[string]string{
				".github/workflows/scripts/docs-diff.js":      "P0 inventory still references retired P11 docs-diff surface .github/workflows/scripts/docs-diff.js",
				".github/workflows/scripts/package-lock.json": "P0 inventory still references retired P11 docs-diff surface .github/workflows/scripts/package-lock.json",
			},
		},
		"docs-preview": {
			requirements:        P11DocsPreviewCompatibilityRequirements(),
			replacementPath:     P11DocsPreviewReplacementPath,
			missingPath:         "internal/docspreview/missing_test.go",
			missingWant:         "missing P11 docs-preview Go replacement surface internal/docspreview/missing_test.go",
			mismatchedPath:      "internal/docspreview/docspreview.go",
			missingSampleID:     "docs-preview-rest-api",
			missingSampleWant:   "missing compatibility sample docs-preview-rest-api",
			withoutRequirement:  p11DocsPreviewRequirementsWithout,
			validateReplacement: ValidateP11DocsPreviewReplacement,
			retiredSurfaces: map[string]string{
				".github/workflows/scripts/docs-preview.js":      "P0 inventory still references retired P11 docs-preview surface .github/workflows/scripts/docs-preview.js",
				".github/workflows/scripts/docs-preview.test.js": "P0 inventory still references retired P11 docs-preview surface .github/workflows/scripts/docs-preview.test.js",
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			p11RunReplacementRejectionCases(t, test)
		})
	}
}

type p11ReplacementRejectionSuite struct {
	requirements        []P11CompatibilitySampleRequirement
	replacementPath     string
	missingPath         string
	missingWant         string
	mismatchedPath      string
	missingSampleID     string
	missingSampleWant   string
	withoutRequirement  func([]P11CompatibilitySampleRequirement, string) []P11CompatibilitySampleRequirement
	validateReplacement func(string, cibaseline.Inventory, string, []P11CompatibilitySampleRequirement) error
	retiredSurfaces     map[string]string
}

type p11ReplacementRejectionCase struct {
	replacementPath string
	requirements    []P11CompatibilitySampleRequirement
	mutateInventory func(*cibaseline.Inventory)
	want            string
}

func p11RunReplacementRejectionCases(t *testing.T, suite p11ReplacementRejectionSuite) {
	t.Helper()

	tests := map[string]p11ReplacementRejectionCase{
		"missing Go replacement": {
			replacementPath: suite.missingPath,
			requirements:    suite.requirements,
			want:            suite.missingWant,
		},
		"mismatched Go replacement": {
			replacementPath: suite.mismatchedPath,
			requirements:    suite.requirements,
			want:            "kind = go-policy-helper, want go-policy-contract-test",
		},
		"missing compatibility sample": {
			replacementPath: suite.replacementPath,
			requirements:    suite.withoutRequirement(suite.requirements, suite.missingSampleID),
			want:            suite.missingSampleWant,
		},
	}

	for path, want := range suite.retiredSurfaces {
		retiredPath := path
		tests["retired surface "+retiredPath] = p11ReplacementRejectionCase{
			replacementPath: suite.replacementPath,
			requirements:    suite.requirements,
			mutateInventory: func(inventory *cibaseline.Inventory) {
				inventory.Surfaces = append(inventory.Surfaces, p11RetiredSurface(retiredPath))
			},
			want: want,
		}
	}

	repoRoot := p11RepoRoot()

	baseInventory, err := ReadInventory(filepath.Join(repoRoot, DefaultInventoryPath))
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			inventory := baseInventory
			inventory.Surfaces = append([]cibaseline.Surface(nil), baseInventory.Surfaces...)

			if test.mutateInventory != nil {
				test.mutateInventory(&inventory)
				p11ResignInventory(t, &inventory)
			}

			err := suite.validateReplacement(repoRoot, inventory, test.replacementPath, test.requirements)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("replacement validation error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestP11RegenAuthReplacementSamplesCoverHardeningMatrix(t *testing.T) {
	replacement := P11JSHelperContract{
		Path:                 P11RegenAuthReplacementPath,
		CompatibilitySamples: P11RegenAuthCompatibilityRequirements(),
	}

	for _, want := range []string{
		"regen-same-repository-agent",
		"regen-fork-untrusted",
		"regen-trusted-base",
		"regen-push-boundary",
		"regen-non-superset-negative",
	} {
		if !p11HasSampleRequirement(replacement, want) {
			t.Fatalf("%s is missing compatibility sample %s", replacement.Path, want)
		}
	}

	declaredSamples := append([]P11CompatibilitySample{}, p11RegenAuthSamples()...)
	for _, negative := range p11RegenAuthNegativeSamples() {
		declaredSamples = append(declaredSamples, negative.Sample)
	}

	p11AssertSampleMatrixMatchesRequirements(t, replacement, declaredSamples)
}

func TestP11RegenAuthCompatibilitySamplesUseClosedWorldPlanFixture(t *testing.T) {
	repoRoot := p11RepoRoot()

	manifest, err := ReadManifest(filepath.Join(repoRoot, DefaultManifestPath))
	if err != nil {
		t.Fatal(err)
	}

	knownFiles, err := P11KnownFilesFromRepository(repoRoot, manifest, []string{"internal/protocol/messages.go"})
	if err != nil {
		t.Fatal(err)
	}

	samples := p11RegenAuthSamples()

	comparisons, err := CompareP11CompatibilitySamples(manifest, knownFiles, samples, p11TestNow)
	if err != nil {
		t.Fatal(err)
	}

	if len(comparisons) != len(samples) {
		t.Fatalf("comparison count = %d, want %d", len(comparisons), len(samples))
	}

	for _, comparison := range comparisons {
		if comparison.PlanDigest == "" || len(comparison.RequiredModes) == 0 || len(comparison.Coordinates) == 0 {
			t.Fatalf("comparison %#v did not exercise a successful Go policy compatibility plan", comparison)
		}

		if !comparison.Superset || !p11HasString(comparison.SupersetReasons, "generated-input") {
			t.Fatalf("comparison %#v did not bind generated-input superset semantics", comparison)
		}
	}
}

func TestP11RegenAuthNonSupersetNegativeSampleRejectsCredentialPlanBinding(t *testing.T) {
	repoRoot := p11RepoRoot()

	manifest, err := ReadManifest(filepath.Join(repoRoot, DefaultManifestPath))
	if err != nil {
		t.Fatal(err)
	}

	for _, negative := range p11RegenAuthNegativeSamples() {
		sample := negative.Sample

		t.Run(sample.ID, func(t *testing.T) {
			if negative.ExpectedErrorSubstr == "" {
				t.Fatal("negative sample must declare its expected failure")
			}

			knownFiles, err := P11KnownFilesFromRepository(repoRoot, manifest, sample.PlanOptions.ChangedFiles)
			if err != nil {
				t.Fatal(err)
			}

			options := sample.PlanOptions
			options.Now = p11TestNow
			options.CreatedAt = p11TestNow.Add(-10 * time.Minute)

			plan, err := BuildP11CompatibilityPlan(manifest, knownFiles, options)
			if err != nil {
				t.Fatal(err)
			}

			if plan.Superset {
				t.Fatalf("negative sample plan unexpectedly selected safe superset: %#v", plan.SupersetReasons)
			}

			if p11HasString(plan.Capabilities, "generated-metadata") {
				t.Fatalf("negative sample plan capabilities = %#v, want generated-metadata absent", plan.Capabilities)
			}

			_, err = CompareP11CompatibilitySamples(manifest, knownFiles, []P11CompatibilitySample{sample}, p11TestNow)
			if err == nil || !strings.Contains(err.Error(), negative.ExpectedErrorSubstr) {
				t.Fatalf("CompareP11CompatibilitySamples() error = %v, want %q", err, negative.ExpectedErrorSubstr)
			}
		})
	}
}

func TestP11RegenWorkflowTrustSemantics(t *testing.T) {
	repoRoot := p11RepoRoot()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/regen.yml"))
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(workflow.Events, []string{"pull_request"}) {
		t.Fatalf("regen events = %#v, want only pull_request", workflow.Events)
	}

	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatalf("regen permissions = %#v, want contents:read only", workflow.Permissions)
	}

	if workflow.PermissionsExpression != "" {
		t.Fatalf("regen permissions expression = %q, want contents:read mapping", workflow.PermissionsExpression)
	}

	if p11MapHasReleaseTokenExpression(workflow.Env) {
		t.Fatal("RELEASE_TOKEN must not be exposed at workflow scope")
	}

	if !reflect.DeepEqual(p11WorkflowJobIDs(workflow), []string{"prepare", "regen", "validate"}) {
		t.Fatalf("regen jobs = %#v, want exactly validate, prepare, and regen", p11WorkflowJobIDs(workflow))
	}

	validate := p11WorkflowJob(t, workflow, "validate")
	prepare := p11WorkflowJob(t, workflow, "prepare")
	regen := p11WorkflowJob(t, workflow, "regen")

	p11AssertJobIf(t, "validate", validate, p11SameRepositoryGuard)
	p11AssertJobIf(t, "prepare", prepare, p11SameRepositoryGuard)
	p11AssertJobIf(t, "regen", regen, "always() && "+p11SameRepositoryGuard)

	p11AssertStepNames(t, "validate", validate, []string{
		"Require workflow-triggering token",
	})
	p11AssertStepNames(t, "prepare", prepare, []string{
		"Check out PR head without persisted credentials",
		"Verify checked-out PR head",
		"",
		"Regenerate generated files",
		"Detect a native dependency lock update",
		"Rotate the complete native dependency unit",
		"Verify regenerated content",
		"Commit generated files if changed",
		"Upload generated commit",
	})
	p11AssertStepNames(t, "regen", regen, []string{
		"Propagate preparation failure",
		"Report clean generated files",
		"Check out source head with workflow-triggering credentials",
		"Download generated commit",
		"Push generated commit",
	})

	for id, job := range map[string]P11WorkflowJob{
		"validate": validate,
		"prepare":  prepare,
		"regen":    regen,
	} {
		if p11MapHasReleaseTokenExpression(job.Env) {
			t.Fatalf("job %s exposes RELEASE_TOKEN at job env scope", id)
		}

		p11AssertNoJobPermissionOverrides(t, id, job)
	}

	if strings.Contains(regen.If, "pull_request_target") {
		t.Fatal("regen job must not rely on pull_request_target trust")
	}

	if !reflect.DeepEqual(prepare.Needs, []string{"validate"}) {
		t.Fatalf("prepare needs = %#v, want validate", prepare.Needs)
	}

	if !reflect.DeepEqual(regen.Needs, []string{"validate", "prepare"}) {
		t.Fatalf("regen needs = %#v, want validate and prepare", regen.Needs)
	}

	validation := p11WorkflowStep(t, validate, "Require workflow-triggering token")
	if validation.Env["RELEASE_TOKEN"] != "${{ secrets.RELEASE_TOKEN }}" {
		t.Fatalf("validation RELEASE_TOKEN env = %q", validation.Env["RELEASE_TOKEN"])
	}

	for _, want := range []string{`if [ -z "$RELEASE_TOKEN" ]; then`, "exit 1"} {
		if !strings.Contains(validation.Run, want) {
			t.Fatalf("validation step missing %q in run block", want)
		}
	}

	if strings.Contains(validation.Run, "$RELEASE_TOKEN\n") || p11RunLineContainsAll(validation.Run, "echo", "$RELEASE_TOKEN") {
		t.Fatal("validation step must not print RELEASE_TOKEN")
	}

	if p11JobUsesAction(validate, "actions/checkout") ||
		p11JobRunsRepositoryControlledCode(validate) {
		t.Fatal("credential validation job must not check out or run repository-controlled generators")
	}

	if p11JobRunsRepositoryControlledCode(regen) {
		t.Fatal("credentialed regen job must not run repository-controlled generators")
	}

	p11AssertJobUsesOnlyAllowedActions(t, "regen", regen, map[string]string{
		"Check out source head with workflow-triggering credentials": "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"Download generated commit":                                  "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c",
	})

	initialCheckout := p11WorkflowStep(t, prepare, "Check out PR head without persisted credentials")
	if !strings.HasPrefix(initialCheckout.Uses, "actions/checkout@") {
		t.Fatalf("initial checkout uses = %q", initialCheckout.Uses)
	}

	if initialCheckout.With["ref"] != "${{ github.head_ref }}" ||
		initialCheckout.With["persist-credentials"] != "false" ||
		initialCheckout.With["token"] != "" {
		t.Fatalf("initial checkout is not an uncredentialed PR-head checkout: %#v", initialCheckout.With)
	}

	if p11JobCheckoutPersistsCredentials(prepare) {
		t.Fatal("prepare job must not contain a checkout with persisted credentials")
	}

	if p11JobHasReleaseTokenExpression(prepare) {
		t.Fatal("RELEASE_TOKEN must not be exposed to prepare job steps")
	}

	commit := p11WorkflowStep(t, prepare, "Commit generated files if changed")
	if !strings.Contains(commit.Run, `git bundle create "$RUNNER_TEMP/generated.bundle" HEAD "^$SOURCE_SHA"`) {
		t.Fatal("prepare job must transfer only a generated commit bundle to the credentialed job")
	}

	pushCheckout := p11WorkflowStep(t, regen, "Check out source head with workflow-triggering credentials")
	if pushCheckout.If != "needs.prepare.result == 'success' && needs.prepare.outputs.changed == 'true'" {
		t.Fatalf("push checkout if = %q", pushCheckout.If)
	}

	if pushCheckout.With["ref"] != "${{ github.event.pull_request.head.sha }}" ||
		pushCheckout.With["token"] != "${{ secrets.RELEASE_TOKEN }}" ||
		pushCheckout.With["persist-credentials"] != "true" {
		t.Fatalf("push checkout is not the isolated credentialed checkout: %#v", pushCheckout.With)
	}

	if got := p11WorkflowReleaseTokenExpressionCount(workflow); got != 2 {
		t.Fatalf("RELEASE_TOKEN structural references = %d, want validation env and push checkout only", got)
	}

	if p11WorkflowHasGitHubTokenExpression(workflow) ||
		p11WorkflowReferences(workflow, "GITHUB_TOKEN") ||
		p11WorkflowReferences(workflow, "gh workflow run") ||
		p11WorkflowRunLineContainsAll(workflow, "https://", "RELEASE_TOKEN") ||
		p11WorkflowRunLineContainsReleaseTokenExpression(workflow, "https://") {
		t.Fatal("regen workflow must not fall back to the default token or manual workflow dispatch")
	}

	push := p11WorkflowStep(t, regen, "Push generated commit")
	if push.Env["HEAD_REF"] != "${{ github.head_ref }}" ||
		push.Env["SOURCE_SHA"] != "${{ github.event.pull_request.head.sha }}" {
		t.Fatalf("push env does not carry head ref/source sha safely: %#v", push.Env)
	}

	for _, want := range []string{
		`git ls-remote origin "refs/heads/$HEAD_REF"`,
		`git show -s --format=%P "$GENERATED_SHA"`,
		`git show -s --format=%an "$GENERATED_SHA")" != "github-actions[bot]"`,
		`git show -s --format=%ae "$GENERATED_SHA")" != "41898282+github-actions[bot]@users.noreply.github.com"`,
		`git show -s --format=%s "$GENERATED_SHA")" != "chore(generated): refresh generated dependency metadata"`,
		`git diff --no-renames --name-only -z "$SOURCE_SHA" "$GENERATED_SHA"`,
		`Generated commit contains a non-allowlisted path.`,
		`git push origin "HEAD:$HEAD_REF"`,
	} {
		if !strings.Contains(push.Run, want) {
			t.Fatalf("push step missing %q", want)
		}
	}

	if strings.Contains(push.Run, "--force") ||
		strings.Contains(push.Run, "git push -f") ||
		strings.Contains(push.Run, "${{ github.head_ref }}") {
		t.Fatal("push step must be a non-force push without inline attacker-controlled head_ref interpolation")
	}

	if got := p11RunLineCountContaining(push.Run, "git push "); got != 1 {
		t.Fatalf("push step git push line count = %d, want one verified push", got)
	}

	p11AssertRunLineOrder(t, push.Run, []string{
		`git ls-remote origin "refs/heads/$HEAD_REF"`,
		`git fetch "$RUNNER_TEMP/regen-output/generated.bundle" HEAD`,
		`git show -s --format=%P "$GENERATED_SHA"`,
		`git show -s --format=%an "$GENERATED_SHA"`,
		`git diff --no-renames --name-only -z "$SOURCE_SHA" "$GENERATED_SHA"`,
		`if [ "$changed" != "true" ]; then`,
		`git checkout --detach "$GENERATED_SHA"`,
		`git push origin "HEAD:$HEAD_REF"`,
	})

	manifest, err := ReadManifest(filepath.Join(repoRoot, DefaultManifestPath))
	if err != nil {
		t.Fatal(err)
	}

	p11AssertTrustTier(t, manifest, p11SameRepoEvent(), "same-repository-agent")
	p11AssertTrustTier(t, manifest, p11ForkEvent(), "fork-untrusted")
	p11AssertTrustTier(t, manifest, p11TrustedBaseEvent(), "trusted-base")

	err = ValidateCredentialOperation(CredentialOperation{
		Operation:  "regeneration-push",
		TrustTier:  "same-repository-agent",
		Capability: "generated-metadata",
		Token: SyntheticToken{
			Name:         "release",
			TrustTier:    "same-repository-agent",
			Class:        syntheticMaintainerToken,
			Scopes:       []string{"contents:write"},
			AllowedRoots: []string{"generated"},
		},
		Target: "generated/braw.bundle",
	})
	if err == nil || !strings.Contains(err.Error(), "same-repository agent branches cannot obtain maintainer credentials") {
		t.Fatalf("same-repository regeneration credential error = %v", err)
	}

	if err := ValidateCredentialOperation(CredentialOperation{
		Operation:  "regeneration-push",
		TrustTier:  "trusted-publication",
		Capability: "generated-metadata",
		Token: SyntheticToken{
			Name:         "release",
			TrustTier:    "trusted-publication",
			Class:        syntheticMaintainerToken,
			Scopes:       []string{"contents:write"},
			AllowedRoots: []string{"generated"},
		},
		Target: "generated/braw.bundle",
	}); err != nil {
		t.Fatalf("trusted publication regeneration credential rejected: %v", err)
	}
}

func TestP11RegenWorkflowTrustSemanticsRejectsUnprojectedTokenScalars(t *testing.T) {
	tests := map[string]struct {
		needle      string
		replacement string
		detected    func(P11WorkflowSummary) bool
	}{
		"hidden release token expression": {
			needle:      "name: Regenerate\n\non:\n",
			replacement: "name: Regenerate\nrun-name: ${{ secrets.RELEASE_TOKEN }}\n\non:\n",
			detected: func(workflow P11WorkflowSummary) bool {
				return p11WorkflowReleaseTokenExpressionCount(workflow) > 2
			},
		},
		"hidden github token expression": {
			needle:      "  group: regen-${{ github.event.pull_request.number }}\n",
			replacement: "  group: ${{ github.token }}\n",
			detected:    p11WorkflowHasGitHubTokenExpression,
		},
		"hidden default token name": {
			needle:      "  group: regen-${{ github.event.pull_request.number }}\n",
			replacement: "  group: GITHUB_TOKEN\n",
			detected: func(workflow P11WorkflowSummary) bool {
				return p11WorkflowReferences(workflow, "GITHUB_TOKEN")
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			workflow := p11ReadMutatedRegenWorkflow(t, test.needle, test.replacement)
			if !test.detected(workflow) {
				t.Fatal("whole-document scalar token sweep did not surface hidden token reference")
			}
		})
	}
}

func TestP11RegenWorkflowScalarSweepCountsAliasedTokenScalars(t *testing.T) {
	workflowSource := `name: Braw
run-name: ${{ secrets.RELEASE_TOKEN }}
on:
  - pull_request
permissions:
  contents: read
env:
  SAFE_TOKEN: &release ${{ secrets.RELEASE_TOKEN }}
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - run: echo braw
  hidden:
    if: *release
    runs-on: ubuntu-latest
    steps:
      - run: echo canny
`
	workflowPath := filepath.Join(t.TempDir(), "regen.yml")

	// #nosec G703 -- workflowPath is rooted in t.TempDir and not user-controlled.
	if err := os.WriteFile(workflowPath, []byte(workflowSource), 0o600); err != nil {
		t.Fatal(err)
	}

	workflow, err := ReadP11WorkflowSummary(workflowPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := p11WorkflowReleaseTokenExpressionCount(workflow); got != 3 {
		t.Fatalf("aliased RELEASE_TOKEN scalar count = %d, want original scalar plus alias reference", got)
	}
}

func TestP11RegenWorkflowTrustSemanticsRejectsScalarJobPermissions(t *testing.T) {
	repoRoot := p11RepoRoot()
	path := filepath.Join(repoRoot, ".github/workflows/regen.yml")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	needle := "\n  regen:\n    name: Regenerate generated files\n"
	replacement := "\n  regen:\n    permissions: write-all\n    name: Regenerate generated files\n"
	workflowSource := string(data)

	if !strings.Contains(workflowSource, needle) {
		t.Fatalf("regen job insertion point %q not found", needle)
	}

	mutated := strings.Replace(workflowSource, needle, replacement, 1)
	mutatedPath := filepath.Join(t.TempDir(), "regen.yml")

	// #nosec G703 -- mutatedPath is rooted in t.TempDir and not user-controlled.
	if err := os.WriteFile(mutatedPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	workflow, err := ReadP11WorkflowSummary(mutatedPath)
	if err != nil {
		t.Fatal(err)
	}

	regen := p11WorkflowJob(t, workflow, "regen")
	if regen.PermissionsExpression != "write-all" {
		t.Fatalf("regen permissions expression = %q, want write-all", regen.PermissionsExpression)
	}

	if !p11HasJobPermissionOverride(regen) {
		t.Fatal("scalar job permissions override was not surfaced to semantic assertions")
	}
}

func TestP11RegenWorkflowTrustSemanticsRejectsCredentialedScriptExecution(t *testing.T) {
	repoRoot := p11RepoRoot()
	path := filepath.Join(repoRoot, ".github/workflows/regen.yml")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	needle := "\n          set -euo pipefail\n          remote_sha=\"$(git ls-remote origin \"refs/heads/$HEAD_REF\" | awk '{print $1}')\"\n"
	replacement := "\n          set -euo pipefail\n          node .github/workflows/scripts/retired-helper.test.js\n          remote_sha=\"$(git ls-remote origin \"refs/heads/$HEAD_REF\" | awk '{print $1}')\"\n"
	workflowSource := string(data)

	if !strings.Contains(workflowSource, needle) {
		t.Fatalf("push step insertion point %q not found", needle)
	}

	mutated := strings.Replace(workflowSource, needle, replacement, 1)
	mutatedPath := filepath.Join(t.TempDir(), "regen.yml")

	// #nosec G703 -- mutatedPath is rooted in t.TempDir and not user-controlled.
	if err := os.WriteFile(mutatedPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	workflow, err := ReadP11WorkflowSummary(mutatedPath)
	if err != nil {
		t.Fatal(err)
	}

	regen := p11WorkflowJob(t, workflow, "regen")
	if !p11JobRunsRepositoryControlledCode(regen) {
		t.Fatal("credentialed repository script execution was not surfaced to semantic assertions")
	}
}

func TestP11CurrentScriptPathsUseGitIndex(t *testing.T) {
	repoRoot := t.TempDir()
	foreignRepoRoot := t.TempDir()

	scriptsDir := filepath.Join(repoRoot, ".github", "workflows", "scripts")
	if err := os.MkdirAll(filepath.Join(scriptsDir, "node_modules"), 0o750); err != nil {
		t.Fatal(err)
	}

	foreignScriptsDir := filepath.Join(foreignRepoRoot, ".github", "workflows", "scripts")
	if err := os.MkdirAll(foreignScriptsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	for path, data := range map[string]string{
		filepath.Join(repoRoot, ".gitignore"):                                 ".github/workflows/scripts/node_modules/\n",
		filepath.Join(scriptsDir, "retained-helper.test.js"):                  "'use strict';\n",
		filepath.Join(scriptsDir, "untracked-helper.test.js"):                 "'use strict';\n",
		filepath.Join(scriptsDir, "node_modules", "ignored-helper.test.js"):   "'use strict';\n",
		filepath.Join(repoRoot, "internal", "cipolicy", "unrelated.testdata"): "braw\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}

		// #nosec G703 -- test paths are rooted in t.TempDir.
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	p11RunGit(t, repoRoot, "init")
	p11RunGit(t, repoRoot, "add", ".gitignore", ".github/workflows/scripts/retained-helper.test.js")

	foreignHelper := filepath.Join(foreignScriptsDir, "foreign-helper.test.js")
	// #nosec G703 -- foreignHelper is rooted in t.TempDir.
	if err := os.WriteFile(foreignHelper, []byte("'use strict';\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p11RunGit(t, foreignRepoRoot, "init")
	p11RunGit(t, foreignRepoRoot, "add", ".github/workflows/scripts/foreign-helper.test.js")

	t.Setenv("GIT_DIR", filepath.Join(foreignRepoRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", foreignRepoRoot)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(foreignRepoRoot, ".git", "index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")

	paths, err := currentP11ScriptPaths(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(paths, []string{".github/workflows/scripts/retained-helper.test.js"}) {
		t.Fatalf("currentP11ScriptPaths() = %#v, want only git-index tracked helper", paths)
	}
}

func TestP11CurrentScriptPathsReportsGitIndexFailureDetails(t *testing.T) {
	_, err := currentP11ScriptPaths(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("currentP11ScriptPaths() error = %v, want git stderr details", err)
	}
}

func TestP11RegenWorkflowTrustSemanticsRejectsDefaultTokenVariants(t *testing.T) {
	tests := map[string]struct {
		needle      string
		replacement string
		detected    func(P11WorkflowSummary) bool
	}{
		"compact github token expression": {
			needle:      "          RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n",
			replacement: "          RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n          DEFAULT_TOKEN: ${{github.token}}\n",
			detected:    p11WorkflowHasGitHubTokenExpression,
		},
		"github token env key": {
			needle:      "          RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n",
			replacement: "          RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n          GITHUB_TOKEN: braw\n",
			detected: func(workflow P11WorkflowSummary) bool {
				return p11WorkflowReferences(workflow, "GITHUB_TOKEN")
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			workflow := p11ReadMutatedRegenWorkflow(t, test.needle, test.replacement)
			if !test.detected(workflow) {
				t.Fatal("default token fallback was not surfaced to semantic assertions")
			}
		})
	}
}

func TestP11RepositoryControlledCommandDetectionMatchesRetainedJSEmbeddedCommands(t *testing.T) {
	tests := map[string]string{
		"embedded go test":                  "if go test ./internal/protocol -run TestManifestUpToDate; then",
		"embedded make package graph":       "env GRAITH_CHECK=braw make package-graph",
		"embedded native helper":            "cd /tmp && scripts/libghostty-native.sh verify-dependency-unit",
		"embedded node helper":              "env GRAITH_CHECK=braw node .github/workflows/scripts/retired-helper.test.js",
		"embedded python helper":            "changed=$(python3 scripts/braw.py)",
		"embedded shell helper":             "if sh scripts/braw.sh; then",
		"backtick package graph":            "GENERATED=`make package-graph`",
		"eval quoted native helper":         `eval "scripts/libghostty-native.sh verify"`,
		"quoted native helper":              `"scripts/libghostty-native.sh" verify`,
		"repo variable native helper":       `${REPO_ROOT}/scripts/libghostty-native.sh verify`,
		"absolute native helper":            `/home/runner/work/graith/graith/scripts/libghostty-native.sh verify`,
		"absolute workflow script helper":   `/tmp/graith/.github/workflows/scripts/publish.sh`,
		"subshell package graph check":      "( make package-graph-check )",
		"relative native helper invocation": "./scripts/libghostty-native.sh generate-dependency-unit",
	}

	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			if !p11RunLineExecutesRepositoryControlledCode(line) {
				t.Fatalf("line %q was not detected as repository-controlled code", line)
			}
		})
	}
}

func TestP11RepositoryControlledCommandDetectionAllowsDataOnlyGitLines(t *testing.T) {
	tests := map[string]string{
		"comment":            "# go test ./internal/protocol",
		"git show":           `parent_count="$(git show -s --format=%P "$GENERATED_SHA" | wc -w)"`,
		"git ls remote":      `remote_sha="$(git ls-remote origin "refs/heads/$HEAD_REF" | awk '{print $1}')"`,
		"word containing go": "if cargo test; then",
		"make as suffix":     "echo brawmake package-graph",
		"node as suffix":     "echo cannynode helper.js",
	}

	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			if p11RunLineExecutesRepositoryControlledCode(line) {
				t.Fatalf("line %q was incorrectly detected as repository-controlled code", line)
			}
		})
	}
}

func TestP11RegenWorkflowTrustSemanticsRejectsCredentialedLocalAction(t *testing.T) {
	workflow := p11ReadMutatedRegenWorkflow(
		t,
		"        uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8\n",
		"        uses: ./.github/actions/download-generated\n",
	)

	regen := p11WorkflowJob(t, workflow, "regen")
	if !p11JobRunsRepositoryControlledCode(regen) {
		t.Fatal("credentialed local action execution was not surfaced to semantic assertions")
	}
}

func TestP11RegenAuthCompatibilitySamplesBindCredentialsToPlan(t *testing.T) {
	repoRoot := p11RepoRoot()

	manifest, err := ReadManifest(filepath.Join(repoRoot, DefaultManifestPath))
	if err != nil {
		t.Fatal(err)
	}

	knownFiles, err := P11KnownFilesFromRepository(repoRoot, manifest, []string{"internal/protocol/messages.go"})
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		mutate func([]P11CompatibilitySample)
		want   string
	}{
		"missing capability": {
			mutate: func(samples []P11CompatibilitySample) {
				p11MutateCredentialExpectation(samples, "regen-push-boundary", func(expectation *P11CredentialExpectation) {
					expectation.Operation.Capability = ""
				})
			},
			want: "requires plan capability identity",
		},
		"missing plan trust tier binding": {
			mutate: func(samples []P11CompatibilitySample) {
				for index := range samples {
					if samples[index].ID == "regen-push-boundary" {
						samples[index].ExpectedTrustTier = ""
					}
				}
			},
			want: "requires explicit plan trust tier binding",
		},
		"wrong sample trust tier binding": {
			mutate: func(samples []P11CompatibilitySample) {
				for index := range samples {
					if samples[index].ID == "regen-push-boundary" {
						samples[index].ExpectedTrustTier = "same-repository-agent"
					}
				}
			},
			want: "trust tier = trusted-base, want same-repository-agent",
		},
		"missing credential trust tier binding": {
			mutate: func(samples []P11CompatibilitySample) {
				p11MutateCredentialExpectation(samples, "regen-push-boundary", func(expectation *P11CredentialExpectation) {
					expectation.Operation.TrustTier = ""
				})
			},
			want: "requires explicit credential trust tier binding",
		},
		"denied credential still requires plan binding": {
			mutate: func(samples []P11CompatibilitySample) {
				for index := range samples {
					if samples[index].ID == "regen-same-repository-agent" {
						samples[index].ExpectedTrustTier = ""
					}
				}
			},
			want: "requires explicit plan trust tier binding",
		},
		"denied credential requires exact error binding": {
			mutate: func(samples []P11CompatibilitySample) {
				p11MutateCredentialExpectation(samples, "regen-same-repository-agent", func(expectation *P11CredentialExpectation) {
					expectation.WantErrorSubstr = ""
				})
			},
			want: "requires an expected error binding",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			samples := p11RegenAuthSamples()
			test.mutate(samples)

			_, err := CompareP11CompatibilitySamples(manifest, knownFiles, samples, p11TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompareP11CompatibilitySamples() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestP11CredentialTrustAllowlistMatchesCredentialOperations(t *testing.T) {
	validPlanTiers := map[string]bool{
		"same-repository-agent": true,
		"trusted-base":          true,
	}

	for operation, policy := range credentialOperationPolicies {
		allowances, ok := p11CredentialTrustAllowlist[operation]
		if !ok {
			t.Fatalf("credential operation %s is missing from P11 plan-to-credential trust allowlist", operation)
		}

		if len(allowances) == 0 {
			t.Fatalf("credential operation %s has no P11 plan-to-credential trust allowances", operation)
		}

		seen := map[p11CredentialTrustAllowance]bool{}

		for _, allowance := range allowances {
			if allowance.PlanTrustTier == "" || allowance.CredentialTrustTier == "" {
				t.Fatalf("credential operation %s has incomplete allowance %#v", operation, allowance)
			}

			if !validPlanTiers[allowance.PlanTrustTier] {
				t.Fatalf("credential operation %s allows invalid plan trust tier %s", operation, allowance.PlanTrustTier)
			}

			if !containsString(policy.TrustTiers, allowance.CredentialTrustTier) {
				t.Fatalf("credential operation %s allows credential trust tier %s outside policy trust tiers %#v", operation, allowance.CredentialTrustTier, policy.TrustTiers)
			}

			if seen[allowance] {
				t.Fatalf("credential operation %s has duplicate P11 trust allowance %#v", operation, allowance)
			}

			seen[allowance] = true
		}
	}

	for operation := range p11CredentialTrustAllowlist {
		if _, ok := credentialOperationPolicies[operation]; !ok {
			t.Fatalf("P11 plan-to-credential trust allowlist references unknown credential operation %s", operation)
		}
	}
}

func TestP11CredentialTrustAllowlistRejectsCredentialTrustNotAllowedForPlan(t *testing.T) {
	repoRoot := p11RepoRoot()

	manifest, err := ReadManifest(filepath.Join(repoRoot, DefaultManifestPath))
	if err != nil {
		t.Fatal(err)
	}

	sample := P11CompatibilitySample{
		ID:                "docs-preview-trusted-base-credential-negative",
		HelperPath:        P11RegenAuthReplacementPath,
		Description:       "trusted-base plan must not borrow same-repository docs-preview write credentials",
		PlanOptions:       p11PlanOptionsWithChangedFiles(p11TrustedBaseEvent(), []string{"docs/design/2026-07-24-ci-north-star.md"}),
		ExpectedTrustTier: "trusted-base",
		CredentialExpectations: []P11CredentialExpectation{
			{
				Operation: CredentialOperation{
					Operation:  "docs-preview-write",
					TrustTier:  "same-repository-agent",
					Capability: "docs-preview",
					Token: SyntheticToken{
						Name:         "repository-write",
						TrustTier:    "same-repository-agent",
						Class:        syntheticRepositoryWriteToken,
						Scopes:       []string{"contents:write", "pull-requests:write"},
						AllowedRoots: []string{"screenshots"},
					},
					Target: "screenshots/braw.png",
				},
				Allowed: true,
			},
		},
	}

	knownFiles, err := P11KnownFilesFromRepository(repoRoot, manifest, sample.PlanOptions.ChangedFiles)
	if err != nil {
		t.Fatal(err)
	}

	_, err = CompareP11CompatibilitySamples(manifest, knownFiles, []P11CompatibilitySample{sample}, p11TestNow)
	if err == nil || !strings.Contains(err.Error(), "credential trust tier same-repository-agent is not allowed for plan trust tier trusted-base") {
		t.Fatalf("CompareP11CompatibilitySamples() error = %v, want explicit plan-to-credential trust allowlist rejection", err)
	}
}

func p11ReadMutatedRegenWorkflow(t *testing.T, needle, replacement string) P11WorkflowSummary {
	t.Helper()

	repoRoot := p11RepoRoot()
	path := filepath.Join(repoRoot, ".github/workflows/regen.yml")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	workflowSource := string(data)
	if !strings.Contains(workflowSource, needle) {
		t.Fatalf("regen workflow mutation point %q not found", needle)
	}

	mutated := strings.Replace(workflowSource, needle, replacement, 1)
	mutatedPath := filepath.Join(t.TempDir(), "regen.yml")

	// #nosec G703 -- mutatedPath is rooted in t.TempDir and not user-controlled.
	if err := os.WriteFile(mutatedPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	workflow, err := ReadP11WorkflowSummary(mutatedPath)
	if err != nil {
		t.Fatal(err)
	}

	return workflow
}

func p11RunGit(t *testing.T, repoRoot string, args ...string) {
	t.Helper()

	commandArgs := append([]string{"-C", repoRoot}, args...)
	// #nosec G204 -- test-only git invocation with explicit arguments and no shell.
	cmd := exec.Command("git", commandArgs...)
	cmd.Env = p11IsolatedGitEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(commandArgs, " "), err, output)
	}
}

func p11RepoRoot() string {
	return filepath.Join("..", "..")
}

func p11RegenAuthSamples() []P11CompatibilitySample {
	return []P11CompatibilitySample{
		{
			ID:                      "regen-same-repository-agent",
			HelperPath:              P11RegenAuthReplacementPath,
			Description:             "same-repository generated-file PR selects generated-metadata while soft regen jobs stay outside the required plan and maintainer credentials stay unavailable",
			PlanOptions:             p11PlanOptions(p11SameRepoEvent()),
			ExpectedTrustTier:       "same-repository-agent",
			ExpectedCapabilities:    []string{"generated-metadata"},
			ExpectedSupersetReasons: []string{"generated-input"},
			ExpectedManifestModes:   p11RegenModeExpectations(),
			ExpectedAbsentCoordinates: []string{
				"regen/prepare",
				"regen/regen",
				"regen/validate",
			},
			CredentialExpectations: []P11CredentialExpectation{
				{
					Operation: CredentialOperation{
						Operation:  "regeneration-push",
						TrustTier:  "same-repository-agent",
						Capability: "generated-metadata",
						Token: SyntheticToken{
							Name:         "release",
							TrustTier:    "same-repository-agent",
							Class:        syntheticMaintainerToken,
							Scopes:       []string{"contents:write"},
							AllowedRoots: []string{"generated"},
						},
						Target: "generated/braw.bundle",
					},
					Allowed:         false,
					BindToPlan:      true,
					WantErrorSubstr: "same-repository agent branches cannot obtain maintainer credentials",
				},
			},
		},
		{
			ID:                      "regen-fork-untrusted",
			HelperPath:              P11RegenAuthReplacementPath,
			Description:             "fork generated-file PR is fork-untrusted while same-repository workflow guards keep credentialed regen jobs unreachable",
			PlanOptions:             p11PlanOptions(p11ForkEvent()),
			ExpectedTrustTier:       "fork-untrusted",
			ExpectedCapabilities:    []string{"generated-metadata"},
			ExpectedSupersetReasons: []string{"generated-input"},
			ExpectedManifestModes:   p11RegenModeExpectations(),
			ExpectedAbsentCoordinates: []string{
				"regen/prepare",
				"regen/regen",
				"regen/validate",
			},
			CredentialExpectations: []P11CredentialExpectation{
				{
					Operation: CredentialOperation{
						Operation:  "regeneration-push",
						TrustTier:  "fork-untrusted",
						Capability: "generated-metadata",
						Token: SyntheticToken{
							Name:         "release",
							TrustTier:    "fork-untrusted",
							Class:        syntheticMaintainerToken,
							Scopes:       []string{"contents:write"},
							AllowedRoots: []string{"generated"},
						},
						Target: "generated/braw.bundle",
					},
					Allowed:         false,
					BindToPlan:      true,
					WantErrorSubstr: "fork pull requests may use only synthetic read tokens",
				},
			},
		},
		{
			ID:                      "regen-trusted-base",
			HelperPath:              P11RegenAuthReplacementPath,
			Description:             "trusted-base PR replay keeps soft regen modes tied to generated-metadata policy",
			PlanOptions:             p11PlanOptions(p11TrustedBaseEvent()),
			ExpectedTrustTier:       "trusted-base",
			ExpectedCapabilities:    []string{"generated-metadata"},
			ExpectedSupersetReasons: []string{"generated-input"},
			ExpectedManifestModes:   p11RegenModeExpectations(),
			ExpectedAbsentCoordinates: []string{
				"regen/prepare",
				"regen/regen",
				"regen/validate",
			},
		},
		{
			ID:                      "regen-push-boundary",
			HelperPath:              P11RegenAuthReplacementPath,
			Description:             "trusted-publication regeneration credential is valid only for generated metadata while workflow push semantics verify the generated commit",
			PlanOptions:             p11PlanOptions(p11TrustedBaseEvent()),
			ExpectedTrustTier:       "trusted-base",
			ExpectedCapabilities:    []string{"generated-metadata"},
			ExpectedSupersetReasons: []string{"generated-input"},
			ExpectedManifestModes:   p11RegenModeExpectations(),
			ExpectedAbsentCoordinates: []string{
				"regen/prepare",
				"regen/regen",
				"regen/validate",
			},
			CredentialExpectations: []P11CredentialExpectation{
				{
					Operation: CredentialOperation{
						Operation:  "regeneration-push",
						TrustTier:  "trusted-publication",
						Capability: "generated-metadata",
						Token: SyntheticToken{
							Name:         "release",
							TrustTier:    "trusted-publication",
							Class:        syntheticMaintainerToken,
							Scopes:       []string{"contents:write"},
							AllowedRoots: []string{"generated"},
						},
						Target: "generated/braw.bundle",
					},
					Allowed: true,
				},
			},
		},
	}
}

type p11NegativeCompatibilitySample struct {
	Sample              P11CompatibilitySample
	ExpectedErrorSubstr string
}

func p11RegenAuthNegativeSamples() []p11NegativeCompatibilitySample {
	return []p11NegativeCompatibilitySample{
		{
			Sample:              p11RegenAuthNonSupersetNegativeSample(),
			ExpectedErrorSubstr: "requires plan capability generated-metadata",
		},
	}
}

func p11RegenAuthNonSupersetNegativeSample() P11CompatibilitySample {
	return P11CompatibilitySample{
		ID:                "regen-non-superset-negative",
		HelperPath:        P11RegenAuthReplacementPath,
		Description:       "docs-only PR plan is not a safe superset and cannot bind regeneration credentials to absent generated-metadata capability",
		PlanOptions:       p11PlanOptionsWithChangedFiles(p11TrustedBaseEvent(), []string{"docs/design/2026-07-24-ci-north-star.md"}),
		ExpectedTrustTier: "trusted-base",
		CredentialExpectations: []P11CredentialExpectation{
			{
				Operation: CredentialOperation{
					Operation:  "regeneration-push",
					TrustTier:  "trusted-publication",
					Capability: "generated-metadata",
					Token: SyntheticToken{
						Name:         "release",
						TrustTier:    "trusted-publication",
						Class:        syntheticMaintainerToken,
						Scopes:       []string{"contents:write"},
						AllowedRoots: []string{"generated"},
					},
					Target: "generated/braw.bundle",
				},
				Allowed: true,
			},
		},
	}
}

func p11MutateCredentialExpectation(samples []P11CompatibilitySample, sampleID string, mutate func(*P11CredentialExpectation)) {
	for sampleIndex := range samples {
		if samples[sampleIndex].ID != sampleID {
			continue
		}

		for expectationIndex := range samples[sampleIndex].CredentialExpectations {
			mutate(&samples[sampleIndex].CredentialExpectations[expectationIndex])
		}
	}
}

func p11RegenModeExpectations() []P11ManifestModeExpectation {
	return []P11ManifestModeExpectation{
		p11RegenModeExpectation("legacy/regen/prepare", "regen/prepare"),
		p11RegenModeExpectation("legacy/regen/regen", "regen/regen"),
		p11RegenModeExpectation("legacy/regen/validate", "regen/validate"),
	}
}

func p11RegenModeExpectation(modeID, coordinateID string) P11ManifestModeExpectation {
	return P11ManifestModeExpectation{
		ID:           modeID,
		Capability:   "generated-metadata",
		Requiredness: "soft",
		TrustTiers:   []string{"same-repository-agent", "trusted-base"},
		Coordinates: []P11ManifestCoordinateExpectation{
			{ID: coordinateID, Requiredness: "soft"},
		},
	}
}

func p11PlanOptions(event EventInput) PlanOptions {
	return p11PlanOptionsWithChangedFiles(event, []string{"internal/protocol/messages.go"})
}

func p11PlanOptionsWithChangedFiles(event EventInput, changedFiles []string) PlanOptions {
	return PlanOptions{
		Event:          event,
		ChangedFiles:   append([]string(nil), changedFiles...),
		ExactFileList:  true,
		DetectorErrors: nil,
	}
}

func p11SameRepoEvent() EventInput {
	return EventInput{
		GitHubEvent:         "pull_request",
		Ref:                 "refs/pull/17/merge",
		BaseRef:             "main",
		HeadRef:             "braw-regen",
		BaseRepository:      DefaultRepository,
		HeadRepository:      DefaultRepository,
		Commit:              "1111111111111111111111111111111111111111",
		Tree:                "2222222222222222222222222222222222222222",
		SameRepositoryAgent: true,
	}
}

func p11ForkEvent() EventInput {
	event := p11SameRepoEvent()
	event.HeadRepository = "croft/graith"
	event.SameRepositoryAgent = false
	event.PullRequestFork = true

	return event
}

func p11TrustedBaseEvent() EventInput {
	event := p11SameRepoEvent()
	event.SameRepositoryAgent = false
	event.TrustedBase = true

	return event
}

func p11AssertTrustTier(t *testing.T, manifest Manifest, event EventInput, want string) {
	t.Helper()

	_, got, err := SelectEvent(manifest, event)
	if err != nil {
		t.Fatal(err)
	}

	if got != want {
		t.Fatalf("trust tier = %s, want %s", got, want)
	}
}

func p11HasSampleRequirement(contract P11JSHelperContract, id string) bool {
	for _, sample := range contract.CompatibilitySamples {
		if sample.ID == id {
			return true
		}
	}

	return false
}

func p11DocsDiffRequirementsWithout(requirements []P11CompatibilitySampleRequirement, id string) []P11CompatibilitySampleRequirement {
	return p11RequirementsWithout(requirements, id)
}

func p11DocsPreviewRequirementsWithout(requirements []P11CompatibilitySampleRequirement, id string) []P11CompatibilitySampleRequirement {
	return p11RequirementsWithout(requirements, id)
}

func p11RequirementsWithout(requirements []P11CompatibilitySampleRequirement, id string) []P11CompatibilitySampleRequirement {
	filtered := make([]P11CompatibilitySampleRequirement, 0, len(requirements))

	for _, requirement := range requirements {
		if requirement.ID != id {
			filtered = append(filtered, requirement)
		}
	}

	return filtered
}

func p11RetiredSurface(path string) cibaseline.Surface {
	return cibaseline.Surface{
		Path:        path,
		Owner:       "graith-maintainers",
		Kind:        "workflow-helper",
		GitMode:     "100644",
		SHA256:      strings.Repeat("0", 64),
		Contract:    "retired JS surface must not remain in closed-world inventory",
		Disposition: "grandfathered",
		Retirement:  "owned Go replacement has equivalent executable coverage",
	}
}

func p11ResignInventory(t *testing.T, inventory *cibaseline.Inventory) {
	t.Helper()

	inventory.Digest = ""

	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}

	digest := sha256.Sum256(data)
	inventory.Digest = hex.EncodeToString(digest[:])
}

func p11AssertSampleMatrixMatchesRequirements(t *testing.T, contract P11JSHelperContract, samples []P11CompatibilitySample) {
	t.Helper()

	requirements := map[string]bool{}

	for _, requirement := range contract.CompatibilitySamples {
		requirements[requirement.ID] = true
	}

	executed := map[string]bool{}

	for _, sample := range samples {
		if sample.HelperPath != contract.Path {
			t.Fatalf("sample %s helper path = %s, want %s", sample.ID, sample.HelperPath, contract.Path)
		}

		if !requirements[sample.ID] {
			t.Fatalf("sample %s is executed but not declared in %s", sample.ID, contract.Path)
		}

		executed[sample.ID] = true
	}

	for id := range requirements {
		if !executed[id] {
			t.Fatalf("compatibility sample %s is declared but not executed", id)
		}
	}
}

func p11WorkflowJobIDs(workflow P11WorkflowSummary) []string {
	ids := make([]string, 0, len(workflow.Jobs))
	for id := range workflow.Jobs {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	return ids
}

func p11WorkflowJob(t *testing.T, workflow P11WorkflowSummary, id string) P11WorkflowJob {
	t.Helper()

	job, ok := workflow.Jobs[id]
	if !ok {
		t.Fatalf("workflow job %s not found", id)
	}

	return job
}

func p11WorkflowStep(t *testing.T, job P11WorkflowJob, name string) P11WorkflowStep {
	t.Helper()

	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}

	t.Fatalf("step %q not found", name)

	return P11WorkflowStep{}
}

func p11AssertJobIf(t *testing.T, id string, job P11WorkflowJob, want string) {
	t.Helper()

	if got := p11NormalizeExpression(job.If); got != p11NormalizeExpression(want) {
		t.Fatalf("job %s if = %q, want exactly %q", id, job.If, want)
	}
}

func p11AssertStepNames(t *testing.T, id string, job P11WorkflowJob, want []string) {
	t.Helper()

	got := make([]string, 0, len(job.Steps))
	for _, step := range job.Steps {
		got = append(got, step.Name)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("job %s steps = %#v, want %#v", id, got, want)
	}
}

func p11AssertNoJobPermissionOverrides(t *testing.T, id string, job P11WorkflowJob) {
	t.Helper()

	if job.PermissionsExpression != "" {
		t.Fatalf("job %s must inherit top-level read-only permissions, got expression %q", id, job.PermissionsExpression)
	}

	if len(job.Permissions) != 0 {
		t.Fatalf("job %s must inherit top-level read-only permissions, got %#v", id, job.Permissions)
	}
}

func p11HasJobPermissionOverride(job P11WorkflowJob) bool {
	return job.PermissionsExpression != "" || len(job.Permissions) != 0
}

func p11NormalizeExpression(expression string) string {
	return strings.Join(strings.Fields(expression), " ")
}

func p11JobUsesAction(job P11WorkflowJob, action string) bool {
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, action+"@") {
			return true
		}
	}

	return false
}

func p11AssertJobUsesOnlyAllowedActions(t *testing.T, jobID string, job P11WorkflowJob, allowed map[string]string) {
	t.Helper()

	seen := map[string]bool{}

	for _, step := range job.Steps {
		if step.Uses == "" {
			continue
		}

		want, ok := allowed[step.Name]
		if !ok {
			t.Fatalf("job %s step %q uses unexpected action %q", jobID, step.Name, step.Uses)
		}

		if step.Uses != want {
			t.Fatalf("job %s step %q uses %q, want %q", jobID, step.Name, step.Uses, want)
		}

		seen[step.Name] = true
	}

	for name := range allowed {
		if !seen[name] {
			t.Fatalf("job %s missing allowed action step %q", jobID, name)
		}
	}
}

func p11JobRunsRepositoryControlledCode(job P11WorkflowJob) bool {
	for _, step := range job.Steps {
		if p11UsesRepositoryControlledAction(step.Uses) {
			return true
		}

		for _, line := range strings.Split(step.Run, "\n") {
			if p11RunLineExecutesRepositoryControlledCode(line) {
				return true
			}
		}
	}

	return false
}

func p11UsesRepositoryControlledAction(action string) bool {
	action = strings.TrimSpace(action)

	return strings.HasPrefix(action, "./") || strings.HasPrefix(action, "../") || strings.HasPrefix(action, ".github/")
}

func p11RunLineExecutesRepositoryControlledCode(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return false
	}

	return p11RepositoryControlledCommandPattern.MatchString(line) ||
		p11RepositoryControlledScriptPattern.MatchString(line)
}

func p11RunLineContainsAll(run string, needles ...string) bool {
	for _, line := range strings.Split(run, "\n") {
		matched := true

		for _, needle := range needles {
			if !strings.Contains(line, needle) {
				matched = false

				break
			}
		}

		if matched {
			return true
		}
	}

	return false
}

func p11WorkflowRunLineContainsAll(workflow P11WorkflowSummary, needles ...string) bool {
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if p11RunLineContainsAll(step.Run, needles...) {
				return true
			}
		}
	}

	return false
}

func p11WorkflowRunLineContainsReleaseTokenExpression(workflow P11WorkflowSummary, needles ...string) bool {
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			for _, line := range strings.Split(step.Run, "\n") {
				matched := p11ReleaseTokenExpression.MatchString(line)

				for _, needle := range needles {
					if !strings.Contains(line, needle) {
						matched = false

						break
					}
				}

				if matched {
					return true
				}
			}
		}
	}

	return false
}

func p11RunLineCountContaining(run string, needle string) int {
	count := 0

	for _, line := range strings.Split(run, "\n") {
		if strings.Contains(line, needle) {
			count++
		}
	}

	return count
}

func p11AssertRunLineOrder(t *testing.T, run string, needles []string) {
	t.Helper()

	lines := strings.Split(run, "\n")
	nextLine := 0

	for _, needle := range needles {
		found := false

		for index := nextLine; index < len(lines); index++ {
			if !strings.Contains(lines[index], needle) {
				continue
			}

			found = true
			nextLine = index + 1

			break
		}

		if !found {
			t.Fatalf("run block does not contain %q after line %d", needle, nextLine)
		}
	}
}

func p11JobCheckoutPersistsCredentials(job P11WorkflowJob) bool {
	for _, step := range job.Steps {
		if !strings.HasPrefix(step.Uses, "actions/checkout@") {
			continue
		}

		if step.With["persist-credentials"] != "false" {
			return true
		}
	}

	return false
}

func p11JobReferences(job P11WorkflowJob, needle string) bool {
	if strings.Contains(job.If, needle) ||
		strings.Contains(job.PermissionsExpression, needle) ||
		p11MapReferences(job.Permissions, needle) ||
		p11MapReferences(job.Env, needle) {
		return true
	}

	for _, step := range job.Steps {
		if strings.Contains(step.If, needle) ||
			strings.Contains(step.Uses, needle) ||
			strings.Contains(step.Run, needle) {
			return true
		}

		if p11MapReferences(step.Env, needle) || p11MapReferences(step.With, needle) {
			return true
		}
	}

	return false
}

func p11WorkflowHasGitHubTokenExpression(workflow P11WorkflowSummary) bool {
	if p11ScalarsMatch(workflow.Scalars, p11GitHubTokenExpression) {
		return true
	}

	if p11GitHubTokenExpression.MatchString(workflow.PermissionsExpression) ||
		p11MapHasGitHubTokenExpression(workflow.Permissions) ||
		p11MapHasGitHubTokenExpression(workflow.Env) {
		return true
	}

	for _, job := range workflow.Jobs {
		if p11JobHasGitHubTokenExpression(job) {
			return true
		}
	}

	return false
}

func p11JobHasGitHubTokenExpression(job P11WorkflowJob) bool {
	if p11GitHubTokenExpression.MatchString(job.If) ||
		p11GitHubTokenExpression.MatchString(job.PermissionsExpression) ||
		p11MapHasGitHubTokenExpression(job.Permissions) ||
		p11MapHasGitHubTokenExpression(job.Env) {
		return true
	}

	for _, step := range job.Steps {
		if p11GitHubTokenExpression.MatchString(step.If) ||
			p11GitHubTokenExpression.MatchString(step.Uses) ||
			p11GitHubTokenExpression.MatchString(step.Run) ||
			p11MapHasGitHubTokenExpression(step.Env) ||
			p11MapHasGitHubTokenExpression(step.With) {
			return true
		}
	}

	return false
}

func p11JobHasReleaseTokenExpression(job P11WorkflowJob) bool {
	if p11ReleaseTokenExpression.MatchString(job.If) || p11MapHasReleaseTokenExpression(job.Env) {
		return true
	}

	for _, step := range job.Steps {
		if p11ReleaseTokenExpression.MatchString(step.If) ||
			p11ReleaseTokenExpression.MatchString(step.Run) ||
			p11MapHasReleaseTokenExpression(step.Env) ||
			p11MapHasReleaseTokenExpression(step.With) {
			return true
		}
	}

	return false
}

func p11WorkflowReferences(workflow P11WorkflowSummary, needle string) bool {
	if p11ScalarsContain(workflow.Scalars, needle) {
		return true
	}

	if strings.Contains(workflow.PermissionsExpression, needle) ||
		p11MapReferences(workflow.Permissions, needle) ||
		p11MapReferences(workflow.Env, needle) {
		return true
	}

	for _, job := range workflow.Jobs {
		if strings.Contains(job.If, needle) || p11JobReferences(job, needle) {
			return true
		}
	}

	return false
}

func p11MapHasGitHubTokenExpression(values map[string]string) bool {
	for key, value := range values {
		if p11GitHubTokenExpression.MatchString(key) || p11GitHubTokenExpression.MatchString(value) {
			return true
		}
	}

	return false
}

func p11MapHasReleaseTokenExpression(values map[string]string) bool {
	for _, value := range values {
		if p11ReleaseTokenExpression.MatchString(value) {
			return true
		}
	}

	return false
}

func p11MapReferences(values map[string]string, needle string) bool {
	for key, value := range values {
		if strings.Contains(key, needle) || strings.Contains(value, needle) {
			return true
		}
	}

	return false
}

func p11WorkflowReleaseTokenExpressionCount(workflow P11WorkflowSummary) int {
	return p11ScalarExpressionCount(workflow.Scalars, p11ReleaseTokenExpression)
}

func p11ScalarsMatch(values []string, pattern *regexp.Regexp) bool {
	for _, value := range values {
		if pattern.MatchString(value) {
			return true
		}
	}

	return false
}

func p11ScalarsContain(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}

	return false
}

func p11ScalarExpressionCount(values []string, pattern *regexp.Regexp) int {
	count := 0
	for _, value := range values {
		count += len(pattern.FindAllString(value, -1))
	}

	return count
}

func p11HasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}

	return false
}
