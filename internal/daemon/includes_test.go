package daemon

import (
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestMergeIncludes covers combining repo-config includes with per-session
// extra includes (issue #1046): order is repo-first then extra, and duplicates
// that resolve to the same path are dropped.
func TestMergeIncludes(t *testing.T) {
	cases := []struct {
		name  string
		repo  []string
		extra []string
		want  []string
	}{
		{
			name: "both empty is nil",
			want: nil,
		},
		{
			name: "repo only",
			repo: []string{"/tmp/bothy", "/tmp/glen"},
			want: []string{"/tmp/bothy", "/tmp/glen"},
		},
		{
			name:  "extra only",
			extra: []string{"/tmp/whin"},
			want:  []string{"/tmp/whin"},
		},
		{
			name:  "repo first then extra",
			repo:  []string{"/tmp/bothy"},
			extra: []string{"/tmp/glen"},
			want:  []string{"/tmp/bothy", "/tmp/glen"},
		},
		{
			name:  "duplicate across repo and extra dropped, repo wins",
			repo:  []string{"/tmp/bothy", "/tmp/glen"},
			extra: []string{"/tmp/glen", "/tmp/whin"},
			want:  []string{"/tmp/bothy", "/tmp/glen", "/tmp/whin"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeIncludes(tc.repo, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeIncludes(%v, %v) = %v, want %v", tc.repo, tc.extra, got, tc.want)
			}
		})
	}
}

// TestCreateOptsIncludesAttachExtraWorktree drives a real session whose repo
// has NO configured includes and asserts CreateOpts.Includes still attaches the
// extra worktree — the scenario-provided includes path (issue #1046).
func TestCreateOptsIncludesAttachExtraWorktree(t *testing.T) {
	repoDir := initTempGitRepo(t)
	incDir := initTempGitRepo(t)

	// Repo config carries no includes; the include comes purely from CreateOpts.
	sm, _ := newRecorderManager(t, repoDir, nil)

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "cursor", RepoPath: repoDir, BaseBranch: "main",
		Includes: []string{incDir}, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if len(created.Includes) != 1 {
		t.Fatalf("created.Includes = %v, want one include from CreateOpts", created.Includes)
	}

	if created.Includes[0].RepoName != filepath.Base(incDir) {
		t.Errorf("included repo name = %q, want %q", created.Includes[0].RepoName, filepath.Base(incDir))
	}
}

// TestCreateOptsIncludesCollisionRejected asserts that per-session includes are
// run through the same collision validation as repo-config includes (#1046): an
// include that resolves to the main repo is rejected up front with a clear
// error, rather than failing with a low-level git error mid worktree setup.
func TestCreateOptsIncludesCollisionRejected(t *testing.T) {
	repoDir := initTempGitRepo(t)

	sm, _ := newRecorderManager(t, repoDir, nil)

	_, err := sm.Create(CreateOpts{
		Name: "dreich", AgentName: "cursor", RepoPath: repoDir, BaseBranch: "main",
		Includes: []string{repoDir}, Rows: 24, Cols: 80,
	})
	if err == nil {
		t.Fatal("expected Create to reject an include that is the main repo")
	}

	if !strings.Contains(err.Error(), "invalid includes") {
		t.Errorf("error = %q, want an invalid-includes complaint", err.Error())
	}
}

// TestCreateOptsIncludesInPlaceRejected asserts the in-place path rejects
// per-session includes (#1046) — an in-place session has no worktree layout to
// attach siblings to.
func TestCreateOptsIncludesInPlaceRejected(t *testing.T) {
	repoDir := initTempGitRepo(t)
	incDir := initTempGitRepo(t)

	sm, _ := newRecorderManager(t, repoDir, nil)

	_, err := sm.Create(CreateOpts{
		Name: "dreich", AgentName: "cursor", RepoPath: repoDir, InPlace: true,
		Includes: []string{incDir}, Rows: 24, Cols: 80,
	})
	if err == nil {
		t.Fatal("expected Create to reject --in-place with includes")
	}

	if !strings.Contains(err.Error(), "in-place") {
		t.Errorf("error = %q, want an in-place complaint", err.Error())
	}
}

// TestCreateOptsStarred asserts CreateOpts.Starred creates a session that is
// already starred (protected from manual delete) and that the flag is persisted
// (issue #1046).
func TestCreateOptsStarred(t *testing.T) {
	repoDir := initTempGitRepo(t)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"60"}}

	dir := t.TempDir()
	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		TmpDir:     filepath.Join(dir, "tmp"),
	}, slog.Default())

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "sleeper", RepoPath: repoDir, BaseBranch: "main",
		Starred: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

	if !created.Starred {
		t.Error("returned session should be starred")
	}

	sm.mu.RLock()
	persisted := sm.state.Sessions[created.ID].Starred
	sm.mu.RUnlock()

	if !persisted {
		t.Error("persisted session state should be starred")
	}

	// A starred session is protected from bulk/sweep soft-deletion.
	sm.mu.RLock()
	deletable := softDeletableLocked(sm.state.Sessions[created.ID])
	sm.mu.RUnlock()

	if deletable {
		t.Error("starred session should not be soft-deletable in a sweep")
	}
}
