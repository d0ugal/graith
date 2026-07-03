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
// leading ~/ is expanded.
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

var approvalsCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Evaluate a shell command (read from stdin) against the built-in approvals rules",
	Long: "Reads a shell command from stdin and prints the policy the built-in\n" +
		"localmost-compatible engine would apply: allow, ask, or deny.\n\n" +
		"Example:\n  echo 'rm -rf /' | gr approvals check",
	RunE: func(cmd *cobra.Command, _ []string) error {
		path := approvalsConfigPath(approvalsCheckConfig)
		if path == "" {
			return fmt.Errorf("no approvals config: set [approvals.builtin] config or pass --config")
		}

		engine, err := localmost.Load(path)
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
		path := approvalsConfigPath(approvalsValidateConfig)
		if path == "" {
			return fmt.Errorf("no approvals config: set [approvals.builtin] config or pass --config")
		}

		if _, err := localmost.Load(path); err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(map[string]any{"ok": true, "config": path})
		}

		out.Printf("OK: %s\n", path)

		return nil
	},
}

func init() {
	approvalsCheckCmd.Flags().StringVar(&approvalsCheckConfig, "config", "", "path to a localmost-format config.json (defaults to [approvals.builtin] config)")
	approvalsValidateCmd.Flags().StringVar(&approvalsValidateConfig, "config", "", "path to a localmost-format config.json (defaults to [approvals.builtin] config)")
	approvalsCmd.AddCommand(approvalsCheckCmd, approvalsValidateCmd)
}
