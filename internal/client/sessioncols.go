package client

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

// SessionColumn is the single source of truth for one column shown in the
// session picker (the TUI overlay) and/or the `gr ls` table (the CLI). A column
// is defined exactly once here, so adding a new column makes it appear in both
// surfaces — flip ShowCLI/ShowTUI to control where it shows up and the two
// views stay in sync automatically.
//
// The two surfaces render very differently: the CLI aligns plain-text cells
// (optionally colorised) by visible width, while the TUI uses lipgloss
// fixed-width cells with unicode glyphs and per-cell styling. So each column
// carries a value formatter (and styling) for each surface rather than a single
// shared string.
//
// The NAME/Session column is deliberately NOT part of this registry: both
// surfaces render it specially (star prefix and tree indentation in the CLI,
// collapse indicators and tree prefixes in the TUI), so it is handled inline by
// each renderer. This registry covers only the trailing columns.
type SessionColumn struct {
	// Key is a stable identifier used for TUI width lookups.
	Key string
	// Header is the title-case header text. The CLI upper-cases it ("STATUS");
	// the TUI uses it verbatim ("Status").
	Header string

	// ShowCLI includes the column in `gr ls`.
	ShowCLI bool
	// ShowTUI includes the column in the TUI session picker.
	ShowTUI bool
	// Wide restricts a CLI column to `gr ls --wide`.
	Wide bool

	// MinWidth is the minimum TUI column width; MaxWidth caps it (0 = no cap).
	MinWidth int
	MaxWidth int

	// CLIValue returns the plain-text cell for `gr ls`. now is the reference
	// time for age/attached columns; other columns ignore it.
	CLIValue func(s protocol.SessionInfo, now time.Time) string
	// CLIColor returns the foreground colour for the CLI cell, or nil for none.
	// The list command applies it via colorize; the renderer measures cells with
	// ansi.StringWidth so the colour escapes don't disturb column alignment.
	CLIColor func(s protocol.SessionInfo) color.Color

	// TUIValue returns the cell text for the TUI picker. It may include "—"
	// placeholders and unicode status glyphs.
	TUIValue func(s protocol.SessionInfo) string
	// TUIStyle returns the lipgloss style (foreground/bold) for the TUI cell.
	// The zero style renders text unchanged.
	TUIStyle func(s protocol.SessionInfo) lipgloss.Style
}

// SessionColumns returns every trailing column in a single display order. Each
// surface filters by ShowCLI/ShowTUI (the CLI additionally hides Wide columns
// unless --wide is set) and renders the survivors in this order. The order is
// chosen so that the two filtered subsets each match their historical layout:
//
//	CLI: Repo Agent Status Activity [Model Branch] Git PR Review Age [Attached]
//	TUI: Status Summary Git PR Review Output
func SessionColumns() []SessionColumn {
	return []SessionColumn{
		{
			Key: "repo", Header: "Repo", ShowCLI: true,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return s.RepoName },
		},
		{
			Key: "agent", Header: "Agent", ShowCLI: true,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return s.Agent },
		},
		{
			Key: "status", Header: "Status", ShowCLI: true, ShowTUI: true, MinWidth: 6,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return s.Status },
			CLIColor: func(s protocol.SessionInfo) color.Color { return StatusColor(s.Status) },
			TUIValue: displayStatus,
			TUIStyle: statusStyle,
		},
		{
			Key: "activity", Header: "Activity", ShowCLI: true,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliActivity(s) },
			CLIColor: func(s protocol.SessionInfo) color.Color { return AgentStatusColor(s.AgentStatus) },
		},
		{
			Key: "summary", Header: "Summary", ShowTUI: true, MinWidth: 7, MaxWidth: maxSummaryWidth,
			TUIValue: displaySummary,
			TUIStyle: summaryStyle,
		},
		{
			Key: "model", Header: "Model", ShowCLI: true, Wide: true,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return s.Model },
		},
		{
			Key: "branch", Header: "Branch", ShowCLI: true, Wide: true,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliBranch(s) },
		},
		{
			Key: "git", Header: "Git", ShowCLI: true, ShowTUI: true, MinWidth: 3,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliGit(s) },
			TUIValue: tuiGit,
			TUIStyle: tuiGitStyle,
		},
		{
			Key: "pr", Header: "PR", ShowCLI: true, ShowTUI: true, MinWidth: 2,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliPR(s) },
			TUIValue: displayPR,
			TUIStyle: func(s protocol.SessionInfo) lipgloss.Style {
				return lipgloss.NewStyle().Foreground(prColor(s))
			},
		},
		{
			// Review decision is its own column so it carries a colour driven by the
			// decision (approved=green, changes_requested=red, review_required=dim)
			// rather than inheriting the PR/CI token colour — a review_required "r"
			// under a green build must not itself read as green.
			Key: "review", Header: "Review", ShowCLI: true, ShowTUI: true, MinWidth: 1,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliReview(s) },
			CLIColor: reviewColor,
			TUIValue: displayReview,
			TUIStyle: func(s protocol.SessionInfo) lipgloss.Style {
				if c := reviewColor(s); c != nil {
					return lipgloss.NewStyle().Foreground(c)
				}

				return lipgloss.NewStyle()
			},
		},
		{
			Key: "tokens", Header: "Tokens", ShowCLI: true, Wide: true,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliTokens(s) },
		},
		{
			// Done/total for the session's subtree task list (issue #591). Wide so
			// it stays out of the default table but appears with `gr list --wide`.
			Key: "todo", Header: "Todo", ShowCLI: true, Wide: true,
			CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliTodo(s) },
		},
		{
			Key: "age", Header: "Age", ShowCLI: true,
			CLIValue: cliAge,
		},
		{
			Key: "output", Header: "Output", ShowTUI: true, MinWidth: 6,
			TUIValue: displayLastOutput,
			TUIStyle: func(protocol.SessionInfo) lipgloss.Style {
				return lipgloss.NewStyle().Foreground(colorDim)
			},
		},
		{
			Key: "attached", Header: "Attached", ShowCLI: true, Wide: true,
			CLIValue: cliAttached,
		},
	}
}

// displayStatus is the merged status shown in the TUI status column: the agent
// status takes over while the session is running (e.g. "thinking", "approval").
func displayStatus(s protocol.SessionInfo) string {
	if s.AgentStatus != "" && s.Status == "running" {
		return s.AgentStatus
	}

	return s.Status
}

// statusStyle colours the TUI status cell by its meaning; approval is bolded so
// it stands out as needing attention.
func statusStyle(s protocol.SessionInfo) lipgloss.Style {
	switch displayStatus(s) {
	case "active", "running":
		return lipgloss.NewStyle().Foreground(colorGreen)
	case "approval":
		return lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	case "ready":
		return lipgloss.NewStyle().Foreground(colorBlue)
	case "errored":
		return lipgloss.NewStyle().Foreground(colorRed)
	default:
		return lipgloss.NewStyle().Foreground(colorDim)
	}
}

// summaryStyle dims the TUI summary cell once the summary has gone stale.
func summaryStyle(s protocol.SessionInfo) lipgloss.Style {
	if s.SummaryFaded {
		return lipgloss.NewStyle().Foreground(colorDim)
	}

	return lipgloss.NewStyle()
}

// tuiGit is the TUI git cell: a dash for mirror sessions (no per-session git
// state), otherwise the compact dirty/ahead form.
func tuiGit(s protocol.SessionInfo) string {
	if s.Mirror {
		return "—"
	}

	return displayGit(s.Dirty, s.UnpushedCount)
}

// tuiGitStyle dims the git cell when there is nothing noteworthy (shared
// worktree or a clean tree) and leaves dirty/ahead states at default colour.
func tuiGitStyle(s protocol.SessionInfo) lipgloss.Style {
	if s.Mirror || tuiGit(s) == "clean" {
		return lipgloss.NewStyle().Foreground(colorDim)
	}

	return lipgloss.NewStyle()
}

// cliActivity is the plain-text agent-activity cell for `gr ls`: empty unless
// the session is running, with the approval glyph and active-tool annotation.
func cliActivity(s protocol.SessionInfo) string {
	if s.Status != "running" || s.AgentStatus == "" {
		return ""
	}

	switch {
	case s.AgentStatus == "approval":
		return "⚠ approval"
	case s.AgentStatus == "active" && s.ToolName != "":
		return fmt.Sprintf("active (%s)", s.ToolName)
	default:
		return s.AgentStatus
	}
}

// cliBranch is the plain-text branch cell for `gr ls`: the leaf of a
// three-segment worktree branch, the raw branch otherwise, or an in-place
// marker when there is no branch.
func cliBranch(s protocol.SessionInfo) string {
	if s.Branch != "" {
		if parts := strings.SplitN(s.Branch, "/", 3); len(parts) == 3 {
			return parts[2]
		}

		return s.Branch
	}

	if s.InPlace {
		return "(in-place)"
	}

	return ""
}

// cliPR is the plain-text PR/CI cell for `gr ls`, e.g. "#42 open",
// "#7 open conflict", "#1 open CI:ok", "#1 open CI:16/22" (running),
// "#1 open CI:19/22 1✗" (failing). While CI runs/fails the passed/total count
// replaces the bare "CI:…"/"CI:fail" badge; it falls back when no count is
// available. The review decision is a separate column (cliReview) so it can be
// coloured independently of the CI/conflict signal.
func cliPR(s protocol.SessionInfo) string {
	if s.PullRequest == nil {
		return ""
	}

	pr := s.PullRequest

	out := fmt.Sprintf("#%d %s", pr.Number, pr.State)

	// A merged/closed PR is terminal: its conflict/CI badges are stale
	// (resolvePR keeps the last-known values once a PR leaves open/draft), so
	// suppress them and show only the state — mirroring displayPR and prColor
	// (issue #773).
	if pr.State == "merged" || pr.State == "closed" {
		return out
	}

	if pr.Conflicting {
		out += " conflict"
	}

	if s.CI != nil {
		counts, haveCounts := ciCounts(s.CI)
		switch s.CI.State {
		case "passing":
			out += " CI:ok"
		case "failing":
			if haveCounts && len(s.CI.FailingChecks) > 0 {
				out += fmt.Sprintf(" CI:%s %d✗", counts, len(s.CI.FailingChecks))
			} else {
				out += " CI:fail"
			}
		case "pending":
			if haveCounts {
				out += " CI:" + counts
			} else {
				out += " CI:…"
			}
		}
	}

	return out
}

// cliReview is the plain-text review-decision cell for `gr ls`: "approved",
// "changes", "needed", or "" when there is no live decision. Coloured by
// reviewColor so review_required reads dim, not green.
func cliReview(s protocol.SessionInfo) string {
	switch reviewActiveDecision(s) {
	case "approved":
		return "approved"
	case "changes_requested":
		return "changes"
	case "review_required":
		return "needed"
	default:
		return ""
	}
}

// cliGit is the plain-text git cell for `gr ls`, e.g. "dirty", "3 ahead",
// "dirty, 2 ahead", or "" when clean.
func cliGit(s protocol.SessionInfo) string {
	out := ""
	if s.Dirty {
		out = "dirty"
	}

	if s.UnpushedCount > 0 {
		if out != "" {
			out += ", "
		}

		out += fmt.Sprintf("%d ahead", s.UnpushedCount)
	}

	return out
}

// cliTodo renders the session's subtree todo progress as done/total, or "" when
// the session has no tracked items.
func cliTodo(s protocol.SessionInfo) string {
	if s.TodoTotal == 0 {
		return ""
	}

	return fmt.Sprintf("%d/%d", s.TodoDone, s.TodoTotal)
}

// cliTokens is the plain-text token cell for `gr ls --wide`: the compact total,
// an empty string when unknown (never observed / unsupported agent), and a
// trailing "~" when the count is approximate (parse conflicts / no-id records).
func cliTokens(s protocol.SessionInfo) string {
	if s.Tokens == nil {
		return ""
	}

	out := CompactCount(s.Tokens.Total)
	if s.Tokens.Degraded {
		out += "~"
	}

	return out
}

// CompactCount renders a token count compactly: 0, 3.4k, 847k, 1.2M, 5B. It
// keeps one decimal below 10 of a unit (1.2M) and drops it above (12M).
func CompactCount(n int64) string {
	if n < 0 {
		return "0"
	}

	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return compactUnit(n, 1000, "k")
	case n < 1_000_000_000:
		return compactUnit(n, 1_000_000, "M")
	default:
		return compactUnit(n, 1_000_000_000, "B")
	}
}

func compactUnit(n, unit int64, suffix string) string {
	whole := n / unit
	if whole < 10 {
		frac := (n % unit) * 10 / unit
		if frac > 0 {
			return fmt.Sprintf("%d.%d%s", whole, frac, suffix)
		}
	}

	return fmt.Sprintf("%d%s", whole, suffix)
}

// cliAge is the plain-text age cell for `gr ls`, derived from the created-at
// timestamp.
func cliAge(s protocol.SessionInfo, now time.Time) string {
	if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
		return ShortDuration(now.Sub(t))
	}

	return ""
}

// cliAttached is the plain-text last-attached cell for `gr ls`, e.g. "5m ago".
func cliAttached(s protocol.SessionInfo, now time.Time) string {
	if s.LastAttachedAt != "" {
		if t, err := time.Parse(time.RFC3339, s.LastAttachedAt); err == nil {
			return ShortDuration(now.Sub(t)) + " ago"
		}
	}

	return ""
}
