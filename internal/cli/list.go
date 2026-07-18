package cli

import (
	"fmt"
	"image/color"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
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
	listTokens   bool
	listNoColor  bool
	listDeleted  bool
	listWatch    bool
)

type listConn interface {
	controlConn
	Close()
}

// listConnectFn lets command-validation and rendering tests run without daemon
// auto-start.
var listConnectFn = func(cfg *config.Config, paths config.Paths, cfgFile string) (listConn, error) {
	return client.Connect(cfg, paths, cfgFile)
}

var (
	listWatchTerminalFn = listWatchTerminal
	listWatchRunFn      = client.RunListWatch
	listWatchFetchFn    = fetchListSessions
	listWatchActionFn   = sendListWatchAction
)

// colorize wraps text in the given foreground color for terminal output. When
// coloring is disabled (or the text is empty) it returns the text unchanged.
// The returned string may contain ANSI escape sequences; column alignment is
// handled by renderRows, which measures cells with ansi.StringWidth (escapes
// excluded) rather than relying on tabwriter's byte counting.
func colorize(text string, c color.Color, enabled bool) string {
	if !enabled || text == "" {
		return text
	}

	return lipgloss.NewStyle().Foreground(c).Render(text)
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
	Short:   "List or watch sessions",
	RunE:    runList,
}

func runList(cmd *cobra.Command, _ []string) error {
	if err := validateListWatch(cmd); err != nil {
		return err
	}

	var updateCh chan *version.UpdateResult
	if !listWatch {
		updateCh = make(chan *version.UpdateResult, 1)
		// Snapshot the data dir before the goroutine so it doesn't read the
		// mutable package-global `paths` after RunE returns (data race under -race).
		updateDataDir := paths.DataDir
		updateCfg := updateSettings(cfg)

		go func() {
			updateCh <- version.CheckForUpdate(updateDataDir, updateCfg)
		}()
	}

	sessions, err := fetchListSessions(listDeleted)
	if err != nil {
		return err
	}

	filter, err := newListSessionFilter(sessions)
	if err != nil {
		return err
	}

	sessions = filter.apply(sessions)
	if listWatch {
		return runListWatch(sessions, filter)
	}

	list := protocol.SessionListMsg{Sessions: sessions}
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
	if listTokens {
		printTokenProjection(cmd, list.Sessions, now, listTree)
	} else if listTree {
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
}

func validateListWatch(cmd *cobra.Command) error {
	if !listWatch {
		return nil
	}

	if jsonOutput {
		return fmt.Errorf("--watch is interactive and cannot be used with --json or --agent-mode")
	}

	if listQuiet {
		return fmt.Errorf("--watch cannot be used with --quiet")
	}

	if listDeleted {
		return fmt.Errorf("--watch cannot be used with --deleted")
	}

	if listTokens {
		return fmt.Errorf("--watch cannot be used with --tokens")
	}

	if !listWatchTerminalFn(cmd) {
		return fmt.Errorf("--watch requires an interactive terminal on stdin and stdout")
	}

	return nil
}

func listWatchTerminal(cmd *cobra.Command) bool {
	in, inOK := cmd.InOrStdin().(*os.File)
	outFile, outOK := cmd.OutOrStdout().(*os.File)
	return inOK && outOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(outFile.Fd()))
}

type listSessionFilter struct {
	parentID string
	repo     string
	starred  bool
}

func newListSessionFilter(sessions []protocol.SessionInfo) (listSessionFilter, error) {
	filter := listSessionFilter{repo: listRepo, starred: listStarred}
	if listChildren == "" {
		return filter, nil
	}

	parent := findSession(sessions, listChildren)
	if parent == nil {
		return listSessionFilter{}, fmt.Errorf("session %q not found", listChildren)
	}

	filter.parentID = parent.ID
	return filter, nil
}

func (f listSessionFilter) apply(sessions []protocol.SessionInfo) []protocol.SessionInfo {
	filtered := make([]protocol.SessionInfo, len(sessions))
	copy(filtered, sessions)
	if f.parentID != "" {
		filtered = descendantsOf(filtered, f.parentID)
	}

	if f.repo != "" {
		matches := make([]protocol.SessionInfo, 0, len(filtered))
		for _, session := range filtered {
			if matchesRepo(session, f.repo) {
				matches = append(matches, session)
			}
		}

		filtered = matches
	}

	if f.starred {
		matches := make([]protocol.SessionInfo, 0, len(filtered))
		for _, session := range filtered {
			if session.Starred {
				matches = append(matches, session)
			}
		}

		filtered = matches
	}

	if filtered == nil {
		return []protocol.SessionInfo{}
	}

	return filtered
}

func fetchListSessions(deleted bool) ([]protocol.SessionInfo, error) {
	c, err := listConnectFn(cfg, paths, cfgFile)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if err := c.SendControl("list", protocol.ListMsg{Deleted: deleted}); err != nil {
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}

	return list.Sessions, nil
}

func runListWatch(sessions []protocol.SessionInfo, filter listSessionFilter) error {
	options := client.ListWatchOptions{
		Wide:    listWide,
		Tree:    listTree,
		NoColor: listNoColor || os.Getenv("NO_COLOR") != "",
	}

	for {
		result, err := listWatchRunFn(sessions, listWatchKeysFromConfig(), options, func() []protocol.SessionInfo {
			fresh, err := listWatchFetchFn(false)
			if err != nil {
				// A transient refresh failure must not clear the screen or lose the
				// selected session; nil tells the model to preserve its state.
				return nil
			}

			return filter.apply(fresh)
		})
		if err != nil {
			return err
		}

		if result == nil {
			return nil
		}

		switch result.Action {
		case "attach":
			c, err := freshClient()
			if err != nil {
				return err
			}

			err = runAttachByID(c, result.SessionID, nil)
			c.Close()
			return err

		case "delete":
			if err := listWatchActionFn("delete", protocol.DeleteMsg{SessionID: result.SessionID}); err != nil {
				return err
			}

		case "stop":
			if err := listWatchActionFn("stop", protocol.StopMsg{SessionID: result.SessionID}); err != nil {
				return err
			}

		case "resume":
			if err := listWatchActionFn("resume", protocol.ResumeMsg{SessionID: result.SessionID}); err != nil {
				return err
			}

		default:
			return fmt.Errorf("unknown list watch action %q", result.Action)
		}

		fresh, err := listWatchFetchFn(false)
		if err != nil {
			return err
		}

		sessions = filter.apply(fresh)
	}
}

func sendListWatchAction(msgType string, payload any) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.SendControl(msgType, payload); err != nil {
		return err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type != "error" {
		return nil
	}

	var response protocol.ErrorMsg
	if err := protocol.DecodePayload(resp, &response); err != nil {
		return err
	}

	return fmt.Errorf("%s", response.Message)
}

// applyListFilters applies the normal session-list filters in command order.
// Keeping selection separate from rendering ensures every projection, including
// --tokens, sees exactly the same sessions.
func applyListFilters(sessions []protocol.SessionInfo, children, repo string, starred bool) ([]protocol.SessionInfo, error) {
	filtered := sessions

	if children != "" {
		parent := findSession(filtered, children)
		if parent == nil {
			return nil, fmt.Errorf("session %q not found", children)
		}

		filtered = descendantsOf(filtered, parent.ID)
	}

	if repo != "" {
		byRepo := make([]protocol.SessionInfo, 0, len(filtered))
		for _, s := range filtered {
			if matchesRepo(s, repo) {
				byRepo = append(byRepo, s)
			}
		}

		filtered = byRepo
	}

	if starred {
		starredSessions := make([]protocol.SessionInfo, 0, len(filtered))
		for _, s := range filtered {
			if s.Starred {
				starredSessions = append(starredSessions, s)
			}
		}

		filtered = starredSessions
	}

	return filtered, nil
}

// printQuiet emits bare session names, one per line (or a JSON array of session
// IDs when --json is set), for scripting.
func printQuiet(cmd *cobra.Command, sessions []protocol.SessionInfo) error {
	sorted := make([]protocol.SessionInfo, len(sessions))
	copy(sorted, sessions)
	sort.Slice(sorted, func(i, j int) bool {
		return sessionInfoLess(sorted[i], sorted[j])
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
// all. Cells with a CLIColor are colourised via colorize; renderRows keeps the
// coloured cells aligned by measuring visible width.
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

// headerCells builds the header cells for the given columns (NAME first).
func headerCells(cols []sessionColumn) []string {
	cells := make([]string, 0, len(cols)+1)
	cells = append(cells, "NAME")

	for _, c := range cols {
		cells = append(cells, c.header)
	}

	return cells
}

// dataCells builds the data cells for a session. The name cell is supplied by
// the caller so callers can prepend tree prefixes and star markers.
func dataCells(name string, s protocol.SessionInfo, cols []sessionColumn, now time.Time, colorOn bool) []string {
	cells := make([]string, 0, len(cols)+1)
	cells = append(cells, name)

	for _, c := range cols {
		cells = append(cells, c.value(s, now, colorOn))
	}

	return cells
}

// renderRows writes a column-aligned table to w. Each column's width is the
// maximum *display* width of its cells, measured with ansi.StringWidth, which
// ignores ANSI colour escapes and accounts for wide runes (emoji, East-Asian
// glyphs, the ⚠/★ markers). Every column except the last is padded to its width
// plus a two-space gap; the last column is written unpadded.
//
// This replaces text/tabwriter: tabwriter measures cells by counting runes and
// its Escape (0xff) bracketing only protects tabs and strips the bracket bytes —
// it still counts the bracketed ANSI sequence's own runes toward the column
// width, so coloured cells were padded short and every following column drifted
// (issue #1093). Measuring visible width sidesteps that and fixes wide runes too.
func renderRows(w io.Writer, rows [][]string) {
	ncol := 0

	for _, r := range rows {
		if len(r) > ncol {
			ncol = len(r)
		}
	}

	widths := make([]int, ncol)

	for _, r := range rows {
		for i, cell := range r {
			if cw := ansi.StringWidth(cell); cw > widths[i] {
				widths[i] = cw
			}
		}
	}

	const gap = 2

	var b strings.Builder

	for _, r := range rows {
		for i, cell := range r {
			b.WriteString(cell)

			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-ansi.StringWidth(cell)+gap))
			}
		}

		b.WriteByte('\n')
	}

	_, _ = io.WriteString(w, b.String())
}

func printFlat(cmd *cobra.Command, sessions []protocol.SessionInfo, now time.Time) {
	sort.Slice(sessions, func(i, j int) bool {
		return sessionInfoLess(sessions[i], sessions[j])
	})

	colorOn := listColorEnabled(cmd)
	cols := visibleColumns(listWide)

	rows := make([][]string, 0, len(sessions)+1)
	rows = append(rows, headerCells(cols))

	for _, s := range sessions {
		name := s.Name
		if s.Starred {
			name = "★ " + name
		}

		rows = append(rows, dataCells(name, s, cols, now, colorOn))
	}

	renderRows(cmd.OutOrStdout(), rows)
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
			return sessionInfoLess(ss[i], ss[j])
		})
	}
	sortSessions(roots)

	for k := range children {
		sortSessions(children[k])
	}

	colorOn := listColorEnabled(cmd)
	cols := visibleColumns(listWide)

	rows := [][]string{headerCells(cols)}

	var walk func(s protocol.SessionInfo, prefix, childPrefix string)

	walk = func(s protocol.SessionInfo, prefix, childPrefix string) {
		name := s.Name
		if s.Starred {
			name = "★ " + name
		}

		rows = append(rows, dataCells(prefix+name, s, cols, now, colorOn))

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

	renderRows(cmd.OutOrStdout(), rows)
}

// sessionInfoLess is the canonical ordering for human session-list projections.
// IDs break repo/name ties so duplicate session names remain deterministic.
func sessionInfoLess(a, b protocol.SessionInfo) bool {
	if a.RepoName != b.RepoName {
		return a.RepoName < b.RepoName
	}

	if a.Name != b.Name {
		return a.Name < b.Name
	}

	return a.ID < b.ID
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
	listCmd.Flags().BoolVar(&listWide, "wide", false, "show all columns, including compact token totals")
	listCmd.Flags().BoolVar(&listTokens, "tokens", false, "show detailed per-session token usage and totals")
	listCmd.Flags().BoolVar(&listNoColor, "no-color", false, "disable colored status output")
	listCmd.Flags().BoolVar(&listDeleted, "deleted", false, "show soft-deleted sessions with their expiry time")
	listCmd.MarkFlagsMutuallyExclusive("quiet", "tokens")
	listCmd.MarkFlagsMutuallyExclusive("wide", "tokens")
	listCmd.Flags().BoolVar(&listWatch, "watch", false, "watch sessions in an interactive live-updating view")

	_ = listCmd.RegisterFlagCompletionFunc("repo", completeRepoPaths)
	_ = listCmd.RegisterFlagCompletionFunc("children", completeSessionNames)
}
