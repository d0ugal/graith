package capabilities

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update rewrites the generated region of the docs page from the manifest.
// Run: go test ./internal/capabilities -run TestDocMatchesManifest -update
var update = flag.Bool("update", false, "regenerate the capability matrix docs page")

// docPath is the docs page rendered from the manifest, relative to this
// package directory (internal/capabilities).
const docPath = "../../website/content/docs/capabilities.md"

func TestManifestLoads(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Capabilities) == 0 {
		t.Fatal("manifest has no capabilities")
	}
	// The acceptance criteria require these areas to be covered.
	wantCategories := []string{
		"Session lifecycle", "Terminal I/O", "Approvals & pairing",
		"Messaging", "Scenarios", "Triggers", "MCP servers",
		"Document store", "Notifications", "Sandbox introspection", "Diagnostics",
	}
	got := map[string]bool{}
	for _, c := range m.Categories() {
		got[c] = true
	}
	for _, want := range wantCategories {
		if !got[want] {
			t.Errorf("manifest missing required category %q", want)
		}
	}
}

func TestValidateRejectsBadManifests(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "bad version",
			json: `{"version":2,"surfaces":[{"id":"cli"}],"states":[{"id":"supported","symbol":"x"}],"capabilities":[]}`,
			want: "unsupported manifest version",
		},
		{
			name: "no surfaces",
			json: `{"version":1,"surfaces":[],"states":[{"id":"supported","symbol":"x"}],"capabilities":[{"id":"a"}]}`,
			want: "no surfaces",
		},
		{
			name: "duplicate capability id",
			json: `{"version":1,"surfaces":[{"id":"cli"},{"id":"ios"},{"id":"macos"}],"states":[{"id":"supported","symbol":"x"}],"capabilities":[` +
				`{"id":"a","category":"c","name":"n","cli":"supported","ios":"supported","macos":"supported"},` +
				`{"id":"a","category":"c","name":"n","cli":"supported","ios":"supported","macos":"supported"}]}`,
			want: "duplicate capability id",
		},
		{
			name: "invalid state",
			json: `{"version":1,"surfaces":[{"id":"cli"},{"id":"ios"},{"id":"macos"}],"states":[{"id":"supported","symbol":"x"}],"capabilities":[` +
				`{"id":"a","category":"c","name":"n","cli":"nope","ios":"supported","macos":"supported"}]}`,
			want: "invalid state",
		},
		{
			name: "unknown field",
			json: `{"version":1,"surfaces":[{"id":"cli"}],"states":[{"id":"supported","symbol":"x"}],"capabilities":[],"bogus":true}`,
			want: "decode capability manifest",
		},
		{
			name: "empty category",
			json: `{"version":1,"surfaces":[{"id":"cli"},{"id":"ios"},{"id":"macos"}],"states":[{"id":"supported","symbol":"x"}],"capabilities":[` +
				`{"id":"a","category":"","name":"n","cli":"supported","ios":"supported","macos":"supported"}]}`,
			want: "empty category",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.json))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestReplaceRegionRoundTrip(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	doc := "intro\n" + BeginMarker + "\nstale\n" + EndMarker + "\noutro\n"
	out, err := m.ReplaceRegion(doc)
	if err != nil {
		t.Fatalf("ReplaceRegion: %v", err)
	}
	if !strings.HasPrefix(out, "intro\n") || !strings.HasSuffix(out, "outro\n") {
		t.Errorf("prose outside markers not preserved:\n%s", out)
	}
	if strings.Contains(out, "stale") {
		t.Error("stale region content survived replacement")
	}
	region, err := ExtractRegion(out)
	if err != nil {
		t.Fatalf("ExtractRegion: %v", err)
	}
	if region != strings.TrimSpace(m.MatrixMarkdown()) {
		t.Error("extracted region does not match rendered matrix")
	}
}

func TestReplaceRegionMissingMarkers(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := m.ReplaceRegion("no markers here"); err == nil {
		t.Error("expected error for missing markers")
	}
	if _, err := m.ReplaceRegion(EndMarker + " " + BeginMarker); err == nil {
		t.Error("expected error for out-of-order markers")
	}
}

// TestDocMatchesManifest is the check the acceptance criteria require: it fails
// if the committed docs page disagrees with the manifest. Run with -update to
// regenerate the page.
func TestDocMatchesManifest(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read docs page: %v", err)
	}
	doc := string(raw)

	if *update {
		out, err := m.ReplaceRegion(doc)
		if err != nil {
			t.Fatalf("ReplaceRegion: %v", err)
		}
		if err := os.WriteFile(docPath, []byte(out), 0o644); err != nil {
			t.Fatalf("write docs page: %v", err)
		}
		t.Logf("regenerated %s", filepath.Clean(docPath))
		return
	}

	got, err := ExtractRegion(doc)
	if err != nil {
		t.Fatalf("ExtractRegion: %v", err)
	}
	want := strings.TrimSpace(m.MatrixMarkdown())
	if got != want {
		t.Errorf("docs page capability matrix is out of date.\n"+
			"Run: go test ./internal/capabilities -run TestDocMatchesManifest -update\n\n"+
			"--- got (doc) ---\n%s\n\n--- want (manifest) ---\n%s", got, want)
	}
}

func TestAccessors(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := m.SurfaceIDs(); len(got) != 3 || got[0] != "cli" {
		t.Errorf("SurfaceIDs = %v, want [cli ios macos]", got)
	}
	if got := m.StateIDs(); len(got) != 3 {
		t.Errorf("StateIDs = %v, want 3 sorted ids", got)
	}
	if got := m.symbolFor("supported"); got != "✅" {
		t.Errorf("symbolFor(supported) = %q, want ✅", got)
	}
	// Unknown state falls back to the raw id.
	if got := m.symbolFor("mystery"); got != "mystery" {
		t.Errorf("symbolFor(mystery) = %q, want mystery", got)
	}
	if len(m.Categories()) == 0 {
		t.Error("Categories returned empty")
	}
}

func TestCoverage(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cov := m.Coverage()
	// The CLI is the reference surface: it supports every capability that is
	// not explicitly n/a. Guard that invariant so a regression (a CLI cell set
	// to "planned") is caught.
	cli := cov["cli"]
	if cli == nil {
		t.Fatal("no coverage for cli surface")
	}
	if cli["planned"] != 0 {
		t.Errorf("CLI should not have planned capabilities, got %d", cli["planned"])
	}
	total := 0
	for _, n := range cli {
		total += n
	}
	if total != len(m.Capabilities) {
		t.Errorf("coverage counts (%d) do not sum to capability count (%d)", total, len(m.Capabilities))
	}
}
