package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/store"
	"github.com/d0ugal/graith/internal/testutil"
)

func TestValidateKey(t *testing.T) {
	t.Run("valid keys", func(t *testing.T) {
		validKeys := []string{
			"loch/api.md",
			"kirk/2026-06-15",
			"neep",
			"glen/wynd/kirk",
			"loch/findings.json",
			"a",
		}
		for _, key := range validKeys {
			if err := store.ValidateKey(key); err != nil {
				t.Errorf("ValidateKey(%q) unexpected error: %v", key, err)
			}
		}
	})

	t.Run("empty key", func(t *testing.T) {
		if err := store.ValidateKey(""); err == nil {
			t.Error("ValidateKey(\"\") expected error, got nil")
		}
	})

	t.Run("leading slash", func(t *testing.T) {
		keys := []string{"/foo", "/foo/bar", "/"}
		for _, key := range keys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error for leading slash, got nil", key)
			}
		}
	})

	t.Run("dotdot component", func(t *testing.T) {
		dotdotKeys := []string{
			"..",
			"../foo",
			"foo/..",
			"foo/../bar",
		}
		for _, key := range dotdotKeys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error for dotdot component, got nil", key)
			}
		}

		okKeys := []string{
			"glen/..braw",
			"glen/braw..",
		}
		for _, key := range okKeys {
			if err := store.ValidateKey(key); err != nil {
				t.Errorf("ValidateKey(%q) unexpected error: %v", key, err)
			}
		}
	})

	t.Run("dot-git component", func(t *testing.T) {
		gitKeys := []string{
			".git/config",
			".git/hooks/pre-commit",
			"foo/.git/bar",
			".git",
			".GIT/hooks/pre-commit",
			".Git/config",
			".GiT/objects",
			"foo/.GIT/bar",
		}
		for _, key := range gitKeys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error for .git component, got nil", key)
			}
		}
	})

	t.Run("dot component", func(t *testing.T) {
		dotKeys := []string{
			".",
			"./foo",
			"foo/./bar",
		}
		for _, key := range dotKeys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error for dot component, got nil", key)
			}
		}
	})

	t.Run("store.lock", func(t *testing.T) {
		lockKeys := []string{"store.lock", "STORE.LOCK", "Store.Lock"}
		for _, key := range lockKeys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error, got nil", key)
			}
		}
		// store.lock in a subdirectory is fine
		if err := store.ValidateKey("glen/store.lock"); err != nil {
			t.Errorf("ValidateKey(\"glen/store.lock\") unexpected error: %v", err)
		}
	})

	t.Run("glob and pathspec characters", func(t *testing.T) {
		globKeys := []string{
			"*.md",
			"foo?bar",
			"foo[0]",
			":(glob)*",
		}
		for _, key := range globKeys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error for glob chars, got nil", key)
			}
		}
	})

	t.Run("backslash", func(t *testing.T) {
		if err := store.ValidateKey("foo\\bar"); err == nil {
			t.Error("ValidateKey(\"foo\\\\bar\") expected error, got nil")
		}
	})

	t.Run("control characters", func(t *testing.T) {
		keys := []string{
			"foo\x00bar",         // NUL byte
			"foo\x01bar",         // SOH
			"foo\nbar",           // newline
			"foo\tbar",           // tab
			"foo\x7fbar",         // DEL
			string([]byte{0x00}), // explicit NUL
		}
		for _, key := range keys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error for control character, got nil", key)
			}
		}
	})

	t.Run("leading dash", func(t *testing.T) {
		keys := []string{"-foo", "-foo/bar"}
		for _, key := range keys {
			if err := store.ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) expected error for leading dash, got nil", key)
			}
		}
		// dash in the middle or at end of component is fine
		okKeys := []string{"glen-braw", "glen/wynd-kirk/loch"}
		for _, key := range okKeys {
			if err := store.ValidateKey(key); err != nil {
				t.Errorf("ValidateKey(%q) unexpected error: %v", key, err)
			}
		}
	})
}

func TestInit(t *testing.T) {
	testutil.IsolateGit(t)

	dir := filepath.Join(t.TempDir(), "store")

	if err := store.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// .git directory should exist
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatalf(".git not created: %v", err)
	}

	if !info.IsDir() {
		t.Fatal(".git is not a directory")
	}

	// Calling Init again should be idempotent
	if err := store.Init(dir); err != nil {
		t.Fatalf("Init (idempotent): %v", err)
	}
}

// TestInitUsesStoreLock verifies config migration is serialized with document
// writes. Without this lock, concurrent Init calls race on .git/config.lock and
// fail instead of waiting for one another.
func TestInitUsesStoreLock(t *testing.T) {
	dir := newTestStore(t)
	lockPath := filepath.Join(dir, "store.lock")

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockFile.Close() }()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- store.Init(dir) }()

	select {
	case err := <-done:
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

		t.Fatalf("Init returned before store lock was released: %v", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: Init is blocked on the store lock.
	}

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Init after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Init remained blocked after store lock release")
	}
}

func TestInitFailsClosedOnGitMarkerStatError(t *testing.T) {
	testutil.IsolateGit(t)

	dir := t.TempDir()
	if err := os.Symlink(".git", filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}

	err := store.Init(dir)
	if err == nil || !strings.Contains(err.Error(), "inspect git repository") {
		t.Fatalf("Init error = %v, want inspect git repository error", err)
	}
}

// TestInitRepairsInheritedCommitSigning is the regression test for store
// commits inheriting a developer's global SSH signing configuration. Init is
// called before each CLI store operation, so it must also repair stores created
// by an older graith version rather than only configuring brand-new repos.
func TestInitRepairsInheritedCommitSigning(t *testing.T) {
	testutil.IsolateGit(t)

	globalConfig := filepath.Join(t.TempDir(), "gitconfig")

	configBody := "[commit]\n\tgpgsign = true\n[gpg]\n\tformat = ssh\n[user]\n\tsigningkey = /nonexistent/thrawn.pub\n"
	if err := os.WriteFile(globalConfig, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("SSH_AUTH_SOCK", "")

	dir := filepath.Join(t.TempDir(), "store")
	if err := store.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Simulate a pre-fix store whose local config did not override the user's
	// global commit.gpgsign=true setting.
	cmd := testutil.GitCommand("config", "--local", "commit.gpgsign", "true")

	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("enable local signing: %v\n%s", err, out)
	}

	if err := store.Init(dir); err != nil {
		t.Fatalf("Init existing store: %v", err)
	}

	if err := store.Put(dir, "loch/braw.md", "bonnie"); err != nil {
		t.Fatalf("Put inherited signing config: %v", err)
	}

	cmd = testutil.GitCommand("config", "--local", "--bool", "commit.gpgsign")
	cmd.Dir = dir
	// Disable the helper's command-scope commit.gpgsign=false so this assertion
	// reads the repository-local value that Init persisted.
	cmd.Env = testutil.GitEnv("GIT_CONFIG_COUNT=0")

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("read local signing config: %v", err)
	}

	if got := strings.TrimSpace(string(out)); got != "false" {
		t.Errorf("local commit.gpgsign = %q, want false", got)
	}
}

// TestRemoveIgnoresInheritedCommitSigning verifies every committing operation
// is safe even when a caller reaches Remove without first running Init.
func TestRemoveIgnoresInheritedCommitSigning(t *testing.T) {
	const key = "loch/skelf.md"

	dir := newTestStore(t)

	if err := store.Put(dir, key, "braw"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	for _, args := range [][]string{
		{"config", "--local", "commit.gpgsign", "true"},
		{"config", "--local", "gpg.format", "ssh"},
		{"config", "--local", "user.signingkey", "/nonexistent/thrawn.pub"},
	} {
		cmd := testutil.GitCommand(args...)
		cmd.Dir = dir

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Disable TestMain's command-scope signing override so this exercises the
	// hostile repository-local config planted above.
	t.Setenv("GIT_CONFIG_COUNT", "0")

	if err := store.Remove(dir, key); err != nil {
		t.Fatalf("Remove inherited signing config: %v", err)
	}
}

func newTestStore(t *testing.T) string {
	t.Helper()
	testutil.IsolateGit(t)

	dir := filepath.Join(t.TempDir(), "store")
	if err := store.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	return dir
}

func TestPutGet(t *testing.T) {
	dir := newTestStore(t)

	const (
		key  = "loch/api.md"
		body = "# Loch Design\n\nBraw content here."
	)

	if err := store.Put(dir, key, body); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(dir, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got != body {
		t.Errorf("Get returned %q, want %q", got, body)
	}

	// File should exist on disk
	info, err := os.Stat(filepath.Join(dir, key))
	if err != nil {
		t.Fatalf("file not found on disk: %v", err)
	}

	if info.IsDir() {
		t.Fatal("expected file, got directory")
	}
}

func TestPutOverwrite(t *testing.T) {
	dir := newTestStore(t)

	const (
		key    = "loch/neep.md"
		first  = "auld value"
		second = "braw value"
	)

	if err := store.Put(dir, key, first); err != nil {
		t.Fatalf("Put (first): %v", err)
	}

	if err := store.Put(dir, key, second); err != nil {
		t.Fatalf("Put (second): %v", err)
	}

	got, err := store.Get(dir, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got != second {
		t.Errorf("Get returned %q, want %q (braw value)", got, second)
	}
}

func TestPutIdenticalContent(t *testing.T) {
	dir := newTestStore(t)

	const (
		key  = "loch/same.md"
		body = "bide content"
	)

	if err := store.Put(dir, key, body); err != nil {
		t.Fatalf("Put (first): %v", err)
	}

	if err := store.Put(dir, key, body); err != nil {
		t.Fatalf("Put (identical): %v", err)
	}

	got, err := store.Get(dir, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got != body {
		t.Errorf("Get returned %q, want %q", got, body)
	}
}

func TestGetNotFound(t *testing.T) {
	dir := newTestStore(t)

	_, err := store.Get(dir, "thrawn/lost.md")
	if err == nil {
		t.Error("Get on nonexistent key expected error, got nil")
	}
}

func TestPutInvalidKey(t *testing.T) {
	dir := newTestStore(t)

	invalidKeys := []string{
		"../escape",
		"",
	}
	for _, key := range invalidKeys {
		if err := store.Put(dir, key, "neep"); err == nil {
			t.Errorf("Put(%q) expected error for invalid key, got nil", key)
		}
	}
}

func TestPutCreatesGitCommit(t *testing.T) {
	dir := newTestStore(t)

	const key = "loch/api.md"
	if err := store.Put(dir, key, "bonnie"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Check that a git commit was created with the right message.
	cmd := testutil.GitCommand("log", "--oneline", "-1")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}

	msg := string(out)

	want := "store: update " + key
	if !strings.Contains(msg, want) {
		t.Errorf("git log output %q does not contain %q", msg, want)
	}
}

func TestCommitMessage(t *testing.T) {
	t.Run("without env vars", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "")
		t.Setenv("GRAITH_SESSION_NAME", "")
		t.Setenv("GRAITH_AGENT_TYPE", "")

		msg := store.CommitMessage("update", "loch/api.md")

		want := "store: update loch/api.md"
		if msg != want {
			t.Errorf("CommitMessage = %q, want %q", msg, want)
		}
	})

	t.Run("with session id and name", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "braw123")
		t.Setenv("GRAITH_SESSION_NAME", "canny-overlay")
		t.Setenv("GRAITH_AGENT_TYPE", "")

		msg := store.CommitMessage("update", "loch/api.md")
		if !strings.Contains(msg, "store: update loch/api.md") {
			t.Errorf("CommitMessage missing first line: %q", msg)
		}

		if !strings.Contains(msg, "session: canny-overlay (braw123)") {
			t.Errorf("CommitMessage missing session trailer: %q", msg)
		}
	})

	t.Run("with session id only", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "braw123")
		t.Setenv("GRAITH_SESSION_NAME", "")
		t.Setenv("GRAITH_AGENT_TYPE", "")

		msg := store.CommitMessage("update", "loch/api.md")
		if !strings.Contains(msg, "session: braw123") {
			t.Errorf("CommitMessage missing session trailer: %q", msg)
		}
	})

	t.Run("with all env vars", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "braw123")
		t.Setenv("GRAITH_SESSION_NAME", "canny-overlay")
		t.Setenv("GRAITH_AGENT_TYPE", "claude")

		msg := store.CommitMessage("update", "loch/api.md")
		if !strings.Contains(msg, "store: update loch/api.md") {
			t.Errorf("CommitMessage missing first line: %q", msg)
		}

		if !strings.Contains(msg, "session: canny-overlay (braw123)") {
			t.Errorf("CommitMessage missing session trailer: %q", msg)
		}

		if !strings.Contains(msg, "agent: claude") {
			t.Errorf("CommitMessage missing agent trailer: %q", msg)
		}
	})
}

func TestList(t *testing.T) {
	dir := newTestStore(t)

	// Put 3 keys across 2 prefixes
	if err := store.Put(dir, "glen/ane.md", "ane"); err != nil {
		t.Fatalf("Put glen/ane.md: %v", err)
	}

	if err := store.Put(dir, "glen/twa.md", "twa"); err != nil {
		t.Fatalf("Put glen/twa.md: %v", err)
	}

	if err := store.Put(dir, "wynd/three.md", "three"); err != nil {
		t.Fatalf("Put wynd/three.md: %v", err)
	}

	t.Run("list all", func(t *testing.T) {
		entries, err := store.List(dir, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		if len(entries) != 3 {
			t.Errorf("List returned %d entries, want 3", len(entries))
		}

		for _, e := range entries {
			if e.UpdatedAt.IsZero() {
				t.Errorf("entry %q has zero UpdatedAt", e.Key)
			}
		}
	})

	t.Run("list with prefix", func(t *testing.T) {
		entries, err := store.List(dir, "glen")
		if err != nil {
			t.Fatalf("List with prefix: %v", err)
		}

		if len(entries) != 2 {
			t.Errorf("List(glen) returned %d entries, want 2", len(entries))
		}

		for _, e := range entries {
			if !strings.HasPrefix(e.Key, "glen/") {
				t.Errorf("entry %q does not have prefix glen/", e.Key)
			}
		}
	})

	t.Run("list nonexistent prefix", func(t *testing.T) {
		entries, err := store.List(dir, "thrawn")
		if err != nil {
			t.Fatalf("List thrawn prefix: %v", err)
		}

		if len(entries) != 0 {
			t.Errorf("List(thrawn) returned %d entries, want 0", len(entries))
		}
	})
}

func TestListRejectsTraversal(t *testing.T) {
	dir := newTestStore(t)

	traversalPrefixes := []string{
		"../../etc",
		"../../../",
		"..",
	}
	for _, prefix := range traversalPrefixes {
		_, err := store.List(dir, prefix)
		if err == nil {
			t.Errorf("List(%q) expected error, got nil", prefix)
		}
	}
}

func TestListEmptyStore(t *testing.T) {
	dir := newTestStore(t)

	entries, err := store.List(dir, "")
	if err != nil {
		t.Fatalf("List on empty store: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("List on empty store returned %d entries, want 0", len(entries))
	}
}

func TestRemove(t *testing.T) {
	dir := newTestStore(t)

	if err := store.Put(dir, "loch/ane.md", "ane"); err != nil {
		t.Fatalf("Put loch/ane.md: %v", err)
	}

	if err := store.Put(dir, "loch/twa.md", "twa"); err != nil {
		t.Fatalf("Put loch/twa.md: %v", err)
	}

	if err := store.Remove(dir, "loch/ane.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// loch/ane.md should be gone
	if _, err := store.Get(dir, "loch/ane.md"); err == nil {
		t.Error("Get loch/ane.md expected error after remove, got nil")
	}

	// loch/twa.md should still exist
	got, err := store.Get(dir, "loch/twa.md")
	if err != nil {
		t.Fatalf("Get loch/twa.md: %v", err)
	}

	if got != "twa" {
		t.Errorf("Get loch/twa.md = %q, want %q", got, "twa")
	}
}

func TestRemoveCleansEmptyParents(t *testing.T) {
	dir := newTestStore(t)

	if err := store.Put(dir, "glen/wynd/kirk/deep.md", "bonnie"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := store.Remove(dir, "glen/wynd/kirk/deep.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// The directory "glen" should be gone since it's now empty
	if _, err := os.Stat(filepath.Join(dir, "glen")); !os.IsNotExist(err) {
		t.Errorf("expected directory 'glen' to be removed, stat err: %v", err)
	}
}

func TestRemoveNonexistent(t *testing.T) {
	dir := newTestStore(t)

	err := store.Remove(dir, "thrawn/nae/here.md")
	if err == nil {
		t.Error("Remove nonexistent key expected error, got nil")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Remove error %q should contain 'not found'", err.Error())
	}
}

func TestStorePath(t *testing.T) {
	t.Run("basic structure", func(t *testing.T) {
		p := store.StorePath("/glen", "/hame/user/croft")
		// Should be: /glen/store/croft-<12hexchars>
		if p == "" {
			t.Fatal("StorePath returned empty string")
		}
		// Check it starts with /glen/store/
		const prefix = "/glen/store/"
		if len(p) < len(prefix) || p[:len(prefix)] != prefix {
			t.Errorf("StorePath = %q, want prefix %q", p, prefix)
		}
	})

	t.Run("repo name is base of path", func(t *testing.T) {
		p := store.StorePath("/glen", "/hame/user/graith")
		if len(p) < len("/glen/store/graith-") || p[:len("/glen/store/graith-")] != "/glen/store/graith-" {
			t.Errorf("StorePath = %q, expected repo name 'graith' in path", p)
		}
	})

	t.Run("hash is 12 hex characters", func(t *testing.T) {
		p := store.StorePath("/glen", "/hame/user/croft")
		// Extract the hash suffix after the last '-'
		last := -1

		for i, c := range p {
			if c == '-' {
				last = i
			}
		}

		if last == -1 {
			t.Fatalf("StorePath = %q, no dash found", p)
		}

		hash := p[last+1:]
		if len(hash) != 12 {
			t.Errorf("hash length = %d, want 12; hash = %q", len(hash), hash)
		}

		for _, c := range hash {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Errorf("hash %q contains non-hex character %q", hash, c)
			}
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		p1 := store.StorePath("/glen", "/hame/user/graith")

		p2 := store.StorePath("/glen", "/hame/user/graith")
		if p1 != p2 {
			t.Errorf("StorePath not deterministic: %q != %q", p1, p2)
		}
	})

	t.Run("different repos produce different paths", func(t *testing.T) {
		p1 := store.StorePath("/glen", "/hame/user/graith")

		p2 := store.StorePath("/glen", "/hame/user/bothy")
		if p1 == p2 {
			t.Errorf("different repos produced same path: %q", p1)
		}
	})

	t.Run("known hash value", func(t *testing.T) {
		p := store.StorePath("/glen", "/hame/user/graith")

		p2 := store.StorePath("/glen", "/hame/user/graith")
		if p != p2 {
			t.Errorf("non-deterministic: %q vs %q", p, p2)
		}
	})
}

func TestSharedStorePath(t *testing.T) {
	p := store.SharedStorePath("/glen")
	if p != "/glen/store/shared" {
		t.Errorf("SharedStorePath = %q, want /glen/store/shared", p)
	}
}

func TestListStores(t *testing.T) {
	testutil.IsolateGit(t)

	dataDir := t.TempDir()

	// Create two stores
	s1 := store.StorePath(dataDir, "/hame/user/croft-a")
	s2 := store.StorePath(dataDir, "/hame/user/croft-b")

	if err := store.Init(s1); err != nil {
		t.Fatalf("Init s1: %v", err)
	}

	if err := store.Init(s2); err != nil {
		t.Fatalf("Init s2: %v", err)
	}

	if err := store.Put(s1, "neep.md", "braw"); err != nil {
		t.Fatalf("Put s1: %v", err)
	}

	stores, err := store.ListStores(dataDir)
	if err != nil {
		t.Fatalf("ListStores: %v", err)
	}

	if len(stores) != 2 {
		t.Fatalf("ListStores returned %d stores, want 2", len(stores))
	}

	// Empty data dir
	emptyDir := t.TempDir()

	stores, err = store.ListStores(emptyDir)
	if err != nil {
		t.Fatalf("ListStores empty: %v", err)
	}

	if len(stores) != 0 {
		t.Errorf("ListStores empty returned %d, want 0", len(stores))
	}
}

func TestAppendCreatesFile(t *testing.T) {
	dir := newTestStore(t)

	const (
		key   = "loch/kirk.jsonl"
		line1 = `{"run":1}`
	)

	if err := store.Append(dir, key, line1); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := store.Get(dir, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got != line1+"\n" {
		t.Errorf("Get returned %q, want %q", got, line1+"\n")
	}
}

func TestAppendMultipleLines(t *testing.T) {
	dir := newTestStore(t)

	const key = "loch/multi.jsonl"

	lines := []string{`{"run":1}`, `{"run":2}`, `{"run":3}`}
	for _, line := range lines {
		if err := store.Append(dir, key, line); err != nil {
			t.Fatalf("Append %q: %v", line, err)
		}
	}

	got, err := store.Get(dir, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := strings.Join(lines, "\n") + "\n"
	if got != want {
		t.Errorf("Get returned %q, want %q", got, want)
	}

	// Each append should produce a git commit
	cmd := testutil.GitCommand("log", "--oneline")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}

	commitCount := len(strings.Split(strings.TrimSpace(string(out)), "\n"))
	if commitCount != 3 {
		t.Errorf("expected 3 commits, got %d: %s", commitCount, string(out))
	}
}

func TestAppendPreservesTrailingNewline(t *testing.T) {
	dir := newTestStore(t)

	const key = "loch/newline.jsonl"

	if err := store.Append(dir, key, "line1\n"); err != nil {
		t.Fatalf("Append with newline: %v", err)
	}

	if err := store.Append(dir, key, "line2"); err != nil {
		t.Fatalf("Append without newline: %v", err)
	}

	got, err := store.Get(dir, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got != "line1\nline2\n" {
		t.Errorf("Get returned %q, want %q", got, "line1\nline2\n")
	}
}

func TestAppendInvalidKey(t *testing.T) {
	dir := newTestStore(t)
	if err := store.Append(dir, "../escape", "neep"); err == nil {
		t.Error("Append with invalid key should fail")
	}
}

func TestAppendCoexistsWithPut(t *testing.T) {
	dir := newTestStore(t)

	const key = "loch/mixed.txt"

	if err := store.Put(dir, key, "auld content"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := store.Append(dir, key, "braw line"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := store.Get(dir, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := "auld contentbraw line\n"
	if got != want {
		t.Errorf("Get returned %q, want %q", got, want)
	}
}
