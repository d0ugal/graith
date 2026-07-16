package cli

import (
	"context"
	"errors"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
)

// attachConn is the connection surface the attach loop drives: the control
// round-trips of controlConn plus the interactive passthrough and Close. It is
// an interface (satisfied by *client.Client) so the loop's state-transition
// helpers and the non-UI handlers can be unit-tested against a scripted fake.
type attachConn interface {
	controlConn
	RunPassthrough(ctx context.Context, opts client.PassthroughOpts) client.PassthroughResult
	Close()
}

// attachLoop carries the mutable state of an interactive attach session across
// the passthrough → overlay/shell/switch → reattach cycle. Each RunPassthrough
// returns a result that a handler turns into the next state (a fresh connection
// bound to some session), and the loop passes control back to RunPassthrough.
type attachLoop struct {
	ctx context.Context

	// c is the live connection. RunPassthrough closes it on return, so every
	// handler must replace it with a fresh connection (or end the loop).
	c attachConn

	// sessionID is the session currently attached; prevSessionID backs the
	// "last session" toggle.
	sessionID     string
	prevSessionID string

	// collapsed persists the overlay's collapsed-repo state between openings.
	collapsed map[string]bool

	// opts is handed (by value) to RunPassthrough each iteration; opts.Info
	// always points at info, so decoding into &info updates what RunPassthrough
	// sees on the next pass.
	opts client.PassthroughOpts
	info protocol.SessionInfo
}

func runAttachByID(c attachConn, sessionID string, initialCollapsed map[string]bool) error {
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

	l := &attachLoop{
		ctx:       context.Background(),
		c:         c,
		sessionID: sessionID,
		collapsed: initialCollapsed,
		info:      info,
	}

	l.opts = client.PassthroughOpts{
		Keys:      passthroughKeysFromConfig(),
		SessionID: sessionID,
		Info:      &l.info,
		ReadOnly:  attachReadOnly,
	}
	if cfg.StatusBar.Enabled {
		l.opts.StatusBar = &client.StatusBarCfg{
			Position: cfg.StatusBar.Position,
		}
	}

	l.opts.AutoPopApproval = cfg.Approvals.AutoPop
	l.opts.DragArrowKeys = cfg.Input.DragArrowKeys
	l.opts.DragArrowThreshold = cfg.Input.DragArrowThreshold

	return l.run()
}

func (l *attachLoop) run() error {
	for {
		result := l.c.RunPassthrough(l.ctx, l.opts)
		// RunPassthrough closes the connection — l.c is dead after this point.
		// Every handler must either end the loop or install a fresh client.

		done, err := l.dispatch(result)
		if err != nil {
			return err
		}

		if done {
			return nil
		}
	}
}

// dispatch routes a passthrough result to its handler. A handler returns
// done=true to exit the attach loop (a clean detach/quit or a fatal error via
// the err return), or done=false to loop back into RunPassthrough. An
// unrecognised result simply loops again, matching the original switch's
// fall-through.
func (l *attachLoop) dispatch(result client.PassthroughResult) (bool, error) {
	switch result {
	case client.ResultOverlay:
		return l.onOverlay()
	case client.ResultMessageOverlay:
		return l.onMessageOverlay()
	case client.ResultShell:
		return l.onShell()
	case client.ResultRestart:
		return l.onRestart()
	case client.ResultDisconnected:
		return l.onDisconnected()
	case client.ResultLastSession:
		return l.onLastSession()
	case client.ResultNextSession:
		return l.onCycleSession(true)
	case client.ResultPrevSession:
		return l.onCycleSession(false)
	case client.ResultNewSession:
		return l.onNewSession()
	case client.ResultForkSession:
		return l.onForkSession()
	case client.ResultApprovalOverlay:
		return l.onApprovalOverlay()
	case client.ResultOrchestratorSession:
		return l.onOrchestratorSession()
	case client.ResultRenameSession:
		return l.onRenameSession()
	case client.ResultScrollMode:
		return l.onScrollMode()
	case client.ResultDetached, client.ResultQuit:
		resetTerminal()
		return true, nil
	}

	return false, nil
}

// adoptCurrent reattaches nc to the current session and makes it the live
// connection, without repainting the screen (used where the caller has already
// reset the terminal, e.g. shell/restart/session-cycle).
func (l *attachLoop) adoptCurrent(nc attachConn) {
	attachDecode(nc, l.sessionID, &l.info)
	l.opts.SessionID = l.sessionID
	l.opts.Info = &l.info
	l.c = nc
}

// restoreAndAdopt repaints the current session's screen, then reattaches nc to
// it. Used when returning from an overlay/prompt that drew over the session.
func (l *attachLoop) restoreAndAdopt(nc attachConn) {
	restoreScreen(l.sessionID)
	l.adoptCurrent(nc)
}

// switchTo repaints and attaches nc to newID, adopting it as the current
// session and remembering the previous one for the last-session toggle.
func (l *attachLoop) switchTo(nc attachConn, newID string) {
	restoreScreen(newID)
	attachDecode(nc, newID, &l.info)
	l.prevSessionID = l.sessionID
	l.sessionID = newID
	l.opts.SessionID = newID
	l.opts.Info = &l.info
	l.c = nc
}

func (l *attachLoop) onOverlay() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	list, err := fetchSessionList(nc, struct{}{})
	if err != nil {
		nc.Close()
		return false, err
	}

	repos := client.DiscoverRepos(cfg.AllowedRepoPaths, list.Sessions)
	agents, defaultAgent := agentChoices()

	overlayResult := client.RunOverlay(client.RunOverlayOpts{
		Sessions:         list.Sessions,
		CurrentSessionID: l.sessionID,
		FetchPreview:     previewFetcher(),
		RefreshSessions:  sessionRefresher(),
		RefreshDeleted:   deletedRefresher(),
		DeleteSession:    deleteSession,
		RestartSession:   restartSession,
		StopSession:      stopSession,
		ToggleStar:       toggleStar,
		RestoreSession:   restoreSession,
		Profile:          paths.Profile,
		Collapsed:        l.collapsed,
		RepoSuggestions:  repos,
		ShortcutKeys:     cfg.Overlay.ShortcutKeys,
		Agents:           agents,
		DefaultAgent:     defaultAgent,
		Keys:             overlayKeysFromConfig(),
	})
	if overlayResult != nil {
		l.collapsed = overlayResult.Collapsed
	}

	if overlayResult != nil && overlayResult.Action == "stopped-current" {
		// The user stopped the session they were attached to. Exit instead of
		// reattaching, which would auto-resume it.
		nc.Close()
		resetTerminal()

		return true, nil
	}

	if overlayResult == nil || overlayResult.Action == "" {
		l.restoreAndAdopt(nc)
		return false, nil
	}

	if overlayResult.Action == "create" {
		return l.overlayCreate(nc, overlayResult)
	}

	return l.overlaySwitch(nc, overlayResult.SessionID)
}

// overlayCreate handles the overlay's "create" action: create the session, then
// switch to it (or recover to the current session on failure).
func (l *attachLoop) overlayCreate(nc attachConn, overlayResult *client.OverlayResult) (bool, error) {
	_ = nc.SendControl("create", protocol.CreateMsg{
		Name:     overlayResult.CreateName,
		RepoPath: overlayResult.CreateRepoPath,
		Agent:    overlayResult.CreateAgent,
	})

	createResp, err := nc.ReadControlResponse()
	if err != nil {
		nc.Close()
		return false, err
	}

	if createResp.Type == "error" {
		nc2, err := reattachAfterOverlayFailure(nc, l.sessionID, "Create", createResp, &l.opts, &l.info)
		if err != nil {
			return false, err
		}

		l.c = nc2

		return false, nil
	}

	var newInfo protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &newInfo)
	l.switchTo(nc, newInfo.ID)

	return false, nil
}

// overlaySwitch attaches to a session picked in the overlay. Switching to a
// headless session can't convert inline — conversion needs an interactive
// confirmation we can't safely prompt for mid-loop (the terminal is between
// raw-mode passthroughs) — so it points the user at `gr attach <name>` and
// reattaches to the current session.
func (l *attachLoop) overlaySwitch(nc attachConn, targetID string) (bool, error) {
	restoreScreen(targetID)
	_ = nc.SendControl("attach", attachMsg(targetID))
	attachResp, _ := nc.ReadControlResponse()

	if attachResp.Type == "convert_required" {
		var cr protocol.ConvertRequiredMsg

		_ = protocol.DecodePayload(attachResp, &cr)
		out.Printf("%q is a headless session — run `gr attach %s` to convert it to interactive.\n", cr.Name, cr.Name)

		l.restoreAndAdopt(nc)

		return false, nil
	}

	_ = protocol.DecodePayload(attachResp, &l.info)

	l.prevSessionID = l.sessionID
	l.sessionID = targetID
	l.opts.SessionID = targetID
	l.opts.Info = &l.info
	l.c = nc

	return false, nil
}

func (l *attachLoop) onMessageOverlay() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}
	// Build a peer-id -> name map from the live session list so the rail can
	// label conversations (sent messages carry only a peer id).
	msgList, err := fetchSessionList(nc, struct{}{})
	if err != nil {
		nc.Close()
		return false, err
	}

	names := make(map[string]string, len(msgList.Sessions))
	for _, s := range msgList.Sessions {
		names[s.ID] = s.Name
	}

	client.RunMessageOverlay(l.sessionID, messageKeysFromConfig(), conversationFetcher(l.sessionID), names)

	l.restoreAndAdopt(nc)

	return false, nil
}

func (l *attachLoop) onShell() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	// A read failure here is non-fatal: an empty list falls through to the
	// "session not found" notice, matching the original best-effort behaviour.
	infoList, _ := fetchSessionList(nc, struct{}{})

	var worktreePath string

	for _, s := range infoList.Sessions {
		if s.ID == l.sessionID {
			worktreePath = s.WorktreePath
			break
		}
	}

	if worktreePath == "" {
		out.Printf("Shell failed: session %s not found\n", l.sessionID)
	} else {
		resetTerminal()

		if err := client.RunShellInWorktree(worktreePath); err != nil {
			out.Printf("Shell failed: %s\n", err)
		}
	}

	l.adoptCurrent(nc)

	return false, nil
}

func (l *attachLoop) onRestart() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	_ = nc.SendControl("resume", protocol.ResumeMsg{SessionID: l.sessionID})

	resumeResp, err := nc.ReadControlResponse()
	if err != nil {
		nc.Close()
		return false, err
	}

	if resumeResp.Type == "error" {
		out.Printf("Resume failed: %s\n", errorMessage(resumeResp))
	}

	l.adoptCurrent(nc)

	return false, nil
}

func (l *attachLoop) onDisconnected() (bool, error) {
	out.Printf("Connection lost. Reconnecting...\n")

	nc, attachResp, err := reconnectToSession(l.sessionID)
	if err != nil {
		out.Printf("Could not reconnect: %s\n", err)
		resetTerminal()

		return true, nil
	}

	_ = protocol.DecodePayload(attachResp, &l.info)

	l.opts.SessionID = l.sessionID
	l.opts.Info = &l.info
	l.c = nc

	return false, nil
}

func (l *attachLoop) onLastSession() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	if l.prevSessionID != "" {
		l.sessionID, l.prevSessionID = l.prevSessionID, l.sessionID
	}

	l.adoptCurrent(nc)

	return false, nil
}

func (l *attachLoop) onCycleSession(forward bool) (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	list, err := fetchSessionList(nc, struct{}{})
	if err != nil {
		nc.Close()
		return false, err
	}

	ids := sortedSessionIDs(list.Sessions)
	if next := adjacentSession(ids, l.sessionID, forward); next != "" {
		l.prevSessionID = l.sessionID
		l.sessionID = next
	}

	l.adoptCurrent(nc)

	return false, nil
}

func (l *attachLoop) onNewSession() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	newSessionList, err := fetchSessionList(nc, struct{}{})
	if err != nil {
		nc.Close()
		return false, err
	}

	repos := client.DiscoverRepos(cfg.AllowedRepoPaths, newSessionList.Sessions)
	agents, defaultAgent := agentChoices()

	name, repoPath, agent := client.RunCreateInput(l.info.RepoPath, repos, agents, defaultAgent)
	if name == "" {
		l.restoreAndAdopt(nc)
		return false, nil
	}

	_ = nc.SendControl("create", protocol.CreateMsg{
		Name:     name,
		RepoPath: repoPath,
		Agent:    agent,
	})

	createResp, err := nc.ReadControlResponse()
	if err != nil {
		nc.Close()
		return false, err
	}

	if createResp.Type == "error" {
		nc2, err := reattachAfterOverlayFailure(nc, l.sessionID, "Create", createResp, &l.opts, &l.info)
		if err != nil {
			return false, err
		}

		l.c = nc2

		return false, nil
	}

	var newInfo protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &newInfo)
	l.switchTo(nc, newInfo.ID)

	return false, nil
}

func (l *attachLoop) onForkSession() (bool, error) {
	name := client.RunNameInput("Fork Session")
	if name == "" {
		nc, err := freshClient()
		if err != nil {
			return false, err
		}

		l.restoreAndAdopt(nc)

		return false, nil
	}

	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	_ = nc.SendControl("fork", protocol.ForkMsg{
		Name:            name,
		SourceSessionID: l.sessionID,
	})

	createResp, err := nc.ReadControlResponse()
	if err != nil {
		nc.Close()
		return false, err
	}

	if createResp.Type == "error" {
		nc2, err := reattachAfterOverlayFailure(nc, l.sessionID, "Fork", createResp, &l.opts, &l.info)
		if err != nil {
			return false, err
		}

		l.c = nc2

		return false, nil
	}

	var newInfo protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &newInfo)
	l.switchTo(nc, newInfo.ID)

	return false, nil
}

func (l *attachLoop) onApprovalOverlay() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	_ = nc.SendControl("approval_list", struct{}{})

	listResp, err := nc.ReadControlResponse()
	if err != nil {
		nc.Close()
		return false, err
	}

	var notif protocol.ApprovalNotificationMsg

	_ = protocol.DecodePayload(listResp, &notif)

	if len(notif.Pending) == 0 {
		l.restoreAndAdopt(nc)
		return false, nil
	}

	results := client.RunApprovalOverlay(notif.Pending, approvalKeysFromConfig())
	for _, r := range results {
		_ = nc.SendControl("approval_respond", protocol.ApprovalRespondMsg{
			RequestID: r.RequestID,
			Decision:  r.Decision,
			Reason:    r.Reason,
		})
		_, _ = nc.ReadControlResponse()
	}

	l.restoreAndAdopt(nc)

	return false, nil
}

func (l *attachLoop) onOrchestratorSession() (bool, error) {
	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	list, err := fetchSessionList(nc, struct{}{})
	if err != nil {
		nc.Close()
		return false, err
	}

	var orchID string

	for _, s := range list.Sessions {
		if s.SystemKind == "orchestrator" {
			orchID = s.ID
			break
		}
	}

	if orchID == "" {
		out.Printf("Orchestrator not enabled — set orchestrator.enabled = true in config.toml\n")
		l.restoreAndAdopt(nc)

		return false, nil
	}

	if orchID == l.sessionID {
		if l.prevSessionID != "" {
			l.sessionID, l.prevSessionID = l.prevSessionID, l.sessionID
			l.adoptCurrent(nc)

			return false, nil
		}

		l.restoreAndAdopt(nc)

		return false, nil
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
			return false, err
		}

		if resumeResp.Type == "error" {
			out.Printf("Orchestrator resume failed: %s\n", errorMessage(resumeResp))
			l.restoreAndAdopt(nc)

			return false, nil
		}
	}

	l.switchTo(nc, orchID)

	return false, nil
}

func (l *attachLoop) onRenameSession() (bool, error) {
	newName := client.RunNameInput("Rename Session")
	if newName != "" {
		if err := renameSession(l.sessionID, newName); err != nil {
			out.Printf("Rename failed: %s\n", err)
		}
	}

	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	l.restoreAndAdopt(nc)

	return false, nil
}

func (l *attachLoop) onScrollMode() (bool, error) {
	scrollback := client.FetchScrollback(cfg, paths, cfgFile, l.sessionID, 2000)
	client.RunScrollView("Scrollback — "+l.info.Name, scrollback, scrollKeysFromConfig())

	nc, err := freshClient()
	if err != nil {
		return false, err
	}

	l.restoreAndAdopt(nc)

	return false, nil
}

// reattachAfterOverlayFailure reports a failed create/fork initiated from the
// overlay, then opens a fresh client, reattaches to the original session, and
// updates opts. It returns the reattached client to assign to the loop's live
// connection. verb is "Create" or "Fork". Shared by the overlay create/fork
// error-recovery paths.
func reattachAfterOverlayFailure(nc attachConn, sessionID, verb string, resp protocol.Envelope, opts *client.PassthroughOpts, info *protocol.SessionInfo) (attachConn, error) {
	out.Printf("%s failed: %s\n", verb, errorMessage(resp))

	nc2, err := freshClient()
	if err != nil {
		nc.Close()
		return nil, err
	}

	nc.Close()
	restoreScreen(sessionID)
	attachDecode(nc2, sessionID, info)

	opts.SessionID = sessionID
	opts.Info = info

	return nc2, nil
}
