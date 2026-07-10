package cli

import (
	"fmt"
	"image/color"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	listRepo     string
	listTree     bool
	listChildren string
	listStarred  bool
	listQuiet    bool
	listWide     bool
	listNoColor  bool
	listDeleted  bool
)

// listConnectFn lets command-validation tests fail before daemon auto-start.
var listConnectFn = client.Connect

// ansiSeqRe matches SGR (color) escape sequences so they can be bracketed with
// tabwriter's Escape byte (0xff). Bracketing keeps tabwriter from counting the
// invisible bytes toward column width, so colored cells stay aligned.
var ansiSeqRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// colorize wraps text in the given foreground color for terminal output. When
// coloring is disabled (or the text is empty) it returns the text unchanged.
// The emitted escape sequences are bracketed with tabwriter.Escape bytes so a
// tabwriter created with tabwriter.StripEscape aligns columns by visible width.
func colorize(text string, c color.Color, enabled bool) string {
	if !enabled || text == "" {
		return text
	}

	rendered := lipgloss.NewStyle().Foreground(c).Render(text)

	return ansiSeqRe.ReplaceAllString(rendered, "\xff$0\xff")
}

// shouldColor decides whether colored output is appropriate. Colors are
// disabled by the --no-color flag, by a non-empty NO_COLOR env var (per the
// no-color.org convention), or when stdout is not a terminal (to avoid leaking
// escape codes into pipes and files).
func shouldColor(noColorFlag bool, noColorEnv string, isTTY bool) bool {
	if noColorFlag || noColorEnv != "" {
		return false
	}

	return isTTY
}

// listColorEnabled resolves shouldColor against the current flags, environment,
// and the command's output stream.
func listColorEnabled(cmd *cobra.Command) bool {
	isTTY := false
	if f, ok := cmd.OutOrStdout().(*os.File); ok {
		isTTY = term.IsTerminal(int(f.Fd()))
	}

	return shouldColor(listNoColor, os.Getenv("NO_COLOR"), isTTY)
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		updateCh := make(chan *version.UpdateResult, 1)
		// Snapshot the data dir before the goroutine so it doesn't read the
		// mutable package-global `paths` after RunE returns (data race under -race).
		updateDataDir := paths.DataDir

		go func() {
			updateCh <- version.CheckForUpdate(updateDataDir)
		}()

		c, err := listConnectFn(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("list", protocol.ListMsg{Deleted: listDeleted})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return err
		}

		if listChildren != "" {
			parent := findSession(list.Sessions, listChildren)
			if parent == nil {
				return fmt.Errorf("session %q not found", listChildren)
			}

			list.Sessions = descendantsOf(list.Sessions, parent.ID)
		}

		if listRepo != "" {
			filtered := list.Sessions[:0]
			for _, s := range list.Sessions {
				if matchesRepo(s, listRepo) {
					filtered = append(filtered, s)
				}
			}

			list.Sessions = filtered
		}

		if listStarred {
			filtered := list.Sessions[:0]
			for _, s := range list.Sessions {
				if s.Starred {
					filtered = append(filtered, s)
				}
			}

			list.Sessions = filtered
		}

		if listQuiet {
			return printQuiet(cmd, list.Sessions)
		}

		if jsonOutput {
			return out.JSON(list)
		}

		if paths.Profile != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Profile: %s\n\n", paths.Profile)
		}

		if len(list.Sessions) == 0 {
			if listDeleted {
				out.Printf("No deleted sessions.\n")
			} else {
				out.Printf("No sessions. Create one with: gr new <name>\n")
			}

			return nil
		}

		now := time.Now()

		if listTree {
			printTree(cmd, list.Sessions, now)
		} else {
			printFlat(cmd, list.Sessions, now)
		}

		select {
		case result := <-updateCh:
			if result != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nUpdate available: %s → %s (brew upgrade graith)\n",
					result.CurrentVersion, result.LatestVersion)
			}
		default:
		}

		return nil
	},
}

// printQuiet emits bare session names, one per line (or a JSON array of session
// IDs when --json is set), for scripting.
func printQuiet(cmd *cobra.Command, sessions []protocol.SessionInfo) error {
	sorted := make([]protocol.SessionInfo, len(sessions))
	copy(sorted, sessions)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].RepoName != sorted[j].RepoName {
			return sorted[i].RepoName < sorted[j].RepoName
		}

		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}

		// Tie-break on ID so output ordering is deterministic even when two
		// sessions share a repo and name (names are not globally unique).
		return sorted[i].ID < sorted[j].ID
	})

	if jsonOutput {
		ids := make([]string, 0, len(sorted))
		for _, s := range sorted {
			ids = append(ids, s.ID)
		}

		return out.JSON(ids)
	}

	for _, s := range sorted {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), s.Name)
	}

	return nil
}

// sessionColumn describes a trailing column (everything after NAME) in the
// `gr list` table. Columns marked wide are only shown with the --wide flag.
type sessionColumn struct {
	header string
	wide   bool
	value  func(s protocol.SessionInfo, now time.Time, colorOn bool) string
}

// trailingColumns returns every column after NAME in display order, built from
// the shared client.SessionColumns registry so `gr ls` and the TUI picker stay
// in sync. Only registry columns flagged ShowCLI appear here; the compact
// default hides the wide columns (model, branch, attached) and --wide shows
// all. Cells with a CLIColor are colourised via colorize (which brackets the
// escapes for tabwriter alignment).
func trailingColumns() []sessionColumn {
	var cols []sessionColumn

	for _, c := range client.SessionColumns() {
		if !c.ShowCLI {
			continue
		}

		cols = append(cols, sessionColumn{
			header: strings.ToUpper(c.Header),
			wide:   c.Wide,
			value: func(s protocol.SessionInfo, now time.Time, colorOn bool) string {
				v := c.CLIValue(s, now)
				if c.CLIColor != nil {
					v = colorize(v, c.CLIColor(s), colorOn)
				}

				return v
			},
		})
	}

	// The Deleted view appends a soft-delete-only EXPIRES column (relative time
	// until purge) not carried by the shared live-session registry.
	if listDeleted {
		cols = append(cols,
			sessionColumn{"DELETED", false, func(s protocol.SessionInfo, now time.Time, _ bool) string {
				return formatDeletedAt(s, now)
			}},
			sessionColumn{"EXPIRES", false, func(s protocol.SessionInfo, now time.Time, colorOn bool) string {
				return formatDeleteExpiry(s, now, colorOn)
			}},
		)
	}

	return cols
}

// formatDeletedAt renders how long ago a session was soft-deleted (e.g. "3h
// ago"), or "-" when unknown.
func formatDeletedAt(s protocol.SessionInfo, now time.Time) string {
	if s.DeletedAt == "" {
		return "-"
	}

	t, err := time.Parse(time.RFC3339, s.DeletedAt)
	if err != nil {
		return "-"
	}

	return client.ShortDuration(now.Sub(t)) + " ago"
}

// formatDeleteExpiry renders when a soft-deleted session will be purged,
// relative to now (e.g. "in 23h", "expired"). Empty when the session carries
// no expiry (retention disabled or not deleted).
func formatDeleteExpiry(s protocol.SessionInfo, now time.Time, colorOn bool) string {
	if s.DeleteExpiresAt == "" {
		return "-"
	}

	expires, err := time.Parse(time.RFC3339, s.DeleteExpiresAt)
	if err != nil {
		return "-"
	}

	remaining := expires.Sub(now)
	if remaining <= 0 {
		return colorize("expired", client.StatusColor("errored"), colorOn)
	}

	return "in " + client.ShortDuration(remaining)
}

// visibleColumns filters trailingColumns by the wide flag.
func visibleColumns(wide bool) []sessionColumn {
	all := trailingColumns()
	if wide {
		return all
	}

	visible := make([]sessionColumn, 0, len(all))

	for _, c := range all {
		if !c.wide {
			visible = append(visible, c)
		}
	}

	return visible
}

// headerRow builds the tab-separated header line for the given columns.
func headerRow(cols []sessionColumn) string {
	cells := make([]string, 0, len(cols)+1)
	cells = append(cells, "NAME")

	for _, c := range cols {
		cells = append(cells, c.header)
	}

	return strings.Join(cells, "\t")
}

// dataRow builds the tab-separated data line for a session. The name cell is
// supplied by the caller so callers can prepend tree prefixes.
func dataRow(name string, s protocol.SessionInfo, cols []sessionColumn, now time.Time, colorOn bool) string {
	cells := make([]string, 0, len(cols)+1)
	cells = append(cells, name)

	for _, c := range cols {
		cells = append(cells, c.value(s, now, colorOn))
	}

	return strings.Join(cells, "\t")
}

func printFlat(cmd *cobra.Command, sessions []protocol.SessionInfo, now time.Time) {
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].RepoName != sessions[j].RepoName {
			return sessions[i].RepoName < sessions[j].RepoName
		}

		return sessions[i].Name < sessions[j].Name
	})

	colorOn := listColorEnabled(cmd)
	cols := visibleColumns(listWide)

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', tabwriter.StripEscape)
	_, _ = fmt.Fprintln(tw, headerRow(cols))

	for _, s := range sessions {
		name := s.Name
		if s.Starred {
			name = "★ " + name
		}

		_, _ = fmt.Fprintln(tw, dataRow(name, s, cols, now, colorOn))
	}

	_ = tw.Flush()
}

func printTree(cmd *cobra.Command, sessions []protocol.SessionInfo, now time.Time) {
	byID := make(map[string]protocol.SessionInfo, len(sessions))
	children := make(map[string][]protocol.SessionInfo)

	var roots []protocol.SessionInfo

	for _, s := range sessions {
		byID[s.ID] = s
	}

	for _, s := range sessions {
		if s.ParentID == "" || byID[s.ParentID].ID == "" {
			roots = append(roots, s)
		} else {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	sortSessions := func(ss []protocol.SessionInfo) {
		sort.Slice(ss, func(i, j int) bool {
			if ss[i].RepoName != ss[j].RepoName {
				return ss[i].RepoName < ss[j].RepoName
			}

			return ss[i].Name < ss[j].Name
		})
	}
	sortSessions(roots)

	for k := range children {
		sortSessions(children[k])
	}

	colorOn := listColorEnabled(cmd)
	cols := visibleColumns(listWide)

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', tabwriter.StripEscape)
	_, _ = fmt.Fprintln(tw, headerRow(cols))

	var walk func(s protocol.SessionInfo, prefix, childPrefix string)

	walk = func(s protocol.SessionInfo, prefix, childPrefix string) {
		name := s.Name
		if s.Starred {
			name = "★ " + name
		}

		_, _ = fmt.Fprintln(tw, dataRow(prefix+name, s, cols, now, colorOn))

		kids := children[s.ID]
		for i, kid := range kids {
			if i == len(kids)-1 {
				walk(kid, childPrefix+"`-- ", childPrefix+"    ")
			} else {
				walk(kid, childPrefix+"|-- ", childPrefix+"|   ")
			}
		}
	}

	for _, root := range roots {
		walk(root, "", "")
	}

	_ = tw.Flush()
}

func findSession(sessions []protocol.SessionInfo, nameOrID string) *protocol.SessionInfo {
	for i, s := range sessions {
		if s.Name == nameOrID || s.ID == nameOrID {
			return &sessions[i]
		}
	}

	return nil
}

func descendantsOf(sessions []protocol.SessionInfo, parentID string) []protocol.SessionInfo {
	children := make(map[string][]protocol.SessionInfo)

	for _, s := range sessions {
		if s.ParentID != "" {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	var result []protocol.SessionInfo

	seen := map[string]bool{parentID: true}

	var collect func(string)

	collect = func(id string) {
		for _, child := range children[id] {
			if !seen[child.ID] {
				seen[child.ID] = true
				result = append(result, child)
				collect(child.ID)
			}
		}
	}
	collect(parentID)

	return result
}

// registerListCmd registers this command on rootCmd. Called from registerCommands.
func registerListCmd() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().StringVar(&listRepo, "repo", "", "filter by repo path")
	listCmd.Flags().BoolVar(&listTree, "tree", false, "show parent-child hierarchy")
	listCmd.Flags().StringVar(&listChildren, "children", "", "filter to descendants of a session")
	listCmd.Flags().BoolVar(&listStarred, "starred", false, "show only starred sessions")
	listCmd.Flags().BoolVarP(&listQuiet, "quiet", "q", false, "output only session names (or IDs as JSON with --json)")
	listCmd.Flags().BoolVar(&listWide, "wide", false, "show all columns (model, branch, attached)")
	listCmd.Flags().BoolVar(&listNoColor, "no-color", false, "disable colored status output")
	listCmd.Flags().BoolVar(&listDeleted, "deleted", false, "show soft-deleted sessions with their expiry time")

	_ = listCmd.RegisterFlagCompletionFunc("repo", completeRepoPaths)
	_ = listCmd.RegisterFlagCompletionFunc("children", completeSessionNames)
}
