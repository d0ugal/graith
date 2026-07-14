package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	newAgent               string
	newBase                string
	newBackground          bool
	newPrompt              string
	newPromptFile          string
	newModel               string
	newRepo                string
	newNoRepo              bool
	newMirror              string
	newInPlace             bool
	newAllowConcurrent     bool
	newSkipModelValidation bool
	newYolo                bool
	newHeadless            bool
	newNoFetch             bool
)

var newCmd = &cobra.Command{
	Use:     "new <name>",
	Aliases: []string{"n"},
	Short:   "Create a new agent session",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := daemon.ValidateSessionName(name); err != nil {
			return err
		}

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

		if newAllowConcurrent && !newInPlace {
			return fmt.Errorf("--allow-concurrent requires --in-place")
		}

		if newInPlace && newNoRepo {
			return fmt.Errorf("--in-place and --no-repo are mutually exclusive")
		}

		if newInPlace && newMirror != "" {
			return fmt.Errorf("--in-place and --mirror are mutually exclusive")
		}

		if newInPlace && newBase != "" {
			return fmt.Errorf("--in-place and --base are mutually exclusive (in-place sessions don't create branches)")
		}

		if newPrompt != "" && newPromptFile != "" {
			return fmt.Errorf("--prompt and --prompt-file are mutually exclusive")
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

		_ = c.SendControl("create", protocol.CreateMsg{
			Name:                name,
			ParentID:            os.Getenv("GRAITH_SESSION_ID"),
			Agent:               agent,
			RepoPath:            repoPath,
			Base:                newBase,
			Prompt:              prompt,
			Model:               newModel,
			NoRepo:              newNoRepo,
			Mirror:              newMirror,
			AgentHooks:          true,
			InPlace:             newInPlace,
			AllowConcurrent:     newAllowConcurrent,
			SkipModelValidation: newSkipModelValidation,
			Yolo:                newYolo,
			Headless:            newHeadless,
			NoFetch:             newNoFetch,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var info protocol.SessionInfo

		_ = protocol.DecodePayload(resp, &info)

		if jsonOutput {
			return out.JSON(info)
		}

		location := info.WorktreePath
		if location == "" {
			location = "(no repo)"
		}

		out.Printf("Created session %s (%s) in %s\n", info.Name, info.ID, location)

		// A headless session has no interactive PTY to attach to, so --headless
		// implies --background (attaching would only hit the "headless" refusal).
		if newBackground || newHeadless {
			if newHeadless && !newBackground {
				out.Printf("(headless session — watch it with `gr logs -f %s`)\n", info.Name)
			}

			return nil
		}

		return runAttachByID(c, info.ID, nil)
	},
}

// registerNewCmd registers this command on rootCmd. Called from registerCommands.
func registerNewCmd() {
	rootCmd.AddCommand(newCmd)
	newCmd.Flags().StringVar(&newAgent, "agent", "", "agent to use")
	newCmd.Flags().StringVar(&newBase, "base", "", "base branch")
	newCmd.Flags().BoolVar(&newBackground, "background", false, "create without attaching")
	newCmd.Flags().StringVarP(&newPrompt, "prompt", "p", "", "initial prompt for the agent")
	newCmd.Flags().StringVar(&newPromptFile, "prompt-file", "", "read initial prompt from file")
	newCmd.Flags().StringVarP(&newModel, "model", "m", "", "model for the agent to use (expands {model} in agent args)")
	newCmd.Flags().StringVarP(&newRepo, "repo", "C", "", "path to git repo (default: cwd)")
	newCmd.Flags().BoolVar(&newNoRepo, "no-repo", false, "create session without a git repo or worktree")
	newCmd.Flags().StringVar(&newMirror, "mirror", "", "mirror another session's worktree (read-only)")
	newCmd.Flags().BoolVar(&newInPlace, "in-place", false, "run agent directly in the repo without creating a worktree")
	newCmd.Flags().BoolVar(&newAllowConcurrent, "allow-concurrent", false, "allow multiple in-place sessions on the same repo")
	newCmd.Flags().BoolVar(&newSkipModelValidation, "skip-model-validation", false, "skip validate_model check (use models not in the validation list)")
	newCmd.Flags().BoolVar(&newYolo, "yolo", false, "auto-approve all tool requests for this session (no approval prompts)")
	newCmd.Flags().BoolVar(&newHeadless, "headless", false, "run as a headless stream-json session instead of an interactive PTY (experimental; Claude only)")
	newCmd.Flags().BoolVar(&newNoFetch, "no-fetch", false, "skip git fetch origin and create the worktree from local repo state (use when SSH auth is unavailable or offline)")
	_ = newCmd.RegisterFlagCompletionFunc("agent", completeAgentNames)
	_ = newCmd.RegisterFlagCompletionFunc("repo", completeRepoPaths)
	_ = newCmd.RegisterFlagCompletionFunc("base", completeBranchNames)
	_ = newCmd.RegisterFlagCompletionFunc("mirror", completeSessionNames)
}
