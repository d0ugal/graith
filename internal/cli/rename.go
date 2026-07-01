package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var renameCmd = &cobra.Command{
	Use:               "rename <name-or-id> <new-name>",
	Short:             "Rename a session",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := daemon.ValidateSessionName(args[1]); err != nil {
			return err
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

		_ = c.SendControl("rename", protocol.RenameMsg{SessionID: sessionID, NewName: args[1]})

		resp, _ := c.ReadControlResponse()
		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		out.Printf("Renamed to %s\n", args[1])

		return nil
	},
}

// registerRenameCmd registers this command on rootCmd. Called from registerCommands.
func registerRenameCmd() {
	rootCmd.AddCommand(renameCmd)
}
