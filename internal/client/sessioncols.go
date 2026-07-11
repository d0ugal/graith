package client

import (
	"fmt"
	"image/color"
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
// The two surfaces render very differently: the CLI uses a tabwriter with
// plain-text cells (optionally colorised), while the TUI uses lipgloss
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
	// The list command applies it via colorize (which brackets the escapes for
	// tabwriter alignment).
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
//	CLI: Repo Agent Status Activity [Model Branch] Git PR Age [Attached]
//	TUI: Status Summary Git PR Output
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
// "#7 open conflict", "#1 open CI:ok".
func cliPR(s protocol.SessionInfo) string {
	if s.PullRequest == nil {
		return ""
	}

	pr := s.PullRequest

	out := fmt.Sprintf("#%d %s", pr.Number, pr.State)
	if pr.Conflicting {
		out += " conflict"
	}

	if s.CI != nil {
		switch s.CI.State {
		case "passing":
			out += " CI:ok"
		case "failing":
			out += " CI:fail"
		case "pending":
			out += " CI:…"
		}
	}

	return out
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
