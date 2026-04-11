package cli

import (
	"fmt"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/protocol"
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

		c.SendControl("list", struct{}{})
		listResp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(listResp, &list); err != nil {
			return err
		}

		var sessionID string
		for _, s := range list.Sessions {
			if s.Name == args[0] || s.ID == args[0] {
				sessionID = s.ID
				break
			}
		}
		if sessionID == "" {
			return fmt.Errorf("session %q not found", args[0])
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
