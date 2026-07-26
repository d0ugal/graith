package cipolicy

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var p11TestNow = p2TestNow

const p11SameRepositoryGuard = "github.event.pull_request.head.repo.full_name == github.repository"

var (
	p11ReleaseTokenExpression = regexp.MustCompile(`\$\{\{\s*secrets\s*(?:\.\s*RELEASE_TOKEN|\[\s*['"]RELEASE_TOKEN['"]\s*\])\s*\}\}`)
	p11GitHubTokenExpression  = regexp.MustCompile(`\$\{\{\s*github\s*(?:\.\s*token|\[\s*['"]token['"]\s*\])\s*\}\}`)
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

	regen := p11ContractByPath(t, contracts, P11NextTrancheHelper)
	if regen.Disposition != "port" {
		t.Fatalf("%s disposition = %s, want port", regen.Path, regen.Disposition)
	}

	for _, want := range []string{
		"regen-same-repository-agent",
		"regen-fork-untrusted",
		"regen-trusted-base",
		"regen-push-boundary",
	} {
		if !p11HasSampleRequirement(regen, want) {
			t.Fatalf("%s is missing compatibility sample %s", regen.Path, want)
		}
	}

	p11AssertSampleMatrixMatchesRequirements(t, regen, p11RegenAuthSamples())

	for _, path := range []string{
		".github/workflows/scripts/package.json",
		".github/workflows/scripts/package-lock.json",
		".github/workflows/scripts/docs-diff.js",
	} {
		contract := p11ContractByPath(t, contracts, path)
		if contract.Disposition != "retain" {
			t.Fatalf("%s disposition = %s, want retain", path, contract.Disposition)
		}

		if !strings.Contains(strings.Join(append(contract.PolicyInputs, contract.DeletionCriterion), " "), "pngjs") {
			t.Fatalf("%s does not document the pngjs retained exception", path)
		}
	}
}

func TestP11RegenAuthCompatibilitySamplesUsePolicyFixture(t *testing.T) {
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
		if comparison.PlanDigest == "" || comparison.FanInStatus != "passed" || comparison.AcceptedCount == 0 {
			t.Fatalf("comparison %#v did not exercise a successful Go policy/result fixture", comparison)
		}

		if !comparison.Superset || !p11HasString(comparison.SupersetReasons, "generated-input") {
			t.Fatalf("comparison %#v did not bind generated-input superset semantics", comparison)
		}
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
		p11WorkflowRunContains(workflow, "gh workflow run") ||
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
	replacement := "\n          set -euo pipefail\n          node .github/workflows/scripts/regen-auth.test.js\n          remote_sha=\"$(git ls-remote origin \"refs/heads/$HEAD_REF\" | awk '{print $1}')\"\n"
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

func TestP11RegenAuthCompatibilitySamplesBindAllowedCredentialsToPlan(t *testing.T) {
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
		mutate func(*P11CredentialExpectation)
		want   string
	}{
		"missing capability": {
			mutate: func(expectation *P11CredentialExpectation) {
				expectation.Operation.Capability = ""
			},
			want: "requires plan capability identity",
		},
		"missing plan trust tier binding": {
			mutate: func(expectation *P11CredentialExpectation) {
				expectation.PlanTrustTier = ""
			},
			want: "requires explicit plan trust tier binding",
		},
		"missing credential trust tier binding": {
			mutate: func(expectation *P11CredentialExpectation) {
				expectation.CredentialTrustTier = ""
			},
			want: "requires explicit credential trust tier binding",
		},
		"wrong plan trust tier binding": {
			mutate: func(expectation *P11CredentialExpectation) {
				expectation.PlanTrustTier = "same-repository-agent"
			},
			want: "plan trust tier = trusted-base, want same-repository-agent",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			samples := p11RegenAuthSamples()
			for index := range samples {
				if samples[index].ID != "regen-push-boundary" {
					continue
				}

				test.mutate(&samples[index].CredentialExpectations[0])
			}

			_, err := CompareP11CompatibilitySamples(manifest, knownFiles, samples, p11TestNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompareP11CompatibilitySamples() error = %v, want %q", err, test.want)
			}
		})
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

func p11RepoRoot() string {
	return filepath.Join("..", "..")
}

func p11RegenAuthSamples() []P11CompatibilitySample {
	return []P11CompatibilitySample{
		{
			ID:                      "regen-same-repository-agent",
			HelperPath:              P11NextTrancheHelper,
			Description:             "same-repository generated-file PR selects generated-metadata while soft regen jobs stay outside required fan-in and maintainer credentials stay unavailable",
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
					WantErrorSubstr: "same-repository agent branches cannot obtain maintainer credentials",
				},
			},
		},
		{
			ID:                      "regen-fork-untrusted",
			HelperPath:              P11NextTrancheHelper,
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
		},
		{
			ID:                      "regen-trusted-base",
			HelperPath:              P11NextTrancheHelper,
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
			HelperPath:              P11NextTrancheHelper,
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
					Allowed:             true,
					PlanTrustTier:       "trusted-base",
					CredentialTrustTier: "trusted-publication",
				},
			},
		},
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
	return PlanOptions{
		Event:          event,
		ChangedFiles:   []string{"internal/protocol/messages.go"},
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

func p11ContractByPath(t *testing.T, contracts []P11JSHelperContract, path string) P11JSHelperContract {
	t.Helper()

	for _, contract := range contracts {
		if contract.Path == path {
			return contract
		}
	}

	t.Fatalf("contract for %s not found", path)

	return P11JSHelperContract{}
}

func p11HasSampleRequirement(contract P11JSHelperContract, id string) bool {
	for _, sample := range contract.CompatibilitySamples {
		if sample.ID == id {
			return true
		}
	}

	return false
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

func p11JobRunContains(job P11WorkflowJob, needles ...string) bool {
	for _, step := range job.Steps {
		for _, needle := range needles {
			if strings.Contains(step.Run, needle) {
				return true
			}
		}
	}

	return false
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

	for _, prefix := range []string{
		"go ",
		"make ",
		"node ",
		"npm ",
		"npx ",
		"pnpm ",
		"python ",
		"python3 ",
		"sh ",
		"bash ",
		"./",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return strings.Contains(line, ".github/workflows/scripts/") ||
		strings.Contains(line, "scripts/libghostty-native.sh")
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

func p11WorkflowRunContains(workflow P11WorkflowSummary, needle string) bool {
	for _, job := range workflow.Jobs {
		if p11JobRunContains(job, needle) {
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
	count := p11MapReleaseTokenExpressionCount(workflow.Permissions)
	count += len(p11ReleaseTokenExpression.FindAllString(workflow.PermissionsExpression, -1))
	count += p11MapReleaseTokenExpressionCount(workflow.Env)

	for _, job := range workflow.Jobs {
		count += len(p11ReleaseTokenExpression.FindAllString(job.If, -1))
		count += p11MapReleaseTokenExpressionCount(job.Permissions)
		count += len(p11ReleaseTokenExpression.FindAllString(job.PermissionsExpression, -1))
		count += p11MapReleaseTokenExpressionCount(job.Env)

		for _, step := range job.Steps {
			count += len(p11ReleaseTokenExpression.FindAllString(step.If, -1))
			count += len(p11ReleaseTokenExpression.FindAllString(step.Uses, -1))
			count += len(p11ReleaseTokenExpression.FindAllString(step.Run, -1))
			count += p11MapReleaseTokenExpressionCount(step.Env)
			count += p11MapReleaseTokenExpressionCount(step.With)
		}
	}

	return count
}

func p11MapReleaseTokenExpressionCount(values map[string]string) int {
	count := 0

	for _, value := range values {
		count += len(p11ReleaseTokenExpression.FindAllString(value, -1))
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
