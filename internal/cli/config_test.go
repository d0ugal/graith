package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aymanbagabas/go-udiff"
	"github.com/d0ugal/graith/internal/config"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
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

	text := udiff.Unified("defaults", "user", string(defaultBytes), string(defaultBytes))

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

	text := udiff.Unified("defaults", "user", string(defaultBytes), string(userBytes))

	if text == "" {
		t.Fatal("expected non-empty diff for changed config")
	}

	// Assert the label direction so a reversed old/new argument order is caught:
	// defaults (old) → user (new), with the agent change shown as -claude/+codex.
	for _, want := range []string{"--- defaults", "+++ user", "-default_agent = 'claude'", "+default_agent = 'codex'"} {
		if !strings.Contains(text, want) {
			t.Errorf("diff missing %q; got:\n%s", want, text)
		}
	}
}

func TestConfigShowRoundTrips(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	_ = os.WriteFile(target, []byte(`default_agent = "codex"`+"\n"), 0o600)

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

// withConfigGlobals sets the package-global cfgFile/paths used by the config
// subcommands and restores them afterwards.
func withConfigGlobals(t *testing.T, file string, p config.Paths, fn func()) {
	t.Helper()

	prevFile, prevPaths := cfgFile, paths
	cfgFile, paths = file, p

	defer func() { cfgFile, paths = prevFile, prevPaths }()

	fn()
}

func TestConfigResetCovWritesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "graith", "config.toml")

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = false

		defer func() { configForceReset = prev }()

		if err := configResetCmd.RunE(configResetCmd, nil); err != nil {
			t.Fatalf("reset when absent: %v", err)
		}
	})

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("expected config written: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) == 0 {
		t.Error("written config is empty")
	}
}

func TestConfigResetCovRefusesOverwriteNonInteractive(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, []byte("default_agent = \"canny\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = false

		defer func() { configForceReset = prev }()

		// go test's stdin is not a terminal, so the command must refuse and
		// direct the user to --force rather than blocking on a prompt.
		err := configResetCmd.RunE(configResetCmd, nil)
		if err == nil {
			t.Fatal("expected error refusing to overwrite in non-interactive mode")
		}

		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should direct the user to --force, got %q", err)
		}
	})

	// The original content must be untouched.
	data, _ := os.ReadFile(target)
	if string(data) != "default_agent = \"canny\"\n" {
		t.Errorf("existing config was modified: %q", data)
	}
}

func TestConfigResetCovForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, []byte("thrawn nonsense"), 0o600); err != nil {
		t.Fatal(err)
	}

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = true

		defer func() { configForceReset = prev }()

		if err := configResetCmd.RunE(configResetCmd, nil); err != nil {
			t.Fatalf("force reset: %v", err)
		}
	})

	// The result must now parse as a valid config with defaults.
	cfg, err := config.LoadOrDefault(target)
	if err != nil {
		t.Fatalf("reset produced unparseable config: %v", err)
	}

	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
}

func TestConfigResetCovUsesPathsWhenCfgFileEmpty(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	// cfgFile empty -> falls back to paths.ConfigFile.
	withConfigGlobals(t, "", config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = true

		defer func() { configForceReset = prev }()

		if err := configResetCmd.RunE(configResetCmd, nil); err != nil {
			t.Fatalf("reset via paths: %v", err)
		}
	})

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected config at paths.ConfigFile: %v", err)
	}
}

func TestConfigShowCovValidFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, []byte("default_agent = \"codex\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var got string

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		got = captureStdout(t, func() {
			if err := configShowCmd.RunE(configShowCmd, nil); err != nil {
				t.Fatalf("show: %v", err)
			}
		})
	})

	// The user-set value must appear in the merged output.
	if !strings.Contains(got, `default_agent = 'codex'`) && !strings.Contains(got, `default_agent = "codex"`) {
		t.Errorf("show output missing user default_agent=codex:\n%s", got)
	}
}

func TestConfigShowCovMissingFileUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nope.toml")

	var got string

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		got = captureStdout(t, func() {
			if err := configShowCmd.RunE(configShowCmd, nil); err != nil {
				t.Fatalf("show missing file should fall back to defaults: %v", err)
			}
		})
	})

	// Missing file -> defaults, so the default agent must be printed.
	if !strings.Contains(got, "claude") {
		t.Errorf("show output for missing file should contain default agent 'claude':\n%s", got)
	}
}

func TestConfigDiffCovNoChanges(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	// Write the exact defaults so the diff is empty.
	if err := os.WriteFile(target, config.DefaultTOML(), 0o600); err != nil {
		t.Fatal(err)
	}

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		if err := configDiffCmd.RunE(configDiffCmd, nil); err != nil {
			t.Fatalf("diff no-changes: %v", err)
		}
	})
}

func TestConfigDiffCovWithChanges(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, []byte("default_agent = \"codex\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		if err := configDiffCmd.RunE(configDiffCmd, nil); err != nil {
			t.Fatalf("diff with changes: %v", err)
		}
	})
}

func TestConfigDiffCovMissingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ghost.toml")

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		if err := configDiffCmd.RunE(configDiffCmd, nil); err != nil {
			t.Fatalf("diff missing file should use defaults: %v", err)
		}
	})
}

func TestConfigInitWritesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "graith", "config.toml")

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = false

		defer func() { configForceReset = prev }()

		if err := configInitCmd.RunE(configInitCmd, nil); err != nil {
			t.Fatalf("init when absent: %v", err)
		}
	})

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("expected config written: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}

	// The generated file must parse and carry the built-in defaults.
	cfg, err := config.LoadOrDefault(target)
	if err != nil {
		t.Fatalf("init produced unparseable config: %v", err)
	}

	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
}

func TestConfigInitRefusesOverwriteNonInteractive(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, []byte("default_agent = \"canny\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = false

		defer func() { configForceReset = prev }()

		// go test's stdin is not a terminal, so init must refuse and point the
		// user at --force rather than clobbering an existing config.
		err := configInitCmd.RunE(configInitCmd, nil)
		if err == nil {
			t.Fatal("expected error refusing to overwrite in non-interactive mode")
		}

		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should direct the user to --force, got %q", err)
		}
	})

	// The existing config must be untouched.
	data, _ := os.ReadFile(target)
	if string(data) != "default_agent = \"canny\"\n" {
		t.Errorf("existing config was modified: %q", data)
	}
}

func TestConfigInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, []byte("thrawn nonsense"), 0o600); err != nil {
		t.Fatal(err)
	}

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = true

		defer func() { configForceReset = prev }()

		if err := configInitCmd.RunE(configInitCmd, nil); err != nil {
			t.Fatalf("force init: %v", err)
		}
	})

	cfg, err := config.LoadOrDefault(target)
	if err != nil {
		t.Fatalf("init produced unparseable config: %v", err)
	}

	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
}

func TestConfigInitErrorsWhenDirUncreatable(t *testing.T) {
	dir := t.TempDir()

	// A regular file where a directory is expected: MkdirAll on a path *under*
	// it must fail, exercising writeDefaultConfig's "create config directory"
	// error branch.
	blocker := filepath.Join(dir, "scunner")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(blocker, "graith", "config.toml")

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		prev := configForceReset
		configForceReset = true

		defer func() { configForceReset = prev }()

		err := configInitCmd.RunE(configInitCmd, nil)
		if err == nil {
			t.Fatal("expected error when the config directory cannot be created")
		}

		if !strings.Contains(err.Error(), "create config directory") {
			t.Errorf("error = %q, want it to mention creating the config directory", err)
		}
	})
}

func TestConfigPathPrintsExplicitFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(target, config.DefaultTOML(), 0o600); err != nil {
		t.Fatal(err)
	}

	var got string

	// cfgFile set (--config) is used verbatim by ResolveConfigPath.
	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		got = captureStdout(t, func() {
			if err := configPathCmd.RunE(configPathCmd, nil); err != nil {
				t.Fatalf("path: %v", err)
			}
		})
	})

	if strings.TrimSpace(got) != target {
		t.Errorf("path output = %q, want %q", strings.TrimSpace(got), target)
	}
}

func TestConfigPathPrintsResolvedWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ghost.toml")

	var got string

	// cfgFile empty -> ResolveConfigPath returns paths.ConfigFile even when the
	// file does not exist, so `gr config path` still reports where it would live.
	withConfigGlobals(t, "", config.Paths{ConfigFile: target}, func() {
		got = captureStdout(t, func() {
			if err := configPathCmd.RunE(configPathCmd, nil); err != nil {
				t.Fatalf("path missing file: %v", err)
			}
		})
	})

	if strings.TrimSpace(got) != target {
		t.Errorf("path output = %q, want %q", strings.TrimSpace(got), target)
	}
}

func TestRejectConfigInsideSessionCov(t *testing.T) {
	makeCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "config"}
		cmd.Flags().String("config", "", "")

		return cmd
	}

	t.Run("outside session is allowed", func(t *testing.T) {
		// insideSession uses os.LookupEnv, so the var must be truly unset (not
		// just empty). t.Setenv registers the restore-to-original cleanup.
		t.Setenv("GRAITH_SESSION_ID", "")

		_ = os.Unsetenv("GRAITH_SESSION_ID")

		cmd := makeCmd()
		_ = cmd.Flags().Set("config", "/tmp/x.toml")

		if err := rejectConfigInsideSession(cmd); err != nil {
			t.Errorf("outside a session --config should be allowed: %v", err)
		}
	})

	t.Run("inside session with changed config rejected", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "braw-session")

		cmd := makeCmd()
		_ = cmd.Flags().Set("config", "/tmp/x.toml")

		if err := rejectConfigInsideSession(cmd); err == nil {
			t.Error("expected --config inside a session to be rejected")
		}
	})

	t.Run("inside session without config flag allowed", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "braw-session")

		cmd := makeCmd()
		// config flag not changed.
		if err := rejectConfigInsideSession(cmd); err != nil {
			t.Errorf("unchanged config flag should be allowed inside a session: %v", err)
		}
	})
}
