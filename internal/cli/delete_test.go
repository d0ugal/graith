package cli

import (
	"errors"
	"testing"

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
