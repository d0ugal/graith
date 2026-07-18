package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestWithCommas(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}

	for _, c := range cases {
		if got := withCommas(c.n); got != c.want {
			t.Errorf("withCommas(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestPrintTokenProjectionPreservesStatesAndTotals(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	sessions := []protocol.SessionInfo{
		{
			ID: "braw", Name: "braw", RepoName: "croft", Agent: "claude",
			Tokens: &protocol.TokenInfo{
				Input: 10, Output: 20, CacheRead: 30, CacheCreation: 40,
				Unclassified: 50, Total: 150, CountedAt: now.Add(-2 * time.Minute).Format(time.RFC3339),
			},
		},
		{
			ID: "canny", Name: "canny", RepoName: "croft", Agent: "codex",
			Tokens: &protocol.TokenInfo{
				Total: 0, Degraded: true, CountedAt: now.Add(-30 * time.Second).Format(time.RFC3339),
			},
		},
		{ID: "dreich", Name: "dreich", RepoName: "bothy", Agent: "cursor"},
		{ID: "thrawn", Name: "thrawn", RepoName: "bothy", Agent: "claude"},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTokenProjection(cmd, sessions, now, false)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want header + 4 sessions + total:\n%s", len(lines), buf.String())
	}

	if !strings.Contains(lines[0], "CACHE-R") || !strings.Contains(lines[0], "COUNTED") {
		t.Errorf("missing token projection columns: %q", lines[0])
	}

	assertLineFields(t, lines, "braw",
		"braw", "croft", "claude", "10", "20", "30", "40", "50", "150", "2m", "ago")
	assertLineFields(t, lines, "canny",
		"canny", "croft", "codex", "0", "0", "0", "0", "0", "0~", "30s", "ago")
	assertLineFields(t, lines, "dreich",
		"dreich", "bothy", "cursor", "—", "—", "—", "—", "—", "(unsupported)", "—")
	assertLineFields(t, lines, "thrawn",
		"thrawn", "bothy", "claude", "—", "—", "—", "—", "—", "(unknown)", "—")
	assertLineFields(t, lines, "TOTAL",
		"TOTAL", "10", "20", "30", "40", "50", "150~", "2/4", "known")
}

func TestPrintTokenProjectionAllUnknownAggregate(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "braw", Name: "braw", Agent: "claude"},
		{ID: "canny", Name: "canny", Agent: "cursor"},
	}

	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printTokenProjection(cmd, sessions, time.Now(), false)

	total := lineStartingWith(t, strings.Split(strings.TrimSpace(buf.String()), "\n"), "TOTAL")
	if !strings.Contains(total, "(unknown)") || !strings.Contains(total, "0/2 known") {
		t.Errorf("all-unknown aggregate must not claim a zero total: %q", total)
	}
}

func TestOrderTokenProjectionSessionsTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "wee", ParentID: "bairn", Name: "wee-bairn", RepoName: "croft"},
		{ID: "root", Name: "hame", RepoName: "croft"},
		{ID: "other", Name: "auld", RepoName: "bothy"},
		{ID: "bairn", ParentID: "root", Name: "bairn", RepoName: "croft", Starred: true},
	}

	rows := orderTokenProjectionSessions(sessions, true)
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.name)
	}

	want := []string{"auld", "hame", "\x60-- ★ bairn", "    \x60-- wee-bairn"}
	if len(got) != len(want) {
		t.Fatalf("tree rows = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tree row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestApplyListFiltersComposeForTokenProjection(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben", RepoName: "croft", RepoPath: "/src/croft"},
		{ID: "bairn", ParentID: "ben", Name: "bairn", RepoName: "croft", RepoPath: "/src/croft", Starred: true},
		{ID: "canny", ParentID: "ben", Name: "canny", RepoName: "bothy", RepoPath: "/src/bothy", Starred: true},
		{ID: "wee", ParentID: "bairn", Name: "wee", RepoName: "croft", RepoPath: "/src/croft"},
	}

	got, err := applyListFilters(sessions, "ben", "croft", true)
	if err != nil {
		t.Fatalf("applyListFilters: %v", err)
	}

	if len(got) != 1 || got[0].ID != "bairn" {
		t.Fatalf("composed children/repo/starred filters = %+v, want bairn only", got)
	}

	if _, err := applyListFilters(sessions, "haar", "", false); err == nil {
		t.Fatal("missing --children session should fail")
	}
}

func TestListTokensJSONUsesCanonicalSessionInfoShape(t *testing.T) {
	origConnect := listConnectFn
	origOut := out
	origJSON := jsonOutput
	origQuiet := listQuiet
	origTokens := listTokens
	origTree := listTree
	origRepo := listRepo
	origChildren := listChildren
	origStarred := listStarred
	origDeleted := listDeleted

	t.Cleanup(func() {
		listConnectFn = origConnect
		out = origOut
		jsonOutput = origJSON
		listQuiet = origQuiet
		listTokens = origTokens
		listTree = origTree
		listRepo = origRepo
		listChildren = origChildren
		listStarred = origStarred
		listDeleted = origDeleted
	})

	wantTokens := &protocol.TokenInfo{
		Input: 1, Output: 2, CacheRead: 3, CacheCreation: 4, Unclassified: 5, Total: 15,
		CountedAt: "2026-07-18T12:00:00Z",
	}
	fake := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", protocol.SessionListMsg{
		Sessions: []protocol.SessionInfo{{ID: "braw", Name: "braw", Agent: "claude", Tokens: wantTokens}},
	}))}}
	listConnectFn = func(*config.Config, config.Paths, string) (listConn, error) {
		return fake, nil
	}

	var buf bytes.Buffer

	jsonOutput = true
	listQuiet = false
	listTokens = true
	listTree = false
	listRepo = ""
	listChildren = ""
	listStarred = false
	listDeleted = false
	out = output.NewWithWriter(true, &buf)

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := listCmd.RunE(cmd, nil); err != nil {
		t.Fatalf("list --tokens --json: %v", err)
	}

	if fake.closed != 1 {
		t.Errorf("list connection closed %d times, want once", fake.closed)
	}

	var decoded protocol.SessionListMsg
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid SessionListMsg JSON: %v\n%s", err, buf.String())
	}

	if len(decoded.Sessions) != 1 || decoded.Sessions[0].Tokens == nil || decoded.Sessions[0].Tokens.Total != 15 {
		t.Fatalf("canonical nested tokens missing from output: %+v", decoded)
	}

	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw JSON: %v", err)
	}

	rawSessions, ok := raw["sessions"].([]any)
	if !ok || len(rawSessions) != 1 {
		t.Fatalf("top-level JSON must contain sessions array: %v", raw)
	}

	session, ok := rawSessions[0].(map[string]any)
	if !ok {
		t.Fatalf("session JSON is not an object: %T", rawSessions[0])
	}

	if _, ok := session["tokens"].(map[string]any); !ok {
		t.Fatalf("session tokens are not nested: %v", session)
	}

	for _, legacy := range []string{"input", "output", "cache_read", "cache_creation", "total"} {
		if _, exists := session[legacy]; exists {
			t.Errorf("legacy flat token field %q leaked into session JSON: %v", legacy, session)
		}
	}
}

func TestListTokensHumanDeletedProjection(t *testing.T) {
	origConnect := listConnectFn
	origOut := out
	origJSON := jsonOutput
	origQuiet := listQuiet
	origWide := listWide
	origTokens := listTokens
	origTree := listTree
	origRepo := listRepo
	origChildren := listChildren
	origStarred := listStarred
	origDeleted := listDeleted
	origPaths := paths

	t.Cleanup(func() {
		listConnectFn = origConnect
		out = origOut
		jsonOutput = origJSON
		listQuiet = origQuiet
		listWide = origWide
		listTokens = origTokens
		listTree = origTree
		listRepo = origRepo
		listChildren = origChildren
		listStarred = origStarred
		listDeleted = origDeleted
		paths = origPaths
	})

	fake := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", protocol.SessionListMsg{
		Sessions: []protocol.SessionInfo{{
			ID: "dreich", Name: "dreich", RepoName: "croft", Agent: "claude",
			Tokens: &protocol.TokenInfo{Input: 10, Output: 5, Total: 15},
		}},
	}))}}
	listConnectFn = func(*config.Config, config.Paths, string) (listConn, error) {
		return fake, nil
	}

	var buf bytes.Buffer

	jsonOutput = false
	listQuiet = false
	listWide = false
	listTokens = true
	listTree = false
	listRepo = ""
	listChildren = ""
	listStarred = false
	listDeleted = true
	paths.Profile = ""
	out = output.NewWithWriter(false, &buf)

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := listCmd.RunE(cmd, nil); err != nil {
		t.Fatalf("list --deleted --tokens: %v", err)
	}

	if len(fake.sends) != 1 || fake.sends[0].Type != "list" {
		t.Fatalf("list sends = %+v, want one list request", fake.sends)
	}

	msg, ok := fake.sends[0].Payload.(protocol.ListMsg)
	if !ok || !msg.Deleted {
		t.Fatalf("list payload = %#v, want Deleted true", fake.sends[0].Payload)
	}

	if !strings.Contains(buf.String(), "SESSION") || !strings.Contains(buf.String(), "dreich") ||
		!strings.Contains(buf.String(), "15") {
		t.Errorf("human token projection missing deleted session detail:\n%s", buf.String())
	}
}

func assertLineFields(t *testing.T, lines []string, prefix string, want ...string) {
	t.Helper()

	got := strings.Fields(lineStartingWith(t, lines, prefix))
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("fields for %q = %v, want %v", prefix, got, want)
	}
}

func lineStartingWith(t *testing.T, lines []string, prefix string) string {
	t.Helper()

	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}

	t.Fatalf("no line starts with %q in:\n%s", prefix, strings.Join(lines, "\n"))

	return ""
}
