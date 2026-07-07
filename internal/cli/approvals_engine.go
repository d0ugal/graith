package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/d0ugal/graith/internal/approvals/localmost"
	"github.com/spf13/cobra"
)

var (
	approvalsCheckConfig    string
	approvalsValidateConfig string
)

// approvalsConfigPath resolves the built-in approvals config path: the --config
// flag takes precedence, else the configured [approvals.builtin] config. A
// leading ~/ is expanded. Returns "" when neither is set.
func approvalsConfigPath(flag string) string {
	path := strings.TrimSpace(flag)
	if path == "" {
		path = strings.TrimSpace(cfg.Approvals.Builtin.Config)
	}

	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	return path
}

// approvalsEngine compiles the built-in approvals engine for the CLI. An
// explicit --config flag always wins (external file); otherwise inline
// [approvals.builtin] rules are used when present, falling back to the
// configured external config path. The returned source describes where the
// rules came from, for human-readable output.
func approvalsEngine(flag string) (engine *localmost.Engine, source string, err error) {
	if strings.TrimSpace(flag) == "" && cfg.Approvals.Builtin.HasInline() {
		data, jerr := cfg.Approvals.Builtin.InlineJSON()
		if jerr != nil {
			return nil, "", jerr
		}

		eng, perr := localmost.Parse(data)
		if perr != nil {
			return nil, "", perr
		}

		return eng, "inline [approvals.builtin]", nil
	}

	path := approvalsConfigPath(flag)
	if path == "" {
		return nil, "", fmt.Errorf("no approvals config: set [approvals.builtin] config, add inline rules, or pass --config")
	}

	eng, lerr := localmost.Load(path)
	if lerr != nil {
		return nil, "", lerr
	}

	return eng, path, nil
}

var approvalsCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Evaluate a shell command (read from stdin) against the built-in approvals rules",
	Long: "Reads a shell command from stdin and prints the policy the built-in\n" +
		"localmost-compatible engine would apply: allow, ask, or deny.\n\n" +
		"Example:\n  echo 'rm -rf /' | gr approvals check",
	RunE: func(cmd *cobra.Command, _ []string) error {
		engine, _, err := approvalsEngine(approvalsCheckConfig)
		if err != nil {
			return err
		}

		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return err
		}

		policy, err := engine.Evaluate(strings.TrimSpace(string(data)))
		if err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(map[string]string{"policy": string(policy)})
		}

		out.Printf("%s\n", policy)

		return nil
	},
}

var approvalsValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate the built-in approvals config (localmost-format config.json)",
	RunE: func(_ *cobra.Command, _ []string) error {
		_, source, err := approvalsEngine(approvalsValidateConfig)
		if err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(map[string]any{"ok": true, "config": source})
		}

		out.Printf("OK: %s\n", source)

		return nil
	},
}

// registerApprovalsEngineCmds wires the check/validate subcommands and their
// flags onto approvalsCmd. Called from registerApprovalsCmd.
func registerApprovalsEngineCmds() {
	approvalsCheckCmd.Flags().StringVar(&approvalsCheckConfig, "config", "", "path to a localmost-format config.json (defaults to [approvals.builtin] config)")
	approvalsValidateCmd.Flags().StringVar(&approvalsValidateConfig, "config", "", "path to a localmost-format config.json (defaults to [approvals.builtin] config)")
	approvalsCmd.AddCommand(approvalsCheckCmd, approvalsValidateCmd)
}
