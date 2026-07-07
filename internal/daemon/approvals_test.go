package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

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
		wantErr bool
	}{
		{"default prompt ok", config.Approvals{}, false},
		{"command with no command errors", config.Approvals{Backend: "command"}, true},
		{"command with command ok", config.Approvals{Backend: "command", Command: "my-approver"}, false},
		{"builtin invalid config errors", config.Approvals{Backend: "builtin", Builtin: config.ApprovalsBuiltin{Config: badCfg}}, true},
		{"builtin valid config ok", config.Approvals{Backend: "builtin", Builtin: config.ApprovalsBuiltin{Config: goodCfg}}, false},
		{"builtin inline rules ok", config.Approvals{Backend: "builtin", Builtin: config.ApprovalsBuiltin{Allow: []any{"echo @*"}}}, false},
		{"localmost missing binary errors", config.Approvals{Backend: "localmost", Command: "definitely-not-real-xyz"}, true},
		{"conflicting mode+backend errors", config.Approvals{Backend: "builtin", Mode: "localmost"}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sm := newTestSessionManager(t)
			sm.cfg.Approvals = tt.appr

			err := sm.validateApprovalsBackend()
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
