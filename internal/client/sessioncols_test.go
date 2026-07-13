package client

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

// TestSessionColumnsOrder pins the single canonical column order and the
// per-surface subsets so the CLI and TUI layouts stay in sync.
func TestSessionColumnsOrder(t *testing.T) {
	var cli, tui []string

	for _, c := range SessionColumns() {
		if c.ShowCLI {
			cli = append(cli, c.Key)
		}

		if c.ShowTUI {
			tui = append(tui, c.Key)
		}
	}

	wantCLI := []string{"repo", "agent", "status", "activity", "model", "branch", "git", "pr", "tokens", "age", "attached"}
	wantTUI := []string{"status", "summary", "git", "pr", "output"}

	if join(cli) != join(wantCLI) {
		t.Errorf("CLI columns = %v, want %v", cli, wantCLI)
	}

	if join(tui) != join(wantTUI) {
		t.Errorf("TUI columns = %v, want %v", tui, wantTUI)
	}
}

// TestSessionColumnsFormattersPresent guards against a column missing the
// formatter it needs for a surface it claims to support.
func TestSessionColumnsFormattersPresent(t *testing.T) {
	for _, c := range SessionColumns() {
		if c.ShowCLI && c.CLIValue == nil {
			t.Errorf("column %q is ShowCLI but has no CLIValue", c.Key)
		}

		if c.ShowTUI && (c.TUIValue == nil || c.TUIStyle == nil) {
			t.Errorf("column %q is ShowTUI but missing TUIValue/TUIStyle", c.Key)
		}
	}
}

func TestCliActivity(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{"not running clears activity", protocol.SessionInfo{Status: "stopped", AgentStatus: "active"}, ""},
		{"approval gets glyph", protocol.SessionInfo{Status: "running", AgentStatus: "approval"}, "⚠ approval"},
		{"active with tool annotated", protocol.SessionInfo{Status: "running", AgentStatus: "active", ToolName: "Bash"}, "active (Bash)"},
		{"active without tool passes through", protocol.SessionInfo{Status: "running", AgentStatus: "active"}, "active"},
		{"idle passes through", protocol.SessionInfo{Status: "running", AgentStatus: "idle"}, "idle"},
		{"empty agent status", protocol.SessionInfo{Status: "running"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliActivity(tt.in); got != tt.want {
				t.Errorf("cliActivity = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCliBranch(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{"three-part strips to leaf", protocol.SessionInfo{Branch: "d0ugal/graith/fix-overlay"}, "fix-overlay"},
		{"leaf keeps deeper slashes", protocol.SessionInfo{Branch: "user/repo/feature/nested"}, "feature/nested"},
		{"two-part intact", protocol.SessionInfo{Branch: "origin/main"}, "origin/main"},
		{"in-place marker", protocol.SessionInfo{InPlace: true}, "(in-place)"},
		{"empty not in-place", protocol.SessionInfo{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliBranch(tt.in); got != tt.want {
				t.Errorf("cliBranch = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCliPR(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{"no PR", protocol.SessionInfo{}, ""},
		{"open PR", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 42, State: "open"}}, "#42 open"},
		{"conflicting PR", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 7, State: "open", Conflicting: true}}, "#7 open conflict"},
		{"CI passing", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 1, State: "open"}, CI: &protocol.CIInfo{State: "passing"}}, "#1 open CI:ok"},
		{"CI failing", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 2, State: "draft"}, CI: &protocol.CIInfo{State: "failing"}}, "#2 draft CI:fail"},
		{"CI pending", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 3, State: "open"}, CI: &protocol.CIInfo{State: "pending"}}, "#3 open CI:…"},
		{"CI empty adds nothing", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 4, State: "merged"}, CI: &protocol.CIInfo{State: ""}}, "#4 merged"},
		{"conflict and CI combine", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 5, State: "open", Conflicting: true}, CI: &protocol.CIInfo{State: "failing"}}, "#5 open conflict CI:fail"},
		{"review approved", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 6, State: "open", ReviewDecision: "approved"}}, "#6 open review:ok"},
		{"review changes requested", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 7, State: "open", ReviewDecision: "changes_requested"}}, "#7 open review:changes"},
		{"review required", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 8, State: "draft", ReviewDecision: "review_required"}}, "#8 draft review:needed"},
		{"CI and review combine", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 9, State: "open", ReviewDecision: "approved"}, CI: &protocol.CIInfo{State: "passing"}}, "#9 open CI:ok review:ok"},
		{"unknown review decision ignored", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 10, State: "open", ReviewDecision: "dismissed"}}, "#10 open"},
		{"conflict and review combine", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 11, State: "open", Conflicting: true, ReviewDecision: "changes_requested"}}, "#11 open conflict review:changes"},
		{"merged suppresses stale review and CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 12, State: "merged", ReviewDecision: "approved"}, CI: &protocol.CIInfo{State: "passing"}}, "#12 merged"},
		{"closed suppresses stale review", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 13, State: "closed", ReviewDecision: "changes_requested"}}, "#13 closed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliPR(tt.in); got != tt.want {
				t.Errorf("cliPR = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCliGit(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{"clean", protocol.SessionInfo{}, ""},
		{"dirty only", protocol.SessionInfo{Dirty: true}, "dirty"},
		{"ahead only", protocol.SessionInfo{UnpushedCount: 3}, "3 ahead"},
		{"dirty and ahead", protocol.SessionInfo{Dirty: true, UnpushedCount: 2}, "dirty, 2 ahead"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliGit(tt.in); got != tt.want {
				t.Errorf("cliGit = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCliAge(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	created := now.Add(-90 * time.Minute).Format(time.RFC3339)
	if got := cliAge(protocol.SessionInfo{CreatedAt: created}, now); got == "" {
		t.Error("expected non-empty age for valid timestamp")
	}

	if got := cliAge(protocol.SessionInfo{CreatedAt: "not-a-time"}, now); got != "" {
		t.Errorf("expected empty age for invalid timestamp, got %q", got)
	}

	if got := cliAge(protocol.SessionInfo{}, now); got != "" {
		t.Errorf("expected empty age for missing timestamp, got %q", got)
	}
}

func TestCliAttached(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	attached := now.Add(-5 * time.Minute).Format(time.RFC3339)
	if got := cliAttached(protocol.SessionInfo{LastAttachedAt: attached}, now); got != "5m ago" {
		t.Errorf("cliAttached = %q, want %q", got, "5m ago")
	}

	if got := cliAttached(protocol.SessionInfo{}, now); got != "" {
		t.Errorf("expected empty for never-attached, got %q", got)
	}

	if got := cliAttached(protocol.SessionInfo{LastAttachedAt: "bogus"}, now); got != "" {
		t.Errorf("expected empty for invalid timestamp, got %q", got)
	}
}

// TestDisplayStatus checks the agent-status override while running.
func TestDisplayStatus(t *testing.T) {
	if got := displayStatus(protocol.SessionInfo{Status: "running", AgentStatus: "thinking"}); got != "thinking" {
		t.Errorf("running override = %q, want thinking", got)
	}

	if got := displayStatus(protocol.SessionInfo{Status: "stopped", AgentStatus: "thinking"}); got != "stopped" {
		t.Errorf("non-running should not override = %q, want stopped", got)
	}
}

// TestTuiGitAndStyle covers the mirror dash, the clean/dim path, and
// the dirty default-colour path.
func TestTuiGitAndStyle(t *testing.T) {
	shared := protocol.SessionInfo{Mirror: true, Dirty: true}
	if got := tuiGit(shared); got != "—" {
		t.Errorf("mirror git = %q, want —", got)
	}

	clean := protocol.SessionInfo{}
	if got := tuiGit(clean); got != "clean" {
		t.Errorf("clean git = %q, want clean", got)
	}

	// Dirty gets the default (no-foreground) style; clean/shared are dimmed.
	if tuiGitStyle(protocol.SessionInfo{Dirty: true}).GetForeground() == colorDim {
		t.Error("dirty git should not be dimmed")
	}

	if tuiGitStyle(clean).GetForeground() != colorDim {
		t.Error("clean git should be dimmed")
	}

	if tuiGitStyle(shared).GetForeground() != colorDim {
		t.Error("shared git should be dimmed")
	}
}

// TestStatusStyle checks the approval styling is bold and red.
func TestStatusStyle(t *testing.T) {
	st := statusStyle(protocol.SessionInfo{Status: "running", AgentStatus: "approval"})
	if st.GetForeground() != colorRed || !st.GetBold() {
		t.Errorf("approval style = fg %v bold %v, want red bold", st.GetForeground(), st.GetBold())
	}

	if statusStyle(protocol.SessionInfo{Status: "ready"}).GetForeground() != colorBlue {
		t.Error("ready should be blue")
	}
}

func join(ss []string) string {
	out := ""

	for i, s := range ss {
		if i > 0 {
			out += ","
		}

		out += s
	}

	return out
}

// TestTUIColumnCellsGolden pins the exact ANSI-stripped bytes of the TUI
// trailing column cells (value + width padding + separators), reproducing the
// per-row loop in compactDelegate.Render. This is the byte-for-byte guard the
// refactor needs: any drift in a column's value, order, or width padding fails
// here. It also confirms coloured cells still carry ANSI styling.
func TestTUIColumnCellsGolden(t *testing.T) {
	created := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Add(-90 * time.Minute).Format(time.RFC3339)
	s := protocol.SessionInfo{
		ID: "s1", Name: "braw", RepoName: "croft",
		Status: "running", AgentStatus: "active",
		Dirty: true, UnpushedCount: 2,
		SummaryText:  "fixing bothy",
		PullRequest:  &protocol.PRInfo{Number: 42, State: "open"},
		CI:           &protocol.CIInfo{State: "passing"},
		LastOutputAt: created, CreatedAt: created,
	}

	cols := computeColumnWidths([]protocol.SessionInfo{s}, "")

	// Reproduce Render's trailing loop: "  " separator then the styled cell.
	// For every column, styling must add ONLY ANSI — the ANSI-stripped cell must
	// equal the plain padded value (this also guards the tab-conversion edge,
	// since lipgloss.Render expands tabs). Concatenate the deterministic columns
	// for an exact golden; the output column is time-based (displayLastOutput
	// uses time.Since) so only its width is pinned.
	var deterministic strings.Builder

	var sawANSI bool

	for _, c := range tuiColumns() {
		w := cols.col(c.Key)
		padded := pad(c.TUIValue(s), w)
		cell := c.TUIStyle(s).Render(padded)

		if strings.Contains(cell, "\x1b[") {
			sawANSI = true
		}

		if stripped := ansi.Strip(cell); stripped != padded {
			t.Errorf("column %q: stripped cell %q != padded value %q", c.Key, stripped, padded)
		}

		if c.Key == "output" {
			if lipgloss.Width(ansi.Strip(cell)) != w {
				t.Errorf("output cell width = %d, want %d", lipgloss.Width(ansi.Strip(cell)), w)
			}

			continue
		}

		deterministic.WriteString("  ")
		deterministic.WriteString(ansi.Strip(cell))
	}

	// status="active"(6) summary="fixing bothy"(12) git="M ↑2"(4) pr="#42 ✓"(5).
	want := "  active  fixing bothy  M ↑2  #42 ✓"
	if got := deterministic.String(); got != want {
		t.Errorf("TUI column cells mismatch:\n got %q\nwant %q", got, want)
	}

	if !sawANSI {
		t.Error("expected ANSI styling in rendered TUI cells")
	}
}
