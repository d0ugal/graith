package daemon

import (
	"context"
	"fmt"
	"net"
	"time"
	"unicode/utf8"

	"github.com/d0ugal/graith/internal/approvals"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/headless"
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

	yolo := false
	if sess, ok := sm.state.Sessions[req.SessionID]; ok {
		yolo = sess.Yolo
	}

	sm.mu.RUnlock()

	// A yolo session auto-approves every request via the "auto" backend,
	// overriding whatever global backend is configured. This is the composition
	// point for a future dangerous-command blocklist: the auto backend decides,
	// so a hard block would surface here rather than being silently allowed.
	if yolo {
		if decision, handled := sm.decideWithBackend(ctx, req, approvals.BackendAuto, approvalsCfg); handled {
			// Auto-approve bypasses human review, so leave an audit trail: this
			// is the only record that a yolo session allowed this tool call.
			sm.log.Info("approval auto-decided (yolo)",
				"request_id", req.RequestID, "session", req.SessionID,
				"tool", req.ToolName, "decision", decision.Decision)

			return decision
		}
	}

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

// headlessPermissionFunc returns the OnPermission callback a headless session
// uses to answer inbound can_use_tool control requests. It bridges the headless
// PermissionRequest/Decision types to the daemon's approval decision logic via
// SubmitHeadlessApproval (issue #1136).
func (sm *SessionManager) headlessPermissionFunc(sessionID string) func(headless.PermissionRequest) headless.PermissionDecision {
	return func(req headless.PermissionRequest) headless.PermissionDecision {
		decision := sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
			RequestID: req.RequestID,
			SessionID: sessionID,
			ToolName:  req.ToolName,
			ToolInput: string(req.Input),
		})

		return headless.PermissionDecision{
			Allow:  approvals.Normalise(decision.Decision) == approvals.DecisionAllow,
			Reason: decision.Reason,
		}
	}
}

// SubmitHeadlessApproval resolves a can_use_tool decision for a headless session
// over the control protocol. Unlike SubmitApproval it never queues for a human:
// a headless session is fire-and-forget, so a policy that would defer to a human
// is resolved by *denying* (the non-blocking-backend rule from the design),
// escalating once to the orchestrator inbox so the deny is visible.
//
// It is **fail-closed**: a headless session always has `--permission-prompt-tool
// stdio` installed and is never sandboxed (v1 rejects headless + sandbox), so —
// unlike the interactive path, where a disabled gate means no interception and
// the sandbox is the guardrail — a disabled gate here must NOT blanket-allow.
// Resolution is exactly the design's rule: a yolo session auto-allows via the
// auto backend; a configured non-blocking backend (auto/external/builtin/
// localmost) gives a definitive allow/block; everything else — including the
// default (prompt/human) backend and an unconfigured gate — is denied.
func (sm *SessionManager) SubmitHeadlessApproval(ctx context.Context, req protocol.ApprovalRequestMsg) protocol.ApprovalDecisionMsg {
	sm.mu.RLock()
	approvalsCfg := sm.cfg.Approvals

	yolo := false
	if sess, ok := sm.state.Sessions[req.SessionID]; ok {
		yolo = sess.Yolo
	}

	sm.mu.RUnlock()

	// Bound the backend decision so a hung backend can't stall the agent turn
	// forever. Most backends self-bound (command/localmost at their configured
	// execution timeout, default 5s; auto is instant), but a caller-side deadline
	// is the backstop the interactive path gets from its own timeout/queue.
	tctx, cancel := context.WithTimeout(ctx, approvalsCfg.TimeoutDuration())
	defer cancel()

	if yolo {
		if decision, handled := sm.decideWithBackend(tctx, req, approvals.BackendAuto, approvalsCfg); handled {
			sm.log.Info("headless approval auto-decided (yolo)",
				"request_id", req.RequestID, "session", req.SessionID,
				"tool", req.ToolName, "decision", decision.Decision)

			return decision
		}
	}

	if decision, handled := sm.tryApprovalBackend(tctx, req, approvalsCfg); handled {
		return decision
	}

	// Non-blocking rule: a headless session has no human to answer, so a policy
	// that would queue for one (or no non-blocking backend at all) is denied
	// rather than left to hang or silently allowed. Surface it once to the
	// orchestrator so a silent deny doesn't strand the agent.
	//nolint:contextcheck // the escalation is a durable orchestrator notice whose delivery (and any auto-resume) must outlive this approval request, so it deliberately does not inherit the request ctx.
	sm.escalateHeadlessDenyOnce(req)

	return protocol.ApprovalDecisionMsg{
		Decision: approvals.DecisionBlock,
		Reason:   "headless session: no non-blocking approval backend gave a decision, and a headless session has no human to ask — denied (configure a non-blocking approvals backend, e.g. auto/external, or run the session with --yolo)",
	}
}

// escalateHeadlessDenyOnce posts a one-time notice to the orchestrator inbox the
// first time a headless session's tool approval is denied for want of a
// non-blocking backend. Escalating once per session avoids flooding the inbox
// when an agent retries the same blocked tool.
//
// The "escalated" flag is set only after a *successful* delivery: if there is no
// orchestrator yet, or the message store is unavailable, or the publish fails, a
// later deny retries rather than losing the notice permanently. Sequential
// can_use_tool asks (the CLI blocks on each) make a duplicate-send race
// effectively impossible, so the plain check-then-set is sufficient.
func (sm *SessionManager) escalateHeadlessDenyOnce(req protocol.ApprovalRequestMsg) {
	sm.mu.Lock()

	if sm.headlessEscalated[req.SessionID] {
		sm.mu.Unlock()

		return
	}

	sessName := req.SessionID
	if sess, ok := sm.state.Sessions[req.SessionID]; ok && sess.Name != "" {
		sessName = sess.Name
	}

	orchID := sm.findOrchestratorID()
	sm.mu.Unlock()

	if orchID == "" {
		return // no orchestrator to notify yet — retry on a later deny
	}

	body := fmt.Sprintf(
		"Headless session %q was denied a %q tool call: no non-blocking approval backend gave a decision, and a headless session has no human to ask. Set a non-blocking approvals backend (auto/external) or run it with --yolo.",
		sessName, req.ToolName)
	if err := sm.notifyFromDaemon(orchID, body); err != nil {
		return // delivery failed — leave unmarked so a later deny retries
	}

	sm.mu.Lock()
	sm.headlessEscalated[req.SessionID] = true
	sm.mu.Unlock()
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

// AddApprovalSubscriber registers a connection to receive approval
// notifications without attaching to a session (design §C.6), so a remote app
// browsing the fleet gets approval prompts without kicking a desktop attach.
func (sm *SessionManager) AddApprovalSubscriber(conn net.Conn, sendControl func(string, any)) {
	sm.mu.Lock()
	sm.approvalSubs[conn] = sendControl
	sm.mu.Unlock()
}

// RemoveApprovalSubscriber deregisters an approval subscriber (on disconnect).
// It is a no-op if the connection was not subscribed.
func (sm *SessionManager) RemoveApprovalSubscriber(conn net.Conn) {
	sm.mu.Lock()
	delete(sm.approvalSubs, conn)
	sm.mu.Unlock()
}

func (sm *SessionManager) broadcastApprovalNotification() {
	pending := sm.PendingApprovals()
	msg := protocol.ApprovalNotificationMsg{Pending: pending}

	sm.mu.RLock()

	clients := make([]func(string, any), 0, len(sm.attachedClients)+len(sm.approvalSubs))
	for _, ac := range sm.attachedClients {
		if ac.sendControl != nil {
			clients = append(clients, ac.sendControl)
		}
	}
	// Also fan out to explicit subscribers (connected but not attached).
	for _, send := range sm.approvalSubs {
		if send != nil {
			clients = append(clients, send)
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

	// Resolve the per-backend subprocess execution timeout for the backends that
	// spawn one. config.Approvals.Validate has already checked it against the
	// enclosing approval deadline, so the value here is coherent by construction.
	switch backend {
	case approvals.BackendCommand, approvals.BackendExternal:
		acfg.ExecTimeout = cfg.CommandTimeoutDuration()
	case approvals.BackendLocalmost:
		acfg.ExecTimeout = cfg.LocalmostTimeoutDuration()
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

	return sm.decideWithBackend(ctx, req, backendName, cfg)
}

// decideWithBackend consults a specific, already-resolved approvals backend and
// returns (decision, true) only for a definitive allow/block. A deferred,
// errored, or unloadable backend returns (_, false) so the caller falls through
// to the human queue. It is the shared decision core used both by
// tryApprovalBackend (config-resolved backend) and by the per-session yolo path
// (forced "auto" backend).
func (sm *SessionManager) decideWithBackend(ctx context.Context, req protocol.ApprovalRequestMsg, backendName string, cfg config.Approvals) (protocol.ApprovalDecisionMsg, bool) {
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

	// The auto backend ignores all config, so don't parse the builtin ruleset
	// for it — otherwise an unrelated malformed [approvals.builtin] config would
	// make a yolo session fall through to the human queue.
	var acfg approvals.Config

	if backendName != approvals.BackendAuto {
		var err error

		acfg, err = approvalsBackendConfig(backendName, cfg, sm.approvalsConfigDir())
		if err != nil {
			sm.log.Error("approvals: invalid builtin config, deferring to user", "err", err)
			return protocol.ApprovalDecisionMsg{}, false
		}
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
// shown to and broadcast to attached clients. The cut backs off to a rune
// boundary so a multi-byte character is never split into a mojibake tail.
func truncateForDisplay(s string) string {
	if len(s) <= approvalDisplayLimit {
		return s
	}

	cut := approvalDisplayLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + "..."
}
