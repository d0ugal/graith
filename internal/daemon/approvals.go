package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

type pendingApproval struct {
	Info       protocol.ApprovalInfo
	ResponseCh chan protocol.ApprovalDecisionMsg
}

// SubmitApproval registers a pending approval and blocks until a decision is
// made, the context is cancelled (hook disconnect), or the configured timeout
// elapses. If localmost is configured, it is tried first.
func (sm *SessionManager) SubmitApproval(ctx context.Context, req protocol.ApprovalRequestMsg) protocol.ApprovalDecisionMsg {
	if sm.cfg.Approvals.Mode == "localmost" {
		if decision, ok := sm.tryLocalmost(req); ok {
			return decision
		}
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
		ToolInput:   req.ToolInput,
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

	timeout := sm.cfg.Approvals.TimeoutDuration()

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

type localmostRequest struct {
	ToolName    string `json:"tool_name"`
	ToolInput   string `json:"tool_input,omitempty"`
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
}

type localmostResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func (sm *SessionManager) tryLocalmost(req protocol.ApprovalRequestMsg) (protocol.ApprovalDecisionMsg, bool) {
	command := sm.cfg.Approvals.Command
	if command == "" {
		command = "localmost"
	}

	sm.mu.RLock()
	var sessionName string
	if sess, ok := sm.state.Sessions[req.SessionID]; ok {
		sessionName = sess.Name
	}
	sm.mu.RUnlock()

	input := localmostRequest{
		ToolName:    req.ToolName,
		ToolInput:   req.ToolInput,
		SessionID:   req.SessionID,
		SessionName: sessionName,
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		sm.log.Error("localmost: marshal input", "err", err)
		return protocol.ApprovalDecisionMsg{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command)
	cmd.Stdin = strings.NewReader(string(inputJSON))
	out, err := cmd.Output()
	if err != nil {
		sm.log.Warn("localmost: command failed, deferring to user", "err", err)
		return protocol.ApprovalDecisionMsg{}, false
	}

	var resp localmostResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		sm.log.Warn("localmost: invalid JSON response, deferring to user", "err", err)
		return protocol.ApprovalDecisionMsg{}, false
	}

	if resp.Decision == "defer" || resp.Decision == "" {
		return protocol.ApprovalDecisionMsg{}, false
	}

	return protocol.ApprovalDecisionMsg{
		Decision: resp.Decision,
		Reason:   resp.Reason,
	}, true
}
