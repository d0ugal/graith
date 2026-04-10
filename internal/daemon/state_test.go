package daemon

import (
	"path/filepath"
	"testing"
	"time"
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
