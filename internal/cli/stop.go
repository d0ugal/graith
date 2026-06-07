package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:               "stop <name-or-id>",
	Short:             "Stop a running session without deleting it",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		c.SendControl("stop", protocol.StopMsg{SessionID: sessionID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		out.Print("Session stopped (worktree preserved)\n")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
