package daemon

import (
	"context"

	"github.com/d0ugal/graith/internal/protocol"
)

// handleTriggerList returns all trigger status records. Read-only.
func handleTriggerList(sm *SessionManager, send func(string, any)) {
	send("trigger_list", protocol.TriggerListResponse{Triggers: sm.TriggerList()})
}

// handleTriggerStatus returns the status record for a named trigger.
func handleTriggerStatus(sm *SessionManager, send func(string, any), msg protocol.Envelope) {
	s, ok := decodePayload[protocol.TriggerStatusMsg](msg, send, "invalid trigger_status message")
	if !ok {
		return
	}

	rec, err := sm.TriggerStatus(s.Name)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("trigger_status", protocol.TriggerStatusResponse{Trigger: rec})
	}
}

// handleTriggerRun fires a schedule trigger once now (respecting overlap).
func handleTriggerRun(ctx context.Context, sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	s, ok := decodePayload[protocol.TriggerRunMsg](msg, send, "invalid trigger_run message")
	if !ok {
		return
	}

	if !auth.authorizeTriggerOp(sm, send) {
		return
	}

	if err := sm.TriggerRunNow(ctx, s.Name); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("trigger_run", struct {
			Name string `json:"name"`
		}{s.Name})
	}
}

// handleTriggerPause pauses or resumes a named trigger (persists across restart).
func handleTriggerPause(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	s, ok := decodePayload[protocol.TriggerPauseMsg](msg, send, "invalid trigger_pause message")
	if !ok {
		return
	}

	if !auth.authorizeTriggerOp(sm, send) {
		return
	}

	if err := sm.TriggerPause(s.Name, s.Pause); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("trigger_pause", struct {
			Name  string `json:"name"`
			Pause bool   `json:"pause"`
		}{s.Name, s.Pause})
	}
}

// handleMCPList lists the daemon-managed MCP server processes. Read-only.
func handleMCPList(sm *SessionManager, send func(string, any)) {
	if sm.mcpManager == nil {
		send("mcp_list", protocol.MCPListResponse{})

		return
	}

	send("mcp_list", protocol.MCPListResponse{Servers: sm.mcpManager.List()})
}

// handleMCPRestart stops a server's processes so proxies reconnect with fresh
// ones. Mutating; gated by authorizeTriggerOp.
func handleMCPRestart(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	mr, ok := decodePayload[protocol.MCPRestartMsg](msg, send, "invalid mcp_restart message")
	if !ok {
		return
	}

	if !auth.authorizeTriggerOp(sm, send) {
		return
	}

	if sm.mcpManager == nil {
		send("error", protocol.ErrorMsg{Message: "MCP manager not initialized"})

		return
	}

	stopped, err := sm.mcpManager.Restart(mr.Name)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("mcp_restart", protocol.MCPRestartResponse{Name: mr.Name, Stopped: stopped})
	}
}

// handleMCPLogs returns the tail of an MCP server's log files. Read-only.
func handleMCPLogs(sm *SessionManager, send func(string, any), msg protocol.Envelope) {
	ml, ok := decodePayload[protocol.MCPLogsMsg](msg, send, "invalid mcp_logs message")
	if !ok {
		return
	}

	if sm.mcpManager == nil {
		send("error", protocol.ErrorMsg{Message: "MCP manager not initialized"})

		return
	}

	files, err := sm.mcpManager.LogFiles(ml.Name, ml.Lines)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("mcp_logs", protocol.MCPLogsResponse{Name: ml.Name, Files: files})
	}
}

// handleNotify sends a push notification to the human. Orchestrator/human only
// (re-checked via authorizeNotify).
func handleNotify(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	n, ok := decodePayload[protocol.NotifyMsg](msg, send, "invalid notify message")
	if !ok {
		return
	}

	if !auth.authorizeNotify(sm, send) {
		return
	}

	delivered, reason := sm.SendPushNotification(pushNotification{
		Title:    n.Title,
		Message:  n.Message,
		Priority: n.Priority,
	})
	send("notify_response", protocol.NotifyResponse{Delivered: delivered, Reason: reason})
}
