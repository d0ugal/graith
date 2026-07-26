package cipolicy

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/cibaseline"
)

func TestRenderShadowSummaryIsDiagnosticAndListsCurrentCIShape(t *testing.T) {
	inventory := shadowSummaryInventory(t)
	plan := &RunPlan{
		DetectorVersion:      "cipolicy-detector-v1",
		DetectorDigest:       "canny",
		DetectedCapabilities: []string{"native", "workflow-policy"},
		ExactFileList:        true,
		Superset:             true,
		SupersetReasons:      []string{"ci-policy-change"},
	}

	summary, err := RenderShadowSummary(ShadowSummaryInput{
		Inventory:           inventory,
		Plan:                plan,
		ChangedFiles:        []string{".github/workflows/ci.yml", "internal/pty/vt.go"},
		EventName:           "pull_request",
		Ref:                 "refs/pull/17/merge",
		HeadSHA:             strings.Repeat("2", 40),
		RunURL:              "https://github.com/d0ugal/graith/actions/runs/123",
		MacOSDetectorResult: "success",
		MacOSDetectorOutput: "true",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"Diagnostic only",
		"not a source-isolated PR gate",
		"`permissions: contents: read`",
		"`Native backend gate`",
		"`libghostty/native` is core runtime validation",
		"CI shadow summary",
		"Use the normal Actions run UI",
		"does not aggregate repository-wide observed job results",
		"libghostty/native core runtime (native)",
		"workflow policy (workflow-policy)",
		"Safe-superset selected: ci-policy-change.",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}

	if regexp.MustCompile(`authoritative check-run completion`).MatchString(summary) {
		t.Fatalf("summary must not claim authoritative check-run completion:\n%s", summary)
	}
}

func TestRenderShadowSummaryHandlesPlanErrorFallback(t *testing.T) {
	summary, err := RenderShadowSummary(ShadowSummaryInput{
		Inventory:           shadowSummaryInventory(t),
		PlanError:           "dreich detector failed\nstack omitted",
		ChangedFiles:        nil,
		MacOSDetectorResult: "failure",
		MacOSDetectorOutput: "",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"Static inventory only",
		"dreich detector failed",
		"fail safe toward running",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestShadowSummaryHelperLanguageClassification(t *testing.T) {
	tests := map[string]string{
		".github/workflows/scripts/docs-preview.js": "JavaScript",
		"internal/cipolicy/shadow_summary.go":       "Go",
		"scripts/libghostty-native.sh":              "Shell",
		"scripts/libghostty-linux-archive.py":       "Python",
		"Makefile":                                  "Make",
	}

	for path, want := range tests {
		if got := helperLanguage(path); got != want {
			t.Fatalf("helperLanguage(%q) = %s, want %s", path, got, want)
		}
	}
}

func shadowSummaryInventory(t *testing.T) cibaseline.Inventory {
	t.Helper()

	inventory, err := ReadInventory(filepath.Join("..", "cibaseline", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}

	return inventory
}
