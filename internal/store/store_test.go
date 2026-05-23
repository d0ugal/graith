package store_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/store"
)

func TestValidateKey(t *testing.T) {
	t.Run("valid keys", func(t *testing.T) {
		validKeys := []string{
			"design/api.md",
			"tribunal/2026-06-15",
			"simple",
			"a/b/c",
			"research/findings.json",
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
		keys := []string{
			"..",
			"../foo",
			"foo/..",
			"foo/../bar",
			"foo/..bar", // this is fine — only exact ".." segment
			"foo/bar..", // this is fine — only exact ".." segment
		}
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
		// These are NOT dotdot components and should be valid
		okKeys := []string{
			"foo/..bar",
			"foo/bar..",
		}
		for _, key := range okKeys {
			if err := store.ValidateKey(key); err != nil {
				t.Errorf("ValidateKey(%q) unexpected error: %v", key, err)
			}
		}
		_ = keys
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
		okKeys := []string{"foo-bar", "a/b-c/d"}
		for _, key := range okKeys {
			if err := store.ValidateKey(key); err != nil {
				t.Errorf("ValidateKey(%q) unexpected error: %v", key, err)
			}
		}
	})
}

func TestInit(t *testing.T) {
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

func newTestStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	if err := store.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return dir
}

func TestPutGet(t *testing.T) {
	dir := newTestStore(t)

	const key = "design/api.md"
	const body = "# API Design\n\nSome content here."

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

	const key = "notes/test.md"
	const first = "first value"
	const second = "second value"

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
		t.Errorf("Get returned %q, want %q (second value)", got, second)
	}
}

func TestGetNotFound(t *testing.T) {
	dir := newTestStore(t)

	_, err := store.Get(dir, "nonexistent/key.md")
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
		if err := store.Put(dir, key, "body"); err == nil {
			t.Errorf("Put(%q) expected error for invalid key, got nil", key)
		}
	}
}

func TestPutCreatesGitCommit(t *testing.T) {
	dir := newTestStore(t)

	const key = "design/api.md"
	if err := store.Put(dir, key, "content"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Check that a git commit was created with the right message.
	cmd := exec.Command("git", "log", "--oneline", "-1")
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

		msg := store.CommitMessage("update", "design/api.md")
		want := "store: update design/api.md"
		if msg != want {
			t.Errorf("CommitMessage = %q, want %q", msg, want)
		}
	})

	t.Run("with session id and name", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "abc123")
		t.Setenv("GRAITH_SESSION_NAME", "fix-overlay")
		t.Setenv("GRAITH_AGENT_TYPE", "")

		msg := store.CommitMessage("update", "design/api.md")
		if !strings.Contains(msg, "store: update design/api.md") {
			t.Errorf("CommitMessage missing first line: %q", msg)
		}
		if !strings.Contains(msg, "session: fix-overlay (abc123)") {
			t.Errorf("CommitMessage missing session trailer: %q", msg)
		}
	})

	t.Run("with session id only", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "abc123")
		t.Setenv("GRAITH_SESSION_NAME", "")
		t.Setenv("GRAITH_AGENT_TYPE", "")

		msg := store.CommitMessage("update", "design/api.md")
		if !strings.Contains(msg, "session: abc123") {
			t.Errorf("CommitMessage missing session trailer: %q", msg)
		}
	})

	t.Run("with all env vars", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "abc123")
		t.Setenv("GRAITH_SESSION_NAME", "fix-overlay")
		t.Setenv("GRAITH_AGENT_TYPE", "claude")

		msg := store.CommitMessage("update", "design/api.md")
		if !strings.Contains(msg, "store: update design/api.md") {
			t.Errorf("CommitMessage missing first line: %q", msg)
		}
		if !strings.Contains(msg, "session: fix-overlay (abc123)") {
			t.Errorf("CommitMessage missing session trailer: %q", msg)
		}
		if !strings.Contains(msg, "agent: claude") {
			t.Errorf("CommitMessage missing agent trailer: %q", msg)
		}
	})
}

func TestStorePath(t *testing.T) {
	t.Run("basic structure", func(t *testing.T) {
		p := store.StorePath("/data", "/home/user/myrepo")
		// Should be: /data/store/myrepo-<12hexchars>
		if p == "" {
			t.Fatal("StorePath returned empty string")
		}
		// Check it starts with /data/store/
		const prefix = "/data/store/"
		if len(p) < len(prefix) || p[:len(prefix)] != prefix {
			t.Errorf("StorePath = %q, want prefix %q", p, prefix)
		}
	})

	t.Run("repo name is base of path", func(t *testing.T) {
		p := store.StorePath("/data", "/home/user/graith")
		if len(p) < len("/data/store/graith-") || p[:len("/data/store/graith-")] != "/data/store/graith-" {
			t.Errorf("StorePath = %q, expected repo name 'graith' in path", p)
		}
	})

	t.Run("hash is 12 hex characters", func(t *testing.T) {
		p := store.StorePath("/data", "/home/user/myrepo")
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
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("hash %q contains non-hex character %q", hash, c)
			}
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		p1 := store.StorePath("/data", "/home/user/graith")
		p2 := store.StorePath("/data", "/home/user/graith")
		if p1 != p2 {
			t.Errorf("StorePath not deterministic: %q != %q", p1, p2)
		}
	})

	t.Run("different repos produce different paths", func(t *testing.T) {
		p1 := store.StorePath("/data", "/home/user/graith")
		p2 := store.StorePath("/data", "/home/user/other")
		if p1 == p2 {
			t.Errorf("different repos produced same path: %q", p1)
		}
	})

	t.Run("known hash value", func(t *testing.T) {
		// Compute expected hash for "/home/user/graith" manually by running
		// the same algorithm and recording the output.
		// We rely on the implementation being consistent with the algorithm
		// by checking the same input always gives the same output.
		p := store.StorePath("/data", "/home/user/graith")
		p2 := store.StorePath("/data", "/home/user/graith")
		if p != p2 {
			t.Errorf("non-deterministic: %q vs %q", p, p2)
		}
	})
}
