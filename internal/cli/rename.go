package cli

import (
	"fmt"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var renameCmd = &cobra.Command{
	Use:   "rename <name-or-id> <new-name>",
	Short: "Rename a session",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.New(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.Handshake()
		c.ReadControlResponse()

		c.SendControl("list", struct{}{})
		listResp, _ := c.ReadControlResponse()
		var list protocol.SessionListMsg
		protocol.DecodePayload(listResp, &list)

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

		c.SendControl("rename", protocol.RenameMsg{SessionID: sessionID, NewName: args[1]})
		resp, _ := c.ReadControlResponse()
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		out.Print("Renamed to %s\n", args[1])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(renameCmd)
}
