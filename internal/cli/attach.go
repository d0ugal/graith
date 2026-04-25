package cli

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:               "attach [name-or-id]",
	Aliases:           []string{"a"},
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
		result := client.RunOverlay(list.Sessions, "", previewFetcher())
		if result == nil {
			return nil
		}
		if result.Action == "delete" {
			c.SendControl("delete", protocol.DeleteMsg{SessionID: result.SessionID})
			c.ReadControlResponse()
			return nil
		}
		if result.Action == "restart" {
			c.SendControl("resume", protocol.ResumeMsg{SessionID: result.SessionID})
			resumeResp, err := c.ReadControlResponse()
			if err != nil {
				return err
			}
			if resumeResp.Type == "error" {
				var e protocol.ErrorMsg
				protocol.DecodePayload(resumeResp, &e)
				return fmt.Errorf("restart failed: %s", e.Message)
			}
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
	keys := client.PassthroughKeys{
		Prefix:      parsePrefixKey(cfg.Keybindings.Prefix),
		NextSession: parseKeyByte(cfg.Keybindings.NextSession),
		PrevSession: parseKeyByte(cfg.Keybindings.PrevSession),
		LastSession: parseKeyByte(cfg.Keybindings.LastSession),
		NewSession:  parseKeyByte(cfg.Keybindings.NewSession),
		ForkSession: parseKeyByte(cfg.Keybindings.ForkSession),
	}

	prevSessionID := ""

	opts := client.PassthroughOpts{
		Keys:      keys,
		SessionID: sessionID,
		Info:      &info,
	}
	if cfg.StatusBar.Enabled {
		opts.StatusBar = &client.StatusBarCfg{
			Position: cfg.StatusBar.Position,
		}
	}

	for {
		result := c.RunPassthrough(ctx, opts)
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

			overlayResult := client.RunOverlay(list.Sessions, sessionID, previewFetcher())
			if overlayResult == nil {
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc
				continue
			}
			if overlayResult.Action == "delete" {
				nc.SendControl("delete", protocol.DeleteMsg{SessionID: overlayResult.SessionID})
				nc.ReadControlResponse()
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc
				continue
			}
			if overlayResult.Action == "restart" {
				nc.SendControl("resume", protocol.ResumeMsg{SessionID: overlayResult.SessionID})
				resumeResp, _ := nc.ReadControlResponse()
				if resumeResp.Type == "error" {
					var e protocol.ErrorMsg
					protocol.DecodePayload(resumeResp, &e)
					out.Print("Restart failed: %s\n", e.Message)
					restoreScreen(sessionID)
					nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
					attachResp, _ := nc.ReadControlResponse()
					protocol.DecodePayload(attachResp, &info)
					opts.SessionID = sessionID
					opts.Info = &info
					c = nc
					continue
				}
			}
			restoreScreen(overlayResult.SessionID)
			nc.SendControl("attach", protocol.AttachMsg{SessionID: overlayResult.SessionID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)
			prevSessionID = sessionID
			sessionID = overlayResult.SessionID
			opts.SessionID = sessionID
			opts.Info = &info
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
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)
			opts.SessionID = sessionID
			opts.Info = &info
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
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc
			continue

		case client.ResultDisconnected:
			out.Print("Connection lost. Reconnecting...\n")
			nc, attachResp, err := reconnectToSession(sessionID)
			if err != nil {
				out.Print("Could not reconnect: %s\n", err)
				return nil
			}
			protocol.DecodePayload(attachResp, &info)
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc
			continue

		case client.ResultLastSession:
			if prevSessionID == "" {
				nc, err := freshClient()
				if err != nil {
					return err
				}
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc
				continue
			}
			nc, err := freshClient()
			if err != nil {
				return err
			}
			oldSessionID := sessionID
			sessionID = prevSessionID
			prevSessionID = oldSessionID
			nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc
			continue

		case client.ResultNextSession, client.ResultPrevSession:
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
			if err := protocol.DecodePayload(listResp, &list); err != nil {
				nc.Close()
				return err
			}

			ids := sortedSessionIDs(list.Sessions, sessionID)
			if next := adjacentSession(ids, sessionID, result == client.ResultNextSession); next != "" {
				prevSessionID = sessionID
				sessionID = next
			}
			nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc
			continue

		case client.ResultNewSession:
			name := client.RunNameInput("New Session")
			if name == "" {
				nc, err := freshClient()
				if err != nil {
					return err
				}
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc
				continue
			}
			nc, err := freshClient()
			if err != nil {
				return err
			}
			nc.SendControl("create", protocol.CreateMsg{
				Name:     name,
				RepoPath: info.RepoPath,
			})
			createResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}
			if createResp.Type == "error" {
				var e protocol.ErrorMsg
				protocol.DecodePayload(createResp, &e)
				out.Print("Create failed: %s\n", e.Message)
				nc2, err := freshClient()
				if err != nil {
					nc.Close()
					return err
				}
				nc.Close()
				restoreScreen(sessionID)
				nc2.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc2.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc2
				continue
			}
			var newInfo protocol.SessionInfo
			protocol.DecodePayload(createResp, &newInfo)
			restoreScreen(newInfo.ID)
			nc.SendControl("attach", protocol.AttachMsg{SessionID: newInfo.ID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)
			prevSessionID = sessionID
			sessionID = newInfo.ID
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc
			continue

		case client.ResultForkSession:
			name := client.RunNameInput("Fork Session")
			if name == "" {
				nc, err := freshClient()
				if err != nil {
					return err
				}
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc
				continue
			}
			nc, err := freshClient()
			if err != nil {
				return err
			}
			nc.SendControl("fork", protocol.ForkMsg{
				Name:            name,
				SourceSessionID: sessionID,
			})
			createResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}
			if createResp.Type == "error" {
				var e protocol.ErrorMsg
				protocol.DecodePayload(createResp, &e)
				out.Print("Fork failed: %s\n", e.Message)
				nc2, err := freshClient()
				if err != nil {
					nc.Close()
					return err
				}
				nc.Close()
				restoreScreen(sessionID)
				nc2.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc2.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc2
				continue
			}
			var newInfo protocol.SessionInfo
			protocol.DecodePayload(createResp, &newInfo)
			restoreScreen(newInfo.ID)
			nc.SendControl("attach", protocol.AttachMsg{SessionID: newInfo.ID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)
			prevSessionID = sessionID
			sessionID = newInfo.ID
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc
			continue

		case client.ResultDetached, client.ResultQuit:
			return nil
		}
	}
}

func restoreScreen(sessionID string) {
	snap := client.FetchScreenSnapshot(cfg, paths, cfgFile, sessionID)
	client.WriteScreenRestore(snap)
}

func previewFetcher() func(string) string {
	return func(sessionID string) string {
		return client.FetchScrollbackPreview(cfg, paths, cfgFile, sessionID)
	}
}

func freshClient() (*client.Client, error) {
	return client.Connect(cfg, paths, cfgFile)
}

// sortedSessionIDs returns session IDs in the same order as the overlay:
// grouped by repo (alphabetically), then within each group sorted by
// current → running → recently attached → alphabetical.
func sortedSessionIDs(sessions []protocol.SessionInfo, currentSessionID string) []string {
	groups := map[string][]protocol.SessionInfo{}
	var repoOrder []string
	seen := map[string]bool{}
	for _, s := range sessions {
		repo := s.RepoName
		if repo == "" {
			repo = "(no repo)"
		}
		if !seen[repo] {
			repoOrder = append(repoOrder, repo)
			seen[repo] = true
		}
		groups[repo] = append(groups[repo], s)
	}
	sort.Strings(repoOrder)
	for _, g := range groups {
		client.SortSessions(g)
	}
	var ids []string
	for _, repo := range repoOrder {
		for _, s := range groups[repo] {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

func adjacentSession(ids []string, current string, forward bool) string {
	if len(ids) < 2 {
		return ""
	}
	for i, id := range ids {
		if id == current {
			if forward {
				return ids[(i+1)%len(ids)]
			}
			return ids[(i-1+len(ids))%len(ids)]
		}
	}
	return ""
}

func reconnectToSession(sessionID string) (*client.Client, protocol.Envelope, error) {
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
			return nil, protocol.Envelope{}, fmt.Errorf("session unavailable: %s", e.Message)
		}
		return c, resp, nil
	}
	return nil, protocol.Envelope{}, fmt.Errorf("timed out after 10s")
}
