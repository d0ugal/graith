package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	newAgent         string
	newBase          string
	newBackground    bool
	newPrompt        string
	newPromptFile    string
	newRepo          string
	newNoRepo        bool
	newShareWorktree string
)

var newCmd = &cobra.Command{
	Use:     "new <name>",
	Aliases: []string{"n"},
	Short:   "Create a new agent session",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		repoPath := newRepo
		if repoPath == "" {
			repoPath, _ = os.Getwd()
		} else {
			repoPath, _ = filepath.Abs(repoPath)
		}
		agent := newAgent
		if agent == "" {
			agent = cfg.DefaultAgent
		}

		prompt := newPrompt
		if newPromptFile != "" {
			data, err := os.ReadFile(newPromptFile)
			if err != nil {
				return fmt.Errorf("read prompt file: %w", err)
			}
			prompt = string(data)
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("create", protocol.CreateMsg{
			Name:          name,
			Agent:         agent,
			RepoPath:      repoPath,
			Base:          newBase,
			Prompt:        prompt,
			NoRepo:        newNoRepo,
			ShareWorktree: newShareWorktree,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		var info protocol.SessionInfo
		protocol.DecodePayload(resp, &info)

		if jsonOutput {
			return out.JSON(info)
		}

		location := info.WorktreePath
		if location == "" {
			location = "(no repo)"
		}
		out.Print("Created session %s (%s) in %s\n", info.Name, info.ID, location)

		if newBackground {
			return nil
		}

		return runAttachByID(c, info.ID)
	},
}

func init() {
	rootCmd.AddCommand(newCmd)
	newCmd.Flags().StringVar(&newAgent, "agent", "", "agent to use")
	newCmd.Flags().StringVar(&newBase, "base", "", "base branch")
	newCmd.Flags().BoolVar(&newBackground, "background", false, "create without attaching")
	newCmd.Flags().StringVarP(&newPrompt, "prompt", "p", "", "initial prompt for the agent")
	newCmd.Flags().StringVar(&newPromptFile, "prompt-file", "", "read initial prompt from file")
	newCmd.Flags().StringVarP(&newRepo, "repo", "C", "", "path to git repo (default: cwd)")
	newCmd.Flags().BoolVar(&newNoRepo, "no-repo", false, "create session without a git repo or worktree")
	newCmd.Flags().StringVar(&newShareWorktree, "share-worktree", "", "share another session's worktree (read-only)")
	newCmd.RegisterFlagCompletionFunc("agent", completeAgentNames)
	newCmd.RegisterFlagCompletionFunc("repo", completeRepoPaths)
	newCmd.RegisterFlagCompletionFunc("base", completeBranchNames)
	newCmd.RegisterFlagCompletionFunc("share-worktree", completeSessionNames)
}
