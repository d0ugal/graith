package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/git"
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

// registerCompletionCmd registers this command on rootCmd. Called from registerCommands.
func registerCompletionCmd() {
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

	_ = c.SendControl("list", struct{}{})

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

func completeAgentNames(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	if cfg == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}

	sort.Strings(names)

	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeRepoPaths(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return nil, cobra.ShellCompDirectiveFilterDirs
	}
	defer c.Close()

	_ = c.SendControl("list", struct{}{})

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, cobra.ShellCompDirectiveFilterDirs
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, cobra.ShellCompDirectiveFilterDirs
	}

	seen := make(map[string]bool)

	var repos []string

	for _, s := range list.Sessions {
		if s.RepoPath != "" && !seen[s.RepoPath] {
			seen[s.RepoPath] = true
			repos = append(repos, fmt.Sprintf("%s\t%s", s.RepoPath, s.RepoName))
		}
	}

	sort.Strings(repos)

	return repos, cobra.ShellCompDirectiveFilterDirs
}

func completeBranchNames(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	repoPath, _ := cmd.Flags().GetString("repo")
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}

	out, err := git.RunOutput(repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	seen := make(map[string]bool)

	var branches []string

	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}

		name := strings.TrimPrefix(line, "origin/")
		if name == "HEAD" || seen[name] {
			continue
		}

		seen[name] = true
		branches = append(branches, name)
	}

	return branches, cobra.ShellCompDirectiveNoFileComp
}

func completeTopicNames(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer c.Close()

	senderID, _ := detectSender()
	_ = c.SendControl("msg_topics", protocol.MsgTopicsMsg{
		Subscriber: senderID,
	})

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var topics struct {
		Streams []struct {
			Name string `json:"name"`
		} `json:"streams"`
	}
	if err := protocol.DecodePayload(resp, &topics); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, 0, len(topics.Streams))
	for _, s := range topics.Streams {
		names = append(names, s.Name)
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}
