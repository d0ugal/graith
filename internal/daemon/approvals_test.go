package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// TestTruncateForDisplayRuneBoundary guards issue #798: the display cut must not
// split a multi-byte UTF-8 rune and leave a mojibake tail in the approval
// overlay. A multi-byte rune straddling the byte limit should be dropped whole,
// keeping the result valid UTF-8.
func TestTruncateForDisplayRuneBoundary(t *testing.T) {
	// "ü" is two bytes (0xC3 0xBC). Pad with ASCII so a "ü" straddles the
	// approvalDisplayLimit byte offset, forcing a mid-rune cut.
	s := strings.Repeat("a", approvalDisplayLimit-1) + "üüüü"

	got := truncateForDisplay(s)

	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncation ellipsis, got %q", got)
	}

	body := strings.TrimSuffix(got, "...")
	if !utf8.ValidString(body) {
		t.Fatalf("truncated display split a rune: %q is not valid UTF-8", body)
	}

	// A short string is returned unchanged.
	if short := "echo ünïcödé"; truncateForDisplay(short) != short {
		t.Fatalf("short string altered: got %q, want %q", truncateForDisplay(short), short)
	}
}

func TestSubmitApprovalTimeoutBlocksBeforeContextCancel(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Timeout = "100ms"

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep1",
		SessionID: "braw1",
		ToolName:  "Bash",
	}

	decision := sm.SubmitApproval(context.Background(), req)

	if decision.Decision != "block" {
		t.Errorf("expected block on timeout, got %q", decision.Decision)
	}

	if decision.Reason != "approval request timed out" {
		t.Errorf("unexpected reason: %q", decision.Reason)
	}

	sm.mu.RLock()

	if _, exists := sm.pendingApprovals["neep1"]; exists {
		t.Error("pending approval not cleaned up after timeout")
	}

	if sm.state.Sessions["braw1"].AgentStatus == "approval" {
		t.Error("session status not restored after timeout")
	}

	sm.mu.RUnlock()
}

func TestSubmitApprovalContextCancelAllows(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Timeout = "10s"

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep2",
		SessionID: "braw1",
		ToolName:  "Bash",
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	decision := sm.SubmitApproval(ctx, req)

	if decision.Decision != "allow" {
		t.Errorf("expected allow on context cancel, got %q", decision.Decision)
	}

	sm.mu.RLock()

	if _, exists := sm.pendingApprovals["neep2"]; exists {
		t.Error("pending approval not cleaned up after context cancel")
	}

	if sm.state.Sessions["braw1"].AgentStatus == "approval" {
		t.Error("session status not restored after context cancel")
	}

	sm.mu.RUnlock()
}

func TestSubmitApprovalUserDecision(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Timeout = "10s"

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep3",
		SessionID: "braw1",
		ToolName:  "Bash",
	}

	go func() {
		time.Sleep(50 * time.Millisecond)

		_ = sm.RespondToApproval("neep3", "allow", "user approved")
	}()

	decision := sm.SubmitApproval(context.Background(), req)

	if decision.Decision != "allow" {
		t.Errorf("expected allow, got %q", decision.Decision)
	}

	if decision.Reason != "user approved" {
		t.Errorf("unexpected reason: %q", decision.Reason)
	}

	sm.mu.RLock()

	if _, exists := sm.pendingApprovals["neep3"]; exists {
		t.Error("pending approval not cleaned up after user decision")
	}

	if sm.state.Sessions["braw1"].AgentStatus == "approval" {
		t.Error("session status not restored after user decision")
	}

	sm.mu.RUnlock()
}

// TestSubmitApprovalLocalmostCancelledByContext verifies the localmost
// pre-check subprocess inherits the caller's context: cancelling the parent
// ctx aborts the localmost command promptly rather than waiting out its own
// 5s timeout, so SubmitApproval falls through and resolves on ctx cancel.
func TestSubmitApprovalLocalmostCancelledByContext(t *testing.T) {
	// A localmost command that blocks far longer than the test would tolerate
	// if the context were not threaded into the subprocess.
	script := filepath.Join(t.TempDir(), "localmost.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
		t.Fatalf("write localmost script: %v", err)
	}

	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Timeout = "10s"
	sm.cfg.Approvals.Mode = "localmost"
	sm.cfg.Approvals.Command = script

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep5",
		SessionID: "braw1",
		ToolName:  "Bash",
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan protocol.ApprovalDecisionMsg, 1)

	go func() {
		done <- sm.SubmitApproval(ctx, req)
	}()

	select {
	case decision := <-done:
		if decision.Decision != "allow" {
			t.Errorf("expected allow after context cancel, got %q", decision.Decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SubmitApproval did not return promptly; localmost subprocess likely ignored the cancelled context")
	}
}

// TestSubmitApprovalCommandBackendDecision drives the "command" backend
// through SubmitApproval and confirms a definitive decision is returned to the
// agent (not queued for a human).
func TestSubmitApprovalCommandBackendDecision(t *testing.T) {
	script := filepath.Join(t.TempDir(), "approve.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"decision\":\"block\",\"reason\":\"nae the day\"}'\n"), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatalf("write script: %v", err)
	}

	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Timeout = "10s"
	sm.cfg.Approvals.Backend = "command"
	sm.cfg.Approvals.Command = script

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	decision := sm.SubmitApproval(context.Background(), protocol.ApprovalRequestMsg{
		RequestID: "neep-cmd",
		SessionID: "braw1",
		ToolName:  "Bash",
		ToolInput: `{"command":"rm -rf /"}`,
	})

	if decision.Decision != "block" || decision.Reason != "nae the day" {
		t.Errorf("got %q / %q, want block / nae the day", decision.Decision, decision.Reason)
	}
}

// TestSubmitApprovalCommandBackendDenyBecomesBlock guards the load-bearing
// deny->block normalisation: a backend returning "deny" must block, not
// silently defer to the human.
func TestSubmitApprovalCommandBackendDenyBecomesBlock(t *testing.T) {
	script := filepath.Join(t.TempDir(), "approve.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"decision\":\"deny\"}'\n"), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatalf("write script: %v", err)
	}

	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Timeout = "10s"
	sm.cfg.Approvals.Backend = "command"
	sm.cfg.Approvals.Command = script

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	decision := sm.SubmitApproval(context.Background(), protocol.ApprovalRequestMsg{
		RequestID: "neep-deny",
		SessionID: "braw1",
		ToolName:  "Bash",
	})

	if decision.Decision != "block" {
		t.Errorf("deny should normalise to block, got %q", decision.Decision)
	}
}

// TestValidateApprovalsBackend covers the fail-closed check invoked at
// session-create: an approvals backend that can't enforce is a hard error.
func TestValidateApprovalsBackend(t *testing.T) {
	badCfg := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badCfg, []byte(`{"allow":[{"rule":"foo @("}]}`), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}

	goodCfg := filepath.Join(t.TempDir(), "good.json")
	if err := os.WriteFile(goodCfg, []byte(`{"allow":[{"rule":"echo @*"}]}`), 0o600); err != nil {
		t.Fatalf("write good config: %v", err)
	}

	cases := []struct {
		name    string
		appr    config.Approvals
		yolo    bool
		wantErr bool
	}{
		{"default prompt ok", config.Approvals{}, false, false},
		{"command with no command errors", config.Approvals{Backend: "command"}, false, true},
		{"command with command ok", config.Approvals{Backend: "command", Command: "my-approver"}, false, false},
		{"builtin invalid config errors", config.Approvals{Backend: "builtin", Builtin: config.ApprovalsBuiltin{Config: badCfg}}, false, true},
		{"builtin valid config ok", config.Approvals{Backend: "builtin", Builtin: config.ApprovalsBuiltin{Config: goodCfg}}, false, false},
		{"builtin inline rules ok", config.Approvals{Backend: "builtin", Builtin: config.ApprovalsBuiltin{Allow: []any{"echo @*"}}}, false, false},
		{"localmost missing binary errors", config.Approvals{Backend: "localmost", Command: "definitely-not-real-xyz"}, false, true},
		{"conflicting mode+backend errors", config.Approvals{Backend: "builtin", Mode: "localmost"}, false, true},
		// A yolo session uses the auto backend, so an otherwise-unenforceable
		// global backend must not block it.
		{"yolo skips unavailable global backend", config.Approvals{Backend: "command"}, true, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sm := newTestSessionManager(t)
			sm.cfg.Approvals = tt.appr

			err := sm.validateApprovalsBackend(tt.yolo)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateApprovalsBackend() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestResumeValidatesApprovalsBackend verifies the fail-closed check also fires
// on the Resume path (parity with Create/Fork): resuming a session with an
// unenforceable approvals backend errors before restarting the process.
func TestResumeValidatesApprovalsBackend(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals = config.Approvals{Backend: "command"} // no command -> unenforceable

	sm.mu.Lock()
	sm.state.Sessions["bide1"] = &SessionState{
		Name:   "bide-session",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.mu.Unlock()

	_, err := sm.Resume("bide1", 24, 80)
	if err == nil || !strings.Contains(err.Error(), "approvals") {
		t.Fatalf("expected approvals availability error on resume, got %v", err)
	}
}

func TestSubmitApprovalMissingSessionAllows(t *testing.T) {
	sm := newTestSessionManager(t)

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep4",
		SessionID: "haar-mist",
		ToolName:  "Bash",
	}

	decision := sm.SubmitApproval(context.Background(), req)

	if decision.Decision != "allow" {
		t.Errorf("expected allow for missing session, got %q", decision.Decision)
	}
}

func TestSubmitApprovalDisabledAllows(t *testing.T) {
	sm := newTestSessionManager(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled
	// A short timeout ensures the test would fail (block) rather than hang if
	// the disabled path did not short-circuit to allow.
	sm.cfg.Approvals.Timeout = "100ms"

	sm.mu.Lock()
	sm.state.Sessions["braw1"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep6",
		SessionID: "braw1",
		ToolName:  "Bash",
	}

	decision := sm.SubmitApproval(context.Background(), req)

	if decision.Decision != "allow" {
		t.Errorf("expected allow when approvals disabled, got %q", decision.Decision)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if _, exists := sm.pendingApprovals["neep6"]; exists {
		t.Error("approval should not be queued when approvals disabled")
	}
}

// TestSubmitApprovalYoloAllowsWhenGatingEnabled verifies a yolo session
// auto-approves via the "auto" backend even when global approval gating is on
// and the configured backend is the human-prompt default (which would otherwise
// queue for a human). A short timeout ensures the test fails rather than hangs
// if the yolo override did not short-circuit.
func TestSubmitApprovalYoloAllowsWhenGatingEnabled(t *testing.T) {
	sm := newTestSessionManager(t) // Enabled=true, backend defaults to prompt
	sm.cfg.Approvals.Timeout = "100ms"

	sm.mu.Lock()
	sm.state.Sessions["braw-yolo"] = &SessionState{Name: "bonnie-session", Yolo: true}
	sm.mu.Unlock()

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep-yolo",
		SessionID: "braw-yolo",
		ToolName:  "Bash",
		ToolInput: `{"command":"rm -rf /"}`,
	}

	decision := sm.SubmitApproval(context.Background(), req)

	if decision.Decision != "allow" {
		t.Errorf("expected allow for yolo session, got %q", decision.Decision)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if _, exists := sm.pendingApprovals["neep-yolo"]; exists {
		t.Error("yolo approval should not be queued for a human")
	}
}

// TestSubmitApprovalYoloAllowsWhenGatingDisabled verifies a yolo session still
// resolves through the auto backend (allow) even when global gating is off — the
// PreToolUse hook is installed per-session, so SubmitApproval is reached.
func TestSubmitApprovalYoloAllowsWhenGatingDisabled(t *testing.T) {
	sm := newTestSessionManager(t)
	disabled := false
	sm.cfg.Approvals.Enabled = &disabled

	sm.mu.Lock()
	sm.state.Sessions["braw-yolo"] = &SessionState{Name: "bonnie-session", Yolo: true}
	sm.mu.Unlock()

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep-yolo2",
		SessionID: "braw-yolo",
		ToolName:  "Bash",
	}

	decision := sm.SubmitApproval(context.Background(), req)

	if decision.Decision != "allow" {
		t.Errorf("expected allow for yolo session, got %q", decision.Decision)
	}

	if decision.Reason == "" {
		t.Error("expected an auto-approve reason on the yolo decision")
	}
}

// TestSubmitApprovalNonYoloStillQueues guards against the yolo override leaking
// to other sessions: a non-yolo session under the same enabled config must still
// queue for a human (and time out here rather than auto-allow).
func TestSubmitApprovalNonYoloStillQueues(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Timeout = "100ms"

	sm.mu.Lock()
	sm.state.Sessions["canny-plain"] = &SessionState{Name: "canny-session", Yolo: false}
	sm.mu.Unlock()

	req := protocol.ApprovalRequestMsg{
		RequestID: "neep-plain",
		SessionID: "canny-plain",
		ToolName:  "Bash",
	}

	decision := sm.SubmitApproval(context.Background(), req)

	if decision.Decision != "block" {
		t.Errorf("expected block (timeout) for non-yolo session, got %q", decision.Decision)
	}
}

// TestApprovalsBackendConfigExpandsTilde verifies the resolved backend config
// carries an expanded external path, not a literal ~/.
func TestApprovalsBackendConfigExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	acfg, err := approvalsBackendConfig("builtin", config.Approvals{
		Builtin: config.ApprovalsBuiltin{Config: "~/.config/graith/approvals.json"},
	}, "/etc/graith")
	if err != nil {
		t.Fatalf("approvalsBackendConfig: %v", err)
	}

	want := filepath.Join(home, ".config/graith/approvals.json")
	if acfg.BuiltinConfig != want {
		t.Errorf("BuiltinConfig = %q, want %q", acfg.BuiltinConfig, want)
	}

	if strings.HasPrefix(acfg.BuiltinConfig, "~") {
		t.Error("BuiltinConfig should not retain a literal ~")
	}
}

// TestApprovalsBackendConfigRelativeToConfigDir verifies a relative
// [approvals.builtin] config path resolves against the config directory (issue
// #790) rather than being passed verbatim to be opened against the daemon's
// working directory.
func TestApprovalsBackendConfigRelativeToConfigDir(t *testing.T) {
	acfg, err := approvalsBackendConfig("builtin", config.Approvals{
		Builtin: config.ApprovalsBuiltin{Config: "approvals.json"},
	}, "/etc/graith")
	if err != nil {
		t.Fatalf("approvalsBackendConfig: %v", err)
	}

	if want := filepath.Join("/etc/graith", "approvals.json"); acfg.BuiltinConfig != want {
		t.Errorf("BuiltinConfig = %q, want %q", acfg.BuiltinConfig, want)
	}
}

// TestApprovalsConfigDir verifies the config directory is derived from the
// resolved config file path, and is empty when that path is unknown.
func TestApprovalsConfigDir(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.paths.ConfigFile = "/home/canny/.config/graith/config.toml"
	if got, want := sm.approvalsConfigDir(), "/home/canny/.config/graith"; got != want {
		t.Errorf("approvalsConfigDir() = %q, want %q", got, want)
	}

	sm.paths.ConfigFile = ""
	if got := sm.approvalsConfigDir(); got != "" {
		t.Errorf("approvalsConfigDir() with no config file = %q, want empty", got)
	}
}

// TestApprovalsConfigDirPrefersConfigOverride verifies the daemon resolves a
// relative [approvals.builtin] config path against the directory of the config
// file it was actually started with (--config), not the default location — so a
// relative path enforces against the same file `gr --config X approvals
// validate` resolves (issue #790).
func TestApprovalsConfigDirPrefersConfigOverride(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.paths.ConfigFile = "/home/canny/.config/graith/config.toml"
	sm.configFile = "/tmp/clachan/config.toml"

	if got, want := sm.approvalsConfigDir(), "/tmp/clachan"; got != want {
		t.Errorf("approvalsConfigDir() with --config override = %q, want %q", got, want)
	}

	sm.configFile = "   "
	if got, want := sm.approvalsConfigDir(), "/home/canny/.config/graith"; got != want {
		t.Errorf("approvalsConfigDir() with blank override = %q, want %q (should fall back)", got, want)
	}
}

// --- headless (non-blocking) approvals (issue #1136) ------------------------

// TestSubmitHeadlessApprovalYoloAllows: a yolo headless session auto-allows
// every can_use_tool via the auto backend, never queuing.
func TestSubmitHeadlessApprovalYoloAllows(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["braw"] = &SessionState{Name: "bonnie-session", Yolo: true}
	sm.mu.Unlock()

	decision := sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
		RequestID: "neep", SessionID: "braw", ToolName: "Bash",
	})

	if decision.Decision != "allow" {
		t.Fatalf("yolo headless session should allow, got %q (%s)", decision.Decision, decision.Reason)
	}
}

// TestSubmitHeadlessApprovalGateDisabledDenies: a headless session is fail-closed
// — it always installs stdio permission routing and is never sandboxed, so a
// disabled/unset approval gate must NOT blanket-allow (unlike the interactive
// path). With no yolo and no non-blocking backend, it denies.
func TestSubmitHeadlessApprovalGateDisabledDenies(t *testing.T) {
	sm := newTestSessionManager(t)

	disabled := false
	sm.cfg.Approvals.Enabled = &disabled

	sm.mu.Lock()
	sm.state.Sessions["braw"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	decision := sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
		RequestID: "neep", SessionID: "braw", ToolName: "Bash",
	})

	if decision.Decision != "block" {
		t.Fatalf("disabled gate should fail closed (deny) for headless, got %q", decision.Decision)
	}
}

// TestSubmitHeadlessApprovalDefaultGateDenies: the daemon default (Enabled unset,
// backend prompt) must also deny, not allow — this is the fail-open case Codex
// flagged. A headless session with no explicit approval config gets no free
// pass.
func TestSubmitHeadlessApprovalDefaultGateDenies(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Enabled = nil // daemon default: gate unset

	sm.mu.Lock()
	sm.state.Sessions["braw"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	decision := sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
		RequestID: "neep", SessionID: "braw", ToolName: "Write",
	})

	if decision.Decision != "block" {
		t.Fatalf("unset gate should fail closed for headless, got %q", decision.Decision)
	}
}

// TestSubmitHeadlessApprovalBackendDecidesEvenWhenGateUnset: a configured
// non-blocking backend still decides even with the gate unset — the way to let a
// headless session use tools without yolo.
func TestSubmitHeadlessApprovalBackendDecidesEvenWhenGateUnset(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Enabled = nil
	sm.cfg.Approvals.Backend = "auto"

	sm.mu.Lock()
	sm.state.Sessions["braw"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	decision := sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
		RequestID: "neep", SessionID: "braw", ToolName: "Bash",
	})

	if decision.Decision != "allow" {
		t.Fatalf("a configured auto backend should allow even with gate unset, got %q", decision.Decision)
	}
}

// TestSubmitHeadlessApprovalAutoBackendAllows: an explicit non-blocking auto
// backend decides without a human.
func TestSubmitHeadlessApprovalAutoBackendAllows(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Approvals.Backend = "auto"

	sm.mu.Lock()
	sm.state.Sessions["braw"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	decision := sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
		RequestID: "neep", SessionID: "braw", ToolName: "Bash",
	})

	if decision.Decision != "allow" {
		t.Fatalf("auto backend should allow, got %q", decision.Decision)
	}
}

// TestSubmitHeadlessApprovalNonBlockingDeny: the default (prompt) backend would
// queue for a human. A headless session has none, so it must DENY promptly
// rather than block — the non-blocking-backend rule. This test would hang under
// the interactive SubmitApproval; it must return immediately here.
func TestSubmitHeadlessApprovalNonBlockingDeny(t *testing.T) {
	sm := newTestSessionManager(t)
	// Default backend resolves to prompt (human queue).

	sm.mu.Lock()
	sm.state.Sessions["braw"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	done := make(chan protocol.ApprovalDecisionMsg, 1)
	go func() {
		done <- sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
			RequestID: "neep", SessionID: "braw", ToolName: "Bash",
		})
	}()

	select {
	case decision := <-done:
		if decision.Decision != "block" {
			t.Fatalf("headless human-queue resolution should deny, got %q", decision.Decision)
		}

		if !strings.Contains(decision.Reason, "human") {
			t.Fatalf("deny reason should explain the human-backend rule, got %q", decision.Reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SubmitHeadlessApproval blocked — it must never queue for a human")
	}
}

// TestSubmitHeadlessApprovalEscalatesOnce: a non-blocking deny posts exactly one
// notice to the orchestrator inbox per session, even across repeated denies.
func TestSubmitHeadlessApprovalEscalatesOnce(t *testing.T) {
	sm := newTestSessionManager(t)

	// Use a per-test DB so the assertion isn't polluted by messages left by
	// earlier runs (sm.paths.MessagesDB is not run-isolated).
	store, err := NewMsgStore(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}

	t.Cleanup(func() { _ = store.Close() })

	sm.messages = store

	sm.mu.Lock()
	sm.state.Sessions["ben"] = &SessionState{Name: "ben-orch", SystemKind: SystemKindOrchestrator}
	sm.state.Sessions["braw"] = &SessionState{Name: "bonnie-session"}
	sm.mu.Unlock()

	for range 3 {
		sm.SubmitHeadlessApproval(context.Background(), protocol.ApprovalRequestMsg{
			RequestID: "neep", SessionID: "braw", ToolName: "Write",
		})
	}

	msgs, err := store.Read("inbox:ben", "ben", false, "")
	if err != nil {
		t.Fatalf("Read orchestrator inbox: %v", err)
	}

	// Count only the escalation notices (the inbox may also carry resume-path
	// noise for the driver-less test orchestrator). Exactly one proves the
	// once-per-session guard across the three denies.
	var escalations int

	for _, m := range msgs {
		if strings.Contains(m.Body, "was denied") && strings.Contains(m.Body, "bonnie-session") {
			escalations++

			if !strings.Contains(m.Body, "Write") {
				t.Fatalf("escalation body missing tool name: %q", m.Body)
			}
		}
	}

	if escalations != 1 {
		t.Fatalf("expected exactly one escalation across repeated denies, got %d", escalations)
	}
}
