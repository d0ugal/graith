package ignore_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/ignore"
)

// The conformance suite pins graith's gitignore matcher to Git's own behaviour.
// For each case it materialises a real repo (git init + .gitignore files +
// the candidate paths), asks `git check-ignore` whether each path is ignored,
// and asserts ignore.Dir agrees. `git check-ignore` is the behavioural oracle,
// so these tests document exactly which corner of the gitignore grammar graith
// supports — and fail loudly if the underlying library ever drifts from Git.

// gitConfCase is one conformance scenario: a set of .gitignore files, a tree of
// paths to create, and the paths whose ignore status we compare against Git.
type gitConfCase struct {
	name string
	// gitignores maps a repo-relative .gitignore path to its contents, e.g.
	// ".gitignore" or "sub/.gitignore" or ".git/info/exclude".
	gitignores map[string]string
	// dirs are directories to create (relative, slash-separated).
	dirs []string
	// files are files to create (relative, slash-separated).
	files []string
	// check are the repo-relative paths to compare against git check-ignore.
	check []string
}

func requireGit(t *testing.T) string {
	t.Helper()

	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available; skipping conformance corpus")
	}

	return git
}

// buildRepo materialises the case into a fresh git repo and returns its root.
func (c gitConfCase) buildRepo(t *testing.T, git string) string {
	t.Helper()

	root := t.TempDir()

	run := func(args ...string) {
		t.Helper()

		cmd := exec.Command(git, args...)
		cmd.Dir = root
		// A pristine environment so a developer's global excludesfile or
		// committer identity can't perturb the oracle.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init")

	for _, d := range c.dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	for _, f := range c.files {
		p := filepath.Join(root, filepath.FromSlash(f))

		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for name, body := range c.gitignores {
		p := filepath.Join(root, filepath.FromSlash(name))

		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

// gitIgnores reports whether `git check-ignore` considers rel ignored.
func gitIgnores(t *testing.T, git, root, rel string) bool {
	t.Helper()

	cmd := exec.Command(git, "check-ignore", "-q", "--", filepath.FromSlash(rel))
	cmd.Dir = root

	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)

	err := cmd.Run()
	if err == nil {
		return true // exit 0: path is ignored
	}

	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false // exit 1: path is not ignored
	}

	t.Fatalf("git check-ignore %q: %v", rel, err)

	return false
}

// isDir reports whether rel exists as a directory in root.
func isDir(root, rel string) bool {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))

	return err == nil && fi.IsDir()
}

func conformanceCases() []gitConfCase {
	return []gitConfCase{
		{
			name:       "blank lines and comments",
			gitignores: map[string]string{".gitignore": "\n# a comment\n\n   \n*.log\n"},
			files:      []string{"run.log", "notes.txt", "comment"},
			check:      []string{"run.log", "notes.txt", "comment", "a"},
		},
		{
			name:       "escaped hash pattern",
			gitignores: map[string]string{".gitignore": "\\#keep\n"},
			files:      []string{"#keep", "keep"},
			check:      []string{"#keep", "keep"},
		},
		{
			name:       "negation re-includes",
			gitignores: map[string]string{".gitignore": "*.log\n!important.log\n"},
			files:      []string{"run.log", "important.log"},
			check:      []string{"run.log", "important.log"},
		},
		{
			name:       "directory-only pattern",
			gitignores: map[string]string{".gitignore": "build/\n"},
			dirs:       []string{"build", "src"},
			files:      []string{"build/out.bin", "src/build", "keep.build"},
			check:      []string{"build", "build/out.bin", "src/build", "keep.build"},
		},
		{
			name:       "leading slash anchors to root",
			gitignores: map[string]string{".gitignore": "/foo\n"},
			dirs:       []string{"sub"},
			files:      []string{"foo", "sub/foo"},
			check:      []string{"foo", "sub/foo"},
		},
		{
			name:       "trailing slash on nested path",
			gitignores: map[string]string{".gitignore": "a/b/\n"},
			dirs:       []string{"a/b"},
			files:      []string{"a/b/c.txt", "a/b.txt"},
			check:      []string{"a/b", "a/b/c.txt", "a/b.txt"},
		},
		{
			name:       "single star does not cross slash",
			gitignores: map[string]string{".gitignore": "foo/*.go\n"},
			dirs:       []string{"foo", "foo/bar"},
			files:      []string{"foo/a.go", "foo/bar/b.go"},
			check:      []string{"foo/a.go", "foo/bar/b.go"},
		},
		{
			name:       "double star matches across directories",
			gitignores: map[string]string{".gitignore": "**/*.go\n"},
			dirs:       []string{"foo", "foo/bar"},
			files:      []string{"a.go", "foo/b.go", "foo/bar/c.go", "foo/d.ts"},
			check:      []string{"a.go", "foo/b.go", "foo/bar/c.go", "foo/d.ts"},
		},
		{
			name:       "leading double star",
			gitignores: map[string]string{".gitignore": "**/logs\n"},
			dirs:       []string{"a", "a/b", "a/b/logs"},
			files:      []string{"a/b/logs/x.txt", "logs/y.txt"},
			check:      []string{"a/b/logs", "a/b/logs/x.txt", "logs"},
		},
		{
			name:       "trailing double star",
			gitignores: map[string]string{".gitignore": "abc/**\n"},
			dirs:       []string{"abc", "abc/def"},
			files:      []string{"abc/x.txt", "abc/def/y.txt", "abcd.txt"},
			check:      []string{"abc/x.txt", "abc/def/y.txt", "abcd.txt"},
		},
		{
			name:       "question mark single char",
			gitignores: map[string]string{".gitignore": "file?.txt\n"},
			files:      []string{"file1.txt", "file12.txt", "file.txt"},
			check:      []string{"file1.txt", "file12.txt", "file.txt"},
		},
		{
			name:       "character class",
			gitignores: map[string]string{".gitignore": "*.[oa]\n"},
			files:      []string{"main.o", "lib.a", "main.c"},
			check:      []string{"main.o", "lib.a", "main.c"},
		},
		{
			name: "nested gitignore scopes to subtree",
			gitignores: map[string]string{
				".gitignore":     "*.log\n",
				"sub/.gitignore": "*.tmp\n",
			},
			dirs:  []string{"sub", "other"},
			files: []string{"a.tmp", "sub/b.tmp", "sub/c.log", "other/d.tmp"},
			check: []string{"a.tmp", "sub/b.tmp", "sub/c.log", "other/d.tmp"},
		},
		{
			name: "nested gitignore negation",
			gitignores: map[string]string{
				".gitignore":     "*.log\n",
				"sub/.gitignore": "!keep.log\n",
			},
			dirs:  []string{"sub"},
			files: []string{"a.log", "sub/keep.log", "sub/other.log"},
			check: []string{"a.log", "sub/keep.log", "sub/other.log"},
		},
		{
			name:       "git info exclude",
			gitignores: map[string]string{".git/info/exclude": "*.secret\n"},
			files:      []string{"api.secret", "api.txt"},
			check:      []string{"api.secret", "api.txt"},
		},
		{
			name:       "non-ascii filename",
			gitignores: map[string]string{".gitignore": "café/\n*.日本\n"},
			dirs:       []string{"café"},
			files:      []string{"café/x.txt", "data.日本", "data.txt"},
			check:      []string{"café", "café/x.txt", "data.日本", "data.txt"},
		},
		{
			name:       "middle slash anchors",
			gitignores: map[string]string{".gitignore": "doc/frotz\n"},
			dirs:       []string{"doc", "a", "a/doc"},
			files:      []string{"doc/frotz", "a/doc/frotz"},
			check:      []string{"doc/frotz", "a/doc/frotz"},
		},
		{
			name:       "star matches leading dot",
			gitignores: map[string]string{".gitignore": "*.o\n"},
			files:      []string{".hidden.o", "vis.o"},
			check:      []string{".hidden.o", "vis.o"},
		},
		{
			// A leading space is a significant part of the filename: the pattern
			// " foo" matches the file " foo" but not "foo". Guards against the
			// matcher stripping whitespace from the path it is handed.
			name:       "leading space in filename",
			gitignores: map[string]string{".gitignore": " foo\n"},
			files:      []string{" foo", "foo"},
			check:      []string{" foo", "foo"},
		},
		{
			// An escaped trailing space in a pattern ("bar\ ") is preserved by
			// Git and must match the file "bar " (trailing space) but not "bar".
			name:       "escaped trailing space",
			gitignores: map[string]string{".gitignore": "bar\\ \n"},
			files:      []string{"bar ", "bar"},
			check:      []string{"bar ", "bar"},
		},
	}
}

// TestConformanceLinkedWorktree pins graith's matcher to `git check-ignore` in a
// real linked worktree, where .git is a "gitdir:" pointer file and the shared
// info/exclude lives in the common git directory (not under the worktree root).
// It also confirms priority ordering: a worktree-local .gitignore negation
// re-includes a path the shared exclude would otherwise ignore.
func TestConformanceLinkedWorktree(t *testing.T) {
	git := requireGit(t)

	runGit := func(dir string, args ...string) {
		t.Helper()

		cmd := exec.Command(git, args...)
		cmd.Dir = dir

		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	main := t.TempDir()

	runGit(main, "init")
	runGit(main, "config", "user.email", "graith@example.com")
	runGit(main, "config", "user.name", "graith")

	// A worktree can only be added once the repo has a commit.
	if err := os.WriteFile(filepath.Join(main, "seed.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	runGit(main, "add", "-A")
	runGit(main, "commit", "-m", "seed")

	// Shared exclude in the common git dir — reachable only via the worktree's
	// commondir pointer, never under the worktree root.
	if err := os.MkdirAll(filepath.Join(main, ".git", "info"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(main, ".git", "info", "exclude"), []byte("*.secret\ncommon-only/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The linked worktree lives in its own (pre-existing) parent dir.
	wt := filepath.Join(t.TempDir(), "bothy")
	runGit(main, "worktree", "add", "-q", wt)

	// A worktree-local .gitignore layered on top of the shared exclude, including
	// a negation that must win over the lower-priority exclude.
	if err := os.WriteFile(filepath.Join(wt, ".gitignore"), []byte("local.log\n!keep.secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"api.secret", "keep.secret", "app.log", "local.log", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(wt, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Join(wt, "common-only"), 0o750); err != nil {
		t.Fatal(err)
	}

	m := ignore.Dir(wt)

	for _, rel := range []string{"api.secret", "keep.secret", "app.log", "local.log", "keep.txt", "common-only"} {
		want := gitIgnores(t, git, wt, rel)
		got := m.Match(rel, isDir(wt, rel))

		if got != want {
			t.Errorf("Match(%q, isDir=%v) = %v, git check-ignore = %v",
				rel, isDir(wt, rel), got, want)
		}
	}
}

// TestConformanceWithGit is the characterization corpus: graith's matcher must
// agree with `git check-ignore` on every candidate path.
func TestConformanceWithGit(t *testing.T) {
	git := requireGit(t)

	for _, c := range conformanceCases() {
		t.Run(c.name, func(t *testing.T) {
			root := c.buildRepo(t, git)
			m := ignore.Dir(root)

			for _, rel := range c.check {
				want := gitIgnores(t, git, root, rel)
				got := m.Match(rel, isDir(root, rel))

				if got != want {
					t.Errorf("Match(%q, isDir=%v) = %v, git check-ignore = %v",
						rel, isDir(root, rel), got, want)
				}
			}
		})
	}
}
