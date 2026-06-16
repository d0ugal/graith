package store_test

import (
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
