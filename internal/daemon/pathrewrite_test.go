package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/testutil"
	"github.com/d0ugal/graith/internal/tools"
)

func needles(subs []pathSubstitution) []string {
	out := make([]string, len(subs))
	for i, s := range subs {
		out[i] = s.needle
	}

	return out
}

func TestBuildIncludePathRewrites(t *testing.T) {
	home := "/hame/dougal"
	subs := buildIncludePathRewrites(
		home,
		"/hame/dougal/Code/devenv",
		"/data/worktrees/devenv/abc/id/devenv",
		[]IncludedRepoState{
			{RepoPath: "/hame/dougal/Code/grafana", WorktreePath: "/data/worktrees/devenv/abc/id/grafana"},
			{RepoPath: "/opt/croft/examples", WorktreePath: "/data/worktrees/devenv/abc/id/examples"},
		},
	)

	// 2 forms for each home-relative repo (devenv, grafana) + 1 for the
	// out-of-home repo (examples).
	if len(subs) != 5 {
		t.Fatalf("want 5 substitutions, got %d: %v", len(subs), needles(subs))
	}

	// Every substitution must be sorted longest-needle-first.
	for i := 1; i < len(subs); i++ {
		if len(subs[i-1].needle) < len(subs[i].needle) {
			t.Errorf("not sorted longest-first: %v", needles(subs))
			break
		}
	}

	// Tilde forms are derived for home-relative repos.
	byNeedle := map[string]string{}
	for _, s := range subs {
		byNeedle[s.needle] = s.worktree
	}

	if byNeedle["~/Code/grafana"] != "/data/worktrees/devenv/abc/id/grafana" {
		t.Errorf("grafana tilde form missing/wrong: %v", byNeedle)
	}

	if byNeedle["/hame/dougal/Code/grafana"] != "/data/worktrees/devenv/abc/id/grafana" {
		t.Errorf("grafana absolute form missing/wrong: %v", byNeedle)
	}

	// Out-of-home repo has only the absolute form (no tilde spelling exists).
	if _, ok := byNeedle["/opt/croft/examples"]; !ok {
		t.Errorf("examples absolute form missing: %v", byNeedle)
	}

	for n := range byNeedle {
		if strings.HasPrefix(n, "~/") && strings.Contains(n, "examples") {
			t.Errorf("unexpected tilde form for out-of-home repo: %q", n)
		}
	}
}

func TestBuildIncludePathRewritesNoHome(t *testing.T) {
	subs := buildIncludePathRewrites("", "/hame/dougal/Code/devenv", "/wt/devenv", nil)
	if len(subs) != 1 {
		t.Fatalf("want 1 substitution (absolute only), got %d: %v", len(subs), needles(subs))
	}

	if subs[0].needle != "/hame/dougal/Code/devenv" {
		t.Errorf("needle = %q", subs[0].needle)
	}
}

func TestBuildIncludePathRewritesSkipsEmpty(t *testing.T) {
	subs := buildIncludePathRewrites("/hame", "", "", nil)
	if len(subs) != 0 {
		t.Fatalf("want 0 substitutions for empty main paths, got %d", len(subs))
	}
}

func TestRewritePathsInContent(t *testing.T) {
	subs := buildIncludePathRewrites(
		"/hame/dougal",
		"/hame/dougal/Code/devenv", "/data/wt/devenv",
		[]IncludedRepoState{
			{RepoPath: "/hame/dougal/Code/grafana", WorktreePath: "/data/wt/grafana"},
			{RepoPath: "/hame/dougal/Code/examples", WorktreePath: "/data/wt/examples"},
		},
	)

	tests := []struct {
		name        string
		in          string
		want        string
		wantChanged bool
	}{
		{
			name:        "env var absolute path",
			in:          "GRAFANA_PATH=/hame/dougal/Code/grafana\n",
			want:        "GRAFANA_PATH=/data/wt/grafana\n",
			wantChanged: true,
		},
		{
			name:        "tilde form",
			in:          "GRAFANA_PATH=~/Code/grafana\n",
			want:        "GRAFANA_PATH=/data/wt/grafana\n",
			wantChanged: true,
		},
		{
			name:        "docker-compose bind mount preserves suffix and colon",
			in:          "    volumes:\n      - ~/Code/grafana/conf:/etc/grafana\n",
			want:        "    volumes:\n      - /data/wt/grafana/conf:/etc/grafana\n",
			wantChanged: true,
		},
		{
			name:        "quoted absolute path",
			in:          `path: "/hame/dougal/Code/grafana"`,
			want:        `path: "/data/wt/grafana"`,
			wantChanged: true,
		},
		{
			name:        "multiple repos on separate lines",
			in:          "A=~/Code/grafana\nB=~/Code/examples\n",
			want:        "A=/data/wt/grafana\nB=/data/wt/examples\n",
			wantChanged: true,
		},
		{
			name:        "hyphen sibling left untouched",
			in:          "PATH=~/Code/grafana-enterprise\n",
			want:        "PATH=~/Code/grafana-enterprise\n",
			wantChanged: false,
		},
		{
			name:        "dotted sibling left untouched",
			in:          "PATH=~/Code/grafana.bak\n",
			want:        "PATH=~/Code/grafana.bak\n",
			wantChanged: false,
		},
		{
			name:        "plus sibling left untouched",
			in:          "PATH=~/Code/grafana+ent\n",
			want:        "PATH=~/Code/grafana+ent\n",
			wantChanged: false,
		},
		{
			name:        "at sibling left untouched",
			in:          "PATH=~/Code/grafana@next\n",
			want:        "PATH=~/Code/grafana@next\n",
			wantChanged: false,
		},
		{
			name:        "embedded in longer absolute path left untouched",
			in:          "PATH=/mnt/hame/dougal/Code/grafana\n",
			want:        "PATH=/mnt/hame/dougal/Code/grafana\n",
			wantChanged: false,
		},
		{
			name:        "no match",
			in:          "PATH=~/Code/loch\n",
			want:        "PATH=~/Code/loch\n",
			wantChanged: false,
		},
		{
			name:        "exact match at end of string",
			in:          "PATH=~/Code/grafana",
			want:        "PATH=/data/wt/grafana",
			wantChanged: true,
		},
		{
			name:        "same repo twice",
			in:          "A=~/Code/grafana\nB=~/Code/grafana\n",
			want:        "A=/data/wt/grafana\nB=/data/wt/grafana\n",
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := rewritePathsInContent(tt.in, subs)
			if got != tt.want {
				t.Errorf("content:\n got %q\nwant %q", got, tt.want)
			}

			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}

// TestRewritePathsInContentNestedRepo is the regression test for a nested
// included repo: the main repo path is an ancestor of an included repo's path.
// The included (more specific) path must win, not be captured by its parent.
func TestRewritePathsInContentNestedRepo(t *testing.T) {
	subs := buildIncludePathRewrites(
		"/hame/dougal",
		"/hame/dougal/Code/monorepo", "/data/wt/monorepo",
		[]IncludedRepoState{
			{RepoPath: "/hame/dougal/Code/monorepo/tools/widget", WorktreePath: "/data/wt/widget"},
		},
	)

	// Reference to the nested repo must resolve to the nested worktree.
	got, changed := rewritePathsInContent("P=~/Code/monorepo/tools/widget/x\n", subs)
	if !changed || got != "P=/data/wt/widget/x\n" {
		t.Errorf("nested repo: got %q changed=%v, want %q true", got, changed, "P=/data/wt/widget/x\n")
	}

	// A reference to the parent (not into the nested repo) still resolves to the
	// parent worktree.
	got, changed = rewritePathsInContent("P=~/Code/monorepo/pkg\n", subs)
	if !changed || got != "P=/data/wt/monorepo/pkg\n" {
		t.Errorf("parent repo: got %q changed=%v, want %q true", got, changed, "P=/data/wt/monorepo/pkg\n")
	}
}

func TestRewritePathsInContentEmptyRewrites(t *testing.T) {
	got, changed := rewritePathsInContent("PATH=~/Code/grafana\n", nil)
	if changed {
		t.Errorf("want no change with empty rewrites")
	}

	if got != "PATH=~/Code/grafana\n" {
		t.Errorf("content mutated: %q", got)
	}
}

// TestRewriteIncludeConfigPathsUsesPinnedRunner is the #1287 create/fork
// path-rewrite regression: the post-worktree config rewrite must run its
// `git update-index --skip-worktree` on the SAME pinned Runner the lifecycle
// captured, not a fresh package-global git resolution — otherwise a reload
// between SetupSession and the rewrite would split the operation.
func TestRewriteIncludeConfigPathsUsesPinnedRunner(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found on PATH")
	}

	testutil.IsolateGit(t)

	wt := t.TempDir()
	gitRun(t, wt, "init", "-b", "main")

	overridePath := filepath.Join(wt, "docker-compose.override.yml")
	if err := os.WriteFile(overridePath, []byte("services:\n  gr:\n    volumes:\n      - ~/Code/grafana:/app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	gitRun(t, wt, "add", "docker-compose.override.yml")
	gitRun(t, wt, "commit", "-m", "add override")

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "calls.log")
	wrapperA := writePullGitWrapper(t, binDir, "gitA", "A", realGit, logPath, "")

	t.Cleanup(tools.Reset)
	tools.Configure(tools.Config{Git: wrapperA})

	sm := newTestSM(t)
	subs := []pathSubstitution{{needle: "~/Code/grafana", worktree: "/data/wt/grafana"}}

	// Pass a Runner pinned to wrapper A, as the lifecycle does. markSkipWorktree
	// must run on it.
	sm.rewriteIncludeConfigPaths(git.NewRunner(), []string{wt}, subs)

	lines := filterLinesContaining(readNonEmptyLines(t, logPath), "update-index")
	if len(lines) == 0 {
		t.Fatal("skip-worktree git call was not made (rewrite did not run its update-index)")
	}

	for _, line := range lines {
		if !strings.HasPrefix(line, "A ") {
			t.Fatalf("skip-worktree ran on the wrong generation: %q (must use the pinned Runner)", line)
		}
	}
}

func TestRewriteIncludeConfigPaths(t *testing.T) {
	mainWt := t.TempDir()
	incWt := t.TempDir()

	subs := []pathSubstitution{
		{needle: "~/Code/grafana", worktree: "/data/wt/grafana"},
	}

	// docker-compose.override.yml in the main worktree references the sibling.
	overridePath := filepath.Join(mainWt, "docker-compose.override.yml")
	if err := os.WriteFile(overridePath, []byte("services:\n  gr:\n    volumes:\n      - ~/Code/grafana:/app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// .env.local with no matching path — should be left byte-identical.
	envPath := filepath.Join(mainWt, ".env.local")
	envBody := "UNRELATED=~/Code/loch\n"

	if err := os.WriteFile(envPath, []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// A file we don't know about — must be ignored even if it has a match.
	otherPath := filepath.Join(mainWt, "config.yaml")
	otherBody := "path: ~/Code/grafana\n"

	if err := os.WriteFile(otherPath, []byte(otherBody), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newTestSM(t)
	sm.rewriteIncludeConfigPaths(git.NewRunner(), []string{mainWt, incWt}, subs)

	got, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(got), "/data/wt/grafana:/app") {
		t.Errorf("override not rewritten: %q", got)
	}

	// Untouched files stay byte-identical.
	if b, _ := os.ReadFile(envPath); string(b) != envBody {
		t.Errorf(".env.local changed unexpectedly: %q", b)
	}

	if b, _ := os.ReadFile(otherPath); string(b) != otherBody {
		t.Errorf("unknown file was rewritten: %q", b)
	}
}

func TestRewriteIncludeConfigPathsPreservesMode(t *testing.T) {
	wt := t.TempDir()
	path := filepath.Join(wt, ".env.local")

	if err := os.WriteFile(path, []byte("P=~/Code/grafana\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newTestSM(t)
	sm.rewriteIncludeConfigPaths(git.NewRunner(),
		[]string{wt},
		[]pathSubstitution{{needle: "~/Code/grafana", worktree: "/wt/grafana"}},
	)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	if b, _ := os.ReadFile(path); string(b) != "P=/wt/grafana\n" {
		t.Errorf("content = %q", b)
	}
}

// TestRewriteIncludeConfigPathsSkipsSymlink is the regression test for the
// symlink-disclosure finding: a config symlink pointing at an external file must
// not be read (its contents disclosed into the worktree) nor replaced.
func TestRewriteIncludeConfigPathsSkipsSymlink(t *testing.T) {
	wt := t.TempDir()
	externalDir := t.TempDir()

	// An external "secret" that happens to contain a matching source path.
	secret := filepath.Join(externalDir, "secret.env")
	externalBody := "PRIVATE=neep\nP=~/Code/grafana\n"

	if err := os.WriteFile(secret, []byte(externalBody), 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(wt, ".env.local")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	sm := newTestSM(t)
	sm.rewriteIncludeConfigPaths(git.NewRunner(),
		[]string{wt},
		[]pathSubstitution{{needle: "~/Code/grafana", worktree: "/wt/grafana"}},
	)

	// The link is still a symlink (not replaced by a regular file).
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf(".env.local symlink was replaced by a regular file")
	}

	// The external secret is untouched.
	if b, _ := os.ReadFile(secret); string(b) != externalBody {
		t.Errorf("external secret modified: %q", b)
	}
}

func TestRewriteIncludeConfigPathsNoOpCases(t *testing.T) {
	sm := newTestSM(t)

	// No rewrites: returns immediately, no panic on a nil root.
	sm.rewriteIncludeConfigPaths(git.NewRunner(), nil, nil)

	// Missing files in a real dir: no error, nothing created.
	wt := t.TempDir()
	sm.rewriteIncludeConfigPaths(git.NewRunner(),
		[]string{wt, ""},
		[]pathSubstitution{{needle: "~/Code/x", worktree: "/wt/x"}},
	)

	if entries, _ := os.ReadDir(wt); len(entries) != 0 {
		t.Errorf("expected no files created, got %d", len(entries))
	}
}

// TestRewriteIncludeConfigPathsSkipWorktree checks that a rewritten tracked file
// is marked skip-worktree so its session-specific path won't be committed and
// the worktree doesn't report dirty.
func TestRewriteIncludeConfigPathsSkipWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = repo

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-q")
	runGit("config", "user.email", "bide@example.com")
	runGit("config", "user.name", "Bide")

	// A committed (tracked) override referencing the sibling.
	path := filepath.Join(repo, "docker-compose.override.yml")
	if err := os.WriteFile(path, []byte("volumes:\n  - ~/Code/grafana:/app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runGit("add", "docker-compose.override.yml")
	runGit("commit", "-q", "-m", "add override")

	sm := newTestSM(t)
	sm.rewriteIncludeConfigPaths(git.NewRunner(),
		[]string{repo},
		[]pathSubstitution{{needle: "~/Code/grafana", worktree: "/data/wt/grafana"}},
	)

	// File was rewritten on disk.
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "/data/wt/grafana:/app") {
		t.Fatalf("override not rewritten: %q", b)
	}

	// git status must report it clean — the local edit is skipped.
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}

	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("worktree dirty after rewrite, want clean; status:\n%s", out)
	}
}
