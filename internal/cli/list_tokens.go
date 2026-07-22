package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

type tokenProjectionSession struct {
	session protocol.SessionInfo
	name    string
}

// printTokenProjection renders the token-focused human projection of gr list.
// JSON output is handled before this function and remains the canonical nested
// SessionListMsg shape with token data under SessionInfo.tokens.
func printTokenProjection(cmd *cobra.Command, sessions []protocol.SessionInfo, now time.Time, tree bool) {
	ordered := orderTokenProjectionSessions(sessions, tree)
	rows := make([][]string, 0, len(ordered)+2)
	rows = append(rows, []string{
		"SESSION", "REPO", "AGENT", "INPUT", "OUTPUT", "CACHE-R", "CACHE-W", "OTHER", "TOTAL", "COUNTED",
	})

	var totals protocol.TokenInfo

	known := 0
	degraded := false

	for _, item := range ordered {
		rows = append(rows, tokenProjectionCells(item, now))

		s := item.session
		if !transcript.Supported(s.Agent) || s.Tokens == nil {
			continue
		}

		known++
		totals.Input += s.Tokens.Input
		totals.Output += s.Tokens.Output
		totals.CacheRead += s.Tokens.CacheRead
		totals.CacheCreation += s.Tokens.CacheCreation
		totals.Unclassified += s.Tokens.Unclassified
		totals.Total += s.Tokens.Total
		degraded = degraded || s.Tokens.Degraded
	}

	if len(ordered) > 1 {
		rows = append(rows, tokenProjectionTotalCells(totals, known, len(ordered), degraded))
	}

	renderRows(cmd.OutOrStdout(), rows)
}

func tokenProjectionCells(item tokenProjectionSession, now time.Time) []string {
	s := item.session
	base := []string{item.name, s.RepoName, s.Agent}

	if !transcript.Supported(s.Agent) {
		return append(base, "—", "—", "—", "—", "—", "(unsupported)", "—")
	}

	if s.Tokens == nil {
		return append(base, "—", "—", "—", "—", "—", "(unknown)", "—")
	}

	total := withCommas(s.Tokens.Total)
	if s.Tokens.Degraded {
		total += "~"
	}

	return append(base,
		withCommas(s.Tokens.Input),
		withCommas(s.Tokens.Output),
		withCommas(s.Tokens.CacheRead),
		withCommas(s.Tokens.CacheCreation),
		withCommas(s.Tokens.Unclassified),
		total,
		formatTokenCountedAt(s.Tokens.CountedAt, now),
	)
}

func tokenProjectionTotalCells(totals protocol.TokenInfo, known, sessions int, degraded bool) []string {
	coverage := fmt.Sprintf("%d/%d known", known, sessions)
	if known == 0 {
		return []string{"TOTAL", "", "", "—", "—", "—", "—", "—", "(unknown)", coverage}
	}

	total := withCommas(totals.Total)
	if degraded {
		total += "~"
	}

	return []string{
		"TOTAL", "", "",
		withCommas(totals.Input),
		withCommas(totals.Output),
		withCommas(totals.CacheRead),
		withCommas(totals.CacheCreation),
		withCommas(totals.Unclassified),
		total,
		coverage,
	}
}

func formatTokenCountedAt(countedAt string, now time.Time) string {
	counted, err := time.Parse(time.RFC3339, countedAt)
	if err != nil {
		return "—"
	}

	age := now.Sub(counted)
	if age < 0 {
		age = 0
	}

	return client.ShortDuration(age) + " ago"
}

// orderTokenProjectionSessions applies the selected normal list ordering. Tree
// mode reuses the hierarchy used by the default gr list table.
func orderTokenProjectionSessions(sessions []protocol.SessionInfo, tree bool) []tokenProjectionSession {
	if !tree {
		ordered := append([]protocol.SessionInfo(nil), sessions...)
		sortTokenProjectionSessions(ordered)

		rows := make([]tokenProjectionSession, 0, len(ordered))
		for _, s := range ordered {
			rows = append(rows, tokenProjectionSession{session: s, name: tokenProjectionName(s, "")})
		}

		return rows
	}

	rows := make([]tokenProjectionSession, 0, len(sessions))
	for _, item := range orderSessionTree(sessions) {
		rows = append(rows, tokenProjectionSession{
			session: item.session,
			name:    tokenProjectionName(item.session, item.prefix),
		})
	}

	return rows
}

func sortTokenProjectionSessions(sessions []protocol.SessionInfo) {
	sort.Slice(sessions, func(i, j int) bool {
		return sessionInfoLess(sessions[i], sessions[j])
	})
}

func tokenProjectionName(s protocol.SessionInfo, prefix string) string {
	name := s.Name
	if s.Starred {
		name = "★ " + name
	}

	return prefix + name
}

// withCommas formats an integer with thousands separators (e.g. 1234567 →
// "1,234,567").
func withCommas(n int64) string {
	s := strconv.FormatInt(n, 10)

	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}

	var b strings.Builder

	for i, d := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}

		b.WriteRune(d)
	}

	if neg {
		return "-" + b.String()
	}

	return b.String()
}
