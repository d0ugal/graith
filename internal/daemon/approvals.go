package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/d0ugal/graith/internal/approvals"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// approvalDisplayLimit caps the tool input shown in the approval overlay and
// broadcast to attached clients. The full input is still evaluated by backends
// (it travels in ApprovalRequestMsg.ToolInput); only the display copy is cut.
const approvalDisplayLimit = 500

type pendingApproval struct {
	Info       protocol.ApprovalInfo
	ResponseCh chan protocol.ApprovalDecisionMsg
}

// SubmitApproval registers a pending approval and blocks until a decision is
// made, the context is cancelled (hook disconnect), or the configured timeout
// elapses. An automated backend (if configured) is consulted first; a backend
// that defers, errors, or is absent falls through to the human-queue path.
func (sm *SessionManager) SubmitApproval(ctx context.Context, req protocol.ApprovalRequestMsg) protocol.ApprovalDecisionMsg {
	sm.mu.RLock()
	approvalsCfg := sm.cfg.Approvals
	sm.mu.RUnlock()

	// When the approval gate is disabled, never queue for a human who may not
	// be watching — allow by default. This is a defensive backstop; the
	// PreToolUse hook is normally omitted entirely when disabled.
	if !approvalsCfg.HookEnabled() {
		return protocol.ApprovalDecisionMsg{Decision: "allow"}
	}

	if decision, handled := sm.tryApprovalBackend(ctx, req, approvalsCfg); handled {
		return decision
	}

	sm.mu.Lock()

	sess, ok := sm.state.Sessions[req.SessionID]
	if !ok {
		sm.mu.Unlock()
		return protocol.ApprovalDecisionMsg{Decision: "allow"}
	}

	info := protocol.ApprovalInfo{
		RequestID:   req.RequestID,
		SessionID:   req.SessionID,
		SessionName: sess.Name,
		ToolName:    req.ToolName,
		ToolInput:   truncateForDisplay(req.ToolInput),
		Agent:       sess.Agent,
		RepoName:    sess.RepoName,
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}

	pa := &pendingApproval{
		Info:       info,
		ResponseCh: make(chan protocol.ApprovalDecisionMsg, 1),
	}
	sm.pendingApprovals[req.RequestID] = pa
	sess.AgentStatus = "approval"
	sess.HookToolName = req.ToolName
	sm.mu.Unlock()

	sm.log.Info("approval queued for user", "request_id", req.RequestID, "tool", req.ToolName, "session", info.SessionName)
	sm.broadcastApprovalNotification()

	timeout := approvalsCfg.TimeoutDuration()

	select {
	case decision := <-pa.ResponseCh:
		return decision
	case <-time.After(timeout):
		sm.cancelApproval(req.RequestID)

		return protocol.ApprovalDecisionMsg{
			Decision: "block",
			Reason:   "approval request timed out",
		}
	case <-ctx.Done():
		sm.cancelApproval(req.RequestID)
		return protocol.ApprovalDecisionMsg{Decision: "allow"}
	}
}

// RespondToApproval delivers a user decision to a waiting approval request.
func (sm *SessionManager) RespondToApproval(requestID, decision, reason string) error {
	sm.mu.Lock()

	pa, ok := sm.pendingApprovals[requestID]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("approval request %q not found", requestID)
	}

	delete(sm.pendingApprovals, requestID)

	if sess, ok := sm.state.Sessions[pa.Info.SessionID]; ok {
		sess.AgentStatus = "active"
	}
	sm.mu.Unlock()

	pa.ResponseCh <- protocol.ApprovalDecisionMsg{
		Decision: decision,
		Reason:   reason,
	}

	sm.broadcastApprovalNotification()

	return nil
}

// cancelApproval removes a pending approval without delivering a decision.
func (sm *SessionManager) cancelApproval(requestID string) {
	sm.mu.Lock()
	if pa, ok := sm.pendingApprovals[requestID]; ok {
		if sess, ok := sm.state.Sessions[pa.Info.SessionID]; ok {
			sess.AgentStatus = "active"
		}

		delete(sm.pendingApprovals, requestID)
	}
	sm.mu.Unlock()
	sm.broadcastApprovalNotification()
}

// PendingApprovals returns a snapshot of all current pending approvals.
func (sm *SessionManager) PendingApprovals() []protocol.ApprovalInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	infos := make([]protocol.ApprovalInfo, 0, len(sm.pendingApprovals))
	for _, pa := range sm.pendingApprovals {
		infos = append(infos, pa.Info)
	}

	return infos
}

// broadcastApprovalNotification pushes the current pending approvals list
// to all attached clients.
func (sm *SessionManager) broadcastApprovalNotification() {
	pending := sm.PendingApprovals()
	msg := protocol.ApprovalNotificationMsg{Pending: pending}

	sm.mu.RLock()

	clients := make([]func(string, any), 0, len(sm.attachedClients))
	for _, ac := range sm.attachedClients {
		if ac.sendControl != nil {
			clients = append(clients, ac.sendControl)
		}
	}

	sm.mu.RUnlock()

	sm.log.Info("broadcasting approval notification", "pending", len(pending), "clients", len(clients))

	for _, send := range clients {
		send("approval_notification", msg)
	}
}

// approvalsBackendConfig builds the resolved approvals.Config for a backend,
// rendering the builtin engine's inline TOML rules to localmost-format JSON when
// they are present (config.Approvals.Validate guarantees inline and the external
// config path are not both set). The external [approvals.builtin] config path is
// expanded via config.ExpandPathRelative so a leading ~/ resolves and a relative
// path resolves against configDir (the directory holding config.toml) rather
// than the daemon's working directory — matching the CLI's approvals validate so
// a config that validates green also enforces at session-create.
func approvalsBackendConfig(backend string, cfg config.Approvals, configDir string) (approvals.Config, error) {
	acfg := approvals.Config{
		Backend:       backend,
		Command:       cfg.Command,
		BuiltinConfig: config.ExpandPathRelative(cfg.Builtin.Config, configDir),
	}

	if cfg.Builtin.HasInline() {
		inline, err := cfg.Builtin.InlineJSON()
		if err != nil {
			return approvals.Config{}, fmt.Errorf("encode inline builtin approvals rules: %w", err)
		}

		acfg.BuiltinInline = inline
	}

	return acfg, nil
}

// tryApprovalBackend consults the configured approvals backend. It returns
// (decision, true) only for a definitive allow/block; a deferred/errored/absent
// backend returns (_, false) so the caller falls through to the human queue.
func (sm *SessionManager) tryApprovalBackend(ctx context.Context, req protocol.ApprovalRequestMsg, cfg config.Approvals) (protocol.ApprovalDecisionMsg, bool) {
	backendName, deprecation, err := cfg.ResolveBackend()
	if err != nil {
		// Validation should have caught this at config load; fail safe to human.
		sm.log.Error("approvals: invalid backend config, deferring to user", "err", err)
		return protocol.ApprovalDecisionMsg{}, false
	}

	if deprecation != "" {
		sm.approvalsWarnOnce.Do(func() { sm.log.Warn(deprecation) })
	}

	if backendName == "" || backendName == approvals.BackendPrompt {
		return protocol.ApprovalDecisionMsg{}, false
	}

	be, err := approvals.BackendByName(backendName)
	if err != nil {
		sm.log.Error("approvals: unknown backend, deferring to user", "backend", backendName, "err", err)
		return protocol.ApprovalDecisionMsg{}, false
	}

	sm.mu.RLock()

	var sessionName, agent string
	if sess, ok := sm.state.Sessions[req.SessionID]; ok {
		sessionName = sess.Name
		agent = sess.Agent
	}

	sm.mu.RUnlock()

	acfg, err := approvalsBackendConfig(backendName, cfg, sm.approvalsConfigDir())
	if err != nil {
		sm.log.Error("approvals: invalid builtin config, deferring to user", "err", err)
		return protocol.ApprovalDecisionMsg{}, false
	}

	decision, derr := be.Decide(ctx, approvals.Request{
		RequestID:   req.RequestID,
		SessionID:   req.SessionID,
		SessionName: sessionName,
		Agent:       agent,
		ToolName:    req.ToolName,
		ToolInput:   req.ToolInput,
		HookPayload: req.HookPayload,
	}, acfg)
	if derr != nil {
		sm.log.Warn("approvals: backend deferring to user", "backend", backendName, "err", derr)
		return protocol.ApprovalDecisionMsg{}, false
	}

	// Belt-and-suspenders: each backend already normalises deny->block, but
	// repeat it here so a future backend author can't reintroduce a silent
	// defer by returning "deny".
	switch approvals.Normalise(decision.Decision) {
	case approvals.DecisionAllow:
		return protocol.ApprovalDecisionMsg{Decision: approvals.DecisionAllow, Reason: decision.Reason}, true
	case approvals.DecisionBlock:
		return protocol.ApprovalDecisionMsg{Decision: approvals.DecisionBlock, Reason: decision.Reason}, true
	default:
		return protocol.ApprovalDecisionMsg{}, false
	}
}

// truncateForDisplay caps a tool-input string for the approval overlay. The
// untruncated value is still what backends evaluate; this only affects what is
// shown to and broadcast to attached clients.
func truncateForDisplay(s string) string {
	if len(s) > approvalDisplayLimit {
		return s[:approvalDisplayLimit] + "..."
	}

	return s
}
