package cipolicy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

var p2TestNow = p2StableTestTime()

func p2StableTestTime() time.Time {
	return time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
}

func TestPlanClassifiesEventTrustBoundaries(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		event EventInput
		want  string
	}{
		"fork pull request": {
			event: planEvent(func(event *EventInput) {
				event.HeadRepository = "canny/graith"
				event.PullRequestFork = true
				event.SameRepositoryAgent = false
			}),
			want: "fork-untrusted",
		},
		"same repository agent pull request": {
			event: planEvent(func(event *EventInput) {
				event.SameRepositoryAgent = true
			}),
			want: "same-repository-agent",
		},
		"trusted base pull request": {
			event: planEvent(func(event *EventInput) {
				event.SameRepositoryAgent = false
				event.TrustedBase = true
			}),
			want: "trusted-base",
		},
		"publication push": {
			event: pushEvent(func(event *EventInput) {
				event.Publication = true
			}),
			want: "trusted-publication",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			selection, got, err := SelectEvent(manifest, test.event)
			if err != nil {
				t.Fatal(err)
			}

			if got != test.want {
				t.Fatalf("trust tier = %s, want %s", got, test.want)
			}

			if selection.Event == "" || selection.GitHubEvent != test.event.GitHubEvent {
				t.Fatalf("event selection = %#v", selection)
			}
		})
	}
}

func TestPlanIntersectsModeTrustWithSelectedEvent(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, pushEvent(nil), []string{"internal/daemon/session.go"}, nil, true)

	for _, job := range plan.Jobs {
		if job.TrustTier != "trusted-base" {
			t.Fatalf("job trust tier = %s, want trusted-base", job.TrustTier)
		}
	}

	edited := plan
	edited.TrustTier = "fork-untrusted"
	signPlan(t, &edited)

	if err := edited.ValidateAt(manifest, p2TestNow); err == nil ||
		!strings.Contains(err.Error(), "not authorized for event push-main") {
		t.Fatalf("ValidateAt() error = %v, want event trust rejection", err)
	}
}

func TestPlanValidationRejectsTamperedCapabilityInputs(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		edit    func(*RunPlan)
		wantErr string
	}{
		"safe superset cannot keep narrow capabilities": {
			edit: func(plan *RunPlan) {
				plan.ExactFileList = false
				plan.ChangedFilesDigest = ""
				plan.Superset = true
				plan.SupersetReasons = []string{"file-list-unknown"}
			},
			wantErr: "safe-superset plan must select every capability",
		},
		"detected capability cannot be omitted": {
			edit: func(plan *RunPlan) {
				plan.Capabilities = []string{"commit-policy"}
			},
			wantErr: "plan omits detected capability go-core",
		},
		"universal pull request capability cannot be omitted": {
			edit: func(plan *RunPlan) {
				plan.DetectedCapabilities = []string{"workflow-policy"}
				plan.Capabilities = []string{"go-core", "workflow-policy"}
			},
			wantErr: "plan omits universal capability commit-policy",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
			test.edit(&plan)
			signPlan(t, &plan)

			if err := plan.ValidateAt(manifest, p2TestNow); err == nil ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateAt() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestPlanValidationRejectsTamperedTrustTier(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(func(event *EventInput) {
		event.HeadRepository = "canny/graith"
		event.PullRequestFork = true
		event.SameRepositoryAgent = false
		event.TrustedBase = false
	}), []string{"internal/daemon/session.go"}, nil, true)

	plan.TrustTier = "trusted-base"
	signPlan(t, &plan)

	if err := plan.ValidateAt(manifest, p2TestNow); err == nil ||
		!strings.Contains(err.Error(), "derives event pull-request trust tier fork-untrusted") {
		t.Fatalf("ValidateAt() error = %v, want derived trust-tier rejection", err)
	}
}

func TestPlanSafeSupersetTriggers(t *testing.T) {
	manifest := loadManifest(t)
	narrow := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)

	tests := map[string]struct {
		files  []string
		errors []string
		want   string
	}{
		"unknown file": {
			files: []string{"bothy/blether.txt"},
			want:  "unknown-path",
		},
		"empty exact file list": {
			files: []string{},
			want:  "empty-file-list",
		},
		"blank changed path": {
			files: []string{""},
			want:  "detector-error",
		},
		"leading whitespace path": {
			files: []string{" internal/daemon/session.go"},
			want:  "detector-error",
		},
		"trailing whitespace path": {
			files: []string{"website/content/docs/canny.md "},
			want:  "detector-error",
		},
		"absolute path": {
			files: []string{"/internal/daemon/session.go"},
			want:  "detector-error",
		},
		"parent path": {
			files: []string{"../internal/daemon/session.go"},
			want:  "detector-error",
		},
		"nested parent path": {
			files: []string{"internal/../daemon/session.go"},
			want:  "detector-error",
		},
		"detector error": {
			files:  []string{"internal/daemon/session.go"},
			errors: []string{"dreich detector failure"},
			want:   "detector-error",
		},
		"policy change": {
			files: []string{"internal/cipolicy/manifest.json"},
			want:  "ci-policy-change",
		},
		"generated input": {
			files: []string{"internal/architecture/manifest.json"},
			want:  "generated-input",
		},
		"generated go input": {
			files: []string{"internal/daemon/session.generated.go"},
			want:  "generated-input",
		},
		"lockfile": {
			files: []string{"go.sum"},
			want:  "lockfile",
		},
		"lockfile suffix": {
			files: []string{"vendor/bothy.lock"},
			want:  "lockfile",
		},
		"release metadata": {
			files: []string{".goreleaser.yaml"},
			want:  "release-metadata",
		},
		"release metadata prefix": {
			files: []string{"homebrew/graith.rb"},
			want:  "release-metadata",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan := buildTestPlan(t, manifest, planEvent(nil), test.files, test.errors, true)

			if !plan.Superset {
				t.Fatalf("Superset = false, want true")
			}

			if !slices.Contains(plan.SupersetReasons, test.want) {
				t.Fatalf("superset reasons = %v, want %s", plan.SupersetReasons, test.want)
			}

			if !slices.Equal(plan.Capabilities, sortedSet(allCapabilitySet(manifest))) {
				t.Fatalf("capabilities = %v, want all manifest capabilities", plan.Capabilities)
			}

			if len(plan.Jobs) < len(narrow.Jobs) {
				t.Fatalf("jobs = %d, want at least narrow jobs %d", len(plan.Jobs), len(narrow.Jobs))
			}
		})
	}
}

func TestChangedFileDigestDistinguishesEmptyAndBlankPath(t *testing.T) {
	empty := DetectCapabilities(nil, true, nil)
	blank := DetectCapabilities([]string{""}, true, nil)

	if empty.ChangedFilesDigest == blank.ChangedFilesDigest {
		t.Fatalf("empty and blank path digests both = %s", empty.ChangedFilesDigest)
	}
}

func TestDetectCapabilitiesClassifiesWebsiteHugoBuildInputs(t *testing.T) {
	tests := map[string]struct {
		path             string
		wantCapabilities []string
		wantSuperset     bool
		wantReason       string
	}{
		"content page": {
			path:             "website/content/docs/canny.md",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"nested content index": {
			path:             "website/content/docs/configuration/_index.md",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"layout template": {
			path:             "website/layouts/docs/list.html",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"asset pipeline input": {
			path:             "website/assets/js/package-graph.mjs",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"static asset": {
			path:             "website/static/favicon.png",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"hugo configuration": {
			path:             "website/hugo.toml",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"hugo module file": {
			path:             "website/go.mod",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"hugo module checksum": {
			path:             "website/go.sum",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"generated hugo data fails closed": {
			path:         "website/data/package_dependencies.json",
			wantSuperset: true,
			wantReason:   "generated-input",
		},
		"website go helper": {
			path:             "website/cmd/packagegraph/main.go",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"website test helper": {
			path:             "website/tests/package-graph.test.mjs",
			wantCapabilities: []string{"docs-preview", "docs-publication"},
		},
		"unknown website path fails closed": {
			path:         "website/bothy/blether.txt",
			wantSuperset: true,
			wantReason:   "unknown-path",
		},
		"unrelated backend path keeps docs fast path": {
			path:             "internal/client/passthrough.go",
			wantCapabilities: []string{"go-core"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			detection := DetectCapabilities([]string{test.path}, true, nil)

			assertStringsEqual(t, "capabilities", detection.Capabilities, test.wantCapabilities)

			if detection.Superset != test.wantSuperset {
				t.Fatalf("Superset = %v, want %v", detection.Superset, test.wantSuperset)
			}

			if test.wantReason != "" && !slices.Contains(detection.SupersetReasons, test.wantReason) {
				t.Fatalf("superset reasons = %v, want %s", detection.SupersetReasons, test.wantReason)
			}
		})
	}
}

func TestBuildPlanWithUnknownFileListSelectsSuperset(t *testing.T) {
	manifest := loadManifest(t)

	plan := buildTestPlan(t, manifest, planEvent(nil), nil, nil, false)

	if !plan.Superset || !slices.Contains(plan.SupersetReasons, "file-list-unknown") {
		t.Fatalf("superset = %v reasons = %v, want file-list-unknown", plan.Superset, plan.SupersetReasons)
	}

	if plan.ExactFileList {
		t.Fatalf("ExactFileList = true, want false")
	}

	if plan.ChangedFilesDigest != "" {
		t.Fatalf("ChangedFilesDigest = %q, want empty", plan.ChangedFilesDigest)
	}

	if !slices.Equal(plan.Capabilities, sortedSet(allCapabilitySet(manifest))) {
		t.Fatalf("capabilities = %v, want all manifest capabilities", plan.Capabilities)
	}

	if err := plan.ValidateAt(manifest, p2TestNow); err != nil {
		t.Fatal(err)
	}
}

func TestPlanRejectsPullRequestWithoutHeadRepository(t *testing.T) {
	manifest := loadManifest(t)

	_, _, err := SelectEvent(manifest, planEvent(func(event *EventInput) {
		event.HeadRepository = ""
	}))
	if err == nil || !strings.Contains(err.Error(), "head repository") {
		t.Fatalf("SelectEvent() error = %v, want head repository rejection", err)
	}
}

func TestPlanRejectsConflictingPullRequestTrustContexts(t *testing.T) {
	manifest := loadManifest(t)

	_, _, err := SelectEvent(manifest, planEvent(func(event *EventInput) {
		event.TrustedBase = true
	}))
	if err == nil || !strings.Contains(err.Error(), "exactly one trust context") {
		t.Fatalf("SelectEvent() error = %v, want trust context conflict rejection", err)
	}
}

func TestPlanDoesNotMarkPushesAsForks(t *testing.T) {
	manifest := loadManifest(t)

	selection, _, err := SelectEvent(manifest, pushEvent(func(event *EventInput) {
		event.HeadRepository = "canny/graith"
		event.PullRequestFork = true
		event.SameRepositoryAgent = true
		event.TrustedBase = true
	}))
	if err != nil {
		t.Fatal(err)
	}

	if selection.PullRequestFork || selection.SameRepositoryAgent || selection.TrustedBase {
		t.Fatalf("push event carried pull request trust flags: %#v", selection)
	}
}

func TestPlanClassifiesPushRefsFromManifestPatterns(t *testing.T) {
	manifest := loadManifest(t)

	tagSelection, _, err := SelectEvent(manifest, EventInput{
		GitHubEvent:    "push",
		Ref:            "refs/tags/v1.2.3",
		BaseRepository: DefaultRepository,
		Commit:         strings.Repeat("3", 40),
		Tree:           strings.Repeat("4", 40),
	})
	if err != nil {
		t.Fatal(err)
	}

	if tagSelection.Event != "push-tag" {
		t.Fatalf("tag event = %s, want push-tag", tagSelection.Event)
	}

	edited := cloneManifest(t, manifest)
	for index := range edited.Events {
		if edited.Events[index].ID == "push-main" {
			edited.Events[index].Refs = []string{"refs/heads/trunk"}
		}
	}

	signManifest(t, &edited)

	_, _, err = SelectEvent(edited, pushEvent(nil))
	if err == nil || !strings.Contains(err.Error(), "unsupported push ref") {
		t.Fatalf("SelectEvent() error = %v, want manifest ref rejection", err)
	}

	trunkSelection, _, err := SelectEvent(edited, pushEvent(func(event *EventInput) {
		event.Ref = "refs/heads/trunk"
	}))
	if err != nil {
		t.Fatal(err)
	}

	if trunkSelection.Event != "push-main" {
		t.Fatalf("trunk event = %s, want push-main", trunkSelection.Event)
	}

	withoutRefs := cloneManifest(t, manifest)
	for index := range withoutRefs.Events {
		if withoutRefs.Events[index].ID == "push-tag" {
			withoutRefs.Events[index].Refs = nil
		}
	}

	signManifest(t, &withoutRefs)

	_, _, err = SelectEvent(withoutRefs, pushEvent(func(event *EventInput) {
		event.Ref = "refs/tags/v1.2.3"
		event.Publication = true
	}))
	if err == nil || !strings.Contains(err.Error(), "unsupported push ref") {
		t.Fatalf("SelectEvent() error = %v, want missing refs rejection", err)
	}

	wildcardRefs := cloneManifest(t, manifest)
	for index := range wildcardRefs.Events {
		if wildcardRefs.Events[index].ID == "push-main" {
			wildcardRefs.Events[index].Refs = []string{"*"}
		}
	}

	signManifest(t, &wildcardRefs)

	_, _, err = SelectEvent(wildcardRefs, pushEvent(nil))
	if err == nil || !strings.Contains(err.Error(), "unsupported push ref") {
		t.Fatalf("SelectEvent() error = %v, want bare wildcard rejection", err)
	}
}

func TestPlanRejectsForeignBaseRepository(t *testing.T) {
	manifest := loadManifest(t)

	_, _, err := SelectEvent(manifest, planEvent(func(event *EventInput) {
		event.BaseRepository = "canny/graith"
		event.HeadRepository = "canny/graith"
		event.SameRepositoryAgent = true
	}))
	if err == nil || !strings.Contains(err.Error(), "base repository") {
		t.Fatalf("SelectEvent() error = %v, want base repository rejection", err)
	}
}

func TestPlanRejectsExpiredAndZeroJobPlans(t *testing.T) {
	manifest := loadManifest(t)

	if _, err := BuildPlan(manifest, PlanOptions{
		Event:         planEvent(nil),
		ChangedFiles:  []string{"internal/daemon/session.go"},
		ExactFileList: true,
		CreatedAt:     p2TestNow.Add(-2 * time.Hour),
		ExpiresAt:     p2TestNow.Add(-time.Hour),
		Now:           p2TestNow,
	}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("BuildPlan() error = %v, want expiry rejection", err)
	}

	if _, err := BuildPlan(manifest, PlanOptions{
		Event: pushEvent(func(event *EventInput) {
			event.Publication = true
		}),
		ChangedFiles:  []string{"internal/daemon/session.go"},
		ExactFileList: true,
		CreatedAt:     p2TestNow,
		ExpiresAt:     p2TestNow.Add(time.Hour),
		Now:           p2TestNow,
	}); err == nil || !strings.Contains(err.Error(), "zero required jobs") {
		t.Fatalf("BuildPlan() error = %v, want zero-job rejection", err)
	}
}

func TestWorkflowDispatchIncludesCommitPolicyFloor(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, workflowDispatchEvent(nil), []string{"website/content/docs/canny.md"}, nil, true)

	if !slices.Contains(plan.Capabilities, "commit-policy") {
		t.Fatalf("capabilities = %v, want commit-policy", plan.Capabilities)
	}

	if !slices.Contains(plan.RequiredModes, "legacy/commits/commitsar") {
		t.Fatalf("required modes = %v, want commitsar", plan.RequiredModes)
	}
}

func TestNarrowPullRequestPlansKeepLegacyRequiredGates(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string][]string{
		"go module":        {"go.mod"},
		"go source":        {"internal/daemon/session.go"},
		"gui native input": {"gui/shared/Package.swift"},
	}

	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			plan := buildTestPlan(t, manifest, planEvent(nil), files, nil, true)
			for _, mode := range []string{
				"legacy/libghostty-native/native-gate",
				"legacy/sandbox/linux-nono",
				"legacy/sandbox/macos-safehouse",
			} {
				if !slices.Contains(plan.RequiredModes, mode) {
					t.Fatalf("required modes = %v, want %s", plan.RequiredModes, mode)
				}
			}
		})
	}
}

func TestDetectorCapabilitiesUnionEveryMatchingRule(t *testing.T) {
	detection := DetectCapabilities([]string{"internal/cli/root.go"}, true, nil)

	for _, capability := range []string{"go-core", "native"} {
		if !slices.Contains(detection.Capabilities, capability) {
			t.Fatalf("capabilities = %v, want %s", detection.Capabilities, capability)
		}
	}
}

func TestRequiredModeCapabilitiesAreCoveredByUniversalFloor(t *testing.T) {
	manifest := loadManifest(t)

	for _, mode := range manifest.Modes {
		if mode.Requiredness != "required" {
			continue
		}

		for _, sourceEvent := range mode.SourceEvents {
			if sourceEvent.Source != sourceID {
				continue
			}

			if slices.Contains(universalCapabilities(sourceEvent.Event), mode.Capability) {
				continue
			}

			t.Fatalf("required mode %s capability %s is outside universal floor for event %s", mode.ID, mode.Capability, sourceEvent.Event)
		}
	}
}

func TestBuildPlanRejectsRequiredModeOutsideUniversalFloor(t *testing.T) {
	manifest := loadManifest(t)
	edited := cloneManifest(t, manifest)

	for index := range edited.Modes {
		if edited.Modes[index].ID == "legacy/ci/lint" {
			edited.Modes[index].Capability = "gui"
		}
	}

	for index := range edited.Capabilities {
		edited.Capabilities[index].Modes = removeString(edited.Capabilities[index].Modes, "legacy/ci/lint")
		if edited.Capabilities[index].ID == "gui" {
			edited.Capabilities[index].Modes = append(edited.Capabilities[index].Modes, "legacy/ci/lint")
		}
	}

	signManifest(t, &edited)

	_, err := BuildPlan(edited, PlanOptions{
		Event:         planEvent(nil),
		ChangedFiles:  []string{"internal/daemon/session.go"},
		ExactFileList: true,
		CreatedAt:     p2TestNow,
		ExpiresAt:     p2TestNow.Add(time.Hour),
		Now:           p2TestNow,
	})
	if err == nil || !strings.Contains(err.Error(), "universal capability floor") {
		t.Fatalf("BuildPlan() error = %v, want universal floor rejection", err)
	}
}

func TestDetectorDigestPinned(t *testing.T) {
	const want = "39a87be564ef9e9f17cd7c17984a8ae37cc47cd609ed4b14c04368f918b75e9a"

	if got := DetectorDigest(); got != want {
		t.Fatalf("DetectorDigest() = %s, want %s", got, want)
	}
}

func TestDetectorVersionMismatchSelectsSuperset(t *testing.T) {
	manifest := loadManifest(t)

	plan, err := BuildPlan(manifest, PlanOptions{
		Event:           planEvent(nil),
		ChangedFiles:    []string{"internal/daemon/session.go"},
		ExactFileList:   true,
		DetectorVersion: "dreich-detector-v0",
		CreatedAt:       p2TestNow,
		ExpiresAt:       p2TestNow.Add(time.Hour),
		Now:             p2TestNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !plan.Superset || !slices.Contains(plan.SupersetReasons, "detector-error") {
		t.Fatalf("superset = %v reasons = %v, want detector-error superset", plan.Superset, plan.SupersetReasons)
	}
}

func TestPlanCanonicalJSONAndDigestAreDeterministic(t *testing.T) {
	manifest := loadManifest(t)
	left := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/sandbox/nono.go", "internal/daemon/session.go"}, nil, true)
	right := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go", "internal/sandbox/nono.go"}, nil, true)

	leftJSON, err := left.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	rightJSON, err := right.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	if left.PlanDigest != right.PlanDigest {
		t.Fatalf("plan digests differ: %s != %s", left.PlanDigest, right.PlanDigest)
	}

	if left.ChangedFilesDigest != right.ChangedFilesDigest || !digestPattern.MatchString(left.ChangedFilesDigest) {
		t.Fatalf("changed file digests = %q/%q, want stable digest", left.ChangedFilesDigest, right.ChangedFilesDigest)
	}

	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("canonical JSON differs:\nleft=%s\nright=%s", leftJSON, rightJSON)
	}

	var decoded RunPlan
	if err := json.Unmarshal(leftJSON, &decoded); err != nil {
		t.Fatal(err)
	}

	if err := decoded.ValidateAt(manifest, p2TestNow); err != nil {
		t.Fatal(err)
	}
}

func TestPlanCanonicalizationNormalizesTimestampsToUTC(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	offset := time.FixedZone("BRAW", 3600)

	plan.CreatedAt = plan.CreatedAt.In(offset)
	plan.ExpiresAt = plan.ExpiresAt.In(offset)
	signPlan(t, &plan)

	if err := plan.ValidateAt(manifest, p2TestNow); err == nil ||
		!strings.Contains(err.Error(), "plan is not canonical") {
		t.Fatalf("ValidateAt() error = %v, want canonical timestamp rejection", err)
	}
}

func TestPlanValidationRejectsNonCanonicalPlan(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
	slices.Reverse(plan.Capabilities)
	signPlan(t, &plan)

	if err := plan.ValidateAt(manifest, p2TestNow); err == nil ||
		!strings.Contains(err.Error(), "plan is not canonical") {
		t.Fatalf("ValidateAt() error = %v, want canonical rejection", err)
	}
}

func TestPlanValidationRejectsInvalidPlanMetadata(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		edit    func(*RunPlan)
		wantErr string
	}{
		"unknown superset reason": {
			edit: func(plan *RunPlan) {
				plan.Superset = true
				plan.SupersetReasons = []string{"dreich-reason"}
				plan.Capabilities = sortedSet(allCapabilitySet(manifest))
			},
			wantErr: "unknown safe-superset reason",
		},
		"invalid changed files digest": {
			edit: func(plan *RunPlan) {
				plan.ChangedFilesDigest = "dreich"
			},
			wantErr: "changed files digest",
		},
		"non exact file list with digest": {
			edit: func(plan *RunPlan) {
				plan.ExactFileList = false
				plan.Superset = true
				plan.SupersetReasons = []string{"file-list-unknown"}
			},
			wantErr: "cannot bind a changed files digest",
		},
		"non canonical push flags": {
			edit: func(plan *RunPlan) {
				plan.Event.PullRequestFork = true
			},
			wantErr: "plan event selection is not canonical",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
			if name == "non canonical push flags" {
				plan = buildTestPlan(t, manifest, pushEvent(nil), []string{"internal/daemon/session.go"}, nil, true)
			}

			test.edit(&plan)
			signPlan(t, &plan)

			if err := plan.ValidateAt(manifest, p2TestNow); err == nil ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateAt() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestPlanValidateAtRequiresValidationTime(t *testing.T) {
	manifest := loadManifest(t)
	plan := buildTestPlan(t, manifest, planEvent(nil), []string{"internal/daemon/session.go"}, nil, true)

	if err := plan.ValidateAt(manifest, time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "plan validation time is required") {
		t.Fatalf("ValidateAt(time.Time{}) error = %v, want plan validation time rejection", err)
	}
}

func TestPlanRejectsFutureAndLongLivedPlans(t *testing.T) {
	manifest := loadManifest(t)

	tests := map[string]struct {
		createdAt time.Time
		expiresAt time.Time
		wantErr   string
	}{
		"future created at": {
			createdAt: p2TestNow.Add(time.Hour),
			expiresAt: p2TestNow.Add(2 * time.Hour),
			wantErr:   "too far in the future",
		},
		"long ttl": {
			createdAt: p2TestNow,
			expiresAt: p2TestNow.Add(defaultPlanTTL + time.Minute),
			wantErr:   "exceeds maximum",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildPlan(manifest, PlanOptions{
				Event:         planEvent(nil),
				ChangedFiles:  []string{"internal/daemon/session.go"},
				ExactFileList: true,
				CreatedAt:     test.createdAt,
				ExpiresAt:     test.expiresAt,
				Now:           p2TestNow,
			}); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("BuildPlan() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func buildTestPlan(
	t *testing.T,
	manifest Manifest,
	event EventInput,
	files []string,
	detectorErrors []string,
	exact bool,
) RunPlan {
	t.Helper()

	plan, err := BuildPlan(manifest, PlanOptions{
		Event:          event,
		ChangedFiles:   files,
		ExactFileList:  exact,
		DetectorErrors: detectorErrors,
		CreatedAt:      p2TestNow,
		ExpiresAt:      p2TestNow.Add(time.Hour),
		Now:            p2TestNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	return plan
}

func planEvent(edit func(*EventInput)) EventInput {
	event := EventInput{
		GitHubEvent:         "pull_request",
		Ref:                 "refs/pull/17/merge",
		BaseRef:             "refs/heads/main",
		HeadRef:             "refs/heads/canny",
		BaseRepository:      DefaultRepository,
		HeadRepository:      DefaultRepository,
		Commit:              strings.Repeat("1", 40),
		Tree:                strings.Repeat("2", 40),
		SameRepositoryAgent: true,
	}

	if edit != nil {
		edit(&event)
	}

	return event
}

func pushEvent(edit func(*EventInput)) EventInput {
	event := EventInput{
		GitHubEvent:    "push",
		Ref:            "refs/heads/main",
		BaseRepository: DefaultRepository,
		Commit:         strings.Repeat("3", 40),
		Tree:           strings.Repeat("4", 40),
	}

	if edit != nil {
		edit(&event)
	}

	return event
}

func workflowDispatchEvent(edit func(*EventInput)) EventInput {
	event := EventInput{
		GitHubEvent:    "workflow_dispatch",
		BaseRepository: DefaultRepository,
		Commit:         strings.Repeat("5", 40),
		Tree:           strings.Repeat("6", 40),
	}

	if edit != nil {
		edit(&event)
	}

	return event
}

func removeString(values []string, unwanted string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value != unwanted {
			filtered = append(filtered, value)
		}
	}

	return filtered
}

func signPlan(t *testing.T, plan *RunPlan) {
	t.Helper()

	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}

	plan.PlanDigest = digest
}
