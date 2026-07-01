package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
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

// registerAttachCmd registers this command on rootCmd. Called from registerCommands.
func registerAttachCmd() {
	rootCmd.AddCommand(attachCmd)
}

func isInsideGraith() bool {
	return os.Getenv("GRAITH_ATTACHED") != "" || os.Getenv("GRAITH_SESSION_ID") != ""
}

// agentChoices returns the configured agent names (sorted, with the default
// first) and the default agent, for use by the interactive create form.
func agentChoices() ([]string, string) {
	return orderAgents(cfg.Agents, cfg.DefaultAgent), cfg.DefaultAgent
}

// orderAgents returns the agent names sorted alphabetically, with def hoisted to
// the front when it is present in the map. A def that is empty or absent leaves
// the list in plain sorted order.
func orderAgents(agents map[string]config.Agent, def string) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}

	sort.Strings(names)

	if def != "" {
		for i, n := range names {
			if n == def {
				names = append(names[:i], names[i+1:]...)
				names = append([]string{def}, names...)

				break
			}
		}
	}

	return names
}

func runAttach(cmd *cobra.Command, name string) error {
	if isInsideGraith() {
		return fmt.Errorf("cannot attach from inside a graith session (nested sessions are not supported)")
	}

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
		out.Printf("No sessions. Create one with: gr new <name>\n")
		return nil
	}

	if name == "" {
		repos := client.DiscoverRepos(cfg.AllowedRepoPaths, list.Sessions)
		agents, defaultAgent := agentChoices()

		result := client.RunOverlay(list.Sessions, "", previewFetcher(), sessionRefresher(), deleteSession, restartSession, stopSession, toggleStar, paths.Profile, nil, repos, cfg.Overlay.ShortcutKeys, agents, defaultAgent)
		if result == nil || result.Action == "" {
			return nil
		}

		if result.Action == "create" {
			c.SendControl("create", protocol.CreateMsg{
				Name:     result.CreateName,
				RepoPath: result.CreateRepoPath,
				Agent:    result.CreateAgent,
			})

			createResp, err := c.ReadControlResponse()
			if err != nil {
				return err
			}

			if createResp.Type == "error" {
				var e protocol.ErrorMsg
				protocol.DecodePayload(createResp, &e)
				out.Printf("Create failed: %s\n", e.Message)

				return nil
			}

			var newInfo protocol.SessionInfo
			protocol.DecodePayload(createResp, &newInfo)

			return runAttachByID(c, newInfo.ID, result.Collapsed)
		}

		return runAttachByID(c, result.SessionID, result.Collapsed)
	}

	for _, s := range list.Sessions {
		if s.Name == name || s.ID == name {
			return runAttachByID(c, s.ID, nil)
		}
	}

	return fmt.Errorf("session %q not found", name)
}

func runAttachByID(c *client.Client, sessionID string, initialCollapsed map[string]bool) error {
	if isInsideGraith() {
		return fmt.Errorf("cannot attach from inside a graith session (nested sessions are not supported)")
	}

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

	ctx := context.Background()
	keys := client.PassthroughKeys{
		Prefix:              parsePrefixKey(cfg.Keybindings.Prefix),
		NextSession:         parseKeyByte(cfg.Keybindings.NextSession),
		PrevSession:         parseKeyByte(cfg.Keybindings.PrevSession),
		LastSession:         parseKeyByte(cfg.Keybindings.LastSession),
		NewSession:          parseKeyByte(cfg.Keybindings.NewSession),
		ForkSession:         parseKeyByte(cfg.Keybindings.ForkSession),
		OrchestratorSession: parseKeyByte(cfg.Keybindings.OrchestratorSession),
	}

	prevSessionID := ""
	overlayCollapsed := initialCollapsed

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

	opts.AutoPopApproval = cfg.Approvals.AutoPop

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

			repos := client.DiscoverRepos(cfg.AllowedRepoPaths, list.Sessions)
			agents, defaultAgent := agentChoices()

			overlayResult := client.RunOverlay(list.Sessions, sessionID, previewFetcher(), sessionRefresher(), deleteSession, restartSession, stopSession, toggleStar, paths.Profile, overlayCollapsed, repos, cfg.Overlay.ShortcutKeys, agents, defaultAgent)
			if overlayResult != nil {
				overlayCollapsed = overlayResult.Collapsed
			}

			if overlayResult != nil && overlayResult.Action == "stopped-current" {
				// The user stopped the session they were attached to. Exit
				// instead of reattaching, which would auto-resume it.
				nc.Close()
				resetTerminal()

				return nil
			}

			if overlayResult == nil || overlayResult.Action == "" {
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			if overlayResult.Action == "create" {
				nc.SendControl("create", protocol.CreateMsg{
					Name:     overlayResult.CreateName,
					RepoPath: overlayResult.CreateRepoPath,
					Agent:    overlayResult.CreateAgent,
				})

				createResp, err := nc.ReadControlResponse()
				if err != nil {
					nc.Close()
					return err
				}

				if createResp.Type == "error" {
					var e protocol.ErrorMsg
					protocol.DecodePayload(createResp, &e)
					out.Printf("Create failed: %s\n", e.Message)

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

		case client.ResultMessageOverlay:
			nc, err := freshClient()
			if err != nil {
				return err
			}
			// Build a peer-id -> name map from the live session list so the
			// rail can label conversations (sent messages carry only a peer id).
			nc.SendControl("list", struct{}{})

			msgListResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			var msgList protocol.SessionListMsg
			protocol.DecodePayload(msgListResp, &msgList)

			names := make(map[string]string, len(msgList.Sessions))
			for _, s := range msgList.Sessions {
				names[s.ID] = s.Name
			}

			client.RunMessageOverlay(sessionID, conversationFetcher(sessionID), names)

			restoreScreen(sessionID)
			nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)

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

			var worktreePath string

			for _, s := range infoList.Sessions {
				if s.ID == sessionID {
					worktreePath = s.WorktreePath
					break
				}
			}

			if worktreePath == "" {
				out.Printf("Shell failed: session %s not found\n", sessionID)
			} else {
				resetTerminal()

				if err := client.RunShellInWorktree(worktreePath); err != nil {
					out.Printf("Shell failed: %s\n", err)
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
				out.Printf("Resume failed: %s\n", e.Message)
			}

			nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultDisconnected:
			out.Printf("Connection lost. Reconnecting...\n")

			nc, attachResp, err := reconnectToSession(sessionID)
			if err != nil {
				out.Printf("Could not reconnect: %s\n", err)
				resetTerminal()

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

			sessionID, prevSessionID = prevSessionID, sessionID
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

			var newSessionList protocol.SessionListMsg
			protocol.DecodePayload(listResp, &newSessionList)
			repos := client.DiscoverRepos(cfg.AllowedRepoPaths, newSessionList.Sessions)
			agents, defaultAgent := agentChoices()

			name, repoPath, agent := client.RunCreateInput(info.RepoPath, repos, agents, defaultAgent)
			if name == "" {
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			nc.SendControl("create", protocol.CreateMsg{
				Name:     name,
				RepoPath: repoPath,
				Agent:    agent,
			})

			createResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			if createResp.Type == "error" {
				var e protocol.ErrorMsg
				protocol.DecodePayload(createResp, &e)
				out.Printf("Create failed: %s\n", e.Message)

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
				out.Printf("Fork failed: %s\n", e.Message)

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

		case client.ResultApprovalOverlay:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			nc.SendControl("approval_list", struct{}{})

			listResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			var notif protocol.ApprovalNotificationMsg
			protocol.DecodePayload(listResp, &notif)

			if len(notif.Pending) == 0 {
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			results := client.RunApprovalOverlay(notif.Pending)
			for _, r := range results {
				nc.SendControl("approval_respond", protocol.ApprovalRespondMsg{
					RequestID: r.RequestID,
					Decision:  r.Decision,
					Reason:    r.Reason,
				})
				nc.ReadControlResponse()
			}

			restoreScreen(sessionID)
			nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultOrchestratorSession:
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

			var orchID string

			for _, s := range list.Sessions {
				if s.SystemKind == "orchestrator" {
					orchID = s.ID
					break
				}
			}

			if orchID == "" {
				out.Printf("Orchestrator not enabled — set orchestrator.enabled = true in config.toml\n")
				restoreScreen(sessionID)
				nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			if orchID == sessionID {
				if prevSessionID != "" {
					sessionID, prevSessionID = prevSessionID, sessionID
					nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
					attachResp, _ := nc.ReadControlResponse()
					protocol.DecodePayload(attachResp, &info)

					opts.SessionID = sessionID
					opts.Info = &info
					c = nc

					continue
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

			var orchStatus string

			for _, s := range list.Sessions {
				if s.ID == orchID {
					orchStatus = s.Status
					break
				}
			}

			if orchStatus == "stopped" || orchStatus == "errored" {
				nc.SendControl("resume", protocol.ResumeMsg{SessionID: orchID})

				resumeResp, err := nc.ReadControlResponse()
				if err != nil {
					nc.Close()
					return err
				}

				if resumeResp.Type == "error" {
					var e protocol.ErrorMsg
					protocol.DecodePayload(resumeResp, &e)
					out.Printf("Orchestrator resume failed: %s\n", e.Message)
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

			restoreScreen(orchID)
			nc.SendControl("attach", protocol.AttachMsg{SessionID: orchID})
			attachResp, _ := nc.ReadControlResponse()
			protocol.DecodePayload(attachResp, &info)

			prevSessionID = sessionID
			sessionID = orchID
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultDetached, client.ResultQuit:
			resetTerminal()
			return nil
		}
	}
}

// resetTerminal undoes terminal state that agents (Claude Code, etc.) may have
// set during the session: alternate screen buffer, mouse tracking, bracketed
// paste, Kitty keyboard protocol, hidden cursor, scroll regions, and text
// attributes. Safe to call on any terminal — unsupported sequences are ignored.
func resetTerminal() {
	fmt.Print("" +
		"\x1b[r" + // reset scroll region
		"\x1b[?1049l" + // leave alternate screen buffer
		"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" + // disable mouse tracking
		"\x1b[?1004l" + // disable focus event reporting
		"\x1b[?2004l" + // disable bracketed paste
		"\x1b[?1l\x1b>" + // leave application cursor keys and keypad mode
		"\x1b[<u" + // pop Kitty keyboard protocol
		"\x1b[0m" + // reset text attributes (bold, color, etc.)
		"\x1b[?25h" + // show cursor
		"\x1b[2J\x1b[H", // clear screen, cursor home
	)
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

func sessionRefresher() func() []protocol.SessionInfo {
	return func() []protocol.SessionInfo {
		c, err := freshClient()
		if err != nil {
			return nil
		}
		defer c.Close()

		c.SendControl("list", struct{}{})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return nil
		}

		if resp.Type == "error" {
			return nil
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return nil
		}

		return list.Sessions
	}
}

func conversationFetcher(sessionID string) func() ([]protocol.ConversationMessage, bool) {
	return func() ([]protocol.ConversationMessage, bool) {
		msgs, err := client.FetchConversation(cfg, paths, cfgFile, sessionID)
		if err != nil {
			return nil, false
		}

		return msgs, true
	}
}

func freshClient() (*client.Client, error) {
	return client.ConnectPassive(cfg, paths, cfgFile)
}

func toggleStar(sessionID string, star bool) error {
	sc, err := freshClient()
	if err != nil {
		return err
	}
	defer sc.Close()

	if star {
		sc.SendControl("star", protocol.StarMsg{SessionID: sessionID})
	} else {
		sc.SendControl("unstar", protocol.UnstarMsg{SessionID: sessionID})
	}

	resp, err := sc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	return nil
}

func restartSession(sessionID string) error {
	rc, err := freshClient()
	if err != nil {
		return err
	}
	defer rc.Close()

	rc.SendControl("restart", protocol.RestartMsg{SessionID: sessionID})

	resp, err := rc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	return nil
}

func stopSession(sessionID string) error {
	sc, err := freshClient()
	if err != nil {
		return err
	}
	defer sc.Close()

	sc.SendControl("stop", protocol.StopMsg{SessionID: sessionID})

	resp, err := sc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	return nil
}

func deleteSession(sessionID string) error {
	dc, err := freshClient()
	if err != nil {
		return err
	}
	defer dc.Close()

	dc.SendControl("delete", protocol.DeleteMsg{SessionID: sessionID})

	resp, err := dc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	return nil
}

// sortedSessionIDs returns session IDs in the same order as the overlay:
// grouped by repo (alphabetically), then within each group sorted by
// running status first, then alphabetically by name.
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
