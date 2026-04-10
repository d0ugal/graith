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
	s, ok := loaded.Sessions["abc123"]
	if !ok {
		t.Fatal("session not found after load")
	}
	if s.Name != "fix-bug" || s.Agent != "claude" || s.Status != StatusRunning {
		t.Errorf("session = %+v", s)
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
