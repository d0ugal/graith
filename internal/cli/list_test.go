package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

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

// TestFormatAgentStatusCov covers each branch: a non-running session shows no
// agent status at all, an approval-pending session gets the ⚠ prefix, an active
// session with a tool name is annotated, and a plain active status passes
// through unchanged.
func TestFormatAgentStatusCov(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{
			name: "not running clears agent status",
			in:   protocol.SessionInfo{Status: "stopped", AgentStatus: "active"},
			want: "",
		},
		{
			name: "approval gets warning glyph",
			in:   protocol.SessionInfo{Status: "running", AgentStatus: "approval"},
			want: "⚠ approval",
		},
		{
			name: "active with tool name is annotated",
			in:   protocol.SessionInfo{Status: "running", AgentStatus: "active", ToolName: "Bash"},
			want: "active (Bash)",
		},
		{
			name: "active without tool name passes through",
			in:   protocol.SessionInfo{Status: "running", AgentStatus: "active"},
			want: "active",
		},
		{
			name: "idle status passes through",
			in:   protocol.SessionInfo{Status: "running", AgentStatus: "idle"},
			want: "idle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatAgentStatus(tt.in); got != tt.want {
				t.Errorf("formatAgentStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatModelCov(t *testing.T) {
	if got := formatModel(protocol.SessionInfo{Model: "claude-opus-4-8"}); got != "claude-opus-4-8" {
		t.Errorf("formatModel = %q, want claude-opus-4-8", got)
	}

	if got := formatModel(protocol.SessionInfo{}); got != "" {
		t.Errorf("formatModel empty = %q, want empty", got)
	}
}

// TestFormatBranchCov covers the three-segment worktree branch (which is
// stripped to its leaf), a plain branch, the in-place marker, and the empty
// non-in-place case.
func TestFormatBranchCov(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{
			name: "three-part branch strips to leaf",
			in:   protocol.SessionInfo{Branch: "d0ugal/graith/fix-overlay"},
			want: "fix-overlay",
		},
		{
			name: "leaf keeps embedded slashes beyond the third segment",
			in:   protocol.SessionInfo{Branch: "user/repo/feature/nested"},
			want: "feature/nested",
		},
		{
			name: "two-part branch is left intact",
			in:   protocol.SessionInfo{Branch: "origin/main"},
			want: "origin/main",
		},
		{
			name: "in-place session with no branch",
			in:   protocol.SessionInfo{InPlace: true},
			want: "(in-place)",
		},
		{
			name: "empty branch, not in-place",
			in:   protocol.SessionInfo{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBranch(tt.in); got != tt.want {
				t.Errorf("formatBranch = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatPRCov covers a session with no PR, a plain open PR, a conflicting
// PR, and each CI state annotation.
func TestFormatPRCov(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{
			name: "no PR",
			in:   protocol.SessionInfo{},
			want: "",
		},
		{
			name: "open PR",
			in:   protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 42, State: "open"}},
			want: "#42 open",
		},
		{
			name: "conflicting PR",
			in:   protocol.SessionInfo{PullRequest: &protocol.PRInfo{Number: 7, State: "open", Conflicting: true}},
			want: "#7 open conflict",
		},
		{
			name: "CI passing",
			in: protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 1, State: "open"},
				CI:          &protocol.CIInfo{State: "passing"},
			},
			want: "#1 open CI:ok",
		},
		{
			name: "CI failing",
			in: protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 2, State: "draft"},
				CI:          &protocol.CIInfo{State: "failing"},
			},
			want: "#2 draft CI:fail",
		},
		{
			name: "CI pending",
			in: protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 3, State: "open"},
				CI:          &protocol.CIInfo{State: "pending"},
			},
			want: "#3 open CI:…",
		},
		{
			name: "CI present but empty state adds nothing",
			in: protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 4, State: "merged"},
				CI:          &protocol.CIInfo{State: ""},
			},
			want: "#4 merged",
		},
		{
			name: "conflict and CI combine",
			in: protocol.SessionInfo{
				PullRequest: &protocol.PRInfo{Number: 5, State: "open", Conflicting: true},
				CI:          &protocol.CIInfo{State: "failing"},
			},
			want: "#5 open conflict CI:fail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPR(tt.in); got != tt.want {
				t.Errorf("formatPR = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatGitStatusCov covers clean, dirty-only, ahead-only, and the combined
// "dirty, N ahead" rendering.
func TestFormatGitStatusCov(t *testing.T) {
	tests := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{"clean", protocol.SessionInfo{}, ""},
		{"dirty only", protocol.SessionInfo{Dirty: true}, "dirty"},
		{"ahead only", protocol.SessionInfo{UnpushedCount: 3}, "3 ahead"},
		{"dirty and ahead", protocol.SessionInfo{Dirty: true, UnpushedCount: 2}, "dirty, 2 ahead"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatGitStatus(tt.in); got != tt.want {
				t.Errorf("formatGitStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatAgeCov checks that a valid RFC3339 timestamp yields a duration and
// an unparseable one yields an empty string.
func TestFormatAgeCov(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	created := now.Add(-90 * time.Minute).Format(time.RFC3339)
	if got := formatAge(protocol.SessionInfo{CreatedAt: created}, now); got == "" {
		t.Error("expected non-empty age for valid timestamp")
	}

	if got := formatAge(protocol.SessionInfo{CreatedAt: "not-a-time"}, now); got != "" {
		t.Errorf("expected empty age for invalid timestamp, got %q", got)
	}

	if got := formatAge(protocol.SessionInfo{}, now); got != "" {
		t.Errorf("expected empty age for missing timestamp, got %q", got)
	}
}

// TestFormatAttachedCov checks the "N ago" suffix for a valid timestamp and the
// empty result for missing/invalid input.
func TestFormatAttachedCov(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	attached := now.Add(-5 * time.Minute).Format(time.RFC3339)

	got := formatAttached(protocol.SessionInfo{LastAttachedAt: attached}, now)
	if got == "" || !bytes.HasSuffix([]byte(got), []byte(" ago")) {
		t.Errorf("formatAttached = %q, want a non-empty value ending in ' ago'", got)
	}

	if got := formatAttached(protocol.SessionInfo{}, now); got != "" {
		t.Errorf("expected empty for never-attached, got %q", got)
	}

	if got := formatAttached(protocol.SessionInfo{LastAttachedAt: "bogus"}, now); got != "" {
		t.Errorf("expected empty for invalid timestamp, got %q", got)
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

	if !bytes.Contains([]byte(out), []byte("NAME")) || !bytes.Contains([]byte(out), []byte("BRANCH")) {
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
