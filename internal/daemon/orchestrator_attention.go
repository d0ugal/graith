package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

const (
	orchestratorAttentionStaleAfter = 5 * time.Minute
	orchestratorAttentionTextLimit  = 80
	orchestratorAttentionCtxLimit   = 4000
)

func handleOrchestratorAttention(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.OrchestratorAttentionMsg](msg, send, "invalid orchestrator_attention message")
	if !ok {
		return
	}

	if !auth.authorizeOrchestratorAttention(sm, m.Clear, send) {
		return
	}

	if m.Clear {
		text := sm.clearOrchestratorAttention()
		send("orchestrator_attention_response", protocol.OrchestratorAttentionResponse{Active: false, Text: text})

		return
	}

	text := compactAttentionStatusText(m.Text, orchestratorAttentionTextLimit)
	if text == "" {
		send("error", protocol.ErrorMsg{Message: "attention text is required"})

		return
	}

	requestContext := compactAttentionContext(m.Context, orchestratorAttentionCtxLimit)

	state, active, err := sm.setOrchestratorAttention(auth.sessionID, text, requestContext)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("orchestrator_attention_response", protocol.OrchestratorAttentionResponse{
		Active: active,
		Text:   state.Text,
	})
}

func compactAttentionStatusText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	s = strings.Join(strings.Fields(s), " ")

	return truncateAttentionRunes(s, limit)
}

func compactAttentionContext(s string, limit int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	return truncateAttentionRunes(s, limit)
}

func truncateAttentionRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}

	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}

	return string(runes[:limit])
}

func (sm *SessionManager) setOrchestratorAttention(orchestratorID, text, context string) (OrchestratorAttentionRequestState, bool, error) {
	now := time.Now().UTC()

	id, err := generateToken()
	if err != nil {
		return OrchestratorAttentionRequestState{}, false, fmt.Errorf("generate attention id: %w", err)
	}

	state := OrchestratorAttentionRequestState{
		ID:             "attn_" + id[:16],
		OrchestratorID: orchestratorID,
		Text:           text,
		Context:        context,
		RequestedAt:    now,
	}

	sm.mu.Lock()
	previous := sm.state.OrchestratorAttention
	active := true

	if sm.hasAttentionClearingAttachedClientLocked(orchestratorID) {
		sm.state.OrchestratorAttention = nil
		active = false
	} else {
		sm.state.OrchestratorAttention = &state
	}

	err = sm.saveState()
	if err != nil {
		sm.state.OrchestratorAttention = previous
	}
	sm.mu.Unlock()

	if err != nil {
		return OrchestratorAttentionRequestState{}, false, fmt.Errorf("save attention request: %w", err)
	}

	return state, active, nil
}

func (sm *SessionManager) clearOrchestratorAttention() string {
	sm.mu.Lock()

	var text string

	if sm.state.OrchestratorAttention != nil {
		text = sm.state.OrchestratorAttention.Text
		sm.state.OrchestratorAttention = nil

		if err := sm.saveState(); err != nil {
			sm.log.Error("failed to save cleared orchestrator attention", "err", err)
		}
	}
	sm.mu.Unlock()

	return text
}

// clearOrchestratorAttentionForSessionLocked clears the visible attention
// marker for a removed or arrived-at orchestrator and returns the previous
// request. The caller must hold sm.mu.
func (sm *SessionManager) clearOrchestratorAttentionForSessionLocked(sessionID string) *OrchestratorAttentionRequestState {
	if sm.state.OrchestratorAttention == nil || sm.state.OrchestratorAttention.OrchestratorID != sessionID {
		return nil
	}

	copied := *sm.state.OrchestratorAttention
	sm.state.OrchestratorAttention = nil

	return &copied
}

// hasAttentionClearingAttachedClientLocked reports whether a user is already
// attached in the mode that counts as "the user arrived" for attention
// requests. The caller must hold sm.mu.
func (sm *SessionManager) hasAttentionClearingAttachedClientLocked(sessionID string) bool {
	ac, ok := sm.attachedClients[sessionID]

	return ok && ac.userAttach && !ac.readOnly
}

// visibleOrchestratorAttentionLocked returns the status-bar text only when the
// requesting orchestrator still exists and is visible. The caller must hold
// sm.mu.
func (sm *SessionManager) visibleOrchestratorAttentionLocked() string {
	req := sm.state.OrchestratorAttention
	if req == nil {
		return ""
	}

	sess := sm.state.Sessions[req.OrchestratorID]
	if sess == nil || sess.IsSoftDeleted() {
		return ""
	}

	return req.Text
}

// noteOrchestratorAttach clears an outstanding request when the user arrives at
// the requesting orchestrator. Stale requests get one daemon-authored inbox
// prompt so the orchestrator can resume the conversation with fresh context.
func (sm *SessionManager) noteOrchestratorAttach(ctx context.Context, sessionID string, now time.Time) {
	var request *OrchestratorAttentionRequestState

	sm.mu.Lock()
	request = sm.clearOrchestratorAttentionForSessionLocked(sessionID)

	if request != nil {
		if err := sm.saveState(); err != nil {
			sm.log.Error("failed to save cleared orchestrator attention after attach", "session", sessionID, "err", err)
		}
	}
	sm.mu.Unlock()

	if request == nil || now.Sub(request.RequestedAt) < orchestratorAttentionStaleAfter {
		return
	}

	if err := sm.notifyFromDaemonWithOpts(ctx, sessionID, orchestratorArrivalPrompt(*request), daemonNotificationOpts{}); err != nil {
		sm.log.Error("failed to notify orchestrator about user arrival", "session", sessionID, "attention", request.ID, "err", err)
	}
}

func orchestratorArrivalPrompt(request OrchestratorAttentionRequestState) string {
	var b strings.Builder

	b.WriteString("You had an outstanding request for the user. They have arrived.")

	if request.Text != "" {
		b.WriteString("\n\nStatus-bar text: ")
		b.WriteString(request.Text)
	}

	if request.Context != "" {
		b.WriteString("\n\nContext: ")
		b.WriteString(request.Context)
	}

	return b.String()
}
