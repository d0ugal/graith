package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
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
