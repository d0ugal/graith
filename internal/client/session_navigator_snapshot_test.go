package client

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestRenderSessionNavigatorSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	selectedDetail := DefaultSelectedDetailConfig()
	selectedDetail.MinTerminalWidth = 120

	out, err := RenderSessionNavigatorSnapshot(SessionNavigatorSnapshotOptions{
		Sessions: []protocol.SessionInfo{
			{
				ID:           "orchestrator",
				Name:         "orchestrator",
				SystemKind:   "orchestrator",
				Status:       "running",
				AgentStatus:  "active",
				CreatedAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
				LastOutputAt: now.Add(-4 * time.Minute).Format(time.RFC3339),
			},
			{
				ID:              "dreich-session",
				ParentID:        "orchestrator",
				Name:            "dreich-session-with-a-long-name",
				Labels:          []string{"ci", "review-needed", "very-long-snapshot-label"},
				RepoName:        "graith",
				WorktreePath:    "/Users/dougalmatthews/src/graith/.worktrees/dreich-session",
				Branch:          "d0ugal/graith/session-navigator-preview",
				BaseBranch:      "main",
				Agent:           "codex",
				Model:           "gpt-5",
				Status:          "running",
				AgentStatus:     "thinking",
				CreatedAt:       now.Add(-90 * time.Minute).Format(time.RFC3339),
				LastAttachedAt:  now.Add(-15 * time.Minute).Format(time.RFC3339),
				StatusChangedAt: now.Add(-10 * time.Minute).Format(time.RFC3339),
				LastOutputAt:    now.Add(-30 * time.Second).Format(time.RFC3339),
				Dirty:           true,
				UnpushedCount:   2,
				ConfigStale:     true,
				Starred:         true,
				SummaryText:     "Implement deterministic Session Navigator screenshots with fake sessions and PR review state.",
				PullRequest: &protocol.PRInfo{
					Number:         1870,
					State:          "open",
					ReviewDecision: "review_required",
				},
				CI: &protocol.CIInfo{
					State:  "pending",
					Passed: 16,
					Total:  22,
				},
				Tokens: &protocol.TokenInfo{Total: 1361526},
			},
			{
				ID:           "bairn-session",
				ParentID:     "dreich-session",
				Name:         "bairn-session",
				RepoName:     "graith",
				Status:       "stopped",
				Agent:        "claude",
				CreatedAt:    now.Add(-3 * time.Hour).Format(time.RFC3339),
				LastOutputAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
				SummaryText:  "Stopped follow-up with faded output.",
				SummaryFaded: true,
			},
		},
		CurrentSessionID: "orchestrator",
		Profile:          "preview",
		State: SessionNavigatorState{
			View:      SessionNavigatorViewAll,
			SessionID: "dreich-session",
		},
		ShortcutKeys:   "123456789",
		SelectedDetail: &selectedDetail,
		Preview:        "go test ./internal/client\nok github.com/d0ugal/graith/internal/client 0.123s",
		Now:            now,
		Width:          240,
		Height:         40,
	})
	if err != nil {
		t.Fatalf("RenderSessionNavigatorSnapshot returned error: %v", err)
	}

	plain := ansi.Strip(out)
	for _, want := range []string{
		"Session Navigator",
		"graith/dreich-session-with-a-long-name",
		"Selected Session",
		"#1870 open CI:16/22",
		"Config:   restart to apply changes",
		"1h30m ago",
		"review-needed",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, plain)
		}
	}

	for i, line := range strings.Split(out, "\n") {
		if width := ansi.StringWidth(line); width > 240 {
			t.Fatalf("line %d width = %d, want <= 240:\n%s", i+1, width, line)
		}
	}
}

func TestRenderSessionNavigatorTerminalSnapshotIncludesStatusBar(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	out, err := RenderSessionNavigatorTerminalSnapshot(SessionNavigatorTerminalSnapshotOptions{
		Navigator: SessionNavigatorSnapshotOptions{
			Sessions: []protocol.SessionInfo{{
				ID:           "braw",
				Name:         "braw",
				RepoName:     "graith",
				Agent:        "codex",
				Status:       "running",
				AgentStatus:  "active",
				Branch:       "d0ugal/graith/session-nav-terminal-shots",
				CreatedAt:    now.Add(-1 * time.Hour).Format(time.RFC3339),
				LastOutputAt: now.Add(-1 * time.Minute).Format(time.RFC3339),
			}},
			CurrentSessionID: "braw",
			State:            SessionNavigatorState{View: SessionNavigatorViewAll, SessionID: "braw"},
			ShortcutKeys:     "123",
			Help:             DefaultSessionNavigatorHelp(),
			Now:              now,
			Width:            100,
			Height:           24,
		},
		StatusBar: SessionNavigatorStatusBarSnapshotOptions{
			Session: protocol.SessionInfo{
				ID:          "braw",
				Name:        "braw",
				Agent:       "codex",
				Status:      "running",
				AgentStatus: "active",
				Branch:      "d0ugal/graith/session-nav-terminal-shots",
			},
			Fleet:       protocol.FleetSummary{Total: 4, Active: 2, Ready: 1, Errored: 1},
			UnreadCount: 3,
			Position:    "bottom",
		},
	})
	if err != nil {
		t.Fatalf("RenderSessionNavigatorTerminalSnapshot returned error: %v", err)
	}

	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("terminal snapshot line count = %d, want 24", len(lines))
	}

	plain := ansi.Strip(out)
	for _, want := range []string{"Session Navigator", "braw", "codex", "session-nav-terminal-shots", "error", "active", "✉ 3"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("terminal snapshot missing %q:\n%s", want, plain)
		}
	}

	statusLine := lines[len(lines)-1]
	if width := ansi.StringWidth(statusLine); width != 100 {
		t.Fatalf("status line width = %d, want 100:\n%s", width, statusLine)
	}

	for i, line := range lines {
		if width := ansi.StringWidth(line); width > 100 {
			t.Fatalf("line %d width = %d, want <= 100:\n%s", i+1, width, line)
		}
	}
}

func TestRenderSessionNavigatorSnapshotRejectsInvalidSize(t *testing.T) {
	tests := map[string]SessionNavigatorSnapshotOptions{
		"missing width":  {Height: 24},
		"missing height": {Width: 80},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := RenderSessionNavigatorSnapshot(opts); err == nil {
				t.Fatal("RenderSessionNavigatorSnapshot returned nil error")
			}
		})
	}
}

func TestRenderSessionNavigatorSnapshotMatchesLiveModelView(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	sessions := []protocol.SessionInfo{
		{
			ID:           "braw-root",
			Name:         "root",
			Status:       "running",
			AgentStatus:  "active",
			CreatedAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
			LastOutputAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		{
			ID:           "canny-child",
			ParentID:     "braw-root",
			Name:         "canny-child",
			RepoName:     "graith",
			Agent:        "codex",
			Status:       "running",
			AgentStatus:  "thinking",
			CreatedAt:    now.Add(-1 * time.Hour).Format(time.RFC3339),
			LastOutputAt: now.Add(-30 * time.Second).Format(time.RFC3339),
			SummaryText:  "Shared model constructor keeps snapshots aligned with the live navigator.",
		},
		{
			ID:           "bothy-grandchild",
			ParentID:     "canny-child",
			Name:         "bothy-grandchild",
			RepoName:     "graith",
			Agent:        "claude",
			Status:       "stopped",
			CreatedAt:    now.Add(-45 * time.Minute).Format(time.RFC3339),
			LastOutputAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
			SummaryText:  "Hidden by the collapsed parent when options are forwarded correctly.",
		},
	}
	deletedSessions := []protocol.SessionInfo{
		{
			ID:              "dreich-deleted",
			Name:            "dreich-deleted",
			Status:          "stopped",
			DeletedAt:       now.Add(-10 * time.Minute).Format(time.RFC3339),
			DeleteExpiresAt: now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
			CreatedAt:       now.Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}
	state := SessionNavigatorState{View: SessionNavigatorViewAll, SessionID: "canny-child"}
	preview := "go test ./internal/client\nok github.com/d0ugal/graith/internal/client 0.123s"
	help := DefaultSessionNavigatorHelp()
	help.CompactActions = []string{"delete", "filter", "restart_menu", "quit"}
	selectedDetail := DefaultSelectedDetailConfig()
	selectedDetail.MinTerminalWidth = 100
	opts := SessionNavigatorSnapshotOptions{
		Sessions:         sessions,
		DeletedSessions:  deletedSessions,
		CurrentSessionID: "braw-root",
		Profile:          "preview",
		Collapsed:        map[string]bool{"canny-child": true},
		State:            state,
		RepoSuggestions:  []RepoSuggestion{{Name: "graith", Path: "/Users/dougalmatthews/src/graith"}},
		ShortcutKeys:     "123456789",
		Agents:           []string{"codex", "claude"},
		DefaultAgent:     "codex",
		Keys: SessionNavigatorKeys{
			DeleteSession: "D",
			ResumeSession: "Z",
			Search:        "ctrl+f",
			Cancel:        []string{"ctrl+q"},
		},
		Help:           help,
		SelectedDetail: &selectedDetail,
		Preview:        preview,
		Now:            now,
		HomeDir:        "/Users/dougalmatthews",
		Width:          120,
		Height:         30,
	}
	assertSnapshotOptionsFullyPopulated(t, opts)

	got, err := RenderSessionNavigatorSnapshot(opts)
	if err != nil {
		t.Fatalf("RenderSessionNavigatorSnapshot returned error: %v", err)
	}

	snapshotClockMu.Lock()
	defer snapshotClockMu.Unlock()

	restoreClock := installSnapshotClock(now)
	defer restoreClock()

	restoreHome := installSnapshotHomeDir("/Users/dougalmatthews")
	defer restoreHome()

	m := newSessionNavigatorModel(sessionNavigatorModelOptions{
		Sessions:         opts.Sessions,
		DeletedSessions:  opts.DeletedSessions,
		CurrentSessionID: opts.CurrentSessionID,
		FetchPreview: func(string) string {
			return opts.Preview
		},
		Profile:         opts.Profile,
		Collapsed:       opts.Collapsed,
		State:           opts.State,
		RepoSuggestions: opts.RepoSuggestions,
		ShortcutKeys:    opts.ShortcutKeys,
		Agents:          opts.Agents,
		DefaultAgent:    opts.DefaultAgent,
		Keys:            opts.Keys,
		Help:            opts.Help,
		SelectedDetail:  opts.SelectedDetail,
	})
	m.previewContent = opts.Preview

	updated, _ := m.Update(tea.WindowSizeMsg{Width: opts.Width, Height: opts.Height})

	rendered, ok := updated.(*overlayModel)
	if !ok {
		t.Fatalf("live model update returned %T, want *overlayModel", updated)
	}

	want := rendered.View().Content

	if got != want {
		t.Fatal("snapshot output drifted from the configured live Session Navigator model view")
	}
}

func assertSnapshotOptionsFullyPopulated(t *testing.T, opts SessionNavigatorSnapshotOptions) {
	t.Helper()

	value := reflect.ValueOf(opts)

	typ := value.Type()
	for i := range typ.NumField() {
		field := typ.Field(i)
		if value.Field(i).IsZero() {
			t.Fatalf("test setup left SessionNavigatorSnapshotOptions.%s zero; populate every field so forwarding drift is observable", field.Name)
		}
	}
}

func TestSessionNavigatorSnapshotOptionsCoverRenderAffectingLiveOptions(t *testing.T) {
	t.Parallel()

	snapshotFields := exportedStructFields(reflect.TypeOf(SessionNavigatorSnapshotOptions{}))
	snapshotAliases := map[string]string{
		"FetchPreview":   "Preview",
		"RefreshDeleted": "DeletedSessions",
	}
	nonRenderingLiveFields := map[string]bool{
		"DeleteSession":   true,
		"RefreshSessions": true,
		"RestartSession":  true,
		"RestoreSession":  true,
		"StopSession":     true,
		"ToggleStar":      true,
	}

	liveType := reflect.TypeOf(RunSessionNavigatorOpts{})
	for i := range liveType.NumField() {
		field := liveType.Field(i).Name
		if nonRenderingLiveFields[field] {
			continue
		}

		snapshotField := field
		if alias, ok := snapshotAliases[field]; ok {
			snapshotField = alias
		}

		if !snapshotFields[snapshotField] {
			t.Fatalf("RunSessionNavigatorOpts.%s can affect rendering; add SessionNavigatorSnapshotOptions.%s or explicitly classify it as non-rendering", field, snapshotField)
		}
	}
}

func TestRenderSessionNavigatorSnapshotUsesCustomKeys(t *testing.T) {
	t.Parallel()

	help := DefaultSessionNavigatorHelp()
	help.CompactActions = []string{"delete", "filter", "restart_menu", "quit"}

	out, err := RenderSessionNavigatorSnapshot(SessionNavigatorSnapshotOptions{
		Sessions: []protocol.SessionInfo{{
			ID:     "braw",
			Name:   "braw",
			Status: "running",
		}},
		CurrentSessionID: "braw",
		State:            SessionNavigatorState{View: SessionNavigatorViewAll, SessionID: "braw"},
		ShortcutKeys:     "123",
		Keys: SessionNavigatorKeys{
			DeleteSession: "D",
			ResumeSession: "Z",
			Search:        "ctrl+f",
			Cancel:        []string{"ctrl+q"},
		},
		Help:   help,
		Width:  160,
		Height: 24,
	})
	if err != nil {
		t.Fatalf("RenderSessionNavigatorSnapshot returned error: %v", err)
	}

	plain := ansi.Strip(out)
	for _, want := range []string{"D delete", "ctrl+f filter", "Z restart menu", "ctrl+q quit"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("snapshot missing custom key help %q:\n%s", want, plain)
		}
	}
}

func exportedStructFields(typ reflect.Type) map[string]bool {
	fields := make(map[string]bool, typ.NumField())

	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.IsExported() {
			fields[field.Name] = true
		}
	}

	return fields
}
