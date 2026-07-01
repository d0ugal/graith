package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	migrateAgent      string
	migrateModel      string
	migrateBackground bool
)

var migrateCmd = &cobra.Command{
	Use:   "migrate <name-or-id>",
	Short: "Migrate a session to a different agent in place",
	Long: "Swap the agent on an existing session (e.g. claude -> codex) without leaving the worktree.\n" +
		"The current agent's conversation is rendered to a context file, the agent is stopped, and the\n" +
		"target agent is started in the same worktree seeded with that history. This is a lossy reseed,\n" +
		"not a native resume: reasoning/thinking and exact tool-call replay are not carried over. The\n" +
		"agent process is restarted, so attached clients re-attach to the new agent.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if migrateAgent == "" {
			return fmt.Errorf("--agent is required")
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		c.SendControl("migrate", protocol.MigrateMsg{
			SessionID: sessionID,
			Agent:     migrateAgent,
			Model:     migrateModel,
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

		out.Print("Migrated session %s → agent %s (%s)\n", info.Name, info.Agent, info.ID)

		if migrateBackground {
			return nil
		}

		return runAttachByID(c, info.ID, nil)
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().StringVar(&migrateAgent, "agent", "", "target agent to migrate to (required)")
	migrateCmd.Flags().StringVar(&migrateModel, "model", "", "model for the target agent (default: target's default)")
	migrateCmd.Flags().BoolVar(&migrateBackground, "background", false, "migrate without attaching")
}
