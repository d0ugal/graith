package cli

import (
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var typeCmd = &cobra.Command{
	Use:               "type <name-or-id> <text>",
	Short:             "Type text into a session's PTY stdin",
	Args:              cobra.MinimumNArgs(2),
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

		noNewline, _ := cmd.Flags().GetBool("no-newline")
		text := strings.Join(args[1:], " ")

		c.SendControl("type", protocol.TypeMsg{
			SessionID: sessionID,
			Input:     text,
			NoNewline: noNewline,
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

		out.Print("Typed into session\n")
		return nil
	},
}

func init() {
	typeCmd.Flags().Bool("no-newline", false, "Do not append a newline after the text")
	rootCmd.AddCommand(typeCmd)
}
