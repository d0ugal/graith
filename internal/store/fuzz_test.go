package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/store"
)

func FuzzValidateKey(f *testing.F) {
	seeds := []string{
		"",
		"/",
		"/braw",
		"-braw",
		"..",
		"../braw",
		"braw/..",
		".",
		"./braw",
		"braw/.",
		".git",
		".GIT",
		"braw/.Git/config",
		"store.lock",
		"STORE.LOCK",
		"braw/store.lock",
		`braw\canny`,
		"braw*canny",
		"braw?canny",
		"braw[0]",
		":(glob)braw",
		"braw\ncanny",
		"braw\tcanny",
		"braw\x00canny",
		"braw\x7fcanny",
		"braw//canny",
		"braw/canny",
		"blether/\u96ea.md",
		"strath/\U0001f600.md",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, key string) {
		original := key

		err := store.ValidateKey(key)
		secondErr := store.ValidateKey(key)

		if key != original {
			t.Fatalf("ValidateKey mutated key from %q to %q", original, key)
		}

		if got, want := errorString(secondErr), errorString(err); got != want {
			t.Fatalf("ValidateKey(%q) was not deterministic: %q then %q", key, want, got)
		}

		if reason := unsafeStoreKeyReason(key); reason != "" && err == nil {
			t.Fatalf("ValidateKey(%q) accepted key with %s", key, reason)
		}

		if err == nil {
			assertStoreKeySafe(t, key)
		}
	})
}

func FuzzStorePathByID(f *testing.F) {
	dataDir := f.TempDir()

	const validID = "croft-0123456789ab"

	validPath := filepath.Join(dataDir, "store", validID)
	if err := os.MkdirAll(filepath.Join(validPath, ".git"), 0o700); err != nil {
		f.Fatalf("create valid store fixture: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dataDir, "store", "shared", ".git"), 0o700); err != nil {
		f.Fatalf("create shared store fixture: %v", err)
	}

	seeds := []string{
		"",
		"shared",
		"SHARED",
		validID,
		"../escape",
		"braw/../escape",
		"nested/haar",
		`back\slash`,
		"braw..canny",
		"canny",
		"-canny",
		"canny*glob",
		"canny:pathspec",
		"blether-\u96ea",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, id string) {
		got, ok := store.StorePathByID(dataDir, id)

		if reason := unsafeStoreIDReason(id); reason != "" {
			if ok || got != "" {
				t.Fatalf("StorePathByID(%q) = %q, %t; want rejection for %s", id, got, ok, reason)
			}

			return
		}

		if id == validID {
			if !ok || got != validPath {
				t.Fatalf("StorePathByID(%q) = %q, %t; want %q, true", id, got, ok, validPath)
			}

			return
		}

		if !ok {
			if got != "" {
				t.Fatalf("StorePathByID(%q) = %q, false; want empty path", id, got)
			}

			return
		}

		want := filepath.Join(dataDir, "store", id)
		if got != want {
			t.Fatalf("StorePathByID(%q) = %q, true; want %q, true", id, got, want)
		}
	})
}

func FuzzCommitMessage(f *testing.F) {
	seeds := []struct {
		action      string
		key         string
		sessionID   string
		sessionName string
		agentType   string
	}{
		{action: "update", key: "loch/api.md"},
		{action: "append", key: "strath/log.jsonl", sessionID: "braw123"},
		{action: "remove", key: "glen/canny.md", sessionID: "braw123", sessionName: "canny-overlay"},
		{action: "update", key: "blether/\u96ea.md", sessionID: "braw123", sessionName: "canny-overlay", agentType: "codex"},
		{action: "update\nextra", key: "glen.md", sessionID: "braw123", agentType: "claude"},
	}
	for _, seed := range seeds {
		f.Add(seed.action, seed.key, seed.sessionID, seed.sessionName, seed.agentType)
	}

	f.Fuzz(func(t *testing.T, action, key, sessionID, sessionName, agentType string) {
		if strings.Contains(sessionID, "\x00") || strings.Contains(sessionName, "\x00") || strings.Contains(agentType, "\x00") {
			t.Skip("environment variables cannot contain NUL bytes")
		}

		t.Setenv("GRAITH_SESSION_ID", sessionID)
		t.Setenv("GRAITH_SESSION_NAME", sessionName)
		t.Setenv("GRAITH_AGENT_TYPE", agentType)

		got := store.CommitMessage(action, key)

		want := expectedCommitMessage(action, key, sessionID, sessionName, agentType)
		if got != want {
			t.Fatalf("CommitMessage(%q, %q) = %q, want %q", action, key, got, want)
		}

		if sessionID != "" && strings.HasSuffix(got, "\n") {
			t.Fatalf("CommitMessage(%q, %q) has trailing newline: %q", action, key, got)
		}
	})
}

func errorString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func assertStoreKeySafe(t *testing.T, key string) {
	t.Helper()

	if reason := unsafeStoreKeyReason(key); reason != "" {
		t.Fatalf("accepted key %q has %s", key, reason)
	}
}

func unsafeStoreKeyReason(key string) string {
	if key == "" {
		return "empty value"
	}

	if key[0] == '/' {
		return "leading slash"
	}

	if key[0] == '-' {
		return "leading dash"
	}

	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			return "control character"
		}
	}

	if strings.ContainsAny(key, "*?[:") {
		return "glob/pathspec character"
	}

	if strings.Contains(key, "\\") {
		return "backslash"
	}

	for _, component := range strings.Split(key, "/") {
		if component == ".." {
			return "'..' path component"
		}

		if strings.EqualFold(component, ".git") {
			return "'.git' path component"
		}

		if component == "." {
			return "'.' path component"
		}
	}

	if strings.EqualFold(key, "store.lock") {
		return "store.lock collision"
	}

	return ""
}

func unsafeStoreIDReason(id string) string {
	switch {
	case id == "":
		return "empty ID"
	case strings.EqualFold(id, "shared"):
		return "shared store ID"
	case strings.ContainsAny(id, `/\`):
		return "path separator"
	case strings.Contains(id, ".."):
		return "'..' substring"
	default:
		return ""
	}
}

func expectedCommitMessage(action, key, sessionID, sessionName, agentType string) string {
	first := "store: " + action + " " + key
	if sessionID == "" {
		return first
	}

	var sb strings.Builder
	sb.WriteString(first)
	sb.WriteString("\n\n")

	if sessionName != "" {
		sb.WriteString("session: " + sessionName + " (" + sessionID + ")\n")
	} else {
		sb.WriteString("session: " + sessionID + "\n")
	}

	if agentType != "" {
		sb.WriteString("agent: " + agentType + "\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}
