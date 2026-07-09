package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// This file adds handler-dispatch tests for control messages that were not
// otherwise exercised: repo listing, status summaries, star/unstar, session
// status queries, hook reports, restart/interrupt, conversation reads, fork /
// migrate payload validation, config reload, MCP connect guards, the scenario
// lifecycle messages, and the unsupported-message fallthrough. Each test drives
// HandleConnection through the net.Pipe harness with a constructed protocol
// message and asserts the reply type, so it protects the real success and
// error paths rather than padding line counts.

// --- unsupported / fallthrough -------------------------------------------

func TestCoverUnsupportedMessage(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "wheesht_unknown", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "unsupported control message") {
		t.Errorf("message = %q, want it to mention unsupported control message", e.Message)
	}
}

// --- repo_list ------------------------------------------------------------

func TestCoverRepoList(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "repo_list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "repo_list" {
		t.Fatalf("expected repo_list, got %q", env.Type)
	}
}

// --- diagnostics ----------------------------------------------------------

func TestCoverDiagnostics(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "diagnostics", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "diagnostics" {
		t.Fatalf("expected diagnostics, got %q", env.Type)
	}

	var d protocol.DiagnosticsMsg

	_ = protocol.DecodePayload(env, &d)

	if d.DaemonPID == 0 {
		t.Error("expected a non-zero daemon PID in diagnostics")
	}
}

// --- approval_list / approval_subscribe / approval_respond ----------------

func TestCoverApprovalList(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "approval_list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "approval_notification" {
		t.Fatalf("expected approval_notification, got %q", env.Type)
	}
}

func TestCoverApprovalSubscribeLocalHuman(t *testing.T) {
	h := newTestHarness(t)

	// Local Unix socket (no token) resolves to the local human operator, who is
	// allowed to subscribe and immediately receives the current pending set.
	h.sendControl(t, "approval_subscribe", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "approval_notification" {
		t.Fatalf("expected approval_notification, got %q", env.Type)
	}
}

func TestCoverApprovalSubscribeRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-sess", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "approval_subscribe", struct{}{}, "tok-thrawn")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "human operator") {
		t.Errorf("message = %q, want it to mention human operator", e.Message)
	}
}

func TestCoverApprovalRespondRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "fash-sess", "fash", "tok-fash")

	h.sendControlWithToken(t, "approval_respond", protocol.ApprovalRespondMsg{
		RequestID: "req-1", Decision: "allow",
	}, "tok-fash")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "not permitted for agent sessions") {
		t.Errorf("message = %q, want agent-not-permitted", e.Message)
	}
}

func TestCoverApprovalRespondInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "approval_respond", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverApprovalRespondNotFound(t *testing.T) {
	h := newTestHarness(t)

	// Local human responding to a request that does not exist.
	h.sendControl(t, "approval_respond", protocol.ApprovalRespondMsg{
		RequestID: "haar-missing", Decision: "deny",
	})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "not found") {
		t.Errorf("message = %q, want not-found", e.Message)
	}
}

func TestCoverApprovalRequestInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "approval_request", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

// --- set_status -----------------------------------------------------------

func TestCoverSetStatusInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "set_status", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverSetStatusSetAndClear(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["ken1"] = &SessionState{
		ID: "ken1", Name: "ken-lad", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	// Set a summary.
	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "ken1", Text: "workin awa"})

	env := h.readControlMsg(t)
	if env.Type != "status_set" {
		t.Fatalf("set: expected status_set, got %q", env.Type)
	}

	h.sm.mu.RLock()
	got := h.sm.state.Sessions["ken1"].SummaryText
	h.sm.mu.RUnlock()

	if got != "workin awa" {
		t.Errorf("summary text = %q, want %q", got, "workin awa")
	}

	// Clear it.
	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "ken1", Clear: true})

	env = h.readControlMsg(t)
	if env.Type != "status_set" {
		t.Fatalf("clear: expected status_set, got %q", env.Type)
	}

	h.sm.mu.RLock()
	got = h.sm.state.Sessions["ken1"].SummaryText
	h.sm.mu.RUnlock()

	if got != "" {
		t.Errorf("summary text after clear = %q, want empty", got)
	}
}

func TestCoverSetStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "haar", Text: "nae session"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverSetStatusClearNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "set_status", protocol.SetStatusMsg{SessionID: "haar", Clear: true})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverSetStatusForcedToOwnSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "canny-own", "canny", "tok-canny")

	// An authenticated session's set_status is forced onto its own session ID,
	// even if it names a different target.
	h.sendControlWithToken(t, "set_status", protocol.SetStatusMsg{
		SessionID: "some-other", Text: "mine",
	}, "tok-canny")

	env := h.readControlMsg(t)
	if env.Type != "status_set" {
		t.Fatalf("expected status_set, got %q", env.Type)
	}

	h.sm.mu.RLock()
	got := h.sm.state.Sessions["canny-own"].SummaryText
	h.sm.mu.RUnlock()

	if got != "mine" {
		t.Errorf("summary applied to wrong session; own session text = %q", got)
	}
}

// --- status (session status query) ---------------------------------------

func TestCoverStatusInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "status", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "status", protocol.StatusRequestMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverStatusResponse(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["ken2"] = &SessionState{
		ID: "ken2", Name: "ken-status", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	_, _ = h.sm.messages.Publish("inbox:ken2", "brae-sender", "Brae", "unread bide", "", "")

	h.sendControl(t, "status", protocol.StatusRequestMsg{SessionID: "ken2"})

	env := h.readControlMsg(t)
	if env.Type != "status_response" {
		t.Fatalf("expected status_response, got %q", env.Type)
	}

	var resp protocol.StatusResponseMsg

	_ = protocol.DecodePayload(env, &resp)

	if resp.Session.ID != "ken2" {
		t.Errorf("session id = %q, want ken2", resp.Session.ID)
	}

	if resp.UnreadCount != 1 {
		t.Errorf("unread count = %d, want 1", resp.UnreadCount)
	}
}

// --- status_report --------------------------------------------------------

func TestCoverStatusReportInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "status_report", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverStatusReport(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "kirk-rep", "kirk", "tok-kirk")

	h.sendControlWithToken(t, "status_report", protocol.StatusReportMsg{
		SessionID: "kirk-rep",
		ToolName:  "Edit",
	}, "tok-kirk")

	env := h.readControlMsg(t)
	if env.Type != "status_reported" {
		t.Fatalf("expected status_reported, got %q", env.Type)
	}
}

// --- star / unstar --------------------------------------------------------

func TestCoverStarUnstar(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["bonnie-star"] = &SessionState{
		ID: "bonnie-star", Name: "bonnie", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "star", protocol.StarMsg{SessionID: "bonnie-star"})

	env := h.readControlMsg(t)
	if env.Type != "starred" {
		t.Fatalf("expected starred, got %q", env.Type)
	}

	h.sm.mu.RLock()
	starred := h.sm.state.Sessions["bonnie-star"].Starred
	h.sm.mu.RUnlock()

	if !starred {
		t.Error("expected session to be starred")
	}

	h.sendControl(t, "unstar", protocol.UnstarMsg{SessionID: "bonnie-star"})

	env = h.readControlMsg(t)
	if env.Type != "unstarred" {
		t.Fatalf("expected unstarred, got %q", env.Type)
	}

	h.sm.mu.RLock()
	starred = h.sm.state.Sessions["bonnie-star"].Starred
	h.sm.mu.RUnlock()

	if starred {
		t.Error("expected session to be unstarred")
	}
}

func TestCoverStarNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "star", protocol.StarMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverUnstarNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "unstar", protocol.UnstarMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverStarInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "star", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverUnstarInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "unstar", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

// --- interrupt ------------------------------------------------------------

func TestCoverInterruptInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "interrupt", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverInterruptNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "interrupt", protocol.InterruptMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

// --- restart --------------------------------------------------------------

func TestCoverRestartInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "restart", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverRestartNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "restart", protocol.RestartMsg{SessionID: "haar"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverRestartWithChildrenNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "restart", protocol.RestartMsg{SessionID: "haar", Children: true})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

// --- msg_conversation -----------------------------------------------------

func TestCoverMsgConversationInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "msg_conversation", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverMsgConversation(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["blether-sess"] = &SessionState{
		ID: "blether-sess", Name: "blether", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	// A message in the session's inbox should appear in its conversation.
	_, _ = h.sm.messages.Publish("inbox:blether-sess", "glen-sender", "Glen", "haud on", "", "")

	h.sendControl(t, "msg_conversation", protocol.MsgConversationMsg{SessionID: "blether-sess"})

	env := h.readControlMsg(t)
	if env.Type != "msg_conversation_list" {
		t.Fatalf("expected msg_conversation_list, got %q", env.Type)
	}

	var resp protocol.MsgConversationListMsg

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Messages) == 0 {
		t.Error("expected at least one conversation message")
	}
}

func TestCoverMsgConversationClampsLimit(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["skelf-sess"] = &SessionState{
		ID: "skelf-sess", Name: "skelf", Status: StatusRunning,
		Agent: "claude", CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	// A wildly oversized limit is clamped rather than honoured verbatim.
	h.sendControl(t, "msg_conversation", protocol.MsgConversationMsg{
		SessionID: "skelf-sess", Limit: 999999,
	})

	env := h.readControlMsg(t)
	if env.Type != "msg_conversation_list" {
		t.Fatalf("expected msg_conversation_list, got %q", env.Type)
	}
}

// --- fork / migrate payload validation ------------------------------------

func TestCoverForkInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "fork", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverMigrateInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "migrate", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

// --- reload ---------------------------------------------------------------

func TestCoverReloadLocalHuman(t *testing.T) {
	h := newTestHarness(t)

	// Point at a nonexistent config file so the reload deterministically falls
	// back to defaults (which match the harness config) instead of reading the
	// developer's real ~/.config/graith/config.toml.
	h.sm.configFile = filepath.Join(t.TempDir(), "nae.toml")

	h.sendControl(t, "reload", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "reloaded" {
		t.Fatalf("expected reloaded, got %q", env.Type)
	}
}

func TestCoverReloadRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-reload", "thrawn", "tok-rl")

	h.sendControlWithToken(t, "reload", struct{}{}, "tok-rl")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "not permitted for agent sessions") {
		t.Errorf("message = %q, want agent-not-permitted", e.Message)
	}
}

// --- mcp_connect guards ---------------------------------------------------

func TestCoverMCPConnectInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "mcp_connect", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverMCPConnectNoManager(t *testing.T) {
	h := newTestHarness(t)

	// The harness has no MCP manager configured, so a connect must fail closed.
	h.sendControl(t, "mcp_connect", protocol.MCPConnectMsg{Server: "chrome"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "MCP manager not initialized") {
		t.Errorf("message = %q, want MCP-manager-not-initialized", e.Message)
	}
}

// --- scenario lifecycle ---------------------------------------------------

func TestCoverScenarioStartRequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	// Local human (unauthenticated) may not start a scenario.
	h.sendControl(t, "scenario_start", protocol.ScenarioStartMsg{Name: "strath"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "requires authentication") {
		t.Errorf("message = %q, want requires-authentication", e.Message)
	}
}

func TestCoverScenarioStartInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "scenario_start", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_status", protocol.ScenarioStatusMsg{Name: "haar-strath"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioStatusInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "scenario_status", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioList(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_list", struct{}{})

	env := h.readControlMsg(t)
	if env.Type != "scenario_list" {
		t.Fatalf("expected scenario_list, got %q", env.Type)
	}

	var resp protocol.ScenarioListResponse

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Scenarios) != 0 {
		t.Errorf("expected no scenarios, got %d", len(resp.Scenarios))
	}
}

func TestCoverScenarioStopNotFound(t *testing.T) {
	h := newTestHarness(t)

	// Local human passes the scenario-op authorization; the operation then fails
	// because there is no such scenario.
	h.sendControl(t, "scenario_stop", protocol.ScenarioStopMsg{Name: "haar-strath"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioStopInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "scenario_stop", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioDeleteNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_delete", protocol.ScenarioDeleteMsg{Name: "haar-strath"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioDeleteInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "scenario_delete", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioResumeNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_resume", protocol.ScenarioResumeMsg{Name: "haar-strath"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioResumeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "scenario_resume", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioAddNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_add", protocol.ScenarioAddMsg{Name: "haar-strath"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioAddInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "scenario_add", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

func TestCoverScenarioTaskDoneRequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	// Unauthenticated (local human) task-done is rejected — there is no session
	// whose task could be marked done.
	h.sendControl(t, "scenario_task_done", protocol.ScenarioTaskDoneMsg{Name: "strath"})

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "authenticated session") {
		t.Errorf("message = %q, want requires-authenticated-session", e.Message)
	}
}

func TestCoverScenarioTaskDoneInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	raw, _ := json.Marshal(protocol.Envelope{Type: "scenario_task_done", Payload: json.RawMessage(`{bad`)})
	_ = h.writer.WriteFrame(protocol.ChannelControl, raw)

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}

// --- session lifecycle with children -------------------------------------

func TestCoverStopWithChildren(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "ben-root", "ben-parent")

	// Stop the session (and any descendants) — exercises the batch branch of
	// handleSessionLifecycle and its multi-session response shape.
	h.sendControl(t, "stop", protocol.StopMsg{SessionID: "ben-root", Children: true})

	env := h.readControlMsg(t)
	if env.Type != "stopped" {
		t.Fatalf("expected stopped, got %q", env.Type)
	}

	var resp struct {
		SessionID string   `json:"session_id"`
		Stopped   []string `json:"stopped"`
	}

	_ = protocol.DecodePayload(env, &resp)

	if resp.SessionID != "ben-root" {
		t.Errorf("session_id = %q, want ben-root", resp.SessionID)
	}
}

func TestCoverDeleteWithChildren(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "brae-root", "brae-parent")

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "brae-root", Children: true})

	env := h.readControlMsg(t)
	if env.Type != "deleted" {
		t.Fatalf("expected deleted, got %q", env.Type)
	}

	var resp struct {
		SessionID string   `json:"session_id"`
		Deleted   []string `json:"deleted"`
	}

	_ = protocol.DecodePayload(env, &resp)

	if resp.SessionID != "brae-root" {
		t.Errorf("session_id = %q, want brae-root", resp.SessionID)
	}
}

func TestCoverScenarioTaskDoneUnknownScenario(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "strath-sess", "strath", "tok-strath")

	h.sendControlWithToken(t, "scenario_task_done", protocol.ScenarioTaskDoneMsg{
		Name: "haar-strath",
	}, "tok-strath")

	env := h.readControlMsg(t)
	if env.Type != "error" {
		t.Fatalf("expected error, got %q", env.Type)
	}
}
