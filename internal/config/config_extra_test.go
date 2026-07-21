package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"
)

func TestLoadOrDefaultEmptyPath(t *testing.T) {
	// An empty path means "resolve the configured profile", so isolate that
	// lookup from the developer's real ~/.config/graith/config.toml.
	t.Setenv("GRAITH_PROFILE", "canny")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := LoadOrDefault("")
	if err != nil {
		t.Fatalf("LoadOrDefault(\"\") error: %v", err)
	}

	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}

	if _, ok := cfg.Agents["claude"]; !ok {
		t.Error("expected claude agent in default config")
	}
}

func TestLoadOrDefaultNonExistentPath(t *testing.T) {
	cfg, err := LoadOrDefault("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("LoadOrDefault(nonexistent) error: %v", err)
	}

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
github_username = "braw-user"
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := LoadOrDefault(cfgPath)
	if err != nil {
		t.Fatalf("LoadOrDefault(valid) error: %v", err)
	}

	if cfg.DefaultAgent != "codex" {
		t.Errorf("DefaultAgent = %q, want codex", cfg.DefaultAgent)
	}

	if cfg.GitHubUsername != "braw-user" {
		t.Errorf("GitHubUsername = %q, want braw-user", cfg.GitHubUsername)
	}
}

func TestLoadOrDefaultMalformedTOML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfgPath, []byte("not valid [[[ toml"), 0o600)

	_, err := LoadOrDefault(cfgPath)
	if err == nil {
		t.Fatal("expected error for malformed TOML, got nil")
	}
}

func TestLoadOrDefaultPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfgPath, []byte(`default_agent = "codex"`), 0o000)

	_, err := LoadOrDefault(cfgPath)
	if err == nil {
		t.Fatal("expected error for unreadable config, got nil")
	}
}

func TestLoadOrDefaultRejectsInvalidLegacyDefaultAgent(t *testing.T) {
	oldXDGConfigHome := xdg.ConfigHome

	t.Cleanup(func() { xdg.ConfigHome = oldXDGConfigHome })

	// Leave the current XDG config absent and provide only a legacy config,
	// matching the migration fallback used by an unprofiled macOS startup.
	t.Setenv("GRAITH_PROFILE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	legacyDir := t.TempDir()
	xdg.ConfigHome = legacyDir

	legacyConfig := filepath.Join(legacyDir, "graith", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacyConfig), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(legacyConfig, []byte(`default_agent = "thrawn"`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadOrDefault("")
	if err == nil {
		t.Fatal("LoadOrDefault: expected error for unknown legacy default agent, got nil")
	}

	if !strings.Contains(err.Error(), "default_agent") || !strings.Contains(err.Error(), "thrawn") {
		t.Errorf("error %q must name the field and value", err)
	}
}

func TestEnsureDirs(t *testing.T) {
	tmpDir := t.TempDir()

	p := Paths{
		ConfigFile: filepath.Join(tmpDir, "config", "config.toml"),
		DataDir:    filepath.Join(tmpDir, "data"),
		RuntimeDir: filepath.Join(tmpDir, "runtime"),
		LogDir:     filepath.Join(tmpDir, "data", "logs"),
		TmpDir:     filepath.Join(tmpDir, "data", "tmp"),
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
		filepath.Join(tmpDir, "data", "tmp"),
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
		TmpDir:     filepath.Join(tmpDir, "data", "tmp"),
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
	t.Setenv("GRAITH_PROFILE", "")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
	}

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
