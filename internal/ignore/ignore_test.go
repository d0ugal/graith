package ignore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/ignore"
)

func TestLinesMatch(t *testing.T) {
	m := ignore.Lines("*.log", "build/", "!keep.log")

	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"run.log", false, true},       // *.log
		{"keep.log", false, false},     // re-included by !keep.log
		{"build", true, true},          // dir-only pattern, is a dir
		{"build", false, false},        // dir-only pattern, not a dir
		{"build/out.bin", false, true}, // inside ignored dir
		{"main.go", false, false},      // not matched
		{"glen/run.log", false, true},  // *.log matches at any depth
		{"", false, false},             // empty
		{".", false, false},            // dot
		{"src/", true, false},          // trailing slash trimmed, no match
	}

	for _, c := range cases {
		if got := m.Match(c.rel, c.isDir); got != c.want {
			t.Errorf("Match(%q, isDir=%v) = %v, want %v", c.rel, c.isDir, got, c.want)
		}
	}
}

func TestLinesSkipsCommentsAndBlanks(t *testing.T) {
	// Only *.tmp is a real pattern; the comment and blank lines must be ignored
	// rather than treated as literal patterns.
	m := ignore.Lines("# a comment", "", "   ", "*.tmp")

	if !m.Match("scratch.tmp", false) {
		t.Error("*.tmp should match scratch.tmp")
	}

	// A file literally named like the comment must not be matched.
	if m.Match("# a comment", false) {
		t.Error("comment line must not act as a pattern")
	}
}

func TestEmptyMatcherNeverMatches(t *testing.T) {
	m := ignore.Lines()
	if m.Match("anything", false) || m.Match("anything", true) {
		t.Error("empty matcher must never match")
	}
}

func TestDirReadsRootGitignore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "build"), 0o750); err != nil {
		t.Fatal(err)
	}

	m := ignore.Dir(root)

	if !m.Match("build", true) {
		t.Error("build/ dir should be ignored")
	}

	if !m.Match("run.log", false) {
		t.Error("*.log should be ignored")
	}

	if m.Match("main.go", false) {
		t.Error("main.go should not be ignored")
	}
}

func TestDirMissingGitignore(t *testing.T) {
	// A directory with no .gitignore yields a matcher that never matches.
	m := ignore.Dir(t.TempDir())

	if m.Match("anything.go", false) {
		t.Error("matcher over an empty dir must not match")
	}
}

// TestMatchPreservesLeadingWhitespace guards the fix for the Match input
// stripping whitespace: a leading space is a significant part of a filename, so
// the pattern " foo" must match " foo" and not "foo".
func TestMatchPreservesLeadingWhitespace(t *testing.T) {
	m := ignore.Lines(" foo")

	if !m.Match(" foo", false) {
		t.Error("pattern ' foo' should match a file literally named ' foo'")
	}

	if m.Match("foo", false) {
		t.Error("pattern ' foo' must not match 'foo' (leading space is significant)")
	}
}

// TestMatchPreservesTrailingWhitespace covers the trailing-space half of the
// same fix without relying on escaped-pattern parsing: "bar?" matches a 4th
// character, so it matches "bar " (trailing space) but not "bar". If Match
// stripped the trailing space the path would collapse to "bar" and diverge.
func TestMatchPreservesTrailingWhitespace(t *testing.T) {
	m := ignore.Lines("bar?")

	if !m.Match("bar ", false) {
		t.Error("bar? should match 'bar ' — the trailing space is a real character")
	}

	if m.Match("bar", false) {
		t.Error("bar? requires a 4th character; 'bar' must not match")
	}
}

// TestDirLinkedWorktreeInfoExclude confirms Dir consults the shared info/exclude
// of a linked worktree's common git directory. The layout is built by hand (no
// git binary needed): a common git dir holding info/exclude, and a worktree
// whose .git is a "gitdir:" pointer with a commondir back to the common dir.
func TestDirLinkedWorktreeInfoExclude(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, "main", ".git")
	wtGitDir := filepath.Join(common, "worktrees", "bothy")
	wt := filepath.Join(base, "bothy")

	for _, d := range []string{filepath.Join(common, "info"), wtGitDir, wt} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(common, "info", "exclude"), []byte("*.secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// commondir points from the worktree gitdir (.git/worktrees/bothy) back up to
	// the common dir (.git).
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A worktree-local .gitignore layered on top of the shared exclude.
	if err := os.WriteFile(filepath.Join(wt, ".gitignore"), []byte("*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := ignore.Dir(wt)

	if !m.Match("api.secret", false) {
		t.Error("shared info/exclude (*.secret) must apply in a linked worktree")
	}

	if !m.Match("run.log", false) {
		t.Error("worktree-local .gitignore (*.log) must still apply")
	}

	if m.Match("main.go", false) {
		t.Error("an unmatched path must not be ignored")
	}
}

// TestDirPreservesPatternsWhenSubtreeUnreadable is the #1223 regression for the
// fail-open bug: if pattern discovery errors partway (here, an unreadable
// subdirectory) the root patterns gathered before the error must survive, not be
// discarded.
func TestDirPreservesPatternsWhenSubtreeUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A subdirectory pattern discovery will try — and fail — to descend into.
	bad := filepath.Join(root, "dreich")
	if err := os.MkdirAll(bad, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatal(err)
	}

	// Restore owner rwx so t.TempDir's cleanup can walk and remove the subtree.
	t.Cleanup(func() { _ = os.Chmod(bad, 0o750) }) //nolint:gosec // dir needs the execute bit to be traversed and removed

	m := ignore.Dir(root)

	if !m.Match("run.log", false) {
		t.Error("root .gitignore patterns must survive an unreadable subtree")
	}
}
