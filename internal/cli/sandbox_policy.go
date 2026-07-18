package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/d0ugal/graith/internal/commandpolicy/localmost"
	"github.com/d0ugal/graith/internal/config"
	"github.com/spf13/cobra"
)

var (
	sandboxPolicyCheckConfig    string
	sandboxPolicyValidateConfig string
)

func sandboxPolicyConfigDir() string {
	if file := strings.TrimSpace(cfgFile); file != "" {
		return filepath.Dir(file)
	}
	if paths.ConfigFile == "" {
		return ""
	}
	return filepath.Dir(paths.ConfigFile)
}

func sandboxPolicyEngine(flag string) (*localmost.Engine, string, error) {
	if strings.TrimSpace(flag) == "" && cfg.CommandPolicy.Builtin.HasInline() {
		data, err := cfg.CommandPolicy.Builtin.InlineJSON()
		if err != nil {
			return nil, "", err
		}
		engine, err := localmost.Parse(data)
		return engine, "inline [command_policy.builtin]", err
	}
	path := strings.TrimSpace(flag)
	if path != "" {
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
	} else {
		path = config.ExpandPathRelative(cfg.CommandPolicy.Builtin.Config, sandboxPolicyConfigDir())
	}
	if path == "" {
		return nil, "", errors.New("no command policy rules: configure [command_policy.builtin] or pass --config")
	}
	engine, err := localmost.Load(path)
	return engine, path, err
}

var sandboxPolicyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Check and validate the optional shell command policy",
}

var sandboxPolicyCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Evaluate a shell command against the built-in command policy",
	Long:  "Reads a shell command from stdin and prints allow, ask, or deny. Ask is denied during agent execution.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		engine, _, err := sandboxPolicyEngine(sandboxPolicyCheckConfig)
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

var sandboxPolicyValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate built-in localmost-format command-policy rules",
	RunE: func(_ *cobra.Command, _ []string) error {
		_, source, err := sandboxPolicyEngine(sandboxPolicyValidateConfig)
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

func registerSandboxPolicyCmd() {
	sandboxPolicyCheckCmd.Flags().StringVar(&sandboxPolicyCheckConfig, "config", "", "path to localmost-format rules")
	sandboxPolicyValidateCmd.Flags().StringVar(&sandboxPolicyValidateConfig, "config", "", "path to localmost-format rules")
	sandboxPolicyCmd.AddCommand(sandboxPolicyCheckCmd, sandboxPolicyValidateCmd)
	sandboxCmd.AddCommand(sandboxPolicyCmd)
}
