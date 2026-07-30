package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func newSearchTestSM(sessions map[string]*SessionState) *SessionManager {
	return newSearchTestSMWithConfig(sessions, config.Default())
}

func newSearchTestSMWithConfig(sessions map[string]*SessionState, cfg *config.Config) *SessionManager {
	return &SessionManager{
		state:  &State{Sessions: sessions},
		search: newConversationSearchCache(),
		cfg:    cfg,
	}
}

func TestHandleSearchRequiresHuman(t *testing.T) {
	sm := newSearchTestSM(map[string]*SessionState{})

	payload, err := json.Marshal(protocol.SearchMsg{Query: "braw"})
	if err != nil {
		t.Fatal(err)
	}

	var (
		gotType string
		got     protocol.ErrorMsg
	)

	handleSearch(context.Background(), sm, authContext{role: roleSession, authenticated: true, sessionID: "braw"}, func(msgType string, payload any) {
		gotType = msgType

		if errMsg, ok := payload.(protocol.ErrorMsg); ok {
			got = errMsg
		}
	}, protocol.Envelope{Type: "search", Payload: payload})

	if gotType != "error" || !strings.Contains(got.Message, "human operator") {
		t.Fatalf("handler response = %q %+v, want human-only error", gotType, got)
	}
}

func writeSearchClaudeTranscript(t *testing.T, root, agentSessionID string, lines ...string) string {
	t.Helper()

	proj := filepath.Join(root, "projects", "-glen-bothy")
	if err := os.MkdirAll(proj, 0o750); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(proj, agentSessionID+".jsonl")

	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func writeSearchCodexTranscript(t *testing.T, root, worktreePath, agentSessionID string, lines ...string) string {
	t.Helper()

	day := filepath.Join(root, "sessions", "2026", "07", "29")
	if err := os.MkdirAll(day, 0o750); err != nil {
		t.Fatal(err)
	}

	meta, err := json.Marshal(map[string]any{
		"type":      "session_meta",
		"timestamp": "2026-07-29T09:00:00Z",
		"payload": map[string]string{
			"id":  agentSessionID,
			"cwd": worktreePath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(day, "rollout-"+agentSessionID+".jsonl")

	data := string(meta) + "\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSearchConversationsFindsClaudeAndReportsUnsupported(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	writeSearchClaudeTranscript(t, root, "sess-braw",
		`{"type":"user","uuid":"u1","timestamp":"2026-07-29T10:00:00Z","message":{"role":"user","content":"fix the bothy"}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"braw": {
			ID: "braw", Name: "braw", RepoPath: "/repo/croft", RepoName: "croft",
			Agent: "claude", AgentSessionID: "sess-braw", Status: StatusRunning,
			CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
		"cursor": {
			ID: "cursor", Name: "cursor", Agent: "cursor", Status: StatusRunning,
			CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "bothy"})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v, want one Claude match", resp.Results)
	}

	got := resp.Results[0]
	if got.SessionID != "braw" || got.Kind != "user" || got.Timestamp != "2026-07-29T10:00:00Z" {
		t.Fatalf("result = %+v, want braw user timestamped result", got)
	}

	if !strings.Contains(got.Snippet, "bothy") || len(got.Matches) != 1 {
		t.Fatalf("snippet/matches = %q %+v, want one highlighted match", got.Snippet, got.Matches)
	}

	if len(resp.UnsupportedAgents) != 1 || resp.UnsupportedAgents[0].Agent != "cursor" || resp.UnsupportedAgents[0].Count != 1 {
		t.Fatalf("unsupported = %+v, want cursor count", resp.UnsupportedAgents)
	}
}

func TestSearchConversationsSanitizesTerminalControls(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	writeSearchClaudeTranscript(t, root, "sess-braw",
		`{"type":"assistant","uuid":"a1","timestamp":"2026-07-29T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"secret \u001b[31mphrase\u001b[0m \u001b]52;c;Zm9v\u0007done"}]}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"braw": {
			ID: "braw", Name: "braw", Agent: "claude", AgentSessionID: "sess-braw",
			Status: StatusRunning, CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "secret phrase"})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v, want sanitized match", resp.Results)
	}

	got := resp.Results[0]
	if strings.ContainsAny(got.Snippet, "\x1b\x07") || strings.Contains(got.Snippet, "]52") {
		t.Fatalf("snippet contains terminal controls: %q", got.Snippet)
	}

	if got.Snippet != "secret phrase done" {
		t.Fatalf("snippet = %q, want sanitized text", got.Snippet)
	}
}

func TestSearchConversationsUsesNativeTranscriptRootForCapturedCodex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", t.TempDir())

	worktree := t.TempDir()

	writeSearchCodexTranscript(t, root, worktree, "sess-codex",
		`{"type":"response_item","timestamp":"2026-07-29T10:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"captured root phrase"}]}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"canny": {
			ID: "canny", Name: "canny", Agent: "codex", AgentSessionID: "sess-codex",
			NativeTranscriptRoot: root, WorktreePath: worktree, Status: StatusRunning,
			CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "captured root phrase"})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 1 || resp.Results[0].Agent != "codex" {
		t.Fatalf("results = %+v, want codex native-root match", resp.Results)
	}
}

func TestSearchConversationsFallsBackToDefaultCodexRootForOldState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)

	worktree := t.TempDir()

	writeSearchCodexTranscript(t, root, worktree, "sess-codex",
		`{"type":"response_item","timestamp":"2026-07-29T10:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"old state phrase"}]}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"bide": {
			ID: "bide", Name: "bide", Agent: "codex", AgentSessionID: "sess-codex",
			WorktreePath: worktree, Status: StatusRunning,
			CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "old state phrase"})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 1 || resp.Results[0].Agent != "codex" {
		t.Fatalf("results = %+v, want old-state default-root match", resp.Results)
	}
}

func TestSearchConversationsCursorAndWindowValidation(t *testing.T) {
	limits := config.SearchConfig{DefaultLimit: 3, MaxLimit: 5, MaxWindow: 8}.Limits()

	tests := map[string]struct {
		limit   int
		cursor  string
		wantLim int
		want    int
		wantErr string
	}{
		"default limit from config": {
			wantLim: 3,
		},
		"requested limit clamped to config max": {
			limit:   10,
			wantLim: 5,
		},
		"integer cursor": {
			cursor:  "7",
			wantLim: 3,
			want:    7,
		},
		"opaque cursor": {
			cursor:  encodeSearchCursor(4),
			wantLim: 3,
			want:    4,
		},
		"beyond window": {
			cursor:  "1001",
			wantErr: "maximum window",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			filters, err := parseSearchFilters(protocol.SearchMsg{Query: "braw", Limit: test.limit, Cursor: test.cursor}, limits)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseSearchFilters err = %v, want %q", err, test.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseSearchFilters: %v", err)
			}

			if filters.offset != test.want {
				t.Fatalf("offset = %d, want %d", filters.offset, test.want)
			}

			if filters.limit != test.wantLim {
				t.Fatalf("limit = %d, want %d", filters.limit, test.wantLim)
			}
		})
	}
}

func TestBuildSearchSnippetUsesConfiguredShape(t *testing.T) {
	text := "braw canny dreich bothy thrawn strath"
	matches := findRuneMatches(text, lowerRunes("bothy"))

	snippet, ranges := buildSearchSnippet(text, matches, lowerRunes("bothy"), 12, 3)
	if snippet != "...ch bothy thr..." {
		t.Fatalf("snippet = %q, want configured 12-rune window around match", snippet)
	}

	if len(ranges) != 1 || ranges[0].Start != 6 || ranges[0].End != 11 {
		t.Fatalf("ranges = %+v, want match range shifted by ellipsis prefix", ranges)
	}
}

func TestSearchConversationsHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sm := newSearchTestSM(map[string]*SessionState{
		"braw": {
			ID: "braw", Name: "braw", Agent: "claude", AgentSessionID: "sess-braw",
			Status: StatusRunning, CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	_, err := sm.SearchConversations(ctx, protocol.SearchMsg{Query: "braw"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchConversations err = %v, want context.Canceled", err)
	}
}

type searchCancelOnSecondDoneContext struct {
	mu     sync.Mutex
	calls  int
	closed bool
	open   chan struct{}
	done   chan struct{}
}

func newSearchCancelOnSecondDoneContext() *searchCancelOnSecondDoneContext {
	done := make(chan struct{})

	return &searchCancelOnSecondDoneContext{
		open: make(chan struct{}),
		done: done,
	}
}

func (c *searchCancelOnSecondDoneContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *searchCancelOnSecondDoneContext) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.calls++
	if c.calls < 2 {
		return c.open
	}

	if !c.closed {
		close(c.done)
		c.closed = true
	}

	return c.done
}

func (c *searchCancelOnSecondDoneContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.calls >= 2 {
		return context.Canceled
	}

	return nil
}

func (c *searchCancelOnSecondDoneContext) Value(any) any {
	return nil
}

func TestSearchConversationsHonorsCancellationDuringColdParse(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)

	worktree := t.TempDir()
	writeSearchCodexTranscript(t, root, worktree, "sess-codex",
		`{"type":"response_item","timestamp":"2026-07-29T10:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"braw"}]}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"braw": {
			ID: "braw", Name: "braw", Agent: "codex", AgentSessionID: "sess-codex",
			WorktreePath: worktree, Status: StatusRunning,
			CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	ctx := newSearchCancelOnSecondDoneContext()

	_, err := sm.SearchConversations(ctx, protocol.SearchMsg{Query: "braw"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchConversations err = %v, want context.Canceled", err)
	}
}

func TestSearchConversationsBoundsColdTranscriptParse(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)

	worktree := t.TempDir()
	cfg := config.Default()
	cfg.Search.MaxSourceTurns = 2

	lines := make([]string, 0, cfg.Search.MaxSourceTurns+1)
	for i := 0; i < cfg.Search.MaxSourceTurns; i++ {
		lines = append(lines,
			`{"type":"response_item","timestamp":"2026-07-29T10:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"bounded braw"}]}}`,
		)
	}

	lines = append(lines,
		`{"type":"response_item","timestamp":"2026-07-29T10:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"beyond bound phrase"}]}}`,
	)

	writeSearchCodexTranscript(t, root, worktree, "sess-codex", lines...)

	sm := newSearchTestSMWithConfig(map[string]*SessionState{
		"canny": {
			ID: "canny", Name: "canny", Agent: "codex", AgentSessionID: "sess-codex",
			WorktreePath: worktree, Status: StatusRunning,
			CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	}, cfg)

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "beyond bound phrase"})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 0 {
		t.Fatalf("results = %+v, want none past turn bound", resp.Results)
	}

	if !resp.Truncated {
		t.Fatal("Truncated = false, want true for bounded parse")
	}
}

func TestSearchConversationsFilters(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	writeSearchClaudeTranscript(t, root, "sess-parent",
		`{"type":"user","uuid":"u1","timestamp":"2026-07-29T09:00:00Z","message":{"role":"user","content":"shared phrase from parent"}}`,
	)
	writeSearchClaudeTranscript(t, root, "sess-child",
		`{"type":"assistant","uuid":"a1","timestamp":"2026-07-29T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"shared phrase from child"}]}}`,
	)
	writeSearchClaudeTranscript(t, root, "sess-deleted",
		`{"type":"user","uuid":"u1","timestamp":"2026-07-29T11:00:00Z","message":{"role":"user","content":"shared phrase from deleted"}}`,
	)

	deletedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	sm := newSearchTestSM(map[string]*SessionState{
		"parent": {
			ID: "parent", Name: "parent", RepoPath: "/repo/croft", RepoName: "croft",
			Agent: "claude", AgentSessionID: "sess-parent", Status: StatusRunning,
			CreatedAt: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC),
		},
		"child": {
			ID: "child", Name: "child", ParentID: "parent", RepoPath: "/repo/croft", RepoName: "croft",
			Agent: "claude", AgentSessionID: "sess-child", Status: StatusStopped,
			CreatedAt: time.Date(2026, 7, 29, 8, 30, 0, 0, time.UTC),
		},
		"deleted": {
			ID: "deleted", Name: "deleted", RepoPath: "/repo/croft", RepoName: "croft",
			Agent: "claude", AgentSessionID: "sess-deleted", Status: StatusStopped, DeletedAt: &deletedAt,
			CreatedAt: time.Date(2026, 7, 29, 8, 45, 0, 0, time.UTC),
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{
		Query:              "shared phrase",
		SessionID:          "parent",
		IncludeDescendants: true,
		Kinds:              []string{"assistant"},
		State:              "stopped",
		Since:              "2026-07-29T09:30:00Z",
		Repo:               "croft",
	})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 1 || resp.Results[0].SessionID != "child" {
		t.Fatalf("filtered results = %+v, want stopped assistant child only", resp.Results)
	}

	resp, err = sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "deleted"})
	if err != nil {
		t.Fatalf("SearchConversations without deleted: %v", err)
	}

	if len(resp.Results) != 0 {
		t.Fatalf("soft-deleted session returned without opt-in: %+v", resp.Results)
	}

	resp, err = sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "deleted", IncludeDeleted: true})
	if err != nil {
		t.Fatalf("SearchConversations with deleted: %v", err)
	}

	if len(resp.Results) != 1 || resp.Results[0].SessionID != "deleted" {
		t.Fatalf("deleted opt-in results = %+v, want deleted session", resp.Results)
	}
}

func TestSearchConversationsIncludesMigratedSourceGeneration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	writeSearchClaudeTranscript(t, root, "sess-source",
		`{"type":"user","uuid":"u1","timestamp":"2026-07-29T10:00:00Z","message":{"role":"user","content":"migrated source phrase"}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"braw": {
			ID: "braw", Name: "braw", Agent: "codex", AgentSessionID: "sess-current",
			Status: StatusRunning, CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
			MigratedFrom: &MigrationInfo{
				Agent:          "claude",
				AgentSessionID: "sess-source",
				At:             time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC),
			},
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "source phrase", Agent: "claude"})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v, want migrated source result", resp.Results)
	}

	got := resp.Results[0]
	if got.SessionID != "braw" || got.Agent != "claude" || got.AgentSessionID != "sess-source" {
		t.Fatalf("result = %+v, want source generation metadata", got)
	}
}

func TestSearchConversationsKeepsNewestMatchesWithinBoundedWindow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	cfg := config.Default()
	cfg.Search.MaxWindow = 4
	limits := cfg.Search.Limits()

	bulkLines := make([]string, (limits.MaxWindow+1)*2+1)
	for i := range bulkLines {
		bulkLines[i] = fmt.Sprintf(
			`{"type":"user","uuid":"bulk-%d","timestamp":"2026-07-29T08:%02d:%02dZ","message":{"role":"user","content":"common phrase from braw %d"}}`,
			i, (i/60)%60, i%60, i,
		)
	}

	writeSearchClaudeTranscript(t, root, "sess-bulk", bulkLines...)
	writeSearchClaudeTranscript(t, root, "sess-latest",
		`{"type":"assistant","uuid":"latest","timestamp":"2026-07-29T13:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"common phrase from canny"}]}}`,
	)

	sm := newSearchTestSMWithConfig(map[string]*SessionState{
		"bulk": {
			ID: "bulk", Name: "bulk", Agent: "claude", AgentSessionID: "sess-bulk",
			Status: StatusRunning, CreatedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		},
		"latest": {
			ID: "latest", Name: "latest", Agent: "claude", AgentSessionID: "sess-latest",
			Status: StatusRunning, CreatedAt: time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC),
		},
	}, cfg)

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "common phrase", Limit: 1})
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}

	if len(resp.Results) != 1 || resp.Results[0].SessionID != "latest" {
		t.Fatalf("first result = %+v, want latest session despite bounded scan window", resp.Results)
	}

	if !resp.Truncated || resp.NextCursor == "" {
		t.Fatalf("pagination = truncated %v cursor %q, want bounded window cursor", resp.Truncated, resp.NextCursor)
	}
}

func TestSearchConversationsInvalidatesCacheOnAppend(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	path := writeSearchClaudeTranscript(t, root, "sess-braw",
		`{"type":"user","uuid":"u1","timestamp":"2026-07-29T10:00:00Z","message":{"role":"user","content":"first phrase"}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"braw": {
			ID: "braw", Name: "braw", Agent: "claude", AgentSessionID: "sess-braw",
			Status: StatusRunning, CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "second phrase"})
	if err != nil {
		t.Fatalf("initial search: %v", err)
	}

	if len(resp.Results) != 0 {
		t.Fatalf("initial results = %+v, want none", resp.Results)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.WriteString(`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-07-29T10:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"second phrase"}]}}` + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	resp, err = sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "second phrase"})
	if err != nil {
		t.Fatalf("second search: %v", err)
	}

	if len(resp.Results) != 1 || resp.Results[0].Kind != "assistant" {
		t.Fatalf("post-append results = %+v, want appended assistant match", resp.Results)
	}
}

func TestSearchConversationsSkipsHiddenReasoningAndKeepsUnicodeRanges(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	writeSearchClaudeTranscript(t, root, "sess-braw",
		`{"type":"assistant","uuid":"a1","timestamp":"2026-07-29T10:00:00Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret thrawn"},{"type":"text","text":"crème braw bothy"}]}}`,
	)

	sm := newSearchTestSM(map[string]*SessionState{
		"braw": {
			ID: "braw", Name: "braw", Agent: "claude", AgentSessionID: "sess-braw",
			Status: StatusRunning, CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		},
	})

	resp, err := sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "secret"})
	if err != nil {
		t.Fatalf("hidden search: %v", err)
	}

	if len(resp.Results) != 0 {
		t.Fatalf("hidden reasoning leaked into results: %+v", resp.Results)
	}

	resp, err = sm.SearchConversations(context.Background(), protocol.SearchMsg{Query: "braw"})
	if err != nil {
		t.Fatalf("unicode search: %v", err)
	}

	if len(resp.Results) != 1 {
		t.Fatalf("unicode results = %+v, want one", resp.Results)
	}

	got := resp.Results[0]
	if got.Snippet != "crème braw bothy" {
		t.Fatalf("snippet = %q, want full UTF-8 text", got.Snippet)
	}

	if len(got.Matches) != 1 || got.Matches[0].Start != 6 || got.Matches[0].End != 10 {
		t.Fatalf("matches = %+v, want rune range 6..10", got.Matches)
	}
}
