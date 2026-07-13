package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

// tokensConnectFn lets command-validation tests fail before daemon auto-start.
var tokensConnectFn = client.Connect

// tokenRow is the per-session token projection emitted by `gr tokens --json`.
type tokenRow struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	Agent         string `json:"agent"`
	Supported     bool   `json:"supported"`
	Known         bool   `json:"known"`
	Input         int64  `json:"input"`
	Output        int64  `json:"output"`
	CacheCreation int64  `json:"cache_creation"`
	CacheRead     int64  `json:"cache_read"`
	Unclassified  int64  `json:"unclassified"`
	Total         int64  `json:"total"`
	Degraded      bool   `json:"degraded,omitempty"`
	CountedAt     string `json:"counted_at,omitempty"`
}

func newTokenRow(s protocol.SessionInfo) tokenRow {
	r := tokenRow{
		Name:      s.Name,
		ID:        s.ID,
		Agent:     s.Agent,
		Supported: transcript.Supported(s.Agent),
	}
	if s.Tokens != nil {
		t := s.Tokens
		r.Known = true
		r.Input, r.Output = t.Input, t.Output
		r.CacheCreation, r.CacheRead = t.CacheCreation, t.CacheRead
		r.Unclassified, r.Total = t.Unclassified, t.Total
		r.Degraded, r.CountedAt = t.Degraded, t.CountedAt
	}

	return r
}

var tokensCmd = &cobra.Command{
	Use:   "tokens [session]",
	Short: "Show per-session token usage",
	Long: "Show token usage (input, output, cache) per session, extracted from " +
		"each agent's transcript. Counts reflect the session's current agent and " +
		"lag by up to one poll interval. Agents without a transcript reader show —.",
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := tokensConnectFn(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("list", protocol.ListMsg{})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return err
		}

		if len(args) == 1 {
			s, err := resolveByNameOrID(args[0], list.Sessions)
			if err != nil {
				return err
			}

			list.Sessions = []protocol.SessionInfo{*s}
		}

		rows := make([]tokenRow, 0, len(list.Sessions))
		for _, s := range list.Sessions {
			rows = append(rows, newTokenRow(s))
		}

		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Name != rows[j].Name {
				return rows[i].Name < rows[j].Name
			}

			return rows[i].ID < rows[j].ID
		})

		if jsonOutput {
			return out.JSON(rows)
		}

		if len(rows) == 0 {
			out.Printf("No sessions.\n")
			return nil
		}

		printTokenTable(cmd, rows)

		return nil
	},
}

// printTokenTable renders the human-readable token breakdown with a totals row.
func printTokenTable(cmd *cobra.Command, rows []tokenRow) {
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	defer func() { _ = tw.Flush() }()

	_, _ = fmt.Fprintln(tw, "SESSION\tAGENT\tINPUT\tOUTPUT\tCACHE-R\tCACHE-W\tOTHER\tTOTAL")

	var (
		sum      tokenRow
		degraded bool
	)

	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, tokenTableRow(r))

		if r.Known {
			sum.Input += r.Input
			sum.Output += r.Output
			sum.CacheRead += r.CacheRead
			sum.CacheCreation += r.CacheCreation
			sum.Unclassified += r.Unclassified
			sum.Total += r.Total

			if r.Degraded {
				degraded = true
			}
		}
	}

	if len(rows) > 1 {
		// The aggregate is approximate if any contributing row was.
		total := withCommas(sum.Total)
		if degraded {
			total += "~"
		}

		_, _ = fmt.Fprintf(tw, "TOTAL\t\t%s\t%s\t%s\t%s\t%s\t%s\n",
			withCommas(sum.Input), withCommas(sum.Output), withCommas(sum.CacheRead),
			withCommas(sum.CacheCreation), withCommas(sum.Unclassified), total)
	}
}

func tokenTableRow(r tokenRow) string {
	if !r.Supported {
		return fmt.Sprintf("%s\t%s\t—\t—\t—\t—\t—\t(unsupported)", r.Name, r.Agent)
	}

	if !r.Known {
		return fmt.Sprintf("%s\t%s\t—\t—\t—\t—\t—\t(unknown)", r.Name, r.Agent)
	}

	total := withCommas(r.Total)
	if r.Degraded {
		total += "~"
	}

	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
		r.Name, r.Agent, withCommas(r.Input), withCommas(r.Output), withCommas(r.CacheRead),
		withCommas(r.CacheCreation), withCommas(r.Unclassified), total)
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

func registerTokensCmd() {
	rootCmd.AddCommand(tokensCmd)
}
