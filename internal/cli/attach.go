package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:               "attach [name-or-id]",
	Short:             "Attach to a session",
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeSessionNames,
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
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return err
	}

	if len(list.Sessions) == 0 {
		out.Print("No sessions. Create one with: gr new <name>\n")
		return nil
	}

	if name == "" {
		result := client.RunOverlay(list.Sessions, previewFetcher())
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

	var info protocol.SessionInfo
	protocol.DecodePayload(resp, &info)
	if info.Status == "stopped" {
		fmt.Print("Session is stopped. Resume? [Y/n] ")
		var answer string
		fmt.Scanln(&answer)
		if answer == "" || answer == "y" || answer == "Y" || answer == "yes" {
			c.SendControl("resume", protocol.ResumeMsg{SessionID: sessionID})
			resumeResp, err := c.ReadControlResponse()
			if err != nil {
				return err
			}
			if resumeResp.Type == "error" {
				var e protocol.ErrorMsg
				protocol.DecodePayload(resumeResp, &e)
				return fmt.Errorf("resume failed: %s", e.Message)
			}
		}
	}

	ctx := context.Background()
	prefixByte := parsePrefixKey(cfg.Keybindings.Prefix)

	for {
		result := c.RunPassthrough(ctx, prefixByte)
		// RunPassthrough closes the connection — c is dead after this point.
		// Every code path must either return or create a fresh client.

		switch result {
		case client.ResultOverlay:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			nc.SendControl("list", struct{}{})
			listResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}
			var list protocol.SessionListMsg
			protocol.DecodePayload(listResp, &list)

			overlayResult := client.RunOverlay(list.Sessions, previewFetcher())
			if overlayResult == nil {
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				nc.ReadControlResponse()
				c = nc
				continue
			}
			if overlayResult.Action == "delete" {
				nc.SendControl("delete", protocol.DeleteMsg{SessionID: overlayResult.SessionID})
				nc.ReadControlResponse()
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				nc.ReadControlResponse()
				c = nc
				continue
			}
			nc.SendControl("attach", protocol.AttachMsg{SessionID: overlayResult.SessionID})
			nc.ReadControlResponse()
			sessionID = overlayResult.SessionID
			c = nc
			continue

		case client.ResultShell:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			nc.SendControl("list", struct{}{})
			infoResp, _ := nc.ReadControlResponse()
			var infoList protocol.SessionListMsg
			protocol.DecodePayload(infoResp, &infoList)
			for _, s := range infoList.Sessions {
				if s.ID == sessionID {
					client.RunShellInWorktree(s.WorktreePath)
					break
				}
			}

			nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			nc.ReadControlResponse()
			c = nc
			continue

		case client.ResultRestart:
			nc, err := freshClient()
			if err != nil {
				return err
			}
			nc.SendControl("resume", protocol.ResumeMsg{SessionID: sessionID})
			resumeResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}
			if resumeResp.Type == "error" {
				var e protocol.ErrorMsg
				protocol.DecodePayload(resumeResp, &e)
				out.Print("Resume failed: %s\n", e.Message)
			}
			nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			nc.ReadControlResponse()
			c = nc
			continue

		case client.ResultDisconnected:
			out.Print("Connection lost. Reconnecting...\n")
			nc, err := reconnectToSession(sessionID)
			if err != nil {
				out.Print("Could not reconnect: %s\n", err)
				return nil
			}
			c = nc
			continue

		case client.ResultDetached, client.ResultQuit:
			return nil
		}
	}
}

func previewFetcher() func(string) string {
	return func(sessionID string) string {
		return client.FetchScrollbackPreview(cfg, paths, cfgFile, sessionID, 50)
	}
}

func freshClient() (*client.Client, error) {
	return client.Connect(cfg, paths, cfgFile)
}

func reconnectToSession(sessionID string) (*client.Client, error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		c, err := freshClient()
		if err != nil {
			continue
		}
		c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			c.Close()
			continue
		}
		if resp.Type == "error" {
			c.Close()
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return nil, fmt.Errorf("session unavailable: %s", e.Message)
		}
		return c, nil
	}
	return nil, fmt.Errorf("timed out after 10s")
}
