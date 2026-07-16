package daemon

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// --- trigger dispatch -----------------------------------------------------

// TestCoverTriggerListEmpty verifies trigger_list responds with an (empty)
// listing when no triggers are configured — read-only, no auth gate.
func TestCoverTriggerListEmpty(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "trigger_list", struct{}{})

	env := h.expectType(t, "trigger_list")

	var resp protocol.TriggerListResponse
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Triggers) != 0 {
		t.Errorf("expected no triggers, got %d", len(resp.Triggers))
	}
}

// TestCoverTriggerStatusNotFound verifies trigger_status on an unknown trigger
// surfaces an error rather than an empty record.
func TestCoverTriggerStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "trigger_status", protocol.TriggerStatusMsg{Name: "fash"})

	h.expectError(t, "not found")
}

// TestCoverTriggerStatusInvalidPayload verifies a malformed trigger_status
// payload is rejected.
func TestCoverTriggerStatusInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "trigger_status")

	h.expectError(t, "invalid trigger_status")
}

// TestCoverTriggerRunNotFound verifies the human CLI passes the trigger-op auth
// gate but an unknown trigger still errors.
func TestCoverTriggerRunNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "trigger_run", protocol.TriggerRunMsg{Name: "fash"})

	h.expectError(t, "not found")
}

// TestCoverTriggerRunRejectsForeignSession verifies an authenticated session
// with no orchestrator to authorize against is denied the trigger op.
func TestCoverTriggerRunRejectsForeignSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "trigger_run", protocol.TriggerRunMsg{Name: "fash"}, "tok-thrawn")

	h.expectError(t, "not authorized")
}

// TestCoverTriggerPauseNotFound verifies trigger_pause reaches the manager (past
// auth) and errors on an unknown trigger.
func TestCoverTriggerPauseNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "trigger_pause", protocol.TriggerPauseMsg{Name: "fash", Pause: true})

	h.expectError(t, "not found")
}

// --- notify dispatch ------------------------------------------------------

// TestCoverNotifyHumanDeliversResponse verifies the human CLI passes the notify
// auth gate and receives a notify_response (delivery disabled by default config).
func TestCoverNotifyHumanDeliversResponse(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "notify", protocol.NotifyMsg{Title: "braw", Message: "all green"})

	h.expectType(t, "notify_response")
}

// TestCoverNotifyRejectsPlainSession verifies a plain agent session is rejected
// from sending notifications (orchestrator/human only).
func TestCoverNotifyRejectsPlainSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "notify", protocol.NotifyMsg{Title: "scunner", Message: "nope"}, "tok-thrawn")

	h.expectError(t, "not authorized")
}

// TestCoverNotifyInvalidPayload verifies a malformed notify payload is rejected.
func TestCoverNotifyInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "notify")

	h.expectError(t, "invalid notify")
}

// --- mcp dispatch ---------------------------------------------------------

// TestCoverMCPListNoManager verifies mcp_list returns an empty listing when the
// MCP manager is not initialized (the harness has none).
func TestCoverMCPListNoManager(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "mcp_list", struct{}{})

	env := h.expectType(t, "mcp_list")

	var resp protocol.MCPListResponse
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Servers) != 0 {
		t.Errorf("expected no servers, got %d", len(resp.Servers))
	}
}

// TestCoverMCPRestartNoManager verifies mcp_restart passes the human auth gate
// then errors because no MCP manager is initialized.
func TestCoverMCPRestartNoManager(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "mcp_restart", protocol.MCPRestartMsg{Name: "blether"})

	h.expectError(t, "MCP manager not initialized")
}

// TestCoverMCPRestartRejectsForeignSession verifies mcp_restart is gated by the
// trigger-op auth (a plain session with no orchestrator is denied).
func TestCoverMCPRestartRejectsForeignSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "mcp_restart", protocol.MCPRestartMsg{Name: "blether"}, "tok-thrawn")

	h.expectError(t, "not authorized")
}

// TestCoverMCPLogsNoManager verifies mcp_logs errors when no MCP manager exists.
func TestCoverMCPLogsNoManager(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "mcp_logs", protocol.MCPLogsMsg{Name: "blether"})

	h.expectError(t, "MCP manager not initialized")
}

// TestCoverMCPLogsInvalidPayload verifies a malformed mcp_logs payload is
// rejected before touching the manager.
func TestCoverMCPLogsInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "mcp_logs")

	h.expectError(t, "invalid mcp_logs")
}

// --- scenario dispatch ----------------------------------------------------

// TestCoverScenarioStatusNotFound verifies scenario_status errors on an unknown
// scenario.
func TestCoverScenarioStatusNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_status", protocol.ScenarioStatusMsg{Name: "strath"})

	h.expectError(t, "not found")
}

// TestCoverScenarioStopNotFound verifies scenario_stop passes the human auth
// gate then errors on an unknown scenario.
func TestCoverScenarioStopNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_stop", protocol.ScenarioStopMsg{Name: "strath"})

	h.expectError(t, "not found")
}

// TestCoverScenarioDeleteNotFound verifies scenario_delete errors on an unknown
// scenario after the auth gate.
func TestCoverScenarioDeleteNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_delete", protocol.ScenarioDeleteMsg{Name: "strath"})

	h.expectError(t, "not found")
}

// TestCoverScenarioResumeNotFound verifies scenario_resume errors on an unknown
// scenario after the auth gate.
func TestCoverScenarioResumeNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "scenario_resume", protocol.ScenarioResumeMsg{Name: "strath"})

	h.expectError(t, "not found")
}

// TestCoverScenarioStopInvalidPayload verifies a malformed scenario_stop payload
// is rejected.
func TestCoverScenarioStopInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_stop")

	h.expectError(t, "invalid scenario_stop")
}

// --- todo dispatch --------------------------------------------------------

// TestCoverTodoClaimInvalidPayload verifies a malformed todo_claim is rejected.
func TestCoverTodoClaimInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "todo_claim")

	h.expectError(t, "invalid todo_claim")
}

// TestCoverTodoTransitionUnknownItem verifies todo_transition on a missing item
// surfaces an error.
func TestCoverTodoTransitionUnknownItem(t *testing.T) {
	h := newTestHarness(t)
	h.sm.todos = newTestTodoStore(t)
	h.addAuthenticatedSession(t, "braw-id", "bonnie", "tok-braw")

	h.sendControlWithToken(t, "todo_transition", protocol.TodoTransitionMsg{ID: "td-missing", Status: "done"}, "tok-braw")

	h.expectError(t, "todo not found")
}

// TestCoverTodoRemoveUnknownItem verifies todo_remove on a missing item errors.
func TestCoverTodoRemoveUnknownItem(t *testing.T) {
	h := newTestHarness(t)
	h.sm.todos = newTestTodoStore(t)
	h.addAuthenticatedSession(t, "braw-id", "bonnie", "tok-braw")

	h.sendControlWithToken(t, "todo_remove", protocol.TodoRemoveMsg{ID: "td-missing"}, "tok-braw")

	h.expectError(t, "todo not found")
}

// TestCoverTodoClaimRoundTrip verifies a session can add then atomically claim
// an item, receiving a todo_claim response.
func TestCoverTodoClaimRoundTrip(t *testing.T) {
	h := newTestHarness(t)
	h.sm.todos = newTestTodoStore(t)
	h.addAuthenticatedSession(t, "braw-id", "bonnie", "tok-braw")

	h.sendControlWithToken(t, "todo_add", protocol.TodoAddMsg{Title: "wire the claim CAS"}, "tok-braw")

	env := h.expectType(t, "todo")

	var added protocol.TodoResponse
	if err := protocol.DecodePayload(env, &added); err != nil {
		t.Fatal(err)
	}

	h.sendControlWithToken(t, "todo_claim", protocol.TodoClaimMsg{ID: added.Item.ID}, "tok-braw")

	h.expectType(t, "todo_claim")
}

// TestCoverTodoExportInvalidPayload verifies a malformed todo_export is rejected.
func TestCoverTodoExportInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "todo_export")

	h.expectError(t, "invalid todo_export")
}

// TestCoverTodoUpdateRoundTrip verifies a session can add then edit a todo item,
// getting the updated item back.
func TestCoverTodoUpdateRoundTrip(t *testing.T) {
	h := newTestHarness(t)
	h.sm.todos = newTestTodoStore(t)
	h.addAuthenticatedSession(t, "braw-id", "bonnie", "tok-braw")

	id := addTodoViaHandler(t, h, "tok-braw", "draft the wynd")

	newTitle := "pave the wynd"
	h.sendControlWithToken(t, "todo_update", protocol.TodoUpdateMsg{ID: id, Title: &newTitle}, "tok-braw")

	env := h.expectType(t, "todo")

	var resp protocol.TodoResponse
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Item.Title != newTitle {
		t.Errorf("updated title = %q, want %q", resp.Item.Title, newTitle)
	}
}

// TestCoverTodoExportRoundTrip verifies a session can export its todo list to a
// store document and receive the key.
func TestCoverTodoExportRoundTrip(t *testing.T) {
	h := newTestHarness(t)
	h.sm.todos = newTestTodoStore(t)
	h.addAuthenticatedSession(t, "braw-id", "bonnie", "tok-braw")

	_ = addTodoViaHandler(t, h, "tok-braw", "record the loch")

	h.sendControlWithToken(t, "todo_export", protocol.TodoExportMsg{}, "tok-braw")

	env := h.expectType(t, "todo_export")

	var resp protocol.TodoExportResponse
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Key == "" {
		t.Error("expected a non-empty export key")
	}
}

// TestCoverTodoAssignRoundTrip verifies a session can add then assign a todo
// item, getting the updated item back.
func TestCoverTodoAssignRoundTrip(t *testing.T) {
	h := newTestHarness(t)
	h.sm.todos = newTestTodoStore(t)
	h.addAuthenticatedSession(t, "braw-id", "bonnie", "tok-braw")

	id := addTodoViaHandler(t, h, "tok-braw", "mind the croft")

	h.sendControlWithToken(t, "todo_assign", protocol.TodoAssignMsg{ID: id, Assignee: "canny"}, "tok-braw")

	env := h.expectType(t, "todo")

	var resp protocol.TodoResponse
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Item.Assignee != "canny" {
		t.Errorf("assignee = %q, want %q", resp.Item.Assignee, "canny")
	}
}

// TestCoverTodoAssignInvalidPayload verifies a malformed todo_assign is rejected.
func TestCoverTodoAssignInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "todo_assign")

	h.expectError(t, "invalid todo_assign")
}

// addTodoViaHandler adds a todo item through the handler and returns its id.
func addTodoViaHandler(t *testing.T, h *testHarness, token, title string) string {
	t.Helper()

	h.sendControlWithToken(t, "todo_add", protocol.TodoAddMsg{Title: title}, token)

	env := h.expectType(t, "todo")

	var resp protocol.TodoResponse
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	return resp.Item.ID
}

// TestCoverScenarioStartInvalidPayload verifies a malformed scenario_start is
// rejected.
func TestCoverScenarioStartInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_start")

	h.expectError(t, "invalid scenario_start")
}

// TestCoverScenarioAddInvalidPayload verifies a malformed scenario_add is
// rejected.
func TestCoverScenarioAddInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "scenario_add")

	h.expectError(t, "invalid scenario_add")
}

// --- store dispatch -------------------------------------------------------

// TestCoverStoreListRejectsAgent verifies store browsing requires a human
// operator — an authenticated session is refused.
func TestCoverStoreListRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "store_list", protocol.StoreListMsg{Shared: true}, "tok-thrawn")

	h.expectError(t, "store browsing requires a human operator")
}

// TestCoverStoreGetRejectsAgent verifies store_get is likewise human-only.
func TestCoverStoreGetRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "store_get", protocol.StoreGetMsg{Shared: true, Key: "loch/notes.md"}, "tok-thrawn")

	h.expectError(t, "store browsing requires a human operator")
}

// TestCoverStoreListHumanEmpty verifies the human CLI can list an empty store.
func TestCoverStoreListHumanEmpty(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "store_list", protocol.StoreListMsg{Shared: true})

	h.expectType(t, "store_list")
}

// --- jail dispatch --------------------------------------------------------

// TestCoverJailListEmpty verifies msg_jail_list returns an empty metadata-only
// listing when nothing is quarantined.
func TestCoverJailListEmpty(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_jail_list", protocol.MsgJailListMsg{})

	env := h.expectType(t, "msg_jail_list")

	var resp protocol.MsgJailListResponse
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Jailed) != 0 {
		t.Errorf("expected no jailed comments, got %d", len(resp.Jailed))
	}
}

// TestCoverJailShowNotFound verifies msg_jail_show on an unknown id errors.
func TestCoverJailShowNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_jail_show", protocol.MsgJailShowMsg{ID: "haar"})

	h.expectError(t, "no jailed comment")
}

// TestCoverJailReleaseNeedsArgs verifies msg_jail_release with neither an id nor
// --all --author errors (after passing the human release gate).
func TestCoverJailReleaseNeedsArgs(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_jail_release", protocol.MsgJailReleaseMsg{})

	h.expectError(t, "specify a jail id")
}

// TestCoverJailReleaseAllRequiresAuthor verifies --all without --author errors.
func TestCoverJailReleaseAllRequiresAuthor(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_jail_release", protocol.MsgJailReleaseMsg{All: true})

	h.expectError(t, "--all requires --author")
}

// TestCoverJailReleaseRejectsAgent verifies a plain agent session cannot release
// quarantined content (issue #1082).
func TestCoverJailReleaseRejectsAgent(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "msg_jail_release", protocol.MsgJailReleaseMsg{ID: "haar"}, "tok-thrawn")

	h.expectError(t, "not authorized")
}

// --- screen dispatch ------------------------------------------------------

// TestCoverScreenPreviewNotFound verifies screen_preview errors when the target
// session has no live PTY.
func TestCoverScreenPreviewNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "screen_preview", protocol.ScreenPreviewMsg{SessionID: "haar"})

	h.expectError(t, "session not found")
}

// TestCoverScreenSnapshotNotFound verifies screen_snapshot errors when the
// target session has no live PTY.
func TestCoverScreenSnapshotNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: "haar"})

	h.expectError(t, "session not found")
}

// --- pair dispatch (local human) ------------------------------------------

// TestCoverPairListLocalHuman verifies the local human can list pairings (empty).
func TestCoverPairListLocalHuman(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "pair_list", struct{}{})

	env := h.expectType(t, "pair_list")

	var resp protocol.PairListResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Pending) != 0 || len(resp.Paired) != 0 {
		t.Errorf("expected empty pairing lists, got pending=%d paired=%d", len(resp.Pending), len(resp.Paired))
	}
}

// TestCoverPairListRejectsSession verifies pair_list is local-only: an
// authenticated session is refused.
func TestCoverPairListRejectsSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "pair_list", struct{}{}, "tok-thrawn")

	h.expectError(t, "local-only")
}

// TestCoverPairApproveRejectsSession verifies pair_approve is local-only.
func TestCoverPairApproveRejectsSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "pair_approve", protocol.PairApproveMsg{RequestID: "haar"}, "tok-thrawn")

	h.expectError(t, "local-only")
}

// TestCoverPairApproveUnknownRequest verifies pair_approve by the local human
// errors on an unknown request id.
func TestCoverPairApproveUnknownRequest(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "pair_approve", protocol.PairApproveMsg{RequestID: "haar"})

	h.expectError(t, "no pending pairing")
}

// TestCoverPairRevokeRejectsSession verifies pair_revoke is local-only.
func TestCoverPairRevokeRejectsSession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn-id", "thrawn", "tok-thrawn")

	h.sendControlWithToken(t, "pair_revoke", protocol.PairRevokeMsg{DeviceID: "haar"}, "tok-thrawn")

	h.expectError(t, "local-only")
}

// TestCoverPairRevokeUnknownDevice verifies pair_revoke by the local human
// errors on an unknown device id.
func TestCoverPairRevokeUnknownDevice(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "pair_revoke", protocol.PairRevokeMsg{DeviceID: "haar"})

	h.expectError(t, "no paired device")
}

// --- gc dispatch ----------------------------------------------------------

// TestCoverGCDryRun verifies gc without Force reports a dry-run result.
func TestCoverGCDryRun(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "gc", protocol.GCMsg{})

	env := h.expectType(t, "gc_result")

	var resp protocol.GCResultMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if !resp.DryRun {
		t.Error("expected DryRun=true when Force is unset")
	}
}

// TestCoverGCInvalidPayload verifies a malformed gc payload is rejected.
func TestCoverGCInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "gc")

	h.expectError(t, "invalid gc")
}
