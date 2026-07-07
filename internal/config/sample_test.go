package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleConfigPath returns the path to the repo-root config.sample.toml,
// relative to this test file (internal/config).
func sampleConfigPath() string {
	return filepath.Join("..", "..", "config.sample.toml")
}

// TestSampleConfigLoads guards against config.sample.toml — the file users are
// told to copy — drifting into an invalid state. It must parse and validate
// cleanly through the same Load path a real config goes through.
func TestSampleConfigLoads(t *testing.T) {
	if _, err := Load(sampleConfigPath()); err != nil {
		t.Fatalf("config.sample.toml failed to load: %v", err)
	}
}

// TestSampleConfigSandboxDocumentsBackend guards against regressing to the
// pre-pluggable sandbox framing (issue #796). When the sandbox is enabled a
// `backend` is REQUIRED (unset fails closed), so the sample must document it
// and must not frame safehouse as the implicit/only backend.
func TestSampleConfigSandboxDocumentsBackend(t *testing.T) {
	data, err := os.ReadFile(sampleConfigPath())
	if err != nil {
		t.Fatalf("reading config.sample.toml: %v", err)
	}
	sample := string(data)

	if !strings.Contains(sample, "backend = \"nono\"") {
		t.Error("config.sample.toml [sandbox] block does not document the required `backend` key")
	}
	// The old sample framed safehouse as the implicit backend via a bare
	// `command = "safehouse"` with no backend key. That must not return.
	if strings.Contains(sample, "command = \"safehouse\"") {
		t.Error("config.sample.toml still frames safehouse as the implicit backend (stale pre-pluggable framing)")
	}
	// A sandboxed Claude needs a write_files grant for its login file or it
	// stays logged out — the sample's example must include it.
	if !strings.Contains(sample, "write_files = [\"~/.claude.json\"") {
		t.Error("config.sample.toml [agents.claude.sandbox] example is missing the ~/.claude.json login-file grant")
	}
}
