package daemon

import (
	"errors"
	"log/slog"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// createOptsFromMsg maps a create wire request onto CreateOpts. agentName is the
// already-resolved agent (the caller applies the config default when c.Agent is
// empty), and rows/cols are the client's terminal size. Kept as a pure function
// so the field-for-field mapping — easy to silently drop a field in — is unit
// testable without launching an agent.
func createOptsFromMsg(c protocol.CreateMsg, agentName string, rows, cols uint16) CreateOpts {
	return CreateOpts{
		Name:                c.Name,
		AgentName:           agentName,
		RepoPath:            c.RepoPath,
		BaseBranch:          c.Base,
		Prompt:              c.Prompt,
		Model:               c.Model,
		Codex:               codexOptsFromMsg(c.Codex),
		ParentID:            c.ParentID,
		NoRepo:              c.NoRepo,
		Mirror:              c.Mirror,
		AgentHooks:          c.AgentHooks,
		InPlace:             c.InPlace,
		AllowConcurrent:     c.AllowConcurrent,
		SkipModelValidation: c.SkipModelValidation,
		Yolo:                c.Yolo,
		Headless:            c.Headless,
		NoFetch:             c.NoFetch,
		Rows:                rows,
		Cols:                cols,
	}
}

// handleCreate creates a new session. When the caller is an authenticated
// session, the new session is parented to it.
func handleCreate(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	c, ok := decodePayload[protocol.CreateMsg](msg, send, "invalid create message")
	if !ok {
		return
	}

	if auth.authenticated {
		c.ParentID = auth.sessionID
	}

	agentName := c.Agent
	if agentName == "" {
		agentName = sm.Config().DefaultAgent
	}

	sess, err := sm.Create(createOptsFromMsg(c, agentName, rows, cols))
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("created", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
	}
}

// handleFork forks a session (optionally onto a different agent/model).
func handleFork(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	f, ok := decodePayload[protocol.ForkMsg](msg, send, "invalid fork message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, f.SourceSessionID, authSelfOrDescendant, send) {
		return
	}

	sess, err := sm.ForkWithAgent(f.Name, f.SourceSessionID, f.Agent, f.Model, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("created", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
	}
}

// handleMigrate migrates a session onto a different agent/model.
func handleMigrate(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	m, ok := decodePayload[protocol.MigrateMsg](msg, send, "invalid migrate message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, m.SessionID, authSelfOrDescendant, send) {
		return
	}

	sess, err := sm.Migrate(m.SessionID, m.Agent, m.Model, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("migrated", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
	}
}

// handleAttachConvert converts a headless session to an interactive PTY
// (stop → `claude --resume` in a PTY), preserving the conversation/worktree
// (issue #1137).
//
//nolint:dupl // shares the decode→authorize→lifecycle-call→respond shape with handleResume but targets a distinct message/method; merging would obscure both.
func handleAttachConvert(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	ac, ok := decodePayload[protocol.AttachConvertMsg](msg, send, "invalid attach_convert message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, ac.SessionID, authSelfOrDescendant, send) {
		return
	}

	sess, err := sm.ConvertToInteractive(ac.SessionID, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("converted", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
	}
}

// handleRename renames a session.
func handleRename(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	r, ok := decodePayload[protocol.RenameMsg](msg, send, "invalid rename message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, r.SessionID, authSelfOrDescendant, send) {
		return
	}

	if err := sm.Rename(r.SessionID, r.NewName); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("renamed", struct {
			SessionID string `json:"session_id"`
			NewName   string `json:"new_name"`
		}{r.SessionID, r.NewName})
	}
}

// authorizeUpdate checks that the caller may update the target session — and,
// when adopting a new parent, over that parent too — under sm.mu. Clearing the
// parent ("") is a privileged reparent: only the orchestrator and the human CLI
// may orphan a session, otherwise a child could orphan itself to escape its
// parent's control.
func authorizeUpdate(sm *SessionManager, auth authContext, u protocol.UpdateMsg) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	authErr := auth.checkTarget(sm, u.SessionID, authSelfOrDescendant)
	if authErr == nil && u.ParentID != nil {
		if *u.ParentID == "" {
			if auth.authenticated && !auth.isOrchestrator(sm) {
				authErr = errors.New("not authorized: only the orchestrator may orphan a session")
			}
		} else {
			authErr = auth.checkTarget(sm, *u.ParentID, authSelfOrDescendant)
		}
	}

	return authErr
}

// handleUpdate renames and/or reparents a session, guarding the reparent so a
// session can't manufacture a descendant relationship to bypass the auth model.
func handleUpdate(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	u, ok := decodePayload[protocol.UpdateMsg](msg, send, "invalid update message")
	if !ok {
		return
	}

	if authErr := authorizeUpdate(sm, auth, u); authErr != nil {
		send("error", protocol.ErrorMsg{Message: authErr.Error()})

		return
	}

	if err := sm.Update(u.SessionID, u.Name, u.ParentID); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("updated", struct {
			SessionID string `json:"session_id"`
		}{u.SessionID})
	}
}

// handleStar stars a session (protecting it from a manual gr delete).
func handleStar(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	s, ok := decodePayload[protocol.StarMsg](msg, send, "invalid star message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, s.SessionID, authSelfOrDescendant, send) {
		return
	}

	if err := sm.Star(s.SessionID); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("starred", struct {
			SessionID string `json:"session_id"`
		}{s.SessionID})
	}
}

// handleUnstar unstars a session.
func handleUnstar(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	u, ok := decodePayload[protocol.UnstarMsg](msg, send, "invalid unstar message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, u.SessionID, authSelfOrDescendant, send) {
		return
	}

	if err := sm.Unstar(u.SessionID); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("unstarred", struct {
			SessionID string `json:"session_id"`
		}{u.SessionID})
	}
}

// handleSetStatus sets or clears a session's status summary. An authenticated
// session may only set its own status.
func handleSetStatus(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.SetStatusMsg](msg, send, "invalid set_status message")
	if !ok {
		return
	}

	if auth.authenticated {
		m.SessionID = auth.sessionID
	}

	if !auth.authorizeTarget(sm, m.SessionID, authSelfOnly, send) {
		return
	}

	if m.Clear {
		if err := sm.ClearSummary(m.SessionID); err != nil {
			send("error", protocol.ErrorMsg{Message: err.Error()})
		} else {
			send("status_set", protocol.StatusSetMsg{SessionID: m.SessionID})
		}

		return
	}

	if err := sm.SetSummary(m.SessionID, m.Text, m.TTLSeconds); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("status_set", protocol.StatusSetMsg{SessionID: m.SessionID})
	}
}

// handleResume restarts a stopped session's process in its existing worktree.
//
//nolint:dupl // shares the decode→authorize→lifecycle-call→respond shape with handleAttachConvert but targets a distinct message/method; merging would obscure both.
func handleResume(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	r, ok := decodePayload[protocol.ResumeMsg](msg, send, "invalid resume message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, r.SessionID, authSelfOrDescendant, send) {
		return
	}

	sess, err := sm.Resume(r.SessionID, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("resumed", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
	}
}

// handleRestart restarts a running session (optionally with its descendants).
func handleRestart(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	r, ok := decodePayload[protocol.RestartMsg](msg, send, "invalid restart message")
	if !ok {
		return
	}

	sm.log.Debug("control request",
		"op", "restart", "caller", auth.describe(),
		"target", r.SessionID, "children", r.Children)

	if !auth.authorizeTarget(sm, r.SessionID, authSelfOrDescendant, send) {
		return
	}

	if r.Children {
		restarted, err := sm.RestartWithChildren(r.SessionID, r.ExcludeRoot, rows, cols)
		if err != nil {
			send("error", protocol.ErrorMsg{Message: err.Error()})
		} else {
			send("restarted", struct {
				SessionID string   `json:"session_id"`
				Restarted []string `json:"restarted"`
			}{r.SessionID, restarted})
		}

		return
	}

	sess, err := sm.Restart(r.SessionID, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("restarted", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
	}
}

// handleStatus returns a session's info plus its unread count and a fleet
// summary.
func handleStatus(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	sr, ok := decodePayload[protocol.StatusRequestMsg](msg, send, "invalid status message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, sr.SessionID, authSelfOrDescendant, send) {
		return
	}

	sess, ok := sm.Get(sr.SessionID)
	if !ok {
		send("error", protocol.ErrorMsg{Message: "session not found"})

		return
	}

	unread := 0
	if sm.messages != nil {
		unread = sm.messages.TotalUnread(sr.SessionID)
	}

	send("status_response", protocol.StatusResponseMsg{
		Session:     toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)),
		UnreadCount: unread,
		Fleet:       sm.fleetSummary(),
	})
}

// handleStatusReport records an agent hook status report for a session. An
// authenticated session may only report for itself.
func handleStatusReport(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	sr, ok := decodePayload[protocol.StatusReportMsg](msg, send, "invalid status_report")
	if !ok {
		return
	}

	if auth.authenticated {
		sr.SessionID = auth.sessionID
	}

	if !auth.authorizeTarget(sm, sr.SessionID, authSelfOnly, send) {
		return
	}

	sm.HandleHookReport(sr)
	send("status_reported", struct{}{})
}

// handleInterrupt sends an interrupt to a session's agent.
func handleInterrupt(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	in, ok := decodePayload[protocol.InterruptMsg](msg, send, "invalid interrupt message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, in.SessionID, authSelfOrDescendant, send) {
		return
	}

	if err := sm.InterruptSession(in.SessionID); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("interrupted", struct {
		SessionID string `json:"session_id"`
	}{in.SessionID})
}

// handleType injects input into a session's PTY. When a human is attached it
// waits for the user to go idle first, so injected input doesn't collide with
// active typing.
func handleType(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, log *slog.Logger) {
	t, ok := decodePayload[protocol.TypeMsg](msg, send, "invalid type message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, t.SessionID, authSelfOrDescendant, send) {
		return
	}

	pty, ok := sm.GetPTY(t.SessionID)
	if !ok {
		send("error", protocol.ErrorMsg{Message: "session not found"})

		return
	}

	if sm.HasAttachedClient(t.SessionID) {
		if !pty.WaitForUserIdle(typeIdleTimeout, typeMaxWait) {
			log.Warn("gr type: max wait expired, injecting while user may still be active",
				"session", t.SessionID)
		}
	}

	var writeErr error
	if t.NoNewline {
		writeErr = pty.WriteInput([]byte(t.Input))
	} else {
		writeErr = pty.WriteInputAndSubmit([]byte(t.Input))
	}

	if writeErr != nil {
		send("error", protocol.ErrorMsg{Message: "write failed: " + writeErr.Error()})

		return
	}

	pty.Poke()
	send("typed", struct {
		SessionID string `json:"session_id"`
	}{t.SessionID})
}

// lifecycleRequest holds the fields shared by the stop and delete control
// messages, both of which target a session (optionally with descendants).
type lifecycleRequest struct {
	SessionID   string
	Children    bool
	ExcludeRoot bool
}

// handleDelete authorizes and dispatches a delete request. It chooses soft vs
// hard delete on the daemon side (soft when !Purge and retention > 0) and
// replies with a DeleteResultMsg carrying the soft/hard outcome and, for a soft
// delete, the computed expiry — enough for the CLI to render "Recoverable
// until …" vs "Deleted".
func handleDelete(sm *SessionManager, auth authContext, sendControl func(string, any), d protocol.DeleteMsg) {
	sm.log.Debug("control request",
		"op", "delete", "caller", auth.describe(),
		"target", d.SessionID, "children", d.Children, "purge", d.Purge)

	sm.mu.RLock()
	authErr := auth.checkTarget(sm, d.SessionID, authSelfOrDescendant)
	sm.mu.RUnlock()

	if authErr != nil {
		sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
		return
	}

	// A direct orchestrator delete is a reset of a declarative system session:
	// remove this instance immediately, then let the config reconciler create a
	// fresh one. It intentionally bypasses soft-delete retention because keeping
	// the old state would prevent that replacement. Batch deletion continues to
	// skip system sessions.
	target := sm.sessionSnapshot(d.SessionID)
	orchestratorReset := !d.Children && target.SystemKind == SystemKindOrchestrator

	// Routing is owned by the daemon (the CLI only forwards intent via Purge):
	//   orchestratorReset   -> hard delete (fresh config-managed replacement)
	//   Purge               -> hard delete (gr purge)
	//   !Purge && ret > 0   -> soft delete (gr delete, recoverable)
	//   !Purge && ret == 0  -> reject: ordinary gr delete never destroys, so
	//                          with soft delete disabled there is nothing safe
	//                          to do.
	if !orchestratorReset && !d.Purge && sm.Config().Delete.RetentionDuration() <= 0 {
		sendControl("error", protocol.ErrorMsg{Message: "soft delete is disabled (retention=0); use gr purge"})
		return
	}

	soft := !d.Purge && !orchestratorReset

	if d.Children {
		handleDeleteChildren(sm, sendControl, d, soft)
		return
	}

	if soft {
		snap, err := sm.SoftDelete(d.SessionID)
		if err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
			return
		}

		sendControl("deleted", softDeleteResult(snap))

		return
	}

	// Hard delete (gr purge): capture the name before the session is removed.
	name := sm.sessionName(d.SessionID)

	if err := sm.Delete(d.SessionID); err != nil {
		sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	sendControl("deleted", protocol.DeleteResultMsg{SessionID: d.SessionID, Name: name, Soft: false})
}

// handleDeleteChildren runs the with-children delete (soft or hard) and replies
// with a DeleteResultMsg whose Affected list carries the per-descendant outcome.
func handleDeleteChildren(sm *SessionManager, sendControl func(string, any), d protocol.DeleteMsg, soft bool) {
	var (
		affected []string
		err      error
	)

	if soft {
		affected, err = sm.SoftDeleteWithChildren(d.SessionID, d.ExcludeRoot)
	} else {
		affected, err = sm.DeleteWithChildren(d.SessionID, d.ExcludeRoot)
	}

	if err != nil {
		sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	result := protocol.DeleteResultMsg{SessionID: d.SessionID, Soft: soft}
	for _, id := range affected {
		if soft {
			result.Affected = append(result.Affected, softDeleteResult(sm.sessionSnapshot(id)))
		} else {
			result.Affected = append(result.Affected, protocol.DeleteResultMsg{SessionID: id, Soft: false})
		}
	}

	sendControl("deleted", result)
}

// softDeleteResult builds the delete response for a soft-deleted session,
// reading its frozen DeletedAt/ExpiresAt off the session state.
func softDeleteResult(s SessionState) protocol.DeleteResultMsg {
	r := protocol.DeleteResultMsg{SessionID: s.ID, Name: s.Name, Soft: true}
	if s.DeletedAt != nil {
		r.DeletedAt = s.DeletedAt.Format(time.RFC3339)
	}

	if s.ExpiresAt != nil {
		r.ExpiresAt = s.ExpiresAt.Format(time.RFC3339)
	}

	return r
}

// handleRestore authorizes and dispatches a restore request, restoring either a
// single soft-deleted session or (with Children) the whole soft-deleted subtree.
func handleRestore(sm *SessionManager, auth authContext, sendControl func(string, any), r protocol.RestoreMsg) {
	sm.mu.RLock()
	authErr := auth.checkTarget(sm, r.SessionID, authSelfOrDescendant)
	sm.mu.RUnlock()

	if authErr != nil {
		sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
		return
	}

	cfg := sm.Config()

	if r.Children {
		sessions, err := sm.RestoreWithChildren(r.SessionID)
		if err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
			return
		}

		result := protocol.RestoreResultMsg{}
		for _, s := range sessions {
			result.Sessions = append(result.Sessions, toSessionInfo(s, cfg, sm.getHookReport(s.ID)))
		}

		sendControl("restored", result)

		return
	}

	sess, err := sm.Restore(r.SessionID)
	if err != nil {
		sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	result := protocol.RestoreResultMsg{
		Sessions:           []protocol.SessionInfo{toSessionInfo(sess, cfg, sm.getHookReport(sess.ID))},
		DeletedDescendants: sm.softDeletedDescendantCount(sess.ID),
	}

	sendControl("restored", result)
}

// handleSessionLifecycle implements the shared stop/delete dispatch: authorize
// the target, then run either the with-children batch operation or the single
// operation and report the result. event is the success message type
// ("stopped"/"deleted") and resultKey is the JSON field holding the affected
// session names for the with-children response.
func handleSessionLifecycle(
	sm *SessionManager,
	auth authContext,
	sendControl func(string, any),
	req lifecycleRequest,
	event, resultKey string,
	batchFn func(sessionID string, excludeRoot bool) ([]string, error),
	singleFn func(sessionID string) error,
) {
	sm.mu.RLock()
	authErr := auth.checkTarget(sm, req.SessionID, authSelfOrDescendant)
	sm.mu.RUnlock()

	if authErr != nil {
		sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
		return
	}

	if req.Children {
		affected, err := batchFn(req.SessionID, req.ExcludeRoot)
		if err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		} else {
			sendControl(event, map[string]any{
				"session_id": req.SessionID,
				resultKey:    affected,
			})
		}
	} else {
		if err := singleFn(req.SessionID); err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		} else {
			sendControl(event, map[string]any{
				"session_id": req.SessionID,
			})
		}
	}
}
