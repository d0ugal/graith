package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

// attachYes skips the convert-to-interactive confirmation prompt for headless
// sessions (`gr attach --yes`). See runAttachByID / issue #1137.
var attachYes bool

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
	attachCmd.Flags().BoolVarP(&attachYes, "yes", "y", false, "skip the convert-to-interactive confirmation when attaching to a headless session")
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

// passthroughKeysFromConfig builds the prefix-action keybindings for the attach
// passthrough loop from the [keybindings] config table.
func passthroughKeysFromConfig() client.PassthroughKeys {
	return client.PassthroughKeys{
		Prefix:              parsePrefixKey(cfg.Keybindings.Prefix),
		Detach:              parseKeyByte(cfg.Keybindings.Detach),
		SessionList:         parseKeyByte(cfg.Keybindings.SessionList),
		Shell:               parseKeyByte(cfg.Keybindings.Shell),
		NextSession:         parseKeyByte(cfg.Keybindings.NextSession),
		PrevSession:         parseKeyByte(cfg.Keybindings.PrevSession),
		LastSession:         parseKeyByte(cfg.Keybindings.LastSession),
		NewSession:          parseKeyByte(cfg.Keybindings.NewSession),
		ForkSession:         parseKeyByte(cfg.Keybindings.ForkSession),
		OrchestratorSession: parseKeyByte(cfg.Keybindings.OrchestratorSession),
		RenameSession:       parseKeyByte(cfg.Keybindings.RenameSession),
		ScrollMode:          parseKeyByte(cfg.Keybindings.ScrollMode),
	}
}

// overlayKeysFromConfig builds the session-picker keybindings from the
// [keybindings] config table.
func overlayKeysFromConfig() client.OverlayKeys {
	return client.OverlayKeys{
		DeleteSession: cfg.Keybindings.DeleteSession,
		ResumeSession: cfg.Keybindings.ResumeSession,
		Search:        cfg.Keybindings.Search,
	}
}

func runAttach(cmd *cobra.Command, name string) error {
	if isInsideGraith() {
		return errors.New("cannot attach from inside a graith session (nested sessions are not supported)")
	}

	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	_ = c.SendControl("list", struct{}{})

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

		result := client.RunOverlay(client.RunOverlayOpts{
			Sessions:         list.Sessions,
			CurrentSessionID: "",
			FetchPreview:     previewFetcher(),
			RefreshSessions:  sessionRefresher(),
			RefreshDeleted:   deletedRefresher(),
			DeleteSession:    deleteSession,
			RestartSession:   restartSession,
			StopSession:      stopSession,
			ToggleStar:       toggleStar,
			RestoreSession:   restoreSession,
			Profile:          paths.Profile,
			Collapsed:        nil,
			RepoSuggestions:  repos,
			ShortcutKeys:     cfg.Overlay.ShortcutKeys,
			Agents:           agents,
			DefaultAgent:     defaultAgent,
			Keys:             overlayKeysFromConfig(),
		})
		if result == nil || result.Action == "" {
			return nil
		}

		if result.Action == "create" {
			_ = c.SendControl("create", protocol.CreateMsg{
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

				_ = protocol.DecodePayload(createResp, &e)
				out.Printf("Create failed: %s\n", e.Message)

				return nil
			}

			var newInfo protocol.SessionInfo

			_ = protocol.DecodePayload(createResp, &newInfo)

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

// attachWithConvert performs the attach handshake, transparently handling a
// headless session that must first be converted to interactive (issue #1137).
// The daemon answers an attach to a headless session with "convert_required"
// (attaching would restart it as a PTY via `claude --resume`); this prompts the
// human to confirm (unless --yes), sends "attach_convert" to perform the swap,
// then re-attaches to the now-interactive session. It returns the attached
// SessionInfo and attached=true on success, or attached=false when the human
// declines the convert.
func attachWithConvert(c *client.Client, sessionID string) (protocol.SessionInfo, bool, error) {
	var info protocol.SessionInfo

	converted := false

	for {
		_ = c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return info, false, err
		}

		switch resp.Type {
		case "error":
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return info, false, fmt.Errorf("%s", e.Message)

		case "convert_required":
			if converted {
				// We already converted, yet the daemon still reports headless — bail
				// rather than loop forever.
				return info, false, fmt.Errorf("session %q is still headless after convert", sessionID)
			}

			var cr protocol.ConvertRequiredMsg

			_ = protocol.DecodePayload(resp, &cr)

			if !confirmConvert(cr.Name) {
				out.Printf("Aborted\n")
				return info, false, nil
			}

			_ = c.SendControl("attach_convert", protocol.AttachConvertMsg{SessionID: sessionID})

			convResp, err := c.ReadControlResponse()
			if err != nil {
				return info, false, err
			}

			if convResp.Type == "error" {
				var e protocol.ErrorMsg

				_ = protocol.DecodePayload(convResp, &e)

				return info, false, fmt.Errorf("convert failed: %s", e.Message)
			}

			// Require the expected success type rather than treating any non-error
			// reply as success — a malformed/unexpected control frame shouldn't
			// advance the handshake.
			if convResp.Type != "converted" {
				return info, false, fmt.Errorf("unexpected response to attach_convert: %q", convResp.Type)
			}

			converted = true
			// Loop back and attach to the now-interactive session.

		case "attached":
			if err := protocol.DecodePayload(resp, &info); err != nil {
				return info, false, fmt.Errorf("decode attach response: %w", err)
			}

			return info, true, nil

		default:
			return info, false, fmt.Errorf("unexpected response to attach: %q", resp.Type)
		}
	}
}

// confirmConvert asks the human whether to convert a headless session to
// interactive. --yes (attachYes) skips the prompt. A non-terminal stdin is
// treated as a decline (fail-safe: don't restart a session unattended).
func confirmConvert(name string) bool {
	if attachYes {
		return true
	}

	out.Printf("%q is a headless session. Attaching will restart it as an interactive session (conversation is preserved). Continue? [y/N] ", name)

	reader := bufio.NewReader(os.Stdin)

	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	answer = strings.TrimSpace(strings.ToLower(answer))

	return answer == "y" || answer == "yes"
}

// reattachAfterOverlayFailure reports a failed create/fork initiated from the
// overlay, then opens a fresh client, reattaches to the original session, and
// updates opts. It returns the reattached client to assign to c. verb is
// "Create" or "Fork". Shared by the overlay create/fork error-recovery paths.
func reattachAfterOverlayFailure(nc *client.Client, sessionID, verb string, resp protocol.Envelope, opts *client.PassthroughOpts, info *protocol.SessionInfo) (*client.Client, error) {
	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(resp, &e)
	out.Printf("%s failed: %s\n", verb, e.Message)

	nc2, err := freshClient()
	if err != nil {
		nc.Close()
		return nil, err
	}

	nc.Close()
	restoreScreen(sessionID)
	_ = nc2.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
	attachResp, _ := nc2.ReadControlResponse()
	_ = protocol.DecodePayload(attachResp, info)

	opts.SessionID = sessionID
	opts.Info = info

	return nc2, nil
}

func runAttachByID(c *client.Client, sessionID string, initialCollapsed map[string]bool) error {
	if isInsideGraith() {
		return errors.New("cannot attach from inside a graith session (nested sessions are not supported)")
	}

	info, attached, err := attachWithConvert(c, sessionID)
	if err != nil {
		return err
	}

	if !attached {
		// Convert declined by the user — nothing to attach to.
		return nil
	}

	ctx := context.Background()
	keys := passthroughKeysFromConfig()

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
	opts.DragArrowKeys = cfg.Input.DragArrowKeys
	opts.DragArrowThreshold = cfg.Input.DragArrowThreshold

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

			_ = nc.SendControl("list", struct{}{})

			listResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			var list protocol.SessionListMsg

			_ = protocol.DecodePayload(listResp, &list)

			repos := client.DiscoverRepos(cfg.AllowedRepoPaths, list.Sessions)
			agents, defaultAgent := agentChoices()

			overlayResult := client.RunOverlay(client.RunOverlayOpts{
				Sessions:         list.Sessions,
				CurrentSessionID: sessionID,
				FetchPreview:     previewFetcher(),
				RefreshSessions:  sessionRefresher(),
				RefreshDeleted:   deletedRefresher(),
				DeleteSession:    deleteSession,
				RestartSession:   restartSession,
				StopSession:      stopSession,
				ToggleStar:       toggleStar,
				RestoreSession:   restoreSession,
				Profile:          paths.Profile,
				Collapsed:        overlayCollapsed,
				RepoSuggestions:  repos,
				ShortcutKeys:     cfg.Overlay.ShortcutKeys,
				Agents:           agents,
				DefaultAgent:     defaultAgent,
				Keys:             overlayKeysFromConfig(),
			})
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
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			if overlayResult.Action == "create" {
				_ = nc.SendControl("create", protocol.CreateMsg{
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
					nc2, err := reattachAfterOverlayFailure(nc, sessionID, "Create", createResp, &opts, &info)
					if err != nil {
						return err
					}

					c = nc2

					continue
				}

				var newInfo protocol.SessionInfo

				_ = protocol.DecodePayload(createResp, &newInfo)
				restoreScreen(newInfo.ID)
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: newInfo.ID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

				prevSessionID = sessionID
				sessionID = newInfo.ID
				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			restoreScreen(overlayResult.SessionID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: overlayResult.SessionID})
			attachResp, _ := nc.ReadControlResponse()

			// Switching to a headless session from the overlay can't convert
			// inline — conversion needs an interactive confirmation we can't safely
			// prompt for mid-loop (the terminal is between raw-mode passthroughs).
			// Rather than fall through to a dead passthrough on a connection the
			// daemon never attached, point the user at `gr attach <name>` (which
			// does convert) and reattach to the current session.
			if attachResp.Type == "convert_required" {
				var cr protocol.ConvertRequiredMsg

				_ = protocol.DecodePayload(attachResp, &cr)
				out.Printf("%q is a headless session — run `gr attach %s` to convert it to interactive.\n", cr.Name, cr.Name)

				restoreScreen(sessionID)
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				reResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(reResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			_ = protocol.DecodePayload(attachResp, &info)

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
			_ = nc.SendControl("list", struct{}{})

			msgListResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			var msgList protocol.SessionListMsg

			_ = protocol.DecodePayload(msgListResp, &msgList)

			names := make(map[string]string, len(msgList.Sessions))
			for _, s := range msgList.Sessions {
				names[s.ID] = s.Name
			}

			client.RunMessageOverlay(sessionID, conversationFetcher(sessionID), names)

			restoreScreen(sessionID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultShell:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			_ = nc.SendControl("list", struct{}{})
			infoResp, _ := nc.ReadControlResponse()

			var infoList protocol.SessionListMsg

			_ = protocol.DecodePayload(infoResp, &infoList)

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

			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultRestart:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			_ = nc.SendControl("resume", protocol.ResumeMsg{SessionID: sessionID})

			resumeResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			if resumeResp.Type == "error" {
				var e protocol.ErrorMsg

				_ = protocol.DecodePayload(resumeResp, &e)
				out.Printf("Resume failed: %s\n", e.Message)
			}

			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

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

			_ = protocol.DecodePayload(attachResp, &info)

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

				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

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
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultNextSession, client.ResultPrevSession:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			_ = nc.SendControl("list", struct{}{})

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

			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultNewSession:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			_ = nc.SendControl("list", struct{}{})

			listResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			var newSessionList protocol.SessionListMsg

			_ = protocol.DecodePayload(listResp, &newSessionList)
			repos := client.DiscoverRepos(cfg.AllowedRepoPaths, newSessionList.Sessions)
			agents, defaultAgent := agentChoices()

			name, repoPath, agent := client.RunCreateInput(info.RepoPath, repos, agents, defaultAgent)
			if name == "" {
				restoreScreen(sessionID)
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			_ = nc.SendControl("create", protocol.CreateMsg{
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
				nc2, err := reattachAfterOverlayFailure(nc, sessionID, "Create", createResp, &opts, &info)
				if err != nil {
					return err
				}

				c = nc2

				continue
			}

			var newInfo protocol.SessionInfo

			_ = protocol.DecodePayload(createResp, &newInfo)
			restoreScreen(newInfo.ID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: newInfo.ID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

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
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			nc, err := freshClient()
			if err != nil {
				return err
			}

			_ = nc.SendControl("fork", protocol.ForkMsg{
				Name:            name,
				SourceSessionID: sessionID,
			})

			createResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			if createResp.Type == "error" {
				nc2, err := reattachAfterOverlayFailure(nc, sessionID, "Fork", createResp, &opts, &info)
				if err != nil {
					return err
				}

				c = nc2

				continue
			}

			var newInfo protocol.SessionInfo

			_ = protocol.DecodePayload(createResp, &newInfo)
			restoreScreen(newInfo.ID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: newInfo.ID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

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

			_ = nc.SendControl("approval_list", struct{}{})

			listResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			var notif protocol.ApprovalNotificationMsg

			_ = protocol.DecodePayload(listResp, &notif)

			if len(notif.Pending) == 0 {
				restoreScreen(sessionID)
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			results := client.RunApprovalOverlay(notif.Pending)
			for _, r := range results {
				_ = nc.SendControl("approval_respond", protocol.ApprovalRespondMsg{
					RequestID: r.RequestID,
					Decision:  r.Decision,
					Reason:    r.Reason,
				})
				_, _ = nc.ReadControlResponse()
			}

			restoreScreen(sessionID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultOrchestratorSession:
			nc, err := freshClient()
			if err != nil {
				return err
			}

			_ = nc.SendControl("list", struct{}{})

			listResp, err := nc.ReadControlResponse()
			if err != nil {
				nc.Close()
				return err
			}

			var list protocol.SessionListMsg

			_ = protocol.DecodePayload(listResp, &list)

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
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

				opts.SessionID = sessionID
				opts.Info = &info
				c = nc

				continue
			}

			if orchID == sessionID {
				if prevSessionID != "" {
					sessionID, prevSessionID = prevSessionID, sessionID
					_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
					attachResp, _ := nc.ReadControlResponse()
					_ = protocol.DecodePayload(attachResp, &info)

					opts.SessionID = sessionID
					opts.Info = &info
					c = nc

					continue
				}

				restoreScreen(sessionID)
				_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
				attachResp, _ := nc.ReadControlResponse()
				_ = protocol.DecodePayload(attachResp, &info)

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
				_ = nc.SendControl("resume", protocol.ResumeMsg{SessionID: orchID})

				resumeResp, err := nc.ReadControlResponse()
				if err != nil {
					nc.Close()
					return err
				}

				if resumeResp.Type == "error" {
					var e protocol.ErrorMsg

					_ = protocol.DecodePayload(resumeResp, &e)
					out.Printf("Orchestrator resume failed: %s\n", e.Message)
					restoreScreen(sessionID)
					_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
					attachResp, _ := nc.ReadControlResponse()
					_ = protocol.DecodePayload(attachResp, &info)

					opts.SessionID = sessionID
					opts.Info = &info
					c = nc

					continue
				}
			}

			restoreScreen(orchID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: orchID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

			prevSessionID = sessionID
			sessionID = orchID
			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultRenameSession:
			newName := client.RunNameInput("Rename Session")
			if newName != "" {
				if err := renameSession(sessionID, newName); err != nil {
					out.Printf("Rename failed: %s\n", err)
				}
			}

			nc, err := freshClient()
			if err != nil {
				return err
			}

			restoreScreen(sessionID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

			opts.SessionID = sessionID
			opts.Info = &info
			c = nc

			continue

		case client.ResultScrollMode:
			scrollback := client.FetchScrollback(cfg, paths, cfgFile, sessionID, 2000)
			client.RunScrollView("Scrollback — "+info.Name, scrollback)

			nc, err := freshClient()
			if err != nil {
				return err
			}

			restoreScreen(sessionID)
			_ = nc.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
			attachResp, _ := nc.ReadControlResponse()
			_ = protocol.DecodePayload(attachResp, &info)

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

		_ = c.SendControl("list", struct{}{})

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

// deletedRefresher fetches the soft-deleted sessions for the overlay's Deleted
// view (a live-list refresh with ListMsg{Deleted:true}).
func deletedRefresher() func() []protocol.SessionInfo {
	return func() []protocol.SessionInfo {
		c, err := freshClient()
		if err != nil {
			return nil
		}
		defer c.Close()

		_ = c.SendControl("list", protocol.ListMsg{Deleted: true})

		resp, err := c.ReadControlResponse()
		if err != nil || resp.Type == "error" {
			return nil
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return nil
		}

		return list.Sessions
	}
}

// restoreSession un-deletes a soft-deleted session (the overlay Deleted view's
// only action).
func restoreSession(sessionID string) error {
	sc, err := freshClient()
	if err != nil {
		return err
	}
	defer sc.Close()

	_ = sc.SendControl("restore", protocol.RestoreMsg{SessionID: sessionID})

	resp, err := sc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	return nil
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
		_ = sc.SendControl("star", protocol.StarMsg{SessionID: sessionID})
	} else {
		_ = sc.SendControl("unstar", protocol.UnstarMsg{SessionID: sessionID})
	}

	resp, err := sc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

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

	_ = rc.SendControl("restart", protocol.RestartMsg{SessionID: sessionID})

	resp, err := rc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

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

	_ = sc.SendControl("stop", protocol.StopMsg{SessionID: sessionID})

	resp, err := sc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	return nil
}

func renameSession(sessionID, newName string) error {
	if err := daemon.ValidateSessionName(newName); err != nil {
		return err
	}

	rc, err := freshClient()
	if err != nil {
		return err
	}
	defer rc.Close()

	_ = rc.SendControl("rename", protocol.RenameMsg{SessionID: sessionID, NewName: newName})

	resp, err := rc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

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

	_ = dc.SendControl("delete", protocol.DeleteMsg{SessionID: sessionID})

	resp, err := dc.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

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

		_ = c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})

		resp, err := c.ReadControlResponse()
		if err != nil {
			c.Close()
			continue
		}

		if resp.Type == "error" {
			c.Close()

			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return nil, protocol.Envelope{}, fmt.Errorf("session unavailable: %s", e.Message)
		}

		return c, resp, nil
	}

	return nil, protocol.Envelope{}, errors.New("timed out after 10s")
}
