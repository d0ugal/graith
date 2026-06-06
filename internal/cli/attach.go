package cli

import (
	"context"
	"fmt"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:   "attach [name]",
	Short: "Attach to a session",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		return runAttach(cmd, name)
	},
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

func runAttach(cmd *cobra.Command, name string) error {
	c, err := client.New(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Handshake(); err != nil {
		return err
	}
	c.ReadControlResponse()

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	var list protocol.SessionListMsg
	protocol.DecodePayload(resp, &list)

	if len(list.Sessions) == 0 {
		out.Print("No sessions. Create one with: gr new <name>\n")
		return nil
	}

	if name == "" {
		result := client.RunOverlay(list.Sessions)
		if result == nil {
			return nil
		}
		if result.Action == "delete" {
			c.SendControl("delete", protocol.DeleteMsg{SessionID: result.SessionID})
			c.ReadControlResponse()
			return nil
		}
		return runAttachByID(c, result.SessionID)
	}

	for _, s := range list.Sessions {
		if s.Name == name || s.ID == name {
			return runAttachByID(c, s.ID)
		}
	}

	return fmt.Errorf("session %q not found", name)
}

func runAttachByID(c *client.Client, sessionID string) error {
	c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		return fmt.Errorf("%s", e.Message)
	}

	ctx := context.Background()
	prefixByte := byte(0x02) // ctrl+b

	for {
		result := c.RunPassthrough(ctx, prefixByte)
		switch result {
		case client.ResultOverlay:
			c.SendControl("detach", struct{}{})
			c.ReadControlResponse()

			c.SendControl("list", struct{}{})
			listResp, err := c.ReadControlResponse()
			if err != nil {
				return err
			}
			var list protocol.SessionListMsg
			protocol.DecodePayload(listResp, &list)

			overlayResult := client.RunOverlay(list.Sessions)
			if overlayResult == nil {
				c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				c.ReadControlResponse()
				continue
			}
			if overlayResult.Action == "delete" {
				c.SendControl("delete", protocol.DeleteMsg{SessionID: overlayResult.SessionID})
				c.ReadControlResponse()
				c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				c.ReadControlResponse()
				continue
			}
			c.SendControl("attach", protocol.AttachMsg{SessionID: overlayResult.SessionID})
			c.ReadControlResponse()
			sessionID = overlayResult.SessionID
			continue

		case client.ResultShell:
			c.SendControl("detach", struct{}{})
			c.ReadControlResponse()

			c.SendControl("list", struct{}{})
			infoResp, _ := c.ReadControlResponse()
			var infoList protocol.SessionListMsg
			protocol.DecodePayload(infoResp, &infoList)
			for _, s := range infoList.Sessions {
				if s.ID == sessionID {
					client.RunShellInWorktree(s.WorktreePath)
					break
				}
			}

			c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			c.ReadControlResponse()
			continue

		case client.ResultDetached, client.ResultQuit:
			return nil
		}
	}
}
