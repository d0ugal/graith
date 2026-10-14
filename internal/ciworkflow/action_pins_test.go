package ciworkflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinnedUses returns the expected `uses:` value for a strictly pinned action.
func pinnedUses(t *testing.T, action string) string {
	t.Helper()

	pins, err := ReadActionPins(filepath.Join(p11RepoRoot(), ActionPinsPath))
	if err != nil {
		t.Fatal(err)
	}

	sha, ok := pins[action]
	if !ok {
		t.Fatalf("%s has no expected pin in %s", action, ActionPinsPath)
	}

	return action + "@" + sha
}

// TestActionPinsUpToDate is the stale-pin gate: the committed expectations must
// match the source workflow. Regenerate with `make action-pins`.
func TestActionPinsUpToDate(t *testing.T) {
	t.Parallel()

	root := p11RepoRoot()

	source, err := os.ReadFile(filepath.Join(root, ActionPinsSourceWorkflow))
	if err != nil {
		t.Fatal(err)
	}

	want, err := ExtractActionPins(source)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ReadActionPins(filepath.Join(root, ActionPinsPath))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range PinnedActions {
		if got[name] != want[name] {
			t.Errorf("%s pin = %q, workflow has %q; run `make action-pins`", name, got[name], want[name])
		}
	}

	if len(got) != len(PinnedActions) {
		t.Errorf("%s has %d entries, want %d", ActionPinsPath, len(got), len(PinnedActions))
	}
}

func TestExtractActionPins(t *testing.T) {
	t.Parallel()

	const (
		shaA = "1111111111111111111111111111111111111111"
		shaB = "2222222222222222222222222222222222222222"
		shaC = "3333333333333333333333333333333333333333"
	)

	workflow := strings.Join([]string{
		"steps:",
		"  - uses: actions/checkout@" + shaA + " # v7",
		"  - name: x",
		"    uses: actions/upload-artifact@" + shaB + " # v7",
		"  - uses: actions/checkout@" + shaA,
		"  - uses: actions/download-artifact@" + shaC + " # v8",
		"  - uses: other/action@" + shaA,
		"  - uses: ./.github/actions/local",
		"",
	}, "\n")

	got, err := ExtractActionPins([]byte(workflow))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"actions/checkout":          shaA,
		"actions/upload-artifact":   shaB,
		"actions/download-artifact": shaC,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	for name, sha := range want {
		if got[name] != sha {
			t.Errorf("%s = %q, want %q", name, got[name], sha)
		}
	}

	rendered, err := RenderActionPins(got)
	if err != nil {
		t.Fatal(err)
	}

	again, err := RenderActionPins(got)
	if err != nil || string(rendered) != string(again) {
		t.Fatalf("render is not deterministic: %v", err)
	}

	if !strings.HasSuffix(string(rendered), "\n}\n") || strings.Index(string(rendered), "checkout") > strings.Index(string(rendered), "download") {
		t.Fatalf("unexpected rendering:\n%s", rendered)
	}
}

func TestExtractActionPinsErrors(t *testing.T) {
	t.Parallel()

	const (
		shaA = "1111111111111111111111111111111111111111"
		shaB = "2222222222222222222222222222222222222222"
	)

	all := "uses: actions/checkout@" + shaA + "\nuses: actions/upload-artifact@" + shaA + "\nuses: actions/download-artifact@" + shaA + "\n"

	if _, err := ExtractActionPins([]byte(all)); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	if _, err := ExtractActionPins([]byte(all + "uses: actions/checkout@" + shaB + "\n")); err == nil {
		t.Error("conflicting SHAs were accepted")
	}

	if _, err := ExtractActionPins([]byte(strings.Replace(all, "download-artifact@"+shaA, "download-artifact@v8", 1))); err == nil {
		t.Error("tag-pinned action was accepted")
	}
}
