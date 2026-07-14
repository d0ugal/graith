package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	forkBackground bool
	forkAgent      string
	forkModel      string
)

var forkCmd = &cobra.Command{
	Use:   "fork <source-session> <new-name>",
	Short: "Fork a session (new worktree + agent conversation history)",
	Long: `Fork a session into a new worktree and branch, continuing its conversation.

By default the new session uses the same agent and natively resumes the source's
conversation. With --agent <target> it forks into a DIFFERENT agent: the source's
conversation is rendered to a neutral file and the new agent is seeded with it,
while the original session keeps running.

Git state: like any fork, the new worktree branches from the base branch, so the
source's changes (uncommitted edits and any commits on its branch) are not
carried over.`,
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		sourceName := args[0]

		newName := args[1]
		if err := daemon.ValidateSessionName(newName); err != nil {
			return err
		}

		if forkModel != "" && forkAgent == "" {
			return fmt.Errorf("--model requires --agent (it only applies to a cross-agent fork)")
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("list", struct{}{})

		listResp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(listResp, &list); err != nil {
			return err
		}

		var sourceID string

		for _, s := range list.Sessions {
			if s.Name == sourceName || s.ID == sourceName {
				sourceID = s.ID
				break
			}
		}

		if sourceID == "" {
			return fmt.Errorf("session %q not found", sourceName)
		}

		_ = c.SendControl("fork", protocol.ForkMsg{
			Name:            newName,
			SourceSessionID: sourceID,
			Agent:           forkAgent,
			Model:           forkModel,
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

		if info.MigratedFrom != "" {
			out.Printf("Forked session %s → %s (%s) [%s → %s] in %s\n",
				sourceName, info.Name, info.ID, info.MigratedFrom, info.Agent, info.WorktreePath)
			out.Printf("Note: new worktree branched from base — the source's changes (uncommitted edits and branch commits) are not present.\n")
		} else {
			out.Printf("Forked session %s → %s (%s) in %s\n", sourceName, info.Name, info.ID, info.WorktreePath)
		}

		if forkBackground {
			return nil
		}

		return runAttachByID(c, info.ID, nil)
	},
}

// registerForkCmd registers this command on rootCmd. Called from registerCommands.
func registerForkCmd() {
	rootCmd.AddCommand(forkCmd)
	forkCmd.Flags().BoolVar(&forkBackground, "background", false, "fork without attaching")
	forkCmd.Flags().StringVar(&forkAgent, "agent", "", "fork into a different agent, seeding it with the source's conversation history")
	forkCmd.Flags().StringVar(&forkModel, "model", "", "model for the target agent (cross-agent fork only; defaults to the target's default)")
}
