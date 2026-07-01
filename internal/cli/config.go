package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/config"
	"github.com/pelletier/go-toml/v2"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var configForceReset bool

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage graith configuration",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectConfigInsideSession(cmd); err != nil {
			return err
		}

		var err error

		paths, err = config.ResolvePaths()
		if err != nil {
			return err
		}

		return nil
	},
}

var configResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Write built-in defaults to config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		target := cfgFile
		if target == "" {
			target = paths.ConfigFile
		}

		if _, err := os.Stat(target); err == nil {
			if !configForceReset {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("config file exists at %s; use --force to overwrite in non-interactive mode", target)
				}

				fmt.Fprintf(os.Stderr, "This will overwrite your config at %s. Continue? [y/N] ", target)

				var answer string
				fmt.Scanln(&answer)

				if answer != "y" && answer != "Y" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}

		f, err := os.CreateTemp(filepath.Dir(target), ".config-*.toml.tmp")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}

		tmp := f.Name()
		if _, err := f.Write(config.DefaultTOML()); err != nil {
			f.Close()
			os.Remove(tmp)

			return fmt.Errorf("write config: %w", err)
		}

		if err := f.Chmod(0o600); err != nil {
			f.Close()
			os.Remove(tmp)

			return fmt.Errorf("set config permissions: %w", err)
		}

		if err := f.Close(); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("close temp file: %w", err)
		}

		if err := os.Rename(tmp, target); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("rename config: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Wrote default config to %s\n", target)

		return nil
	},
}

var configDiffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show changes from built-in defaults",
	RunE: func(cmd *cobra.Command, args []string) error {
		target := cfgFile
		if target == "" {
			target = paths.ConfigFile
		}

		userCfg, err := config.LoadOrDefault(target)
		if err != nil {
			return fmt.Errorf("parse config: %w", err)
		}

		defaultCfg := config.Default()

		defaultBytes, err := toml.Marshal(defaultCfg)
		if err != nil {
			return fmt.Errorf("marshal defaults: %w", err)
		}

		userBytes, err := toml.Marshal(userCfg)
		if err != nil {
			return fmt.Errorf("marshal user config: %w", err)
		}

		diff := difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(defaultBytes)),
			B:        difflib.SplitLines(string(userBytes)),
			FromFile: "defaults",
			ToFile:   target,
			Context:  3,
		}

		text, err := difflib.GetUnifiedDiffString(diff)
		if err != nil {
			return fmt.Errorf("compute diff: %w", err)
		}

		if text == "" {
			return nil
		}

		fmt.Print(text)

		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective (merged) configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		target := cfgFile
		if target == "" {
			target = paths.ConfigFile
		}

		effectiveCfg, err := config.LoadOrDefault(target)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		data, err := toml.Marshal(effectiveCfg)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}

		fmt.Print(string(data))

		return nil
	},
}

// registerConfigCmd registers this command on rootCmd. Called from registerCommands.
func registerConfigCmd() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configResetCmd)
	configCmd.AddCommand(configDiffCmd)
	configCmd.AddCommand(configShowCmd)
	configResetCmd.Flags().BoolVar(&configForceReset, "force", false, "overwrite without confirmation")
}
