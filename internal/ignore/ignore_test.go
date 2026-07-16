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
