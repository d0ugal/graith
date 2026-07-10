package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

// stubGitChecks swaps the package-level git lookup functions for the duration
// of a test so liveSessionStatus can be exercised without spawning git. The
// stubs key on the worktree path so per-repo results can be varied.
func stubGitChecks(t *testing.T, dirty map[string][]string, dirtyErr map[string]error, unpushed map[string][]string, unpushedErr map[string]error) {
	t.Helper()

	origDirty := dirtyFilesFn
	origUnpushed := unpushedSummariesFn

	t.Cleanup(func() {
		dirtyFilesFn = origDirty
		unpushedSummariesFn = origUnpushed
	})

	dirtyFilesFn = func(dir string) ([]string, error) {
		return dirty[dir], dirtyErr[dir]
	}

	unpushedSummariesFn = func(worktreePath, _ string) ([]string, error) {
		return unpushed[worktreePath], unpushedErr[worktreePath]
	}
}

func TestLiveSessionStatus(t *testing.T) {
	tests := []struct {
		name        string
		session     protocol.SessionInfo
		dirty       map[string][]string
		dirtyErr    map[string]error
		unpushed    map[string][]string
		unpushedErr map[string]error
		want        liveGitStatus
	}{
		{
			name:    "clean worktree",
			session: protocol.SessionInfo{WorktreePath: "/bothy/braw", RepoPath: "/croft/braw", BaseBranch: "main"},
			want:    liveGitStatus{},
		},
		{
			name:    "dirty worktree",
			session: protocol.SessionInfo{WorktreePath: "/bothy/dreich", RepoPath: "/croft/dreich", BaseBranch: "main"},
			dirty:   map[string][]string{"/bothy/dreich": {" M loch.go"}},
			want:    liveGitStatus{dirty: true},
		},
		{
			name:     "unpushed commits",
			session:  protocol.SessionInfo{WorktreePath: "/bothy/bide", RepoPath: "/croft/bide", BaseBranch: "main"},
			unpushed: map[string][]string{"/bothy/bide": {"abc auld", "def bonnie"}},
			want:     liveGitStatus{unpushed: 2},
		},
		{
			name:     "dirty check fails",
			session:  protocol.SessionInfo{WorktreePath: "/bothy/fash", RepoPath: "/croft/fash", BaseBranch: "main"},
			dirtyErr: map[string]error{"/bothy/fash": errors.New("scunner")},
			want:     liveGitStatus{gitFailed: true},
		},
		{
			name:        "unpushed check fails with base branch",
			session:     protocol.SessionInfo{WorktreePath: "/bothy/thrawn", RepoPath: "/croft/thrawn", BaseBranch: "main"},
			unpushedErr: map[string]error{"/bothy/thrawn": errors.New("scunner")},
			want:        liveGitStatus{gitFailed: true},
		},
		{
			name:        "unpushed check fails without base branch is ignored",
			session:     protocol.SessionInfo{WorktreePath: "/bothy/haar", RepoPath: "/croft/haar", BaseBranch: ""},
			unpushedErr: map[string]error{"/bothy/haar": errors.New("scunner")},
			want:        liveGitStatus{},
		},
		{
			name:    "in-place session reported clean",
			session: protocol.SessionInfo{WorktreePath: "/bothy/hame", RepoPath: "/croft/hame", BaseBranch: "main", InPlace: true},
			dirty:   map[string][]string{"/bothy/hame": {" M glen.go"}},
			want:    liveGitStatus{},
		},
		{
			name:    "shared-worktree session reported clean",
			session: protocol.SessionInfo{WorktreePath: "/bothy/shared", RepoPath: "/croft/shared", BaseBranch: "main", SharedWorktree: true},
			dirty:   map[string][]string{"/bothy/shared": {" M glen.go"}},
			want:    liveGitStatus{},
		},
		{
			name:    "no-repo session reported clean",
			session: protocol.SessionInfo{WorktreePath: "/bothy/norepo", RepoPath: "", BaseBranch: "main"},
			dirty:   map[string][]string{"/bothy/norepo": {" M glen.go"}},
			want:    liveGitStatus{},
		},
		{
			name:    "empty worktree path skipped",
			session: protocol.SessionInfo{WorktreePath: "", RepoPath: "/croft/whin", BaseBranch: "main"},
			want:    liveGitStatus{},
		},
		{
			name: "includes aggregate dirty and unpushed",
			session: protocol.SessionInfo{
				WorktreePath: "/bothy/ben",
				RepoPath:     "/croft/ben",
				BaseBranch:   "main",
				Includes: []protocol.IncludedRepoInfo{
					{WorktreePath: "/bothy/wynd", BaseBranch: "main"},
					{WorktreePath: "/bothy/brae", BaseBranch: "main"},
				},
			},
			dirty:    map[string][]string{"/bothy/wynd": {" M kirk.go"}},
			unpushed: map[string][]string{"/bothy/ben": {"abc auld"}, "/bothy/brae": {"def bonnie", "ghi canny"}},
			want:     liveGitStatus{dirty: true, unpushed: 3},
		},
		{
			name: "include git failure surfaces",
			session: protocol.SessionInfo{
				WorktreePath: "/bothy/clachan",
				RepoPath:     "/croft/clachan",
				BaseBranch:   "main",
				Includes: []protocol.IncludedRepoInfo{
					{WorktreePath: "/bothy/skelf", BaseBranch: "main"},
				},
			},
			dirtyErr: map[string]error{"/bothy/skelf": errors.New("scunner")},
			want:     liveGitStatus{gitFailed: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubGitChecks(t, tt.dirty, tt.dirtyErr, tt.unpushed, tt.unpushedErr)

			got := liveSessionStatus(tt.session)

			if got != tt.want {
				t.Errorf("liveSessionStatus() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// setDiscardOutForDelete swaps the package out writer for one that discards, in
// the requested JSON mode, restoring the original on cleanup.
func setDiscardOutForDelete(t *testing.T, jsonMode bool) {
	t.Helper()

	orig := out

	t.Cleanup(func() { out = orig })

	out = output.NewWithWriter(jsonMode, io.Discard)
}

// TestDeleteCmdArgsValidation exercises the custom Args validator on deleteCmd,
// which gates the mutually-exclusive --children / batch-filter combinations and
// the differing arity each mode allows.
func TestDeleteCmdArgsValidation(t *testing.T) {
	origChildren := deleteChildren
	origBatch := deleteBatch

	t.Cleanup(func() {
		deleteChildren = origChildren
		deleteBatch = origBatch
	})

	tests := []struct {
		name     string
		children bool
		batch    batchFlags
		args     []string
		wantErr  bool
	}{
		{name: "children with batch filter rejected", children: true, batch: batchFlags{stopped: true}, args: nil, wantErr: true},
		{name: "batch filter takes no args", batch: batchFlags{repo: "croft"}, args: nil, wantErr: false},
		{name: "batch filter rejects positional arg", batch: batchFlags{repo: "croft"}, args: []string{"braw"}, wantErr: true},
		{name: "children allows zero args", children: true, args: nil, wantErr: false},
		{name: "children allows one arg", children: true, args: []string{"ben"}, wantErr: false},
		{name: "children rejects two args", children: true, args: []string{"ben", "brae"}, wantErr: true},
		{name: "plain requires exactly one arg", args: []string{"braw"}, wantErr: false},
		{name: "plain rejects zero args", args: nil, wantErr: true},
		{name: "plain rejects two args", args: []string{"braw", "canny"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleteChildren = tt.children
			deleteBatch = tt.batch

			err := deleteCmd.Args(deleteCmd, tt.args)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestResolveByNameOrID covers name/ID resolution and the ambiguity guard that
// keeps delete/purge/restore from acting on an arbitrary first match.
func TestResolveByNameOrID(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "id-braw", Name: "braw"},
		{ID: "id-dreich-1", Name: "dreich"},
		{ID: "id-dreich-2", Name: "dreich"}, // duplicate name (delete/recreate cycle)
	}

	t.Run("unique name resolves", func(t *testing.T) {
		s, err := resolveByNameOrID("braw", sessions)
		if err != nil || s.ID != "id-braw" {
			t.Fatalf("resolve braw = %+v, %v", s, err)
		}
	})

	t.Run("exact id wins even when name is duplicated", func(t *testing.T) {
		s, err := resolveByNameOrID("id-dreich-2", sessions)
		if err != nil || s.ID != "id-dreich-2" {
			t.Fatalf("resolve id-dreich-2 = %+v, %v", s, err)
		}
	})

	t.Run("ambiguous name requires explicit id", func(t *testing.T) {
		_, err := resolveByNameOrID("dreich", sessions)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("expected ambiguity error, got %v", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := resolveByNameOrID("scunner", sessions)
		if err == nil {
			t.Fatal("expected not-found error")
		}
	})
}

func TestFormatDeleteDeadline(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := formatDeleteDeadline(""); got != "" {
			t.Errorf("formatDeleteDeadline(\"\") = %q, want empty", got)
		}
	})

	t.Run("unparseable", func(t *testing.T) {
		if got := formatDeleteDeadline("not-a-time"); got != "" {
			t.Errorf("got %q, want empty for unparseable", got)
		}
	})

	t.Run("future shows relative", func(t *testing.T) {
		future := time.Now().Add(23 * time.Hour).Format(time.RFC3339)

		got := formatDeleteDeadline(future)
		if !strings.Contains(got, "in ") {
			t.Errorf("got %q, want a relative '(in ...)' suffix", got)
		}
	})
}

// TestPrintDeleteResult covers the soft vs purge success lines.
func TestPrintDeleteResult(t *testing.T) {
	capture := func(r protocol.DeleteResultMsg, children bool) string {
		orig := out

		t.Cleanup(func() { out = orig })

		var buf bytes.Buffer

		out = output.NewWithWriter(false, &buf)
		printDeleteResult(r, children)

		return buf.String()
	}

	t.Run("soft single", func(t *testing.T) {
		expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)

		got := capture(protocol.DeleteResultMsg{
			SessionID: "id", Name: "braw", Soft: true, ExpiresAt: expires,
		}, false)

		if !strings.Contains(got, "Soft-deleted braw") || !strings.Contains(got, "gr purge braw") {
			t.Errorf("soft output missing expected text:\n%s", got)
		}
	})

	t.Run("purge single", func(t *testing.T) {
		got := capture(protocol.DeleteResultMsg{SessionID: "id", Name: "auld", Soft: false}, false)

		if !strings.Contains(got, "Purged auld") {
			t.Errorf("purge output = %q, want 'Purged auld'", got)
		}
	})
}

// TestConfirmDeleteAutoConfirmsWhenClean verifies a session with no dirty files
// and no unpushed commits is deleted without prompting.
func TestConfirmDeleteAutoConfirmsWhenClean(t *testing.T) {
	setDiscardOutForDelete(t, false)
	stubGitChecks(t, nil, nil, nil, nil)

	session := &protocol.SessionInfo{
		Name:         "braw",
		RepoName:     "croft",
		WorktreePath: "/bothy/braw",
		BaseBranch:   "main",
	}

	confirmed, err := confirmDelete(session)
	if err != nil {
		t.Fatalf("confirmDelete error: %v", err)
	}

	if !confirmed {
		t.Fatalf("expected auto-confirm on clean session, got false")
	}
}

// TestConfirmDeleteJSONModeRefusesDirty verifies that in JSON mode a session
// with unsaved work cannot be interactively confirmed and errors instead.
func TestConfirmDeleteJSONModeRefusesDirty(t *testing.T) {
	setDiscardOutForDelete(t, true)
	stubGitChecks(t,
		map[string][]string{"/bothy/dreich": {" M loch.go"}},
		nil, nil, nil,
	)

	session := &protocol.SessionInfo{
		Name:         "dreich",
		RepoName:     "croft",
		WorktreePath: "/bothy/dreich",
		BaseBranch:   "main",
	}

	confirmed, err := confirmDelete(session)
	if err == nil {
		t.Fatalf("expected error in JSON mode with dirty session")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestConfirmDeleteNonTerminalRefusesUnpushed covers the non-terminal branch:
// with unpushed commits and no TTY (the test environment), confirmDelete must
// refuse rather than prompt.
func TestConfirmDeleteNonTerminalRefusesUnpushed(t *testing.T) {
	setDiscardOutForDelete(t, false)
	stubGitChecks(t, nil, nil,
		map[string][]string{"/bothy/thrawn": {"abc auld"}},
		nil,
	)

	session := &protocol.SessionInfo{
		Name:         "thrawn",
		RepoName:     "croft",
		WorktreePath: "/bothy/thrawn",
		BaseBranch:   "main",
	}

	confirmed, err := confirmDelete(session)
	if err == nil {
		t.Fatalf("expected error with no TTY and unpushed commits")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestConfirmDeleteAggregatesIncludesGitFailure verifies that a failed git
// check on an included repo marks the session as having work (gitFailed) and so
// refuses non-interactive deletion.
func TestConfirmDeleteAggregatesIncludesGitFailure(t *testing.T) {
	setDiscardOutForDelete(t, true)
	stubGitChecks(t, nil,
		map[string]error{"/bothy/skelf": errors.New("scunner")},
		nil, nil,
	)

	session := &protocol.SessionInfo{
		Name:         "clachan",
		RepoName:     "croft",
		WorktreePath: "/bothy/clachan",
		BaseBranch:   "main",
		Includes: []protocol.IncludedRepoInfo{
			{RepoName: "neep", WorktreePath: "/bothy/skelf", BaseBranch: "main"},
		},
	}

	confirmed, err := confirmDelete(session)
	if err == nil {
		t.Fatalf("expected error when an included repo git check fails")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}
