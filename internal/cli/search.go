package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	searchSession  string
	searchChildren bool
	searchRepo     string
	searchAgent    string
	searchKinds    []string
	searchSince    string
	searchUntil    string
	searchState    string
	searchDeleted  bool
	searchLimit    int
	searchCursor   string
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search conversation transcripts across sessions",
	Long: `Search Claude Code and Codex conversation transcripts across sessions.
Search is local to the daemon and uses graith's canonical transcript readers.`,
	Args: validateSearchArgs,
	RunE: runSearch,
}

func validateSearchArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return errors.New("search query is required")
	}

	if searchChildren && searchSession == "" {
		return errors.New("--children requires --session")
	}

	if searchLimit < 0 {
		return errors.New("--limit must not be negative")
	}

	switch searchState {
	case "", "all", "active", "stopped":
	default:
		return errors.New("--state must be one of all, active, stopped")
	}

	return nil
}

func runSearch(cmd *cobra.Command, args []string) error {
	deps := commandDeps(cmd.Context())

	req := protocol.SearchMsg{
		Query:              strings.Join(args, " "),
		IncludeDescendants: searchChildren,
		Repo:               searchRepo,
		Agent:              searchAgent,
		Kinds:              searchKinds,
		Since:              searchSince,
		Until:              searchUntil,
		State:              searchState,
		IncludeDeleted:     searchDeleted,
		Limit:              searchLimit,
		Cursor:             searchCursor,
	}

	if searchSession != "" {
		id, err := resolveSearchSessionID(deps.listSession, searchSession, searchDeleted)
		if err != nil {
			return err
		}

		req.SessionID = id
	}

	resp, err := deps.search.SearchConversations(req)
	if err != nil {
		return err
	}

	if jsonOutput {
		return deps.out.JSON(resp)
	}

	printSearchResults(cmd.OutOrStdout(), resp)

	return nil
}

func resolveSearchSessionID(useCase sessionListUseCase, nameOrID string, includeDeleted bool) (string, error) {
	sessions, err := useCase.ListSessions(false)
	if err != nil {
		return "", err
	}

	if includeDeleted {
		deleted, err := useCase.ListSessions(true)
		if err != nil {
			return "", err
		}

		sessions = append(sessions, deleted...)
	}

	session, err := resolveByNameOrID(nameOrID, sessions)
	if err != nil {
		return "", err
	}

	return session.ID, nil
}

func printSearchResults(w io.Writer, resp protocol.SearchResponseMsg) {
	if len(resp.Results) == 0 {
		_, _ = fmt.Fprintln(w, "No conversation matches.")
	} else {
		for _, result := range resp.Results {
			ts := result.Timestamp
			if ts == "" {
				ts = "no timestamp"
			}

			repo := result.RepoName
			if repo == "" {
				repo = result.RepoPath
			}

			if repo == "" {
				repo = "no repo"
			}

			_, _ = fmt.Fprintf(w, "%s (%s)  %s/%s  %s  %s\n",
				result.SessionName, result.SessionID, result.Agent, result.Kind, repo, ts)
			_, _ = fmt.Fprintf(w, "  %s\n", highlightSearchSnippet(result.Snippet, result.Matches))
			_, _ = fmt.Fprintf(w, "  locator: %s\n", result.Locator)
		}
	}

	if len(resp.UnsupportedAgents) > 0 {
		_, _ = fmt.Fprintf(w, "\nSkipped unsupported agents: %s\n", formatUnsupportedAgents(resp.UnsupportedAgents))
	}

	if resp.NextCursor != "" {
		_, _ = fmt.Fprintf(w, "\nMore results: use --cursor %s with the same filters.\n", resp.NextCursor)
	}
}

func highlightSearchSnippet(snippet string, ranges []protocol.SearchMatchRange) string {
	if snippet == "" || len(ranges) == 0 {
		return snippet
	}

	runes := []rune(snippet)

	var (
		b   strings.Builder
		pos int
	)

	for _, r := range ranges {
		if r.Start < pos || r.Start < 0 || r.End > len(runes) || r.End <= r.Start {
			continue
		}

		b.WriteString(string(runes[pos:r.Start]))
		b.WriteByte('[')
		b.WriteString(string(runes[r.Start:r.End]))
		b.WriteByte(']')

		pos = r.End
	}

	b.WriteString(string(runes[pos:]))

	return b.String()
}

func formatUnsupportedAgents(agents []protocol.SearchUnsupportedAgent) string {
	parts := make([]string, 0, len(agents))
	for _, agent := range agents {
		if agent.Count == 1 {
			parts = append(parts, agent.Agent+" (1 session)")
		} else {
			parts = append(parts, fmt.Sprintf("%s (%d sessions)", agent.Agent, agent.Count))
		}
	}

	return strings.Join(parts, ", ")
}

func registerSearchCmd() {
	searchCmd.Flags().StringVar(&searchSession, "session", "", "filter by session name or ID")
	searchCmd.Flags().BoolVar(&searchChildren, "children", false, "include descendants of --session")
	searchCmd.Flags().StringVar(&searchRepo, "repo", "", "filter by repo name or path")
	searchCmd.Flags().StringVar(&searchAgent, "agent", "", "filter by agent")
	searchCmd.Flags().StringArrayVar(&searchKinds, "kind", nil, "filter by message kind (user, assistant, tool, context); repeat or comma-separate")
	searchCmd.Flags().StringVar(&searchSince, "since", "", "filter to messages at or after an RFC3339 timestamp or YYYY-MM-DD date")
	searchCmd.Flags().StringVar(&searchUntil, "until", "", "filter to messages at or before an RFC3339 timestamp or YYYY-MM-DD date")
	searchCmd.Flags().StringVar(&searchState, "state", "all", "filter by session state: all, active, or stopped")
	searchCmd.Flags().BoolVar(&searchDeleted, "deleted", false, "include soft-deleted sessions")
	searchCmd.Flags().IntVar(&searchLimit, "limit", 0, "maximum results to return (default 20, max 200)")
	searchCmd.Flags().StringVar(&searchCursor, "cursor", "", "pagination cursor returned by a prior search")

	rootCmd.AddCommand(searchCmd)
}
