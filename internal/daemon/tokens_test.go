package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTokenTestSM builds a SessionManager with a token cache and the given
// sessions, suitable for exercising the token loop.
func newTokenTestSM(sessions map[string]*SessionState) *SessionManager {
	return &SessionManager{
		state:  &State{Sessions: sessions},
		tokens: newTokenCache(),
		mu:     sync.RWMutex{},
	}
}

// writeClaudeTranscript lays out a CLAUDE_CONFIG_DIR with a transcript for the
// given agent session id and returns nothing (env is set on t).
func writeClaudeTranscript(t *testing.T, agentSessionID string, lines ...string) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	proj := filepath.Join(root, "projects", "-glen-bothy")
	if err := os.MkdirAll(proj, 0o750); err != nil {
		t.Fatal(err)
	}

	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}

	if err := os.WriteFile(filepath.Join(proj, agentSessionID+".jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func asstLine(msgID string, in, out int64) string {
	return `{"type":"assistant","uuid":"u-` + msgID + `","message":{"id":"` + msgID +
		`","role":"assistant","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) +
		`,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"text","text":"aye"}]}}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}

	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}

	return string(b)
}

func TestTokenLoopPopulatesStats(t *testing.T) {
	// One assistant response written twice (same message id) — the reader must
	// dedup, and the loop must publish the deduped total onto the session.
	writeClaudeTranscript(t, "sess-braw",
		asstLine("msg_1", 10, 20),
		asstLine("msg_1", 10, 20),
	)

	sm := newTokenTestSM(map[string]*SessionState{
		"braw": {ID: "braw", Agent: "claude", AgentSessionID: "sess-braw", Status: StatusRunning},
	})

	sm.runTokenTick(context.Background())

	got := sm.state.Sessions["braw"].Tokens
	if got == nil {
		t.Fatal("Tokens = nil, want populated")
	}

	if got.Input != 10 || got.Output != 20 || got.Total != 30 {
		t.Errorf("stats = %+v, want input 10 output 20 total 30 (deduped)", got)
	}

	if got.CountedAt.IsZero() {
		t.Error("CountedAt is zero, want set")
	}
}

func TestTokenLoopSkipsUnsupportedAgent(t *testing.T) {
	sm := newTokenTestSM(map[string]*SessionState{
		"cursor1": {ID: "cursor1", Agent: "cursor", AgentSessionID: "x", Status: StatusRunning},
	})

	sm.runTokenTick(context.Background())

	if sm.state.Sessions["cursor1"].Tokens != nil {
		t.Error("Tokens set for unsupported agent, want nil")
	}
}

func TestTokenLoopSkipsSoftDeleted(t *testing.T) {
	writeClaudeTranscript(t, "sess-dreich", asstLine("msg_1", 5, 5))

	now := time.Now()
	sm := newTokenTestSM(map[string]*SessionState{
		"dreich": {ID: "dreich", Agent: "claude", AgentSessionID: "sess-dreich", Status: StatusStopped, DeletedAt: &now},
	})

	sm.runTokenTick(context.Background())

	if sm.state.Sessions["dreich"].Tokens != nil {
		t.Error("Tokens set for soft-deleted session, want nil (skipped)")
	}
}

func TestTokenLoopIncludesErrored(t *testing.T) {
	writeClaudeTranscript(t, "sess-fash", asstLine("msg_1", 3, 4))

	sm := newTokenTestSM(map[string]*SessionState{
		"fash": {ID: "fash", Agent: "claude", AgentSessionID: "sess-fash", Status: StatusErrored},
	})

	sm.runTokenTick(context.Background())

	if sm.state.Sessions["fash"].Tokens == nil {
		t.Error("errored session should still be counted (billable usage)")
	}
}

func TestTokenLoopFingerprintCacheSkipsUnchanged(t *testing.T) {
	writeClaudeTranscript(t, "sess-bide", asstLine("msg_1", 10, 20))

	sm := newTokenTestSM(map[string]*SessionState{
		"bide": {ID: "bide", Agent: "claude", AgentSessionID: "sess-bide", Status: StatusRunning},
	})

	sm.runTokenTick(context.Background())
	first := sm.state.Sessions["bide"].Tokens

	// A second tick over an unchanged file is a cache hit: pollTokens returns
	// false and does not re-publish (the same value stays).
	sm.runTokenTick(context.Background())
	second := sm.state.Sessions["bide"].Tokens

	if first != second {
		t.Error("expected the cached stats pointer to be reused on an unchanged transcript")
	}
}

func TestTokenCachePrunesPurged(t *testing.T) {
	c := newTokenCache()
	c.put("braw", tokenCacheEntry{fingerprint: "fp"})
	c.put("gone", tokenCacheEntry{fingerprint: "fp"})

	c.prune(map[string]bool{"braw": true})

	if _, ok := c.get("gone"); ok {
		t.Error("purged session cache entry not pruned")
	}

	if _, ok := c.get("braw"); !ok {
		t.Error("live session cache entry wrongly pruned")
	}
}

func TestTokenInfoProjection(t *testing.T) {
	if tokenInfo(nil) != nil {
		t.Error("tokenInfo(nil) should be nil (unknown)")
	}

	got := tokenInfo(&TokenStats{Input: 1, Output: 2, Total: 3, Degraded: true})
	if got == nil || got.Total != 3 || !got.Degraded {
		t.Errorf("tokenInfo = %+v, want total 3, degraded", got)
	}
}
