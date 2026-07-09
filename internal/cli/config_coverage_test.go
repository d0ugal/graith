package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/spf13/cobra"
)

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
