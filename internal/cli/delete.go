package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:               "delete <name-or-id>",
	Short:             "Delete a session by name or ID",
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

		c.SendControl("delete", protocol.DeleteMsg{SessionID: sessionID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		out.Print("Session deleted\n")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)
}
