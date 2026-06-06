package cli

import (
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion script",
	Long: `Generate a shell completion script for gr.

  bash:  source <(gr completion bash)
  zsh:   gr completion zsh > "${fpath[1]}/_gr"
  fish:  gr completion fish | source`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletion(cmd.OutOrStdout())
		case "zsh":
			return rootCmd.GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return rootCmd.GenFishCompletion(cmd.OutOrStdout(), true)
		case "powershell":
			return rootCmd.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
		default:
			return cmd.Usage()
		}
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
}

func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer c.Close()

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(list.Sessions)*2)
	for _, s := range list.Sessions {
		names = append(names, s.Name, s.ID)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
