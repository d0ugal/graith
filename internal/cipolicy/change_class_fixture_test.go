package cipolicy

import (
	"slices"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/cibaseline"
)

type deterministicChangeClassFixture struct {
	files                    []string
	event                    EventInput
	detectorErrors           []string
	wantTrustTier            string
	wantDetectedCapabilities []string
	wantCapabilities         []string
	wantSupersetReasons      []string
	wantPolicyModes          []deterministicPolicyModeCheck
	wantCredentialChecks     []deterministicCredentialCheck
}

type deterministicPolicyModeCheck struct {
	mode     string
	evidence string
}

func detectedPolicyMode(mode string) deterministicPolicyModeCheck {
	return deterministicPolicyModeCheck{mode: mode, evidence: "detected-capability"}
}

func generatedInputPolicyMode(mode string) deterministicPolicyModeCheck {
	return deterministicPolicyModeCheck{mode: mode, evidence: "generated-input"}
}

func releaseMetadataPolicyMode(mode string) deterministicPolicyModeCheck {
	return deterministicPolicyModeCheck{mode: mode, evidence: "release-metadata"}
}

type deterministicCredentialCheck struct {
	name          string
	operation     CredentialOperation
	bindToPlan    bool
	wantError     string
	wantPlanError string
}

func TestDeterministicChangeClassFixtures(t *testing.T) {
	manifest := loadManifest(t)
	inventory := loadInventory(t)

	assertAuthoritativeRequiredContexts(t, inventory)
	assertCIMacOSFailSafeJobs(t, inventory)

	tests := map[string]deterministicChangeClassFixture{
		"docs-only": {
			files:                    []string{"website/content/docs/contributing/ci-policy.md"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{"docs-preview", "docs-publication"},
			wantCapabilities:         pullRequestCapabilityFloor("docs-preview", "docs-publication"),
			wantSupersetReasons:      []string{},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/docs-preview/preview"),
				detectedPolicyMode("legacy/docs-preview/cleanup"),
			},
			wantCredentialChecks: []deterministicCredentialCheck{
				{
					name:       "docs preview same-repository write is explicitly scoped",
					operation:  docsPreviewWriteOperation("same-repository-agent", syntheticRepositoryWriteToken),
					bindToPlan: true,
				},
			},
		},
		"fork-pr": {
			files: []string{
				"website/content/docs/contributing/ci-policy.md",
				"internal/protocol/messages.go",
				".release-please-manifest.json",
			},
			event:         planEvent(forkPullRequestEvent),
			wantTrustTier: "fork-untrusted",
			wantDetectedCapabilities: []string{
				"docs-preview",
				"docs-publication",
			},
			wantCapabilities:    allManifestCapabilities(),
			wantSupersetReasons: []string{"generated-input", "release-metadata"},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/docs-preview/preview"),
				releaseMetadataPolicyMode("legacy/goreleaser/build-linux"),
			},
			wantCredentialChecks: []deterministicCredentialCheck{
				{
					name:      "fork docs-preview write is unavailable",
					operation: docsPreviewWriteOperation("fork-untrusted", syntheticRepositoryWriteToken),
					wantError: "fork pull requests may use only synthetic read tokens",
				},
				{
					name:      "fork regen mutation is unavailable",
					operation: regenerationPushOperation("fork-untrusted"),
					wantError: "fork pull requests may use only synthetic read tokens",
				},
				{
					name:      "fork release publication is unavailable",
					operation: stableReleasePublishOperation("fork-untrusted"),
					wantError: "fork pull requests may use only synthetic read tokens",
				},
			},
		},
		"generated-metadata": {
			files:                    []string{"internal/protocol/messages.go"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{},
			wantCapabilities:         allManifestCapabilities(),
			wantSupersetReasons:      []string{"generated-input"},
			wantPolicyModes: []deterministicPolicyModeCheck{
				generatedInputPolicyMode("legacy/regen/prepare"),
				generatedInputPolicyMode("legacy/regen/regen"),
				generatedInputPolicyMode("legacy/regen/validate"),
			},
		},
		"go-only": {
			files:                    []string{"internal/client/passthrough.go"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{"go-core"},
			wantCapabilities:         pullRequestCapabilityFloor(),
			wantSupersetReasons:      []string{},
		},
		"gui-only": {
			files:                    []string{"gui/macos/Sources/GraithGUI/ContentView.swift"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{"gui"},
			wantCapabilities:         pullRequestCapabilityFloor("gui"),
			wantSupersetReasons:      []string{},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/gui-ci/build"),
			},
		},
		"libghostty-runtime": {
			files:                    []string{"internal/libghosttydeps/lock.go"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{"go-core", "native"},
			wantCapabilities:         pullRequestCapabilityFloor(),
			wantSupersetReasons:      []string{},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/libghostty-native/linux-adapter"),
				detectedPolicyMode("legacy/libghostty-native/apple-adapter"),
				detectedPolicyMode("legacy/libghostty-native/native-gate"),
			},
		},
		"release-path": {
			files:                    []string{".release-please-manifest.json"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{},
			wantCapabilities:         allManifestCapabilities(),
			wantSupersetReasons:      []string{"release-metadata"},
			wantPolicyModes: []deterministicPolicyModeCheck{
				releaseMetadataPolicyMode("legacy/goreleaser/build-linux"),
				releaseMetadataPolicyMode("legacy/goreleaser/assemble-stable"),
				releaseMetadataPolicyMode("legacy/dev-release/build-linux"),
				releaseMetadataPolicyMode("legacy/dev-release/assemble-dev"),
			},
			wantCredentialChecks: []deterministicCredentialCheck{
				{
					name:          "same-repository release-shaped PR cannot bind publication credentials",
					operation:     stableReleasePublishOperation("trusted-publication"),
					bindToPlan:    true,
					wantPlanError: "credential trust tier trusted-publication is not allowed for plan trust tier same-repository-agent",
				},
			},
		},
		"same-repository-mutation": {
			files: []string{
				"website/content/docs/contributing/ci-policy.md",
				"internal/protocol/messages.go",
			},
			wantTrustTier: "same-repository-agent",
			wantDetectedCapabilities: []string{
				"docs-preview",
				"docs-publication",
			},
			wantCapabilities:    allManifestCapabilities(),
			wantSupersetReasons: []string{"generated-input"},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/docs-preview/preview"),
				generatedInputPolicyMode("legacy/regen/regen"),
				generatedInputPolicyMode("legacy/regen/validate"),
			},
			wantCredentialChecks: []deterministicCredentialCheck{
				{
					name:       "same-repository docs-preview write guard is explicit",
					operation:  docsPreviewWriteOperation("same-repository-agent", syntheticRepositoryWriteToken),
					bindToPlan: true,
				},
				{
					name:      "same-repository regen cannot borrow maintainer credentials",
					operation: regenerationPushOperation("same-repository-agent"),
					wantError: "same-repository agent branches cannot obtain maintainer credentials",
				},
			},
		},
		"sandbox": {
			files:                    []string{"internal/sandbox/nono.go"},
			detectorErrors:           []string{"dreich detector failure"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{"go-core", "sandbox"},
			wantCapabilities:         allManifestCapabilities(),
			wantSupersetReasons:      []string{"detector-error"},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/sandbox/linux-nono"),
				detectedPolicyMode("legacy/sandbox/macos-safehouse"),
			},
		},
		"workflow-script": {
			files: []string{
				"scripts/libghostty-native.sh",
				"internal/cipolicy/p11_js_surface.go",
			},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{"workflow-policy"},
			wantCapabilities:         allManifestCapabilities(),
			wantSupersetReasons:      []string{"ci-policy-change"},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/workflow-lint/actionlint"),
				detectedPolicyMode("legacy/workflow-lint/scripts"),
				detectedPolicyMode("legacy/workflow-lint/shellcheck"),
				detectedPolicyMode("legacy/workflow-lint/zizmor"),
			},
		},
		"workflow-lint-config": {
			files:                    []string{".github/actionlint.yaml"},
			wantTrustTier:            "same-repository-agent",
			wantDetectedCapabilities: []string{"workflow-policy"},
			wantCapabilities:         pullRequestCapabilityFloor("workflow-policy"),
			wantSupersetReasons:      []string{},
			wantPolicyModes: []deterministicPolicyModeCheck{
				detectedPolicyMode("legacy/workflow-lint/actionlint"),
				detectedPolicyMode("legacy/workflow-lint/scripts"),
				detectedPolicyMode("legacy/workflow-lint/shellcheck"),
				detectedPolicyMode("legacy/workflow-lint/zizmor"),
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			event := test.event
			if event.GitHubEvent == "" {
				event = planEvent(nil)
			}

			plan := buildTestPlan(t, manifest, event, test.files, test.detectorErrors, true)

			if plan.TrustTier != test.wantTrustTier {
				t.Fatalf("trust tier = %s, want %s", plan.TrustTier, test.wantTrustTier)
			}

			assertStringsEqual(t, "detected capabilities", plan.DetectedCapabilities, test.wantDetectedCapabilities)
			assertStringsEqual(t, "detector errors", plan.DetectorErrors, test.detectorErrors)
			assertStringsEqual(t, "superset reasons", plan.SupersetReasons, test.wantSupersetReasons)
			assertRequiredModesEqual(t, plan.RequiredModes)
			assertStringsEqual(t, "capabilities", plan.Capabilities, test.wantCapabilities)
			assertPolicyModesVisible(t, manifest, plan, test.wantPolicyModes)
			assertCredentialChecks(t, plan, test.wantCredentialChecks)
		})
	}
}

func assertAuthoritativeRequiredContexts(t *testing.T, inventory cibaseline.Inventory) {
	t.Helper()

	assertStringsEqual(t, "required contexts", inventory.RequiredContexts, []string{
		"Conventional commits",
		"Lint",
		"Linux (nono / Landlock)",
		"Native backend gate",
		"Test (macos-latest)",
		"Test (ubuntu-latest)",
		"macOS (safehouse / Seatbelt)",
	})
}

func assertCIMacOSFailSafeJobs(t *testing.T, inventory cibaseline.Inventory) {
	t.Helper()

	const wantCondition = "!cancelled() && (needs.changes.result != 'success' || needs.changes.outputs.macos == 'true')"

	for _, jobID := range []string{"integration-macos", "test-macos"} {
		job := findInventoryJob(t, inventory, "ci", jobID)
		if job.Condition != wantCondition {
			t.Fatalf("ci/%s condition = %q, want %q", jobID, job.Condition, wantCondition)
		}
	}
}

func findInventoryJob(t *testing.T, inventory cibaseline.Inventory, workflowID, jobID string) cibaseline.Job {
	t.Helper()

	for _, workflow := range inventory.Workflows {
		if workflow.ID != workflowID {
			continue
		}

		for _, job := range workflow.Jobs {
			if job.ID == jobID {
				return job
			}
		}
	}

	t.Fatalf("missing inventory job %s/%s", workflowID, jobID)

	return cibaseline.Job{}
}

func authoritativeRequiredModes() []string {
	return []string{
		"legacy/ci/lint",
		"legacy/ci/test",
		"legacy/ci/test-macos",
		"legacy/commits/commitsar",
		"legacy/libghostty-native/native-gate",
		"legacy/sandbox/linux-nono",
		"legacy/sandbox/macos-safehouse",
	}
}

func assertRequiredModesEqual(t *testing.T, got []string) {
	t.Helper()

	assertStringsEqual(t, "required modes", got, authoritativeRequiredModes())
}

func pullRequestCapabilityFloor(extra ...string) []string {
	capabilities := []string{"commit-policy", "go-core", "native", "sandbox"}
	capabilities = append(capabilities, extra...)

	return sortedStrings(capabilities)
}

func allManifestCapabilities() []string {
	return []string{
		"commit-policy",
		"coverage",
		"dev-release",
		"docs-preview",
		"docs-publication",
		"generated-metadata",
		"go-core",
		"gui",
		"native",
		"native-publication",
		"release-automation",
		"sandbox",
		"security-codeql",
		"security-dependency-review",
		"security-scorecard",
		"security-secret-scan",
		"stable-release",
		"workflow-policy",
	}
}

func assertStringsEqual(t *testing.T, label string, got, want []string) {
	t.Helper()

	got = sortedStrings(got)
	want = sortedStrings(want)

	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want exactly %v", label, got, want)
	}
}

func assertPolicyModesVisible(t *testing.T, manifest Manifest, plan RunPlan, checks []deterministicPolicyModeCheck) {
	t.Helper()

	for _, check := range checks {
		mode := findMode(t, manifest, check.mode)

		if !slices.Contains(plan.Capabilities, mode.Capability) {
			t.Fatalf("capabilities = %v, want capability %s for policy mode %s", plan.Capabilities, mode.Capability, check.mode)
		}

		if !modeAppliesToEvent(mode, plan.Event.Event) {
			t.Fatalf("policy mode %s does not apply to event %s", check.mode, plan.Event.Event)
		}

		if !slices.Contains(mode.TrustTiers, plan.TrustTier) {
			t.Fatalf("policy mode %s trust tiers = %v, want %s", check.mode, mode.TrustTiers, plan.TrustTier)
		}

		assertPolicyModeEvidence(t, plan, mode, check)

		if mode.Requiredness == "required" {
			if !slices.Contains(plan.RequiredModes, check.mode) {
				t.Fatalf("required modes = %v, want required policy mode %s", plan.RequiredModes, check.mode)
			}

			continue
		}

		if slices.Contains(plan.RequiredModes, check.mode) {
			t.Fatalf("required modes = %v, want soft policy mode %s not newly required", plan.RequiredModes, check.mode)
		}
	}
}

func assertPolicyModeEvidence(t *testing.T, plan RunPlan, mode Mode, check deterministicPolicyModeCheck) {
	t.Helper()

	switch check.evidence {
	case "detected-capability":
		if !slices.Contains(plan.DetectedCapabilities, mode.Capability) {
			t.Fatalf("detected capabilities = %v, want %s for policy mode %s", plan.DetectedCapabilities, mode.Capability, check.mode)
		}

	case "generated-input":
		if mode.Capability != "generated-metadata" {
			t.Fatalf("policy mode %s capability = %s, want generated-metadata for generated-input evidence", check.mode, mode.Capability)
		}

		if !slices.Contains(plan.SupersetReasons, "generated-input") {
			t.Fatalf("superset reasons = %v, want generated-input for policy mode %s", plan.SupersetReasons, check.mode)
		}

	case "release-metadata":
		if mode.Capability != "stable-release" && mode.Capability != "dev-release" {
			t.Fatalf("policy mode %s capability = %s, want stable-release or dev-release for release-metadata evidence", check.mode, mode.Capability)
		}

		if !slices.Contains(plan.SupersetReasons, "release-metadata") {
			t.Fatalf("superset reasons = %v, want release-metadata for policy mode %s", plan.SupersetReasons, check.mode)
		}

	default:
		t.Fatalf("policy mode %s has unknown evidence source %q", check.mode, check.evidence)
	}
}

func assertCredentialChecks(t *testing.T, plan RunPlan, checks []deterministicCredentialCheck) {
	t.Helper()

	for _, check := range checks {
		t.Run("credential "+check.name, func(t *testing.T) {
			err := ValidateCredentialOperation(check.operation)
			if check.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), check.wantError) {
					t.Fatalf("ValidateCredentialOperation() error = %v, want %q", err, check.wantError)
				}

				return
			}

			if err != nil {
				t.Fatalf("ValidateCredentialOperation() error = %v", err)
			}

			if !check.bindToPlan {
				return
			}

			policy := credentialOperationPolicies[check.operation.Operation]

			planErr := validateCredentialOperationPlanBinding(check.operation, policy, plan)
			if planErr == nil {
				planErr = validateP11CredentialTrustAllowance(check.name, P11CredentialExpectation{
					Operation: check.operation,
					Allowed:   true,
				}, plan)
			}

			if check.wantPlanError != "" {
				if planErr == nil || !strings.Contains(planErr.Error(), check.wantPlanError) {
					t.Fatalf("plan credential binding error = %v, want %q", planErr, check.wantPlanError)
				}

				return
			}

			if planErr != nil {
				t.Fatalf("plan credential binding error = %v", planErr)
			}
		})
	}
}

func forkPullRequestEvent(event *EventInput) {
	event.HeadRepository = "croft/graith"
	event.SameRepositoryAgent = false
	event.PullRequestFork = true
}

func docsPreviewWriteOperation(trustTier, tokenClass string) CredentialOperation {
	return CredentialOperation{
		Operation:  "docs-preview-write",
		TrustTier:  trustTier,
		Capability: "docs-preview",
		Token: SyntheticToken{
			Name:         "screenshots",
			TrustTier:    trustTier,
			Class:        tokenClass,
			Scopes:       []string{"contents:write", "pull-requests:write"},
			AllowedRoots: []string{"screenshots"},
		},
		Target: "screenshots/pr-17/index.png",
	}
}

func regenerationPushOperation(trustTier string) CredentialOperation {
	return CredentialOperation{
		Operation:  "regeneration-push",
		TrustTier:  trustTier,
		Capability: "generated-metadata",
		Token: SyntheticToken{
			Name:         "release",
			TrustTier:    trustTier,
			Class:        syntheticMaintainerToken,
			Scopes:       []string{"contents:write"},
			AllowedRoots: []string{"generated"},
		},
		Target: "generated/braw.bundle",
	}
}

func stableReleasePublishOperation(trustTier string) CredentialOperation {
	return CredentialOperation{
		Operation:  "stable-release-publish",
		TrustTier:  trustTier,
		Capability: "stable-release",
		Token: SyntheticToken{
			Name:         "release",
			TrustTier:    trustTier,
			Class:        syntheticMaintainerToken,
			Scopes:       []string{"contents:write"},
			AllowedRoots: []string{"dist/stable-release"},
		},
		Target: "dist/stable-release/graith.tar.gz",
	}
}
