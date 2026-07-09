package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/output"
)

// gitInitNoCommitCov2 initializes a bare-minimum git repo (no commit, so it
// never touches the signing agent) — enough for `git rev-parse --show-toplevel`
// to succeed.
func gitInitNoCommitCov2(t *testing.T, dir string) {
	t.Helper()

	if _, err := git.RunOutput(dir, "init", "--quiet"); err != nil {
		t.Fatalf("git init: %v", err)
	}
}

// seedStoreCov2 fabricates a store on disk (a repo-hash dir with a .git marker
// and one document) without going through store.Init/Put — which would commit
// and hit the signing agent. store.ListStores only requires the .git marker and
// store.List merely walks files, so this is enough to exercise listAllStores.
func seedStoreCov2(t *testing.T, dataDir, storeName, key, body string) {
	t.Helper()

	base := filepath.Join(dataDir, "store", storeName)
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	docPath := filepath.Join(base, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(docPath), 0o700); err != nil {
		t.Fatalf("mkdir doc dir: %v", err)
	}

	if err := os.WriteFile(docPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}
}

func TestListAllStoresCov2Empty(t *testing.T) {
	storeTestEnv(t, false, "")

	got := captureStdout(t, func() {
		if err := listAllStores(""); err != nil {
			t.Fatalf("listAllStores: %v", err)
		}
	})

	if !strings.Contains(got, "No stores found") {
		t.Errorf("expected 'No stores found', got %q", got)
	}
}

func TestListAllStoresCov2Populated(t *testing.T) {
	p := storeTestEnv(t, false, "")

	seedStoreCov2(t, p.DataDir, "bonnie-croft", "loch/design.md", "# design\n")

	got := captureStdout(t, func() {
		if err := listAllStores(""); err != nil {
			t.Fatalf("listAllStores: %v", err)
		}
	})

	if !strings.Contains(got, "bonnie-croft") {
		t.Errorf("output %q should list the store name", got)
	}

	if !strings.Contains(got, "loch/design.md") {
		t.Errorf("output %q should list the document key", got)
	}
}

func TestListAllStoresCov2JSON(t *testing.T) {
	p := storeTestEnv(t, false, "")

	jsonOutput = true
	out = output.New(true)

	seedStoreCov2(t, p.DataDir, "kirk-croft", "loch/report.json", "{}")

	got := captureStdout(t, func() {
		if err := listAllStores(""); err != nil {
			t.Fatalf("listAllStores json: %v", err)
		}
	})

	if !strings.Contains(got, "loch/report.json") {
		t.Errorf("JSON output %q should include the key", got)
	}

	if !strings.Contains(got, "kirk-croft") {
		t.Errorf("JSON output %q should include the repo name", got)
	}
}

// TestResolveStoreRepoPathCov2GitDetect covers the git-toplevel detection
// branch (no --repo flag, no GRAITH_REPO_PATH): it should return the repo root.
func TestResolveStoreRepoPathCov2GitDetect(t *testing.T) {
	storeTestEnv(t, false, "")
	t.Setenv("GRAITH_REPO_PATH", "")

	repo := t.TempDir()
	gitInitNoCommitCov2(t, repo)

	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}

	t.Chdir(resolved)

	got, err := resolveStoreRepoPath()
	if err != nil {
		t.Fatalf("resolveStoreRepoPath: %v", err)
	}

	if got != config.ResolvePath(resolved) {
		t.Errorf("got %q, want %q", got, config.ResolvePath(resolved))
	}
}

// TestResolveCurrentRepoCov2GitDetect covers resolveCurrentRepo's git-detection
// branch (env unset, inside a git repo).
func TestResolveCurrentRepoCov2GitDetect(t *testing.T) {
	t.Setenv("GRAITH_REPO_PATH", "")

	repo := t.TempDir()
	gitInitNoCommitCov2(t, repo)

	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}

	t.Chdir(resolved)

	if got := resolveCurrentRepo(); got != config.ResolvePath(resolved) {
		t.Errorf("got %q, want %q", got, config.ResolvePath(resolved))
	}
}
