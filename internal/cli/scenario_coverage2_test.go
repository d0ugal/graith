package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestBuildSessionInputsCov2 verifies the full mapping from parsed TOML sessions
// to protocol inputs: field passthrough, ~-expansion of repo paths, the
// agent_hooks default (nil → true) vs an explicit false, and the shared flag.
func TestBuildSessionInputsCov2(t *testing.T) {
	hooksOff := false

	sf := &scenarioFile{
		Version:  1,
		Scenario: scenarioFileMeta{Name: "strath", Goal: "wire the clachan"},
		Sessions: []scenarioFileSession{
			{
				Name:  "bairn",
				Repo:  "~/Code/croft",
				Agent: "claude",
				Model: "claude-opus-4-8",
				Base:  "main",
				Role:  "Backend engineer",
				Task:  "Add ingest",
				// AgentHooks omitted → defaults to true.
			},
			{
				Name:       "canny",
				Repo:       "/tmp/bothy",
				AgentHooks: &hooksOff,
				Shared:     true,
			},
		},
	}

	got, err := buildSessionInputs(sf)
	if err != nil {
		t.Fatalf("buildSessionInputs: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d inputs, want 2", len(got))
	}

	s0 := got[0]
	if s0.Name != "bairn" || s0.Agent != "claude" || s0.Model != "claude-opus-4-8" ||
		s0.Base != "main" || s0.Role != "Backend engineer" || s0.Task != "Add ingest" {
		t.Errorf("session[0] fields not mapped correctly: %+v", s0)
	}

	if wantRepo := config.ExpandPath("~/Code/croft"); s0.Repo != wantRepo {
		t.Errorf("session[0].Repo = %q, want expanded %q", s0.Repo, wantRepo)
	}

	if !strings.HasPrefix(s0.Repo, "/") {
		t.Errorf("expected ~ expanded to an absolute path, got %q", s0.Repo)
	}

	if !s0.AgentHooks {
		t.Error("session[0].AgentHooks should default to true when omitted")
	}

	if s0.Shared {
		t.Error("session[0].Shared should be false")
	}

	s1 := got[1]
	if s1.AgentHooks {
		t.Error("session[1].AgentHooks should be false (explicitly set)")
	}

	if !s1.Shared {
		t.Error("session[1].Shared should be true")
	}

	if s1.Repo != "/tmp/bothy" {
		t.Errorf("session[1].Repo = %q, want /tmp/bothy unchanged", s1.Repo)
	}
}

// TestBuildSessionInputsCov2MissingName verifies a session without a name is
// rejected with an index-qualified error.
func TestBuildSessionInputsCov2MissingName(t *testing.T) {
	sf := &scenarioFile{
		Sessions: []scenarioFileSession{{Repo: "/tmp/croft"}},
	}

	_, err := buildSessionInputs(sf)
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected missing-name error, got %v", err)
	}
}

// TestBuildSessionInputsCov2MissingRepo verifies a named session without a repo
// is rejected with a name-qualified error.
func TestBuildSessionInputsCov2MissingRepo(t *testing.T) {
	sf := &scenarioFile{
		Sessions: []scenarioFileSession{{Name: "thrawn"}},
	}

	_, err := buildSessionInputs(sf)
	if err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Errorf("expected missing-repo error, got %v", err)
	}

	if !strings.Contains(err.Error(), "thrawn") {
		t.Errorf("error should name the offending session, got %v", err)
	}
}

// TestScenariosDirCov2 verifies scenariosDir derives the scenarios subdir from
// the resolved config file's directory.
func TestScenariosDirCov2(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	dir := t.TempDir()
	paths.ConfigFile = filepath.Join(dir, "config.toml")

	if got, want := scenariosDir(), filepath.Join(dir, "scenarios"); got != want {
		t.Errorf("scenariosDir() = %q, want %q", got, want)
	}
}

// TestResolveScenarioSourceCov2 verifies the package-level resolveScenarioSource
// (which uses scenariosDir()) resolves a name to a file under the config dir's
// scenarios/ folder.
func TestResolveScenarioSourceCov2(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	base := t.TempDir()
	paths.ConfigFile = filepath.Join(base, "config.toml")

	scDir := filepath.Join(base, "scenarios")
	if err := os.MkdirAll(scDir, 0o750); err != nil {
		t.Fatal(err)
	}

	content := []byte("frae the clachan")
	if err := os.WriteFile(filepath.Join(scDir, "strath.toml"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := resolveScenarioSource("strath")
	if err != nil {
		t.Fatalf("resolveScenarioSource: %v", err)
	}

	if string(data) != "frae the clachan" {
		t.Errorf("data = %q", data)
	}
}

// TestResolveScenarioSourceFromCov2StorePrefix verifies the store: prefix is
// rejected with a not-yet-implemented error rather than silently mishandled.
func TestResolveScenarioSourceFromCov2StorePrefix(t *testing.T) {
	_, err := resolveScenarioSourceFrom("store:scenarios/strath.toml", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected store: prefix error, got %v", err)
	}
}

// TestResolveScenarioSourceFromCov2Stdin verifies the "-" source reads the whole
// scenario definition from stdin.
func TestResolveScenarioSourceFromCov2Stdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	orig := os.Stdin
	os.Stdin = r

	defer func() { os.Stdin = orig }()

	go func() {
		_, _ = w.WriteString("version = 1\n")
		_ = w.Close()
	}()

	data, err := resolveScenarioSourceFrom("-", t.TempDir())
	if err != nil {
		t.Fatalf("resolveScenarioSourceFrom(-): %v", err)
	}

	if string(data) != "version = 1\n" {
		t.Errorf("stdin data = %q", data)
	}

	_ = r.Close()
}

// TestListAvailableScenariosCov2 verifies the package-level wrapper reads the
// scenarios dir derived from paths.ConfigFile.
func TestListAvailableScenariosCov2(t *testing.T) {
	oldPaths := paths

	t.Cleanup(func() { paths = oldPaths })

	base := t.TempDir()
	paths.ConfigFile = filepath.Join(base, "config.toml")

	scDir := filepath.Join(base, "scenarios")
	if err := os.MkdirAll(scDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(scDir, "clachan.toml"), []byte(`
version = 1

[scenario]
name = "clachan"
goal = "gather the glen"

[[sessions]]
name = "braw"
repo = "/tmp/croft"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := listAvailableScenarios()
	if len(got) != 1 {
		t.Fatalf("expected 1 available scenario, got %d (%+v)", len(got), got)
	}

	if got[0].Name != "clachan" || got[0].Goal != "gather the glen" {
		t.Errorf("unexpected scenario: %+v", got[0])
	}
}
