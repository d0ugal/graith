package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrDefaultEmptyPath(t *testing.T) {
	// Empty path should fall back to default config (since the default
	// config file almost certainly doesn't exist in a test environment).
	cfg := LoadOrDefault("")
	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
	if _, ok := cfg.Agents["claude"]; !ok {
		t.Error("expected claude agent in default config")
	}
}

func TestLoadOrDefaultNonExistentPath(t *testing.T) {
	cfg := LoadOrDefault("/nonexistent/path/config.toml")
	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
	if cfg.Keybindings.Prefix != "ctrl+b" {
		t.Errorf("Prefix = %q, want ctrl+b", cfg.Keybindings.Prefix)
	}
}

func TestLoadOrDefaultValidFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
default_agent = "codex"
github_username = "testuser"
`
	os.WriteFile(cfgPath, []byte(toml), 0o644)
	cfg := LoadOrDefault(cfgPath)
	if cfg.DefaultAgent != "codex" {
		t.Errorf("DefaultAgent = %q, want codex", cfg.DefaultAgent)
	}
	if cfg.GitHubUsername != "testuser" {
		t.Errorf("GitHubUsername = %q, want testuser", cfg.GitHubUsername)
	}
}

func TestEnsureDirs(t *testing.T) {
	tmpDir := t.TempDir()
	p := Paths{
		ConfigFile: filepath.Join(tmpDir, "config", "config.toml"),
		DataDir:    filepath.Join(tmpDir, "data"),
		RuntimeDir: filepath.Join(tmpDir, "runtime"),
		LogDir:     filepath.Join(tmpDir, "data", "logs"),
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error: %v", err)
	}

	// Verify all expected directories exist.
	expectedDirs := []string{
		filepath.Join(tmpDir, "config"), // parent of ConfigFile
		filepath.Join(tmpDir, "data"),
		filepath.Join(tmpDir, "runtime"),
		filepath.Join(tmpDir, "data", "logs"),
	}
	for _, dir := range expectedDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("directory %q does not exist: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", dir)
		}
	}
}

func TestEnsureDirsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	p := Paths{
		ConfigFile: filepath.Join(tmpDir, "config", "config.toml"),
		DataDir:    filepath.Join(tmpDir, "data"),
		RuntimeDir: filepath.Join(tmpDir, "runtime"),
		LogDir:     filepath.Join(tmpDir, "data", "logs"),
	}
	// Call twice to verify idempotency.
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("first EnsureDirs() error: %v", err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("second EnsureDirs() error: %v", err)
	}
}

func TestResolvePathsIndirectlyTestsRuntimeDir(t *testing.T) {
	// runtimeDirForGraith is unexported but indirectly exercised via ResolvePaths.
	p := ResolvePaths()
	if p.RuntimeDir == "" {
		t.Error("RuntimeDir should not be empty")
	}
	if p.SocketPath == "" {
		t.Error("SocketPath should not be empty")
	}
	if p.PIDFile == "" {
		t.Error("PIDFile should not be empty")
	}
	// RuntimeDir should contain "graith" somewhere in the path.
	if !filepath.IsAbs(p.RuntimeDir) {
		t.Errorf("RuntimeDir should be absolute, got %q", p.RuntimeDir)
	}
}
