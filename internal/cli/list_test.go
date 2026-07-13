package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestFindSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "abc123", Name: "worker-a"},
		{ID: "def456", Name: "worker-b"},
	}

	t.Run("by name", func(t *testing.T) {
		s := findSession(sessions, "worker-a")
		if s == nil || s.ID != "abc123" {
			t.Errorf("findSession by name: got %v", s)
		}
	})

	t.Run("by id", func(t *testing.T) {
		s := findSession(sessions, "def456")
		if s == nil || s.Name != "worker-b" {
			t.Errorf("findSession by id: got %v", s)
		}
	})

	t.Run("not found", func(t *testing.T) {
		s := findSession(sessions, "nope")
		if s != nil {
			t.Errorf("findSession not found: got %v, want nil", s)
		}
	})
}

func TestDescendantsOf(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "bairn", ParentID: "ben", Name: "bairn"},
		{ID: "canny", ParentID: "ben", Name: "canny"},
		{ID: "wee-bairn", ParentID: "bairn", Name: "wee-bairn"},
		{ID: "thrawn", Name: "thrawn"},
	}

	desc := descendantsOf(sessions, "ben")
	if len(desc) != 3 {
		t.Fatalf("expected 3 descendants, got %d", len(desc))
	}

	ids := make(map[string]bool)
	for _, s := range desc {
		ids[s.ID] = true
	}

	for _, expected := range []string{"bairn", "canny", "wee-bairn"} {
		if !ids[expected] {
			t.Errorf("missing descendant %q", expected)
		}
	}

	if ids["ben"] {
		t.Error("ben should not be in descendants")
	}

	if ids["thrawn"] {
		t.Error("thrawn should not be in descendants")
	}
}

func TestDescendantsOfOrphans(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "auld", ParentID: "deleted-parent", Name: "auld"},
		{ID: "bairn", ParentID: "auld", Name: "bairn"},
	}

	desc := descendantsOf(sessions, "auld")
	if len(desc) != 1 || desc[0].ID != "bairn" {
		t.Errorf("expected [bairn], got %v", desc)
	}
}

func TestPrintTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "hame", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", Agent: "codex", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "canny", ParentID: "ben", Name: "canny", RepoName: "croft", Agent: "claude", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "wee-bairn", ParentID: "bairn", Name: "wee-bairn", RepoName: "croft", Agent: "codex", Status: "stopped", CreatedAt: time.Now().Format(time.RFC3339)},
		{ID: "thrawn", Name: "thrawn", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTree(cmd, sessions, time.Now())

	output := buf.String()

	if !bytes.Contains([]byte(output), []byte("hame")) {
		t.Error("missing root session 'hame'")
	}

	if !bytes.Contains([]byte(output), []byte("|-- bairn")) && !bytes.Contains([]byte(output), []byte("`-- bairn")) {
		t.Error("missing tree-indented 'bairn'")
	}

	if !bytes.Contains([]byte(output), []byte("wee-bairn")) {
		t.Error("missing grandchild 'wee-bairn'")
	}

	if !bytes.Contains([]byte(output), []byte("thrawn")) {
		t.Error("missing root session 'thrawn'")
	}
}

func TestPrintQuietNames(t *testing.T) {
	origJSON := jsonOutput

	defer func() { jsonOutput = origJSON }()

	jsonOutput = false

	sessions := []protocol.SessionInfo{
		{ID: "abc", Name: "thrawn", RepoName: "croft"},
		{ID: "def", Name: "braw", RepoName: "croft"},
		{ID: "ghi", Name: "canny", RepoName: "bothy"},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := printQuiet(cmd, sessions); err != nil {
		t.Fatalf("printQuiet: %v", err)
	}

	// Sorted by repo, then name: bothy/canny, croft/braw, croft/thrawn.
	want := "canny\nbraw\nthrawn\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestPrintQuietJSONIDs(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	var buf bytes.Buffer

	jsonOutput = true
	out = output.NewWithWriter(true, &buf)

	sessions := []protocol.SessionInfo{
		{ID: "abc", Name: "thrawn", RepoName: "croft"},
		{ID: "def", Name: "braw", RepoName: "croft"},
		{ID: "ghi", Name: "canny", RepoName: "bothy"},
	}

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := printQuiet(cmd, sessions); err != nil {
		t.Fatalf("printQuiet: %v", err)
	}

	var ids []string
	if err := json.Unmarshal(buf.Bytes(), &ids); err != nil {
		t.Fatalf("output is not a JSON array: %v (%q)", err, buf.String())
	}

	// Sorted by repo, then name: canny(ghi), braw(def), thrawn(abc).
	want := []string{"ghi", "def", "abc"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}

	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
}

func TestPrintQuietEmptyJSON(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	var buf bytes.Buffer

	jsonOutput = true
	out = output.NewWithWriter(true, &buf)

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := printQuiet(cmd, nil); err != nil {
		t.Fatalf("printQuiet: %v", err)
	}

	// An empty list must serialize as [] not null, so jq consumers work.
	var ids []string
	if err := json.Unmarshal(buf.Bytes(), &ids); err != nil {
		t.Fatalf("output is not a JSON array: %v (%q)", err, buf.String())
	}

	if len(ids) != 0 {
		t.Errorf("got %v, want empty array", ids)
	}

	if !bytes.Contains(buf.Bytes(), []byte("[]")) {
		t.Errorf("empty list should serialize as [], got %q", buf.String())
	}
}

func TestPrintQuietDoesNotMutateInput(t *testing.T) {
	origJSON := jsonOutput

	defer func() { jsonOutput = origJSON }()

	jsonOutput = false

	sessions := []protocol.SessionInfo{
		{ID: "abc", Name: "thrawn", RepoName: "croft"},
		{ID: "def", Name: "braw", RepoName: "croft"},
	}

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	if err := printQuiet(cmd, sessions); err != nil {
		t.Fatalf("printQuiet: %v", err)
	}

	if sessions[0].Name != "thrawn" || sessions[1].Name != "braw" {
		t.Errorf("printQuiet mutated caller's slice order: %v", sessions)
	}
}

func TestPrintTreeOrphansAsRoots(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "auld", ParentID: "deleted", Name: "auld-whin", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTree(cmd, sessions, time.Now())

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("auld-whin")) {
		t.Error("orphan should render as root")
	}

	if bytes.Contains([]byte(output), []byte("|--")) || bytes.Contains([]byte(output), []byte("`--")) {
		t.Error("orphan should not have tree indentation")
	}
}

func TestShouldColor(t *testing.T) {
	tests := []struct {
		name        string
		noColorFlag bool
		noColorEnv  string
		isTTY       bool
		want        bool
	}{
		{"tty, no flags", false, "", true, true},
		{"not a tty", false, "", false, false},
		{"no-color flag", true, "", true, false},
		{"NO_COLOR env set", false, "1", true, false},
		{"NO_COLOR empty string is off", false, "", true, true},
		{"flag wins over tty", true, "", true, false},
		{"env wins over tty", false, "anything", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldColor(tt.noColorFlag, tt.noColorEnv, tt.isTTY); got != tt.want {
				t.Errorf("shouldColor(%v, %q, %v) = %v, want %v",
					tt.noColorFlag, tt.noColorEnv, tt.isTTY, got, tt.want)
			}
		})
	}
}

func TestColorize(t *testing.T) {
	green := client.StatusColor("running")

	t.Run("disabled returns text unchanged", func(t *testing.T) {
		if got := colorize("running", green, false); got != "running" {
			t.Errorf("got %q, want %q", got, "running")
		}
	})

	t.Run("empty text is never wrapped", func(t *testing.T) {
		if got := colorize("", green, true); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("enabled wraps text in ANSI with visible width unchanged", func(t *testing.T) {
		got := colorize("running", green, true)

		if !strings.Contains(got, "\x1b[") {
			t.Errorf("expected ANSI escape in %q", got)
		}

		if !strings.Contains(got, "running") {
			t.Errorf("expected visible text preserved in %q", got)
		}

		// No tabwriter Escape (0xff) bytes: renderRows aligns by visible width
		// (ansi.StringWidth), so the bracketing that tabwriter needed is gone.
		if strings.ContainsRune(got, '\xff') {
			t.Errorf("colored output must not contain tabwriter escape bytes: %q", got)
		}

		// The visible width matches the plain text, so alignment is unaffected.
		if w := ansi.StringWidth(got); w != len("running") {
			t.Errorf("visible width = %d, want %d (%q)", w, len("running"), got)
		}
	})
}

func TestVisibleColumns(t *testing.T) {
	compact := visibleColumns(false)
	wide := visibleColumns(true)

	if len(wide) <= len(compact) {
		t.Fatalf("wide (%d) should have more columns than compact (%d)", len(wide), len(compact))
	}

	has := func(cols []sessionColumn, header string) bool {
		for _, c := range cols {
			if c.header == header {
				return true
			}
		}

		return false
	}

	// Wide-only columns are hidden in compact but present in wide.
	for _, h := range []string{"MODEL", "BRANCH", "ATTACHED"} {
		if has(compact, h) {
			t.Errorf("compact should not include %q", h)
		}

		if !has(wide, h) {
			t.Errorf("wide should include %q", h)
		}
	}

	// Core columns appear in both modes.
	for _, h := range []string{"REPO", "AGENT", "STATUS", "ACTIVITY", "GIT", "PR", "AGE"} {
		if !has(compact, h) {
			t.Errorf("compact should include %q", h)
		}

		if !has(wide, h) {
			t.Errorf("wide should include %q", h)
		}
	}
}

func TestPrintFlatWideColumns(t *testing.T) {
	orig := listWide
	defer func() { listWide = orig }()

	sessions := []protocol.SessionInfo{
		{ID: "braw", Name: "braw", RepoName: "croft", Agent: "claude", Status: "running",
			Model: "opus", Branch: "user/graith/braw", CreatedAt: time.Now().Format(time.RFC3339)},
	}

	render := func(wide bool) string {
		listWide = wide

		var buf bytes.Buffer

		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		printFlat(cmd, sessions, time.Now())

		return buf.String()
	}

	compact := render(false)
	if strings.Contains(compact, "MODEL") || strings.Contains(compact, "BRANCH") || strings.Contains(compact, "ATTACHED") {
		t.Errorf("compact output should omit wide columns:\n%s", compact)
	}

	wide := render(true)
	for _, h := range []string{"MODEL", "BRANCH", "ATTACHED"} {
		if !strings.Contains(wide, h) {
			t.Errorf("wide output should include %q:\n%s", h, wide)
		}
	}

	// The model value only shows in wide mode.
	if strings.Contains(compact, "opus") {
		t.Errorf("compact should not show model value:\n%s", compact)
	}

	if !strings.Contains(wide, "opus") {
		t.Errorf("wide should show model value:\n%s", wide)
	}
}

func TestPrintFlatBufferHasNoColor(t *testing.T) {
	// A bytes.Buffer is not a TTY, so printFlat must emit plain text with no
	// ANSI escape codes even for a running (would-be-green) session.
	sessions := []protocol.SessionInfo{
		{ID: "braw", Name: "braw", RepoName: "croft", Agent: "claude", Status: "running",
			CreatedAt: time.Now().Format(time.RFC3339)},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	printFlat(cmd, sessions, time.Now())

	if strings.ContainsRune(buf.String(), '\x1b') {
		t.Errorf("non-tty output must not contain ANSI escapes:\n%q", buf.String())
	}
}

// TestPrintFlatCov exercises the flat table renderer: header, repo/name sort
// order, and the star prefix on starred sessions.
func TestPrintFlatCov(t *testing.T) {
	now := time.Now()
	created := now.Format(time.RFC3339)

	sessions := []protocol.SessionInfo{
		{ID: "c1", Name: "thrawn", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: created},
		{ID: "b1", Name: "braw", RepoName: "bothy", Agent: "codex", Status: "stopped", CreatedAt: created, Starred: true},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printFlat(cmd, sessions, now)

	out := buf.String()

	if !bytes.Contains([]byte(out), []byte("NAME")) || !bytes.Contains([]byte(out), []byte("STATUS")) {
		t.Error("missing table header")
	}

	if !bytes.Contains([]byte(out), []byte("★ braw")) {
		t.Error("starred session should be prefixed with a star")
	}

	// bothy sorts before croft, so braw's row must precede thrawn's row.
	brawIdx := bytes.Index([]byte(out), []byte("braw"))
	thrawnIdx := bytes.Index([]byte(out), []byte("thrawn"))

	if brawIdx == -1 || thrawnIdx == -1 || brawIdx > thrawnIdx {
		t.Errorf("expected braw (bothy) before thrawn (croft); braw=%d thrawn=%d", brawIdx, thrawnIdx)
	}
}

// TestTrailingColumnsFromRegistry verifies that `gr ls` builds its columns from
// the shared client.SessionColumns registry: only ShowCLI columns appear, their
// headers are upper-cased, and the display order matches the historical layout.
func TestTrailingColumnsFromRegistry(t *testing.T) {
	// Wide view exposes every CLI column in registry order.
	wide := visibleColumns(true)

	var gotWide []string
	for _, c := range wide {
		gotWide = append(gotWide, c.header)
	}

	wantWide := []string{"REPO", "AGENT", "STATUS", "ACTIVITY", "MODEL", "BRANCH", "GIT", "PR", "TOKENS", "AGE", "ATTACHED"}
	if len(gotWide) != len(wantWide) {
		t.Fatalf("wide columns = %v, want %v", gotWide, wantWide)
	}

	for i := range wantWide {
		if gotWide[i] != wantWide[i] {
			t.Errorf("wide column %d = %q, want %q", i, gotWide[i], wantWide[i])
		}
	}

	// Compact view drops the wide columns (MODEL, BRANCH, ATTACHED).
	var gotCompact []string
	for _, c := range visibleColumns(false) {
		gotCompact = append(gotCompact, c.header)
	}

	wantCompact := []string{"REPO", "AGENT", "STATUS", "ACTIVITY", "GIT", "PR", "AGE"}
	if strings.Join(gotCompact, ",") != strings.Join(wantCompact, ",") {
		t.Errorf("compact columns = %v, want %v", gotCompact, wantCompact)
	}
}

// TestTrailingColumnsValues checks the plain-text cell values produced through
// the registry adapter for a representative session.
func TestTrailingColumnsValues(t *testing.T) {
	now := time.Now()
	s := protocol.SessionInfo{
		RepoName:      "croft",
		Agent:         "claude",
		Status:        "running",
		Dirty:         true,
		UnpushedCount: 2,
		PullRequest:   &protocol.PRInfo{Number: 42, State: "open"},
		CreatedAt:     now.Add(-90 * time.Minute).Format(time.RFC3339),
	}

	cells := map[string]string{}
	for _, c := range visibleColumns(true) {
		cells[c.header] = c.value(s, now, false)
	}

	checks := map[string]string{
		"REPO":   "croft",
		"AGENT":  "claude",
		"STATUS": "running",
		"GIT":    "dirty, 2 ahead",
		"PR":     "#42 open",
	}
	for header, want := range checks {
		if cells[header] != want {
			t.Errorf("column %s = %q, want %q", header, cells[header], want)
		}
	}

	if cells["AGE"] == "" {
		t.Error("AGE should be non-empty for a valid CreatedAt")
	}
}

// TestTrailingColumnsColorize confirms the STATUS/ACTIVITY columns emit ANSI
// escapes when colour is enabled and stay plain when it is not.
func TestTrailingColumnsColorize(t *testing.T) {
	s := protocol.SessionInfo{Status: "running", AgentStatus: "approval"}

	var statusCol, activityCol sessionColumn

	for _, c := range visibleColumns(true) {
		switch c.header {
		case "STATUS":
			statusCol = c
		case "ACTIVITY":
			activityCol = c
		}
	}

	if plain := activityCol.value(s, time.Time{}, false); plain != "⚠ approval" {
		t.Errorf("activity plain = %q, want %q", plain, "⚠ approval")
	}

	if colored := activityCol.value(s, time.Time{}, true); !strings.Contains(colored, "\x1b[") {
		t.Errorf("activity colored should contain ANSI: %q", colored)
	}

	if colored := statusCol.value(s, time.Time{}, true); !strings.Contains(colored, "\x1b[") {
		t.Errorf("status colored should contain ANSI: %q", colored)
	}

	if plain := statusCol.value(s, time.Time{}, false); plain != "running" {
		t.Errorf("status plain = %q, want %q", plain, "running")
	}
}

// goldenFlatSessions is a fixed fixture used by the golden-output tests below.
// The timestamps are relative to a fixed reference time so age is stable.
func goldenFlatSessions(now time.Time) []protocol.SessionInfo {
	created := now.Add(-90 * time.Minute).Format(time.RFC3339)

	return []protocol.SessionInfo{
		{
			Name: "braw", RepoName: "croft", Agent: "claude",
			Status: "running", AgentStatus: "active", ToolName: "Bash",
			Dirty: true, UnpushedCount: 2,
			PullRequest: &protocol.PRInfo{Number: 42, State: "open"},
			CI:          &protocol.CIInfo{State: "passing"},
			CreatedAt:   created,
		},
		{
			Name: "thrawn", RepoName: "croft", Agent: "codex",
			Status: "stopped", CreatedAt: created,
		},
	}
}

// TestPrintFlatGoldenCompact pins the exact bytes of the compact `gr ls` table
// so the shared-registry refactor (and any future change) cannot silently alter
// column order, padding, or cell content. Non-TTY output carries no ANSI.
func TestPrintFlatGoldenCompact(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	cmd := &cobra.Command{}

	var buf bytes.Buffer

	cmd.SetOut(&buf)

	listWide = false

	printFlat(cmd, goldenFlatSessions(now), now)

	want := "" +
		"NAME    REPO   AGENT   STATUS   ACTIVITY       GIT             PR              AGE\n" +
		"braw    croft  claude  running  active (Bash)  dirty, 2 ahead  #42 open CI:ok  1h30m\n" +
		"thrawn  croft  codex   stopped                                                 1h30m\n"

	if got := buf.String(); got != want {
		t.Errorf("compact table mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestPrintFlatGoldenWide pins the exact bytes of the wide `gr ls` table,
// including the MODEL/BRANCH/ATTACHED columns that only --wide reveals.
func TestPrintFlatGoldenWide(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	cmd := &cobra.Command{}

	var buf bytes.Buffer

	cmd.SetOut(&buf)

	listWide = true

	defer func() { listWide = false }()

	printFlat(cmd, goldenFlatSessions(now), now)

	want := "" +
		"NAME    REPO   AGENT   STATUS   ACTIVITY       MODEL  BRANCH  GIT             PR              TOKENS  AGE    ATTACHED\n" +
		"braw    croft  claude  running  active (Bash)                 dirty, 2 ahead  #42 open CI:ok          1h30m  \n" +
		"thrawn  croft  codex   stopped                                                                        1h30m  \n"

	if got := buf.String(); got != want {
		t.Errorf("wide table mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// columnStarts returns the visible (display-cell) offset at which each cell's
// content begins on a rendered line, given the original (possibly ANSI-coloured)
// cells for that row. It mirrors renderRows' padding rule so a test can assert
// that a rendered line places every column at the offset renderRows promises.
func columnStarts(cells []string, widths []int) []int {
	starts := make([]int, len(cells))
	off := 0

	for i := range cells {
		starts[i] = off
		off += widths[i] + 2 // cell padded to its column width + the 2-space gap
	}

	return starts
}

// TestRenderRowsAlignsColoredAndWideCells is the regression test for issue
// #1093: coloured cells and cells containing wide runes must not throw off
// column alignment. Against the old text/tabwriter renderer this failed —
// tabwriter counted the bytes of the bracketed ANSI escape toward the column
// width (and measured wide runes as a single cell), so coloured/wide cells were
// mis-padded and every following column drifted (worse for longer truecolor
// sequences). renderRows measures visible width via ansi.StringWidth, so each
// column starts at the same display offset on every row.
//
// The fixture deliberately mixes: an ANSI-coloured cell, the ⚠ approval marker
// and ★ star prefix (both ambiguous width-1), a genuinely width-2 CJK glyph
// ("编辑", four display cells for two runes — which tabwriter's rune count would
// get wrong), empty interior cells, and a long name.
func TestRenderRowsAlignsColoredAndWideCells(t *testing.T) {
	green := client.StatusColor("running")

	rows := [][]string{
		{"NAME", "STATUS", "ACTIVITY", "GIT", "AGE"},
		{"braw", colorize("running", green, true), "⚠ approval", colorize("dirty, 1 ahead", green, true), "1h"},
		{"a-much-longer-session-name", colorize("stopped", green, true), "", "", "2h"},
		{"★ canny", colorize("errored", green, true), "编辑 (Bash)", "3 ahead", "2d"},
	}

	// Column widths as renderRows computes them: the max visible width per column.
	widths := make([]int, len(rows[0]))

	for _, r := range rows {
		for i, cell := range r {
			if w := ansi.StringWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	var buf bytes.Buffer

	renderRows(&buf, rows)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(rows) {
		t.Fatalf("rendered %d lines, want %d:\n%s", len(lines), len(rows), buf.String())
	}

	for li, line := range lines {
		visible := ansi.Strip(line)
		starts := columnStarts(rows[li], widths)

		for c, cell := range rows[li] {
			plain := ansi.Strip(cell)
			if plain == "" {
				continue
			}

			// The cell's visible content must begin exactly at its column's
			// display offset — this is the alignment the old renderer broke.
			seg := displaySlice(visible, starts[c])
			if !strings.HasPrefix(seg, plain) {
				t.Errorf("row %d col %d: content %q not at display offset %d\n  line=%q\n  from=%q",
					li, c, plain, starts[c], visible, seg)
			}
		}
	}
}

// displaySlice returns the suffix of s that begins at the given display-cell
// offset. Used to check that a cell's visible content lands at its column start.
func displaySlice(s string, displayOff int) string {
	off := 0
	runes := []rune(s)

	for i, r := range runes {
		if off >= displayOff {
			return string(runes[i:])
		}

		off += ansi.StringWidth(string(r))
	}

	return ""
}
