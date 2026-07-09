package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/store"
)

// storeTestEnv sets up the package globals the store subcommands rely on and
// returns a cleanup that restores them.
func storeTestEnv(t *testing.T, shared bool, repoFlag string) config.Paths {
	t.Helper()

	dataDir := t.TempDir()
	p := config.Paths{DataDir: dataDir}

	prevPaths := paths
	prevOut := out
	prevJSON := jsonOutput
	prevShared := storeSharedFlag
	prevRepo := storeRepoFlag
	prevPutFile := storePutFile
	prevAppendFile := storeAppendFile
	prevListAll := storeListAll

	paths = p
	out = output.New(false)
	jsonOutput = false
	storeSharedFlag = shared
	storeRepoFlag = repoFlag
	storePutFile = ""
	storeAppendFile = ""
	storeListAll = false

	t.Cleanup(func() {
		paths = prevPaths
		out = prevOut
		jsonOutput = prevJSON
		storeSharedFlag = prevShared
		storeRepoFlag = prevRepo
		storePutFile = prevPutFile
		storeAppendFile = prevAppendFile
		storeListAll = prevListAll
	})

	return p
}

func TestResolveStoreRepoPathCovFlag(t *testing.T) {
	storeTestEnv(t, false, "")

	repo := t.TempDir()
	storeRepoFlag = repo

	got, err := resolveStoreRepoPath()
	if err != nil {
		t.Fatalf("resolveStoreRepoPath: %v", err)
	}

	if got != config.ResolvePath(repo) {
		t.Errorf("got %q, want %q", got, config.ResolvePath(repo))
	}
}

func TestResolveStoreRepoPathCovEnv(t *testing.T) {
	storeTestEnv(t, false, "")

	repo := t.TempDir()
	t.Setenv("GRAITH_REPO_PATH", repo)

	got, err := resolveStoreRepoPath()
	if err != nil {
		t.Fatalf("resolveStoreRepoPath: %v", err)
	}

	if got != config.ResolvePath(repo) {
		t.Errorf("got %q, want %q", got, config.ResolvePath(repo))
	}
}

func TestResolveStoreRepoPathCovErrorOutsideGit(t *testing.T) {
	storeTestEnv(t, false, "")

	t.Setenv("GRAITH_REPO_PATH", "")
	os.Unsetenv("GRAITH_REPO_PATH")

	// A fresh temp dir is not a git repo, so detection must fail cleanly.
	t.Chdir(t.TempDir())

	if _, err := resolveStoreRepoPath(); err == nil {
		t.Error("expected error resolving repo path outside a git repo")
	}
}

func TestResolveStorePathCovSharedRepoMutuallyExclusive(t *testing.T) {
	storeTestEnv(t, true, "/some/repo")

	if _, _, err := resolveStorePath(); err == nil {
		t.Error("expected error when both --shared and --repo are set")
	}
}

func TestResolveStorePathCovShared(t *testing.T) {
	p := storeTestEnv(t, true, "")

	sp, label, err := resolveStorePath()
	if err != nil {
		t.Fatalf("resolveStorePath shared: %v", err)
	}

	if label != "shared" {
		t.Errorf("label = %q, want shared", label)
	}

	if sp != store.SharedStorePath(p.DataDir) {
		t.Errorf("shared path = %q, want %q", sp, store.SharedStorePath(p.DataDir))
	}
}

func TestResolveStorePathCovRepo(t *testing.T) {
	p := storeTestEnv(t, false, "")

	repo := t.TempDir()
	storeRepoFlag = repo

	sp, label, err := resolveStorePath()
	if err != nil {
		t.Fatalf("resolveStorePath repo: %v", err)
	}

	resolved := config.ResolvePath(repo)
	if label != resolved {
		t.Errorf("label = %q, want %q", label, resolved)
	}

	if sp != store.StorePath(p.DataDir, resolved) {
		t.Errorf("repo path = %q, want %q", sp, store.StorePath(p.DataDir, resolved))
	}
}

func TestResolveCurrentRepoCovEnv(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("GRAITH_REPO_PATH", repo)

	if got := resolveCurrentRepo(); got != config.ResolvePath(repo) {
		t.Errorf("got %q, want %q", got, config.ResolvePath(repo))
	}
}

func TestResolveCurrentRepoCovNonGit(t *testing.T) {
	t.Setenv("GRAITH_REPO_PATH", "")
	os.Unsetenv("GRAITH_REPO_PATH")
	t.Chdir(t.TempDir())

	if got := resolveCurrentRepo(); got != "" {
		t.Errorf("expected empty string outside git repo, got %q", got)
	}
}

func TestCheckWritePermissionCovNoCurrentRepo(t *testing.T) {
	storeTestEnv(t, false, "")

	t.Setenv("GRAITH_REPO_PATH", "")
	os.Unsetenv("GRAITH_REPO_PATH")
	t.Chdir(t.TempDir())

	// current repo is empty -> always allowed.
	if err := checkWritePermission("/anything"); err != nil {
		t.Errorf("expected nil when no current repo, got %v", err)
	}
}

func TestCheckWritePermissionCovCrossRepoRejected(t *testing.T) {
	storeTestEnv(t, false, "")

	current := t.TempDir()
	t.Setenv("GRAITH_REPO_PATH", current)

	storeRepoFlag = "/a/different/croft"

	if err := checkWritePermission("/a/different/croft"); err == nil {
		t.Error("expected cross-repo write to be rejected")
	}
}

func TestCheckWritePermissionCovSameRepoAllowed(t *testing.T) {
	storeTestEnv(t, false, "")

	current := t.TempDir()
	t.Setenv("GRAITH_REPO_PATH", current)

	resolved := config.ResolvePath(current)
	storeRepoFlag = resolved

	if err := checkWritePermission(resolved); err != nil {
		t.Errorf("same-repo write should be allowed, got %v", err)
	}
}

func TestInGraithSessionWithNoRepoCov(t *testing.T) {
	t.Run("session with no repo", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "braw-session")
		t.Setenv("GRAITH_REPO_PATH", "")
		os.Unsetenv("GRAITH_REPO_PATH")
		t.Chdir(t.TempDir())

		if !inGraithSessionWithNoRepo() {
			t.Error("expected true for session with no repo context")
		}
	})

	t.Run("no session id", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "")
		os.Unsetenv("GRAITH_SESSION_ID")

		if inGraithSessionWithNoRepo() {
			t.Error("expected false when GRAITH_SESSION_ID is unset")
		}
	})

	t.Run("session with repo", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "braw-session")
		t.Setenv("GRAITH_REPO_PATH", t.TempDir())

		if inGraithSessionWithNoRepo() {
			t.Error("expected false when repo context is present")
		}
	})
}

func TestStorePutGetListRmRoundTripCov(t *testing.T) {
	storeTestEnv(t, true, "")

	key := "loch/design.md"
	body := "# Bonnie design\n"

	if err := storePutCmd.RunE(storePutCmd, []string{key, body}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Get writes to stdout via fmt.Print; capture it.
	got := captureStdout(t, func() {
		if err := storeGetCmd.RunE(storeGetCmd, []string{key}); err != nil {
			t.Fatalf("get: %v", err)
		}
	})

	if got != body {
		t.Errorf("get returned %q, want %q", got, body)
	}

	// List should not error and should print the key.
	listOut := captureStdout(t, func() {
		if err := storeListCmd.RunE(storeListCmd, nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})

	if !strings.Contains(listOut, key) {
		t.Errorf("list output missing key %q: %s", key, listOut)
	}

	// Remove and confirm it's gone.
	if err := storeRmCmd.RunE(storeRmCmd, []string{key}); err != nil {
		t.Fatalf("rm: %v", err)
	}

	if err := storeGetCmd.RunE(storeGetCmd, []string{key}); err == nil {
		t.Error("expected get of removed key to fail")
	}
}

func TestStoreGetCovNotFound(t *testing.T) {
	storeTestEnv(t, true, "")

	// Initialize an empty shared store so the store dir exists but the key
	// doesn't.
	sp := store.SharedStorePath(paths.DataDir)
	if err := store.Init(sp); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := storeGetCmd.RunE(storeGetCmd, []string{"haar/missing.md"})
	if err == nil {
		t.Fatal("expected not-found error")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should mention 'not found'", err)
	}
}

func TestStoreAppendCov(t *testing.T) {
	storeTestEnv(t, true, "")

	key := "logs/builds.jsonl"

	if err := storeAppendCmd.RunE(storeAppendCmd, []string{key, `{"status":"pass"}`}); err != nil {
		t.Fatalf("append 1: %v", err)
	}

	if err := storeAppendCmd.RunE(storeAppendCmd, []string{key, `{"status":"fail"}`}); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	got := captureStdout(t, func() {
		if err := storeGetCmd.RunE(storeGetCmd, []string{key}); err != nil {
			t.Fatalf("get: %v", err)
		}
	})

	lines := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1
	if lines != 2 {
		t.Errorf("expected 2 appended lines, got %d in %q", lines, got)
	}
}

func TestStorePutCovJSONOutput(t *testing.T) {
	storeTestEnv(t, true, "")
	jsonOutput = true
	out = output.New(true)

	got := captureStdout(t, func() {
		if err := storePutCmd.RunE(storePutCmd, []string{"kirk/report.json", "{}"}); err != nil {
			t.Fatalf("put json: %v", err)
		}
	})

	if !strings.Contains(got, "kirk/report.json") {
		t.Errorf("JSON output missing key: %s", got)
	}
}

func TestStorePutCovFileBody(t *testing.T) {
	storeTestEnv(t, true, "")

	bodyFile := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(bodyFile, []byte("from a file"), 0o600); err != nil {
		t.Fatal(err)
	}

	storePutFile = bodyFile

	if err := storePutCmd.RunE(storePutCmd, []string{"glen/notes.md"}); err != nil {
		t.Fatalf("put from file: %v", err)
	}

	got := captureStdout(t, func() {
		if err := storeGetCmd.RunE(storeGetCmd, []string{"glen/notes.md"}); err != nil {
			t.Fatalf("get: %v", err)
		}
	})

	if got != "from a file" {
		t.Errorf("got %q, want %q", got, "from a file")
	}
}

func TestStoreListCovEmpty(t *testing.T) {
	storeTestEnv(t, true, "")

	// Init an empty store so List succeeds with zero entries.
	sp := store.SharedStorePath(paths.DataDir)
	if err := store.Init(sp); err != nil {
		t.Fatalf("init: %v", err)
	}

	got := captureStdout(t, func() {
		if err := storeListCmd.RunE(storeListCmd, nil); err != nil {
			t.Fatalf("list empty: %v", err)
		}
	})

	if !strings.Contains(got, "No documents found") {
		t.Errorf("expected 'No documents found', got %q", got)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written. The store get/list commands write directly to os.Stdout, while
// success/JSON messages go through the package-global `out` writer — so we
// rebind that to the same pipe (output.New snapshots os.Stdout at creation).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	orig := os.Stdout
	os.Stdout = w

	prevOut := out
	out = output.NewWithWriter(jsonOutput, w)

	defer func() { out = prevOut }()

	done := make(chan string, 1)

	go func() {
		var sb strings.Builder

		buf := make([]byte, 4096)

		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}

			if err != nil {
				break
			}
		}

		done <- sb.String()
	}()

	fn()

	_ = w.Close()
	os.Stdout = orig

	return <-done
}
