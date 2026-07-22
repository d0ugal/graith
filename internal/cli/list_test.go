package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
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

func TestApplyListFiltersLabelsUseANDAndCompose(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "braw", Name: "braw", RepoPath: "/croft", Starred: true, Labels: []string{"Urgent", "release"}},
		{ID: "canny", Name: "canny", RepoPath: "/bothy", Starred: true, Labels: []string{"urgent"}},
		{ID: "dreich", Name: "dreich", RepoPath: "/croft", Starred: false, Labels: []string{"release", "urgent"}},
	}

	got, err := applyListFilters(sessions, "", "/croft", true, []string{"urgent", "RELEASE"})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].ID != "braw" {
		t.Fatalf("filtered sessions = %+v, want only braw", got)
	}

	if _, err := applyListFilters(sessions, "", "", false, []string{""}); err == nil {
		t.Fatal("empty label filter should be rejected")
	}
}

func TestListLabelFlagIsRepeatable(t *testing.T) {
	registerCommands()

	flag := listCmd.Flags().Lookup("label")
	if flag == nil || flag.Value.Type() != "stringArray" {
		t.Fatalf("list --label = %#v, want repeatable stringArray", flag)
	}
}

func TestListDisplayFlagsAndAlias(t *testing.T) {
	registerCommands()

	for _, name := range []string{"list", "ls"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %q: %v", name, err)
		}

		if cmd != listCmd {
			t.Errorf("%q resolves to %q, want list command", name, cmd.Name())
		}
	}

	flat := listCmd.Flags().Lookup("flat")
	tree := listCmd.Flags().Lookup("tree")

	if flat == nil || !strings.Contains(flat.Usage, "flat repo/name order") {
		t.Fatalf("list --flat = %#v, want documented flat ordering", flat)
	}

	if tree == nil || !strings.Contains(tree.Usage, "deprecated no-op") {
		t.Fatalf("list --tree = %#v, want deprecated compatibility no-op", tree)
	}

	if !strings.Contains(listCmd.Long, "hierarchy") || !strings.Contains(listCmd.Long, "--flat") {
		t.Errorf("list help does not explain the default and migration: %q", listCmd.Long)
	}

	origFlatChanged := flat.Changed
	origTreeChanged := tree.Changed

	t.Cleanup(func() {
		flat.Changed = origFlatChanged
		tree.Changed = origTreeChanged
	})

	flat.Changed = true
	tree.Changed = true

	err := listCmd.Args(listCmd, nil)
	if err == nil {
		t.Fatal("--flat and --tree should be mutually exclusive")
	}

	wantErr := "--tree and --flat are mutually exclusive; use --flat for flat output or omit both for tree output"
	if err.Error() != wantErr {
		t.Errorf("conflict error = %q, want %q", err, wantErr)
	}

	if err := listCmd.ValidateFlagGroups(); err == nil {
		t.Error("Cobra flag group should also reject --flat with --tree")
	}
}

func TestSessionInfoLessBreaksDuplicateNameTiesByID(t *testing.T) {
	a := protocol.SessionInfo{ID: "braw", Name: "canny", RepoName: "croft"}
	b := protocol.SessionInfo{ID: "thrawn", Name: "canny", RepoName: "croft"}

	if !sessionInfoLess(a, b) {
		t.Errorf("sessionInfoLess(%+v, %+v) = false, want ID tie-break", a, b)
	}

	if sessionInfoLess(b, a) {
		t.Errorf("sessionInfoLess(%+v, %+v) = true, want false", b, a)
	}
}

type listRunOptions struct {
	json   bool
	flat   bool
	tree   bool
	tokens bool
}

func runListFixture(t *testing.T, sessions []protocol.SessionInfo, opts listRunOptions) string {
	t.Helper()

	origConnect := listConnectFn
	origOut := out
	origJSON := jsonOutput
	origCfg := cfg
	origPaths := paths
	origRepo := listRepo
	origTree := listTree
	origFlat := listFlat
	origChildren := listChildren
	origStarred := listStarred
	origQuiet := listQuiet
	origWide := listWide
	origTokens := listTokens
	origNoColor := listNoColor
	origDeleted := listDeleted
	origLabels := listLabels

	defer func() {
		listConnectFn = origConnect
		out = origOut
		jsonOutput = origJSON
		cfg = origCfg
		paths = origPaths
		listRepo = origRepo
		listTree = origTree
		listFlat = origFlat
		listChildren = origChildren
		listStarred = origStarred
		listQuiet = origQuiet
		listWide = origWide
		listTokens = origTokens
		listNoColor = origNoColor
		listDeleted = origDeleted
		listLabels = origLabels
	}()

	fake := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", protocol.SessionListMsg{
		Sessions: sessions,
	}))}}
	listConnectFn = func(*config.Config, config.Paths, string) (listConn, error) {
		return fake, nil
	}

	jsonOutput = opts.json
	listRepo = ""
	listTree = opts.tree
	listFlat = opts.flat
	listChildren = ""
	listStarred = false
	listQuiet = false
	listWide = false
	listTokens = opts.tokens
	listNoColor = true
	listDeleted = false
	listLabels = nil
	paths = config.Paths{}
	cfg = config.Default()
	cfg.Updates.Enabled = false

	var buf bytes.Buffer

	out = output.NewWithWriter(jsonOutput, &buf)
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runList(cmd, nil); err != nil {
		t.Fatalf("runList(%+v): %v", opts, err)
	}

	if fake.closed != 1 {
		t.Errorf("list connection closed %d times, want once", fake.closed)
	}

	return buf.String()
}

func TestRunListHumanDisplayModes(t *testing.T) {
	created := time.Now().Add(-time.Hour).Format(time.RFC3339)
	sessions := []protocol.SessionInfo{
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", Agent: "codex", Status: "running", CreatedAt: created, Starred: true},
		{ID: "ben", Name: "hame", RepoName: "croft", Agent: "claude", Status: "running", CreatedAt: created},
	}

	defaultOutput := runListFixture(t, sessions, listRunOptions{})
	if !strings.Contains(defaultOutput, "`-- ★ bairn") {
		t.Fatalf("default human list is not a tree:\n%s", defaultOutput)
	}

	if strings.Index(defaultOutput, "hame") > strings.Index(defaultOutput, "bairn") {
		t.Errorf("default tree should place parent before child:\n%s", defaultOutput)
	}

	compatOutput := runListFixture(t, sessions, listRunOptions{tree: true})
	if compatOutput != defaultOutput {
		t.Errorf("deprecated --tree alias changed output:\n--- default ---\n%s--- --tree ---\n%s", defaultOutput, compatOutput)
	}

	flatOutput := runListFixture(t, sessions, listRunOptions{flat: true})
	if strings.Contains(flatOutput, "|--") || strings.Contains(flatOutput, "`--") {
		t.Errorf("--flat output contains tree connectors:\n%s", flatOutput)
	}

	if strings.Index(flatOutput, "bairn") > strings.Index(flatOutput, "hame") {
		t.Errorf("--flat should use canonical repo/name order:\n%s", flatOutput)
	}

	defaultTokens := runListFixture(t, sessions, listRunOptions{tokens: true})
	if !strings.Contains(defaultTokens, "`-- ★ bairn") {
		t.Errorf("default token projection is not a tree:\n%s", defaultTokens)
	}

	flatTokens := runListFixture(t, sessions, listRunOptions{tokens: true, flat: true})
	if strings.Contains(flatTokens, "|--") || strings.Contains(flatTokens, "`--") {
		t.Errorf("--tokens --flat contains tree connectors:\n%s", flatTokens)
	}
}

func TestRunListJSONIgnoresDisplayFlagsAndPreservesSchema(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{
			ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", Agent: "codex",
			Labels: []string{"urgent"}, Tokens: &protocol.TokenInfo{Input: 2, Output: 3, Total: 5},
		},
		{ID: "ben", Name: "hame", RepoName: "croft", Agent: "claude", Labels: []string{}},
	}

	tests := []struct {
		name string
		opts listRunOptions
	}{
		{name: "JSON default", opts: listRunOptions{json: true}},
		{name: "JSON deprecated tree alias", opts: listRunOptions{json: true, tree: true}},
		{name: "JSON flat", opts: listRunOptions{json: true, flat: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runListFixture(t, sessions, tt.opts)

			var decoded protocol.SessionListMsg
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("decode SessionListMsg: %v\n%s", err, got)
			}

			if !reflect.DeepEqual(decoded.Sessions, sessions) {
				t.Errorf("sessions changed order or shape:\n got: %+v\nwant: %+v", decoded.Sessions, sessions)
			}

			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(got), &raw); err != nil {
				t.Fatalf("decode raw JSON: %v", err)
			}

			if len(raw) != 1 || raw["sessions"] == nil {
				t.Errorf("JSON schema changed, top-level fields = %v", raw)
			}
		})
	}
}

func TestRunListAgentModeUsesStructuredJSONWithFlat(t *testing.T) {
	registerCommands()

	origConnect := listConnectFn
	origOut := out
	origJSON := jsonOutput
	origAgentMode := agentMode
	origCfgFile := cfgFile
	origListFlat := listFlat
	origListTree := listTree
	agentFlag := rootCmd.PersistentFlags().Lookup("agent-mode")
	flatFlag := listCmd.Flags().Lookup("flat")
	treeFlag := listCmd.Flags().Lookup("tree")
	origAgentChanged := agentFlag.Changed
	origFlatChanged := flatFlag.Changed
	origTreeChanged := treeFlag.Changed

	t.Cleanup(func() {
		listConnectFn = origConnect
		out = origOut
		jsonOutput = origJSON
		agentMode = origAgentMode
		cfgFile = origCfgFile
		listFlat = origListFlat
		listTree = origListTree
		agentFlag.Changed = origAgentChanged
		flatFlag.Changed = origFlatChanged
		treeFlag.Changed = origTreeChanged
	})

	sessions := []protocol.SessionInfo{
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", Labels: []string{}},
		{ID: "ben", Name: "hame", RepoName: "croft", Labels: []string{"release"}},
	}
	fake := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", protocol.SessionListMsg{
		Sessions: sessions,
	}))}}
	listConnectFn = func(*config.Config, config.Paths, string) (listConn, error) {
		return fake, nil
	}

	jsonOutput = false
	agentMode = false
	cfgFile = ""
	listFlat = false
	listTree = false

	t.Setenv("GR_AGENT_MODE", "0")

	var runErr error

	got := captureStdout(t, func() {
		runErr = executeWithArgs([]string{"--agent-mode", "list", "--flat"})
	})
	if runErr != nil {
		t.Fatalf("agent-mode list --flat: %v", runErr)
	}

	var decoded protocol.SessionListMsg
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("agent-mode output is not structured SessionListMsg JSON: %v\n%s", err, got)
	}

	if !reflect.DeepEqual(decoded.Sessions, sessions) {
		t.Errorf("agent-mode --flat changed sessions:\n got: %+v\nwant: %+v", decoded.Sessions, sessions)
	}
}

func TestOrderSessionTreeIncludesOrphansAndCyclesDeterministically(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "bairn", ParentID: "auld", Name: "bairn", RepoName: "croft"},
		{ID: "dreich", ParentID: "missing", Name: "dreich", RepoName: "croft"},
		{ID: "auld", ParentID: "bairn", Name: "auld", RepoName: "croft"},
		{ID: "canny", Name: "canny", RepoName: "croft"},
	}

	rows := orderSessionTree(sessions)
	wantNames := []string{"canny", "dreich", "auld", "bairn"}
	wantPrefixes := []string{"", "", "", "`-- "}

	if len(rows) != len(wantNames) {
		t.Fatalf("tree rows = %+v, want %d sessions", rows, len(wantNames))
	}

	for i := range wantNames {
		if rows[i].session.Name != wantNames[i] || rows[i].prefix != wantPrefixes[i] {
			t.Errorf("tree row %d = %q/%q, want %q/%q", i, rows[i].session.Name, rows[i].prefix, wantNames[i], wantPrefixes[i])
		}
	}

	tokenRows := orderTokenProjectionSessions(sessions, true)
	if len(tokenRows) != len(sessions) {
		t.Fatalf("token tree omitted cycle/orphan sessions: %+v", tokenRows)
	}

	for i := range wantNames {
		if tokenRows[i].session.Name != wantNames[i] || tokenRows[i].name != wantPrefixes[i]+wantNames[i] {
			t.Errorf("token tree row %d = %+v, want name %q", i, tokenRows[i], wantPrefixes[i]+wantNames[i])
		}
	}
}

func TestListAndDashboardHaveNoInteractiveCommands(t *testing.T) {
	registerCommands()

	if listCmd.Flags().Lookup("watch") != nil {
		t.Fatal("list command still exposes --watch")
	}

	for _, command := range rootCmd.Commands() {
		if command.Name() == "dashboard" {
			t.Fatal("dashboard command remains registered")
		}
	}

	var completion bytes.Buffer
	if err := rootCmd.GenBashCompletion(&completion); err != nil {
		t.Fatalf("generate completion: %v", err)
	}

	generated := completion.String()
	if strings.Contains(generated, "--watch") {
		t.Error("generated completion still contains list --watch")
	}

	if strings.Contains(generated, "gr_dashboard") {
		t.Error("generated completion still contains dashboard command")
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

	wantWide := []string{"REPO", "AGENT", "STATUS", "ACTIVITY", "MODEL", "BRANCH", "GIT", "PR", "REVIEW", "TOKENS", "TODO", "AGE", "ATTACHED"}
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

	wantCompact := []string{"REPO", "AGENT", "STATUS", "ACTIVITY", "GIT", "PR", "REVIEW", "AGE"}
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
	s := protocol.SessionInfo{Status: "running", AgentStatus: "error"}

	var statusCol, activityCol sessionColumn

	for _, c := range visibleColumns(true) {
		switch c.header {
		case "STATUS":
			statusCol = c
		case "ACTIVITY":
			activityCol = c
		}
	}

	if plain := activityCol.value(s, time.Time{}, false); plain != "error" {
		t.Errorf("activity plain = %q, want %q", plain, "error")
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
			PullRequest: &protocol.PRInfo{Number: 42, State: "open", ReviewDecision: "approved"},
			CI:          &protocol.CIInfo{State: "passing"},
			TodoDone:    1, TodoTotal: 3,
			CreatedAt: created,
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
		"NAME    REPO   AGENT   STATUS   ACTIVITY       GIT             PR              REVIEW    AGE\n" +
		"braw    croft  claude  running  active (Bash)  dirty, 2 ahead  #42 open CI:ok  approved  1h30m\n" +
		"thrawn  croft  codex   stopped                                                           1h30m\n"

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
		"NAME    REPO   AGENT   STATUS   ACTIVITY       MODEL  BRANCH  GIT             PR              REVIEW    TOKENS  TODO  AGE    ATTACHED\n" +
		"braw    croft  claude  running  active (Bash)                 dirty, 2 ahead  #42 open CI:ok  approved          1/3   1h30m  \n" +
		"thrawn  croft  codex   stopped                                                                                        1h30m  \n"

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
// The fixture deliberately mixes: an ANSI-coloured cell, the error marker
// and ★ star prefix (both ambiguous width-1), a genuinely width-2 CJK glyph
// ("编辑", four display cells for two runes — which tabwriter's rune count would
// get wrong), empty interior cells, and a long name.
func TestRenderRowsAlignsColoredAndWideCells(t *testing.T) {
	green := client.StatusColor("running")

	rows := [][]string{
		{"NAME", "STATUS", "ACTIVITY", "GIT", "AGE"},
		{"braw", colorize("running", green, true), "error", colorize("dirty, 1 ahead", green, true), "1h"},
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
