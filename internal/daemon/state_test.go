package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestStateSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &State{
		Sessions: map[string]*SessionState{
			"abc123": {
				ID: "abc123", Name: "fix-bug", RepoPath: "/home/user/repo",
				RepoName: "repo", WorktreePath: "/home/user/.local/share/graith/worktrees/abc123",
				Branch: "d0ugal/graith/fix-bug-abc123", BaseBranch: "main",
				Agent: "claude", Status: StatusRunning, CreatedAt: time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d", loaded.Version, CurrentStateVersion)
	}
	s, ok := loaded.Sessions["abc123"]
	if !ok {
		t.Fatal("session not found after load")
	}
	if s.Name != "fix-bug" || s.Agent != "claude" || s.Status != StatusRunning {
		t.Errorf("session = %+v", s)
	}
}

func TestLoadStateV0Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// Write a v0 state file (no version field)
	v0Data := []byte(`{"sessions":{"s1":{"id":"s1","name":"old-session","status":"running"}}}`)
	if err := writeFileAtomic(path, v0Data); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d after migration", state.Version, CurrentStateVersion)
	}
	if s, ok := state.Sessions["s1"]; !ok {
		t.Fatal("session lost during migration")
	} else if s.Name != "old-session" {
		t.Errorf("name = %q, want %q", s.Name, "old-session")
	}
}

func TestLoadStateFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	futureData := []byte(`{"version":999,"sessions":{}}`)
	if err := writeFileAtomic(path, futureData); err != nil {
		t.Fatal(err)
	}
	_, err := LoadState(path)
	if err == nil {
		t.Fatal("expected error for future version")
	}
}

func TestSaveStateSetsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &State{Version: 0, Sessions: make(map[string]*SessionState)}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	if state.Version != CurrentStateVersion {
		t.Errorf("version = %d after save, want %d", state.Version, CurrentStateVersion)
	}
}

func TestLoadStateMissing(t *testing.T) {
	state, err := LoadState("/nonexistent/state.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 0 {
		t.Error("expected empty state for missing file")
	}
}

func TestLoadStateCorrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeFileAtomic(path, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 0 {
		t.Error("expected empty state for corrupted file")
	}
}

func TestWriteFileAtomicContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.json")
	want := []byte(`{"hello":"world"}`)
	if err := writeFileAtomic(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestWriteFileAtomicPreservesOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	original := []byte(`{"original":true}`)
	if err := writeFileAtomic(path, original); err != nil {
		t.Fatal(err)
	}

	// Writing to a read-only directory should fail, leaving the original intact.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	err := writeFileAtomic(path, []byte(`{"new":true}`))
	if err == nil {
		t.Fatal("expected error writing to read-only dir")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("original file was corrupted: got %q", got)
	}
}

func TestWriteFileAtomicNoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	if err := writeFileAtomic(path, []byte("data")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "test.json" {
			t.Errorf("unexpected temp file left behind: %s", e.Name())
		}
	}
}

func TestSandboxConfigPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	sbx := &config.SandboxConfig{
		Enabled:   true,
		Command:   "safehouse",
		Features:  []string{"net"},
		ReadDirs:  []string{"/usr/share"},
		WriteDirs: []string{"/tmp"},
	}
	state := &State{
		Sessions: map[string]*SessionState{
			"s1": {
				ID: "s1", Name: "test", Agent: "claude",
				Status: StatusRunning, Sandboxed: true,
				SandboxConfig: sbx,
				CreatedAt:     time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s := loaded.Sessions["s1"]
	if s.SandboxConfig == nil {
		t.Fatal("SandboxConfig lost after save/load")
	}
	if !s.SandboxConfig.Enabled {
		t.Error("SandboxConfig.Enabled = false, want true")
	}
	if s.SandboxConfig.Command != "safehouse" {
		t.Errorf("SandboxConfig.Command = %q, want %q", s.SandboxConfig.Command, "safehouse")
	}
	if len(s.SandboxConfig.ReadDirs) != 1 || s.SandboxConfig.ReadDirs[0] != "/usr/share" {
		t.Errorf("SandboxConfig.ReadDirs = %v, want [/usr/share]", s.SandboxConfig.ReadDirs)
	}
	if len(s.SandboxConfig.WriteDirs) != 1 || s.SandboxConfig.WriteDirs[0] != "/tmp" {
		t.Errorf("SandboxConfig.WriteDirs = %v, want [/tmp]", s.SandboxConfig.WriteDirs)
	}
	if len(s.SandboxConfig.Features) != 1 || s.SandboxConfig.Features[0] != "net" {
		t.Errorf("SandboxConfig.Features = %v, want [net]", s.SandboxConfig.Features)
	}
}

func TestSandboxConfigNilBackwardCompat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := []byte(`{"version":1,"sessions":{"s1":{"id":"s1","name":"old","status":"stopped","sandboxed":true}}}`)
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s := loaded.Sessions["s1"]
	if !s.Sandboxed {
		t.Error("Sandboxed = false, want true")
	}
	if s.SandboxConfig != nil {
		t.Error("SandboxConfig should be nil for pre-existing state without the field")
	}
}
