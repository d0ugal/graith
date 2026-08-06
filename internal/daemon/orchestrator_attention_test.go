package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestOrchestratorAttentionHandlerSetsAndDenies(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "orch", "orchestrator", "tok-orch")
	h.addAuthenticatedSession(t, "bairn", "bairn", "tok-bairn")

	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.mu.Unlock()

	h.sendControlWithToken(t, "orchestrator_attention", protocol.OrchestratorAttentionMsg{
		Text:    "Need release decision",
		Context: "PR comments are waiting in jail.",
	}, "tok-orch")
	env := h.expectType(t, "orchestrator_attention_response")

	var resp protocol.OrchestratorAttentionResponse

	_ = protocol.DecodePayload(env, &resp)
	if !resp.Active || resp.Text != "Need release decision" {
		t.Fatalf("attention response = %+v, want active request", resp)
	}

	h.sm.mu.RLock()
	stored := h.sm.state.OrchestratorAttention
	h.sm.mu.RUnlock()

	if stored == nil || stored.OrchestratorID != "orch" || stored.Context != "PR comments are waiting in jail." {
		t.Fatalf("stored attention = %+v", stored)
	}

	h.sendControlWithToken(t, "orchestrator_attention", protocol.OrchestratorAttentionMsg{Text: "   "}, "tok-orch")
	env = h.expectType(t, "error")

	var errMsg protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &errMsg)
	if !strings.Contains(errMsg.Message, "attention text is required") {
		t.Fatalf("empty text error = %q", errMsg.Message)
	}

	h.sendControlWithToken(t, "orchestrator_attention", protocol.OrchestratorAttentionMsg{Text: "plain agent"}, "tok-bairn")
	env = h.expectType(t, "error")

	_ = protocol.DecodePayload(env, &errMsg)
	if !strings.Contains(errMsg.Message, "only the orchestrator") {
		t.Fatalf("plain agent error = %q", errMsg.Message)
	}

	h.sendControlWithToken(t, "orchestrator_attention", protocol.OrchestratorAttentionMsg{Clear: true}, "tok-bairn")
	env = h.expectType(t, "error")

	_ = protocol.DecodePayload(env, &errMsg)
	if !strings.Contains(errMsg.Message, "only the orchestrator or the user") {
		t.Fatalf("plain agent clear error = %q", errMsg.Message)
	}
}

func TestCompactAttentionTextAndContext(t *testing.T) {
	statusText := compactAttentionStatusText("  Need\nrelease\tdecision  ", 80)
	if statusText != "Need release decision" {
		t.Fatalf("status text = %q, want collapsed whitespace", statusText)
	}

	longText := strings.Repeat("a", orchestratorAttentionTextLimit+10)
	if got := compactAttentionStatusText(longText, orchestratorAttentionTextLimit); len([]rune(got)) != orchestratorAttentionTextLimit {
		t.Fatalf("status text length = %d, want %d", len([]rune(got)), orchestratorAttentionTextLimit)
	}

	requestContext := compactAttentionContext("\nUse gr msg jail list\n", 8)
	if requestContext != "Use gr m" {
		t.Fatalf("context = %q, want trimmed and truncated", requestContext)
	}
}

func TestOrchestratorAttentionClearByUser(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_braw",
		OrchestratorID: "orch",
		Text:           "Need user",
		RequestedAt:    time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "orchestrator_attention", protocol.OrchestratorAttentionMsg{Clear: true})
	env := h.expectType(t, "orchestrator_attention_response")

	var resp protocol.OrchestratorAttentionResponse

	_ = protocol.DecodePayload(env, &resp)
	if resp.Active {
		t.Fatalf("clear response active = true")
	}

	h.sm.mu.RLock()
	stored := h.sm.state.OrchestratorAttention
	h.sm.mu.RUnlock()

	if stored != nil {
		t.Fatalf("attention not cleared: %+v", stored)
	}
}

func TestOrchestratorAttentionStaleAttachClearsAndNotifiesOnce(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "orch", "orchestrator")

	requestedAt := time.Now().UTC().Add(-orchestratorAttentionStaleAfter - time.Minute)

	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_canny",
		OrchestratorID: "orch",
		Text:           "Need release decision",
		Context:        "Use gr msg jail list",
		RequestedAt:    requestedAt,
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "orch"})
	h.expectType(t, "attached")

	h.sm.mu.RLock()
	stored := h.sm.state.OrchestratorAttention
	h.sm.mu.RUnlock()

	if stored != nil {
		t.Fatalf("attention not cleared on attach: %+v", stored)
	}

	msgs, err := h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("read orchestrator inbox: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("stale attach messages = %d, want 1", len(msgs))
	}

	for _, want := range []string{"outstanding request", "They have arrived", "Need release decision", "Use gr msg jail list"} {
		if !strings.Contains(msgs[0].Body, want) {
			t.Fatalf("arrival prompt missing %q: %q", want, msgs[0].Body)
		}
	}

	h.sendControl(t, "detach", protocol.DetachedMsg{})
	h.expectType(t, "detached")
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "orch"})
	h.expectType(t, "attached")

	msgs, err = h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("read orchestrator inbox after reattach: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("reattach messages = %d, want still 1", len(msgs))
	}
}

func TestOrchestratorAttentionReadOnlyAttachDoesNotClear(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "orch", "orchestrator")

	requestedAt := time.Now().UTC().Add(-orchestratorAttentionStaleAfter - time.Minute)

	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_thrawn",
		OrchestratorID: "orch",
		Text:           "Need release decision",
		Context:        "Use gr msg jail list",
		RequestedAt:    requestedAt,
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "orch", ReadOnly: true})
	h.expectType(t, "attached")

	h.sm.mu.RLock()
	stored := h.sm.state.OrchestratorAttention
	h.sm.mu.RUnlock()

	if stored == nil {
		t.Fatal("read-only attach cleared attention")
	}

	msgs, err := h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("read orchestrator inbox: %v", err)
	}

	if len(msgs) != 0 {
		t.Fatalf("read-only attach messages = %d, want 0", len(msgs))
	}

	h.sendControl(t, "detach", protocol.DetachedMsg{})
	h.expectType(t, "detached")
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "orch"})
	h.expectType(t, "attached")

	h.sm.mu.RLock()
	stored = h.sm.state.OrchestratorAttention
	h.sm.mu.RUnlock()

	if stored != nil {
		t.Fatalf("writable attach did not clear attention: %+v", stored)
	}

	msgs, err = h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("read orchestrator inbox after writable attach: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("writable attach messages = %d, want 1", len(msgs))
	}
}

func TestOrchestratorAttentionFreshAttachClearsWithoutPrompt(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "orch", "orchestrator")

	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_braw",
		OrchestratorID: "orch",
		Text:           "Need user",
		RequestedAt:    time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "orch"})
	h.expectType(t, "attached")

	h.sm.mu.RLock()
	stored := h.sm.state.OrchestratorAttention
	h.sm.mu.RUnlock()

	if stored != nil {
		t.Fatalf("attention not cleared on fresh attach: %+v", stored)
	}

	msgs, err := h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("read orchestrator inbox: %v", err)
	}

	if len(msgs) != 0 {
		t.Fatalf("fresh attach messages = %d, want 0", len(msgs))
	}
}

func TestOrchestratorAttentionSetWhileUserAttachedDoesNotPersist(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "orch", "orchestrator")

	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.state.Sessions["orch"].Token = "tok-orch"
	h.sm.tokenIndex["tok-orch"] = "orch"
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "orch"})
	h.expectType(t, "attached")

	h.sendControlWithToken(t, "orchestrator_attention", protocol.OrchestratorAttentionMsg{
		Text:    "Need release decision",
		Context: "User is already here.",
	}, "tok-orch")
	env := h.expectType(t, "orchestrator_attention_response")

	var resp protocol.OrchestratorAttentionResponse

	_ = protocol.DecodePayload(env, &resp)
	if resp.Active {
		t.Fatalf("attention response active = true, want false while user attached")
	}

	h.sm.mu.RLock()
	stored := h.sm.state.OrchestratorAttention
	h.sm.mu.RUnlock()

	if stored != nil {
		t.Fatalf("attention persisted while user attached: %+v", stored)
	}
}

func TestOrchestratorAttentionClearsOnDelete(t *testing.T) {
	sm := newTestSessionManager(t)
	s := addStoppedSession(t, sm, "orch", "orchestrator")
	s.SystemKind = SystemKindOrchestrator

	sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_dreich",
		OrchestratorID: "orch",
		Text:           "Need user",
		RequestedAt:    time.Now().UTC(),
	}

	if err := sm.Delete("orch"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	sm.mu.RLock()
	stored := sm.state.OrchestratorAttention
	sm.mu.RUnlock()

	if stored != nil {
		t.Fatalf("attention not cleared on delete: %+v", stored)
	}
}

func TestOrchestratorAttentionClearsOnSoftDelete(t *testing.T) {
	sm := newTestSessionManager(t)
	s := addStoppedSession(t, sm, "orch", "orchestrator")
	s.SystemKind = SystemKindOrchestrator

	sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_bide",
		OrchestratorID: "orch",
		Text:           "Need user",
		RequestedAt:    time.Now().UTC(),
	}

	if _, err := sm.SoftDelete("orch"); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	sm.mu.RLock()
	stored := sm.state.OrchestratorAttention
	sm.mu.RUnlock()

	if stored != nil {
		t.Fatalf("attention not cleared on soft delete: %+v", stored)
	}
}

func TestFleetSummaryIncludesAttentionAndJail(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "orch", "orchestrator", "tok-orch")

	h.sm.mu.Lock()
	h.sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_braw",
		OrchestratorID: "orch",
		Text:           "Need review",
		RequestedAt:    time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.jailOne(t, JailedComment{
		CommentID: 1, Surface: "conversation", PRNumber: 42, RepoSlug: "d0ugal/graith", Author: "scunner",
		TargetSession: "bairn", JailedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})

	fleet := h.sm.fleetSummary()
	if fleet.OrchestratorAttention != "Need review" {
		t.Fatalf("fleet attention = %q", fleet.OrchestratorAttention)
	}

	if fleet.JailedComments != 1 || fleet.JailedNewestAuthor != "scunner" || fleet.JailedNewestPR != 42 {
		t.Fatalf("fleet jail summary = %+v", fleet)
	}
}

func TestFleetSummarySuppressesStaleAttention(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.OrchestratorAttention = &OrchestratorAttentionRequestState{
		ID:             "attn_gone",
		OrchestratorID: "gone",
		Text:           "Need review",
		RequestedAt:    time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	if got := h.sm.fleetSummary().OrchestratorAttention; got != "" {
		t.Fatalf("missing orchestrator attention = %q, want empty", got)
	}

	h.addAuthenticatedSession(t, "gone", "orchestrator", "tok-orch")

	now := time.Now().UTC()

	h.sm.mu.Lock()
	h.sm.state.Sessions["gone"].DeletedAt = &now
	h.sm.mu.Unlock()

	if got := h.sm.fleetSummary().OrchestratorAttention; got != "" {
		t.Fatalf("soft-deleted orchestrator attention = %q, want empty", got)
	}
}
