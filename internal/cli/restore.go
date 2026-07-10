package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var restoreChildren bool

var restoreCmd = &cobra.Command{
	Use:   "restore <name-or-id>",
	Short: "Restore a soft-deleted session within the retention window",
	Long: "Recover a session that was soft-deleted with `gr delete`, as long as its retention " +
		"window has not elapsed. The session returns to the stopped state so it can be resumed.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeDeletedSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		session, err := resolveDeletedSessionInfo(c, args[0])
		if err != nil {
			return err
		}

		_ = c.SendControl("restore", protocol.RestoreMsg{SessionID: session.ID, Children: restoreChildren})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var result protocol.RestoreResultMsg

		_ = protocol.DecodePayload(resp, &result)

		if jsonOutput {
			return out.JSON(result)
		}

		printRestoreResult(result)

		return nil
	},
}

// printRestoreResult reports what was restored and, for a bare restore that
// left hidden children behind, how to bring the subtree back.
func printRestoreResult(r protocol.RestoreResultMsg) {
	if len(r.Sessions) == 0 {
		out.Printf("Nothing restored\n")
		return
	}

	if len(r.Sessions) > 1 {
		out.Printf("Restored %d sessions (stopped). Resume one with: gr resume <name>\n", len(r.Sessions))
		return
	}

	s := r.Sessions[0]
	out.Printf("Restored %s (stopped). Resume it with: gr resume %s\n", s.Name, s.Name)

	if r.DeletedDescendants > 0 {
		out.Printf("Note: %d soft-deleted descendant(s) remain hidden — restore them with: gr restore %s --children\n",
			r.DeletedDescendants, s.Name)
	}
}

// registerRestoreCmd registers this command on rootCmd. Called from registerCommands.
func registerRestoreCmd() {
	restoreCmd.Flags().BoolVar(&restoreChildren, "children", false, "also restore all soft-deleted descendant sessions")
	rootCmd.AddCommand(restoreCmd)
}
