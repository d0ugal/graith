package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/spf13/cobra"
)

// completeAgentNames reads the package-global cfg. Save and restore it so these
// tests don't leak state into siblings that also touch cfg.
func withCfgCov(t *testing.T, c *config.Config, fn func()) {
	t.Helper()

	prev := cfg
	cfg = c

	defer func() { cfg = prev }()

	fn()
}

func TestCompleteAgentNamesCovNilConfig(t *testing.T) {
	withCfgCov(t, nil, func() {
		names, directive := completeAgentNames(nil, nil, "")
		if names != nil {
			t.Errorf("expected nil names with nil cfg, got %v", names)
		}

		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("directive = %v, want NoFileComp", directive)
		}
	})
}

func TestCompleteAgentNamesCovSorted(t *testing.T) {
	c := &config.Config{
		Agents: map[string]config.Agent{
			"whin":  {Command: "whin"},
			"braw":  {Command: "braw"},
			"canny": {Command: "canny"},
		},
	}

	withCfgCov(t, c, func() {
		names, directive := completeAgentNames(nil, nil, "")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("directive = %v, want NoFileComp", directive)
		}

		want := []string{"braw", "canny", "whin"}
		if len(names) != len(want) {
			t.Fatalf("got %v, want %v", names, want)
		}

		for i := range want {
			if names[i] != want[i] {
				t.Errorf("names not sorted: got %v, want %v", names, want)
			}
		}
	})
}

func TestCompleteAgentNamesCovEmpty(t *testing.T) {
	withCfgCov(t, &config.Config{Agents: map[string]config.Agent{}}, func() {
		names, _ := completeAgentNames(nil, nil, "")
		if len(names) != 0 {
			t.Errorf("expected no names for empty agents map, got %v", names)
		}
	})
}

func TestCompleteSessionNamesCovArgsShortCircuit(t *testing.T) {
	// When an argument is already present, completion must not attempt to
	// connect to the daemon — it returns nil immediately.
	names, directive := completeSessionNames(nil, []string{"braw"}, "")
	if names != nil {
		t.Errorf("expected nil names when args present, got %v", names)
	}

	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}
}

func TestCompleteBranchNamesCovLocalAndRemote(t *testing.T) {
	repo := t.TempDir()
	initBranchRepo(t, repo)

	// Create a couple of extra local branches.
	if _, err := git.RunOutput(repo, "branch", "canny"); err != nil {
		t.Fatalf("create branch canny: %v", err)
	}

	if _, err := git.RunOutput(repo, "branch", "thrawn"); err != nil {
		t.Fatalf("create branch thrawn: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("repo", repo, "")

	branches, directive := completeBranchNames(cmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}

	found := make(map[string]bool)
	for _, b := range branches {
		found[b] = true
	}

	if !found["canny"] || !found["thrawn"] {
		t.Errorf("expected local branches canny and thrawn, got %v", branches)
	}
}

func TestCompleteBranchNamesCovDedupesOriginPrefix(t *testing.T) {
	// A fake "remote" ref is created so we can confirm the origin/ prefix is
	// stripped and de-duplicated against the local branch of the same name.
	repo := t.TempDir()
	initBranchRepo(t, repo)

	headRef, err := git.RunOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	sha := headRef
	// Fabricate refs/remotes/origin/main pointing at HEAD.
	if _, err := git.RunOutput(repo, "update-ref", "refs/remotes/origin/main", sha); err != nil {
		t.Fatalf("update-ref: %v", err)
	}

	// And an origin/HEAD symbolic-ish ref which must be filtered out.
	if _, err := git.RunOutput(repo, "update-ref", "refs/remotes/origin/HEAD", sha); err != nil {
		t.Fatalf("update-ref HEAD: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("repo", repo, "")

	branches, _ := completeBranchNames(cmd, nil, "")

	count := 0

	for _, b := range branches {
		if b == "main" {
			count++
		}

		if b == "HEAD" {
			t.Errorf("HEAD ref should be filtered out, got %v", branches)
		}
	}

	if count != 1 {
		t.Errorf("expected exactly one 'main' after dedup, got %d in %v", count, branches)
	}
}

func TestCompleteBranchNamesCovNonGitDir(t *testing.T) {
	dir := t.TempDir()

	cmd := &cobra.Command{}
	cmd.Flags().String("repo", dir, "")

	branches, directive := completeBranchNames(cmd, nil, "")
	if branches != nil {
		t.Errorf("expected nil branches for non-git dir, got %v", branches)
	}

	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}
}

func TestCompleteBranchNamesCovFallsBackToCwd(t *testing.T) {
	repo := t.TempDir()
	initBranchRepo(t, repo)

	// EvalSymlinks so that the repo dir matches git's canonical toplevel on
	// macOS (where /var is a symlink to /private/var).
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}

	t.Chdir(resolved)

	// No --repo flag: it must fall back to the current working directory.
	cmd := &cobra.Command{}
	cmd.Flags().String("repo", "", "")

	branches, directive := completeBranchNames(cmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}

	if len(branches) == 0 {
		t.Error("expected at least the default branch when falling back to cwd")
	}
}

// initBranchRepo creates a git repo with one commit so it has at least one
// branch (the default branch).
func initBranchRepo(t *testing.T, dir string) {
	t.Helper()

	run := func(args ...string) {
		if _, err := git.RunOutput(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	run("init", "--quiet")
	run("config", "user.name", "graith")
	run("config", "user.email", "graith@localhost")
	run("config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(dir, "croft.txt"), []byte("bothy\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	run("add", ".")
	run("commit", "--quiet", "-m", "first")
}
