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

	wantCLI := []string{"repo", "agent", "status", "activity", "model", "branch", "git", "pr", "review", "tokens", "age", "attached"}
	wantTUI := []string{"status", "summary", "git", "pr", "review", "output"}

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
		// The review decision is now a separate column (cliReview); the PR cell must
		// NOT carry it, so CI/conflict colour never bleeds onto the review indicator.
		{"review not in PR cell (approved)", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 6, State: "open", ReviewDecision: "approved"}}, "#6 open"},
		{"review not in PR cell (changes)", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 7, State: "open", ReviewDecision: "changes_requested"}}, "#7 open"},
		{"review not in PR cell (required)", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 8, State: "draft", ReviewDecision: "review_required"}}, "#8 draft"},
		{"CI shown, review omitted", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 9, State: "open", ReviewDecision: "approved"}, CI: &protocol.CIInfo{State: "passing"}}, "#9 open CI:ok"},
		{"conflict shown, review omitted", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 11, State: "open", Conflicting: true, ReviewDecision: "changes_requested"}}, "#11 open conflict"},
		{"merged suppresses stale CI", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 12, State: "merged", ReviewDecision: "approved"}, CI: &protocol.CIInfo{State: "passing"}}, "#12 merged"},
		{"closed", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 13, State: "closed", ReviewDecision: "changes_requested"}}, "#13 closed"},
		// Counts: while CI runs/fails, show passed/total progress in place of the
		// bare CI:… / CI:fail badge, falling back when no count is available.
		{"CI pending with counts", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 14, State: "open"}, CI: &protocol.CIInfo{State: "pending", Passed: 16, Total: 22}}, "#14 open CI:16/22"},
		{"CI failing with counts", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 15, State: "open"}, CI: &protocol.CIInfo{State: "failing", FailingChecks: []string{"build"}, Passed: 19, Total: 22}}, "#15 open CI:19/22 1✗"},
		{"CI failing counts but no names falls back", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 16, State: "open"}, CI: &protocol.CIInfo{State: "failing", Passed: 19, Total: 22}}, "#16 open CI:fail"},
		{"CI pending no counts falls back", protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 17, State: "open"}, CI: &protocol.CIInfo{State: "pending"}}, "#17 open CI:…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliPR(tt.in); got != tt.want {
				t.Errorf("cliPR = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReviewCellValues covers both review-column value formatters together — the
// CLI text (cliReview) and the TUI glyph (displayReview) — including the
// unknown-decision case and merged/closed suppression of a stale decision.
func TestReviewCellValues(t *testing.T) {
	pr := func(state, decision string) protocol.SessionInfo {
		return protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 1, State: state, ReviewDecision: decision}}
	}

	cases := []struct {
		in       protocol.SessionInfo
		cli      string
		tuiGlyph string
	}{
		{protocol.SessionInfo{}, "", "—"},
		{pr("open", ""), "", "—"},
		{pr("open", "approved"), "approved", "a"},
		{pr("open", "changes_requested"), "changes", "c"},
		{pr("draft", "review_required"), "needed", "r"},
		{pr("open", "dismissed"), "", "—"},
		{pr("merged", "approved"), "", "—"},
		{pr("closed", "changes_requested"), "", "—"},
	}

	for _, c := range cases {
		if got := cliReview(c.in); got != c.cli {
			t.Errorf("cliReview(%+v) = %q, want %q", c.in.PullRequest, got, c.cli)
		}

		if got := displayReview(c.in); got != c.tuiGlyph {
			t.Errorf("displayReview(%+v) = %q, want %q", c.in.PullRequest, got, c.tuiGlyph)
		}
	}
}

// TestReviewColor pins the decision→colour mapping — the whole point of the split
// is that review_required is dim/grey, NOT the green a passing-CI PR cell uses.
func TestReviewColor(t *testing.T) {
	pr := func(state, decision string) protocol.SessionInfo {
		return protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 1, State: state, ReviewDecision: decision}}
	}

	if got := reviewColor(pr("open", "")); got != nil {
		t.Errorf("no decision should have no colour, got %v", got)
	}

	if got := reviewColor(pr("open", "approved")); got != colorGreen {
		t.Errorf("approved should be green, got %v", got)
	}

	if got := reviewColor(pr("open", "changes_requested")); got != colorRed {
		t.Errorf("changes_requested should be red, got %v", got)
	}

	// The bug this split fixes: review_required must be dim, not the green a
	// passing-CI PR cell would otherwise lend it.
	if got := reviewColor(pr("open", "review_required")); got != colorDim {
		t.Errorf("review_required should be dim, got %v", got)
	}

	if got := reviewColor(pr("merged", "approved")); got != nil {
		t.Errorf("merged should suppress the stale review colour, got %v", got)
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

	// status="active"(6) summary="fixing bothy"(12) git="M ↑2"(4) pr="#42 ✓"(5)
	// review="—"(1) — no review decision on this PR, so the placeholder glyph.
	want := "  active  fixing bothy  M ↑2  #42 ✓  —"
	if got := deterministic.String(); got != want {
		t.Errorf("TUI column cells mismatch:\n got %q\nwant %q", got, want)
	}

	if !sawANSI {
		t.Error("expected ANSI styling in rendered TUI cells")
	}
}
