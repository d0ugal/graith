package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/pelletier/go-toml/v2"
	"github.com/pmezard/go-difflib/difflib"
)

func TestConfigResetWritesValidTOML(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, config.DefaultTOML(), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	var cfg config.Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("written config is not valid TOML: %v", err)
	}
	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
}

func TestConfigResetOverwritesMalformed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, []byte("this is not valid [[ toml"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(target, config.DefaultTOML(), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("reset config is not valid TOML: %v", err)
	}
}

func TestConfigResetFilePermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, config.DefaultTOML(), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %o, want 600", perm)
	}
}

func TestConfigDiffNoChanges(t *testing.T) {
	defaultCfg := config.Default()
	defaultBytes, err := toml.Marshal(defaultCfg)
	if err != nil {
		t.Fatal(err)
	}

	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(defaultBytes)),
		B:        difflib.SplitLines(string(defaultBytes)),
		FromFile: "defaults",
		ToFile:   "user",
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Errorf("expected empty diff for identical configs, got:\n%s", text)
	}
}

func TestConfigDiffShowsChanges(t *testing.T) {
	defaultCfg := config.Default()
	userCfg := config.Default()
	userCfg.DefaultAgent = "codex"

	defaultBytes, err := toml.Marshal(defaultCfg)
	if err != nil {
		t.Fatal(err)
	}
	userBytes, err := toml.Marshal(userCfg)
	if err != nil {
		t.Fatal(err)
	}

	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(defaultBytes)),
		B:        difflib.SplitLines(string(userBytes)),
		FromFile: "defaults",
		ToFile:   "user",
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Error("expected non-empty diff for changed config")
	}
}

func TestConfigShowRoundTrips(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	os.WriteFile(target, []byte(`default_agent = "codex"`+"\n"), 0o644)

	effectiveCfg, err := config.LoadOrDefault(target)
	if err != nil {
		t.Fatal(err)
	}

	data, err := toml.Marshal(effectiveCfg)
	if err != nil {
		t.Fatal(err)
	}

	var roundTripped config.Config
	if err := toml.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("show output is not valid TOML: %v", err)
	}
	if roundTripped.DefaultAgent != "codex" {
		t.Errorf("DefaultAgent = %q, want codex", roundTripped.DefaultAgent)
	}
	if roundTripped.Agents["claude"].Command != "claude" {
		t.Error("claude agent not preserved through round-trip")
	}
}

func TestConfigShowNoConfigFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nonexistent.toml")

	effectiveCfg, err := config.LoadOrDefault(target)
	if err != nil {
		t.Fatal(err)
	}
	if effectiveCfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude (defaults)", effectiveCfg.DefaultAgent)
	}
}
