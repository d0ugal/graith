package client

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"negative maxLen", "hello", -1, ""},
		{"zero maxLen", "hello", 0, ""},
		{"maxLen 1", "hello", 1, "h"},
		{"maxLen 3", "hello", 3, "hel"},
		{"maxLen 4", "hello world", 4, "h..."},
		{"maxLen equals len", "hello", 5, "hello"},
		{"maxLen exceeds len", "hello", 10, "hello"},
		{"empty string", "", 5, ""},
		{"empty string zero maxLen", "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestApprovalModelViewNarrowTerminal(t *testing.T) {
	approvals := []protocol.ApprovalInfo{
		{
			RequestID:   "1",
			SessionName: "braw-session",
			ToolName:    "Bash",
			ToolInput:   `{"command":"echo hello world"}`,
			Agent:       "claude",
		},
		{
			RequestID:   "2",
			SessionName: "braw-session",
			ToolName:    "Write",
			ToolInput:   `{"file_path":"/tmp/test.go","content":"package main\nfunc main() {}\n"}`,
		},
		{
			RequestID:   "3",
			SessionName: "braw-session",
			ToolName:    "Read",
			ToolInput:   "not json at all",
		},
		{
			RequestID:   "4",
			SessionName: "braw-session",
			ToolName:    "CustomTool",
			ToolInput:   `{"longkey":"some value","another":"data"}`,
		},
	}

	for _, width := range []int{0, 1, 2, 3, 4, 5, 8, 20} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := approvalModel{
				approvals: approvals,
				width:     width,
				height:    40,
			}

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("View() panicked at width=%d: %v", width, r)
				}
			}()

			m.View()
		})
	}
}

func approvalTestList() []protocol.ApprovalInfo {
	return []protocol.ApprovalInfo{
		{RequestID: "braw-1", SessionName: "braw", ToolName: "Bash", ToolInput: `{"command":"ls -la"}`, Agent: "claude"},
		{RequestID: "canny-2", SessionName: "canny", ToolName: "Write", ToolInput: `{"file_path":"/a/b/c.go","content":"x"}`},
		{RequestID: "dreich-3", SessionName: "dreich", ToolName: "Read", ToolInput: `{"file_path":"/a/b/c.go"}`},
	}
}

func updateApproval(m approvalModel, s string) approvalModel {
	res, _ := m.Update(tea.KeyPressMsg{Code: rune(s[0]), Text: s})
	return res.(approvalModel)
}

func updateApprovalKey(m approvalModel, k rune) (approvalModel, tea.Cmd) {
	res, cmd := m.Update(tea.KeyPressMsg{Code: k})
	return res.(approvalModel), cmd
}

func TestApprovalModelInitAndConstructor2(t *testing.T) {
	m := newApprovalModel(approvalTestList())
	if len(m.approvals) != 3 {
		t.Fatalf("constructor stored %d approvals, want 3", len(m.approvals))
	}

	if m.Init() != nil {
		t.Error("Init should return nil cmd")
	}
}

func TestApprovalModelNavigationClamps2(t *testing.T) {
	m := newApprovalModel(approvalTestList())

	// Up at top stays at 0.
	m = updateApproval(m, "k")
	if m.cursor != 0 {
		t.Fatalf("k at top: cursor=%d want 0", m.cursor)
	}

	m = updateApproval(m, "j")

	m = updateApproval(m, "j")
	if m.cursor != 2 {
		t.Fatalf("after 2×j: cursor=%d want 2", m.cursor)
	}

	// Down at bottom stays at last index.
	m = updateApproval(m, "j")
	if m.cursor != 2 {
		t.Fatalf("j at bottom: cursor=%d want 2", m.cursor)
	}

	m = updateApproval(m, "k")
	if m.cursor != 1 {
		t.Fatalf("after k: cursor=%d want 1", m.cursor)
	}
}

func TestApprovalModelAllowRemovesAndRecordsResult2(t *testing.T) {
	m := newApprovalModel(approvalTestList())

	m = updateApproval(m, "y")
	if len(m.approvals) != 2 {
		t.Fatalf("after allow: %d approvals remain, want 2", len(m.approvals))
	}

	if len(m.results) != 1 || m.results[0].Decision != "allow" || m.results[0].RequestID != "braw-1" {
		t.Fatalf("allow result wrong: %+v", m.results)
	}
}

func TestApprovalModelDenyRecordsReason2(t *testing.T) {
	m := newApprovalModel(approvalTestList())

	m = updateApproval(m, "n")
	if m.results[0].Decision != "block" || m.results[0].Reason != "denied by user" {
		t.Fatalf("deny result wrong: %+v", m.results[0])
	}

	// 'x' is an alias for deny.
	m = updateApproval(m, "x")
	if len(m.results) != 2 || m.results[1].Decision != "block" {
		t.Fatalf("x-deny result wrong: %+v", m.results)
	}
}

func TestApprovalModelCursorClampsWhenLastRemoved2(t *testing.T) {
	m := newApprovalModel(approvalTestList())
	m = updateApproval(m, "j")
	m = updateApproval(m, "j") // cursor at last (index 2)

	m = updateApproval(m, "y") // allow last; cursor must step back
	if m.cursor != 1 {
		t.Fatalf("cursor after removing last = %d, want 1", m.cursor)
	}

	if len(m.approvals) != 2 {
		t.Fatalf("approvals left = %d, want 2", len(m.approvals))
	}
}

func TestApprovalModelAllowLastQuits2(t *testing.T) {
	m := newApprovalModel([]protocol.ApprovalInfo{{RequestID: "neep", SessionName: "neep", ToolName: "Bash"}})

	res, cmd := updateApprovalKey(m, tea.KeyEnter) // enter == allow
	if len(res.approvals) != 0 {
		t.Fatalf("expected empty after allowing only approval, got %d", len(res.approvals))
	}

	if cmd == nil {
		t.Error("expected tea.Quit when last approval cleared")
	}
}

func TestApprovalModelAllowAllQuits2(t *testing.T) {
	m := newApprovalModel(approvalTestList())

	res, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	got := res.(approvalModel)

	if len(got.results) != 3 {
		t.Fatalf("allow-all recorded %d results, want 3", len(got.results))
	}

	if got.approvals != nil {
		t.Error("allow-all should clear approvals slice")
	}

	if cmd == nil {
		t.Error("allow-all should return tea.Quit")
	}
}

func TestApprovalModelQuitKeys2(t *testing.T) {
	// 'q' quits.
	if _, cmd := m2Update(newApprovalModel(approvalTestList()), tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Error("q should quit")
	}

	// esc quits.
	if _, cmd := m2Update(newApprovalModel(approvalTestList()), tea.KeyPressMsg{Code: tea.KeyEscape}); cmd == nil {
		t.Error("esc should quit")
	}
}

func m2Update(m approvalModel, msg tea.Msg) (approvalModel, tea.Cmd) {
	res, cmd := m.Update(msg)
	return res.(approvalModel), cmd
}

func TestApprovalModelWindowSize2(t *testing.T) {
	m := newApprovalModel(approvalTestList())
	res, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = res.(approvalModel)

	if m.width != 100 || m.height != 40 {
		t.Fatalf("window size not stored: w=%d h=%d", m.width, m.height)
	}
}

func TestApprovalModelActionsNoopOnEmpty2(t *testing.T) {
	m := newApprovalModel(nil)

	// allow/deny with no approvals must not panic or record anything.
	m = updateApproval(m, "y")
	m = updateApproval(m, "n")

	if len(m.results) != 0 {
		t.Fatalf("no approvals should yield no results, got %d", len(m.results))
	}
}

func TestFormatToolSummaryVariants2(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"no input", "Bash", "", "Bash"},
		{"bad json", "Bash", "not json", "Bash"},
		{"bash short", "Bash", `{"command":"ls"}`, "Bash: ls"},
		{"bash first line", "Bash", `{"command":"echo hi\nsecond"}`, "Bash: echo hi"},
		{"write", "Write", `{"file_path":"/a/b.go"}`, "Write: /a/b.go"},
		{"edit", "Edit", `{"file_path":"/a/b.go"}`, "Edit: /a/b.go"},
		{"read", "Read", `{"file_path":"/a/b.go"}`, "Read: /a/b.go"},
		{"skill", "Skill", `{"skill":"ship-it"}`, "Skill: ship-it"},
		{"agent", "Agent", `{"description":"do it"}`, "Agent: do it"},
		{"unknown tool", "Whatsit", `{"x":"y"}`, "Whatsit"},
		{"bash missing command", "Bash", `{"other":"x"}`, "Bash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatToolSummary(tt.tool, tt.input); got != tt.want {
				t.Errorf("formatToolSummary(%q,%q) = %q, want %q", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatToolSummaryTruncatesLongBash2(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := formatToolSummary("Bash", `{"command":"`+long+`"}`)

	if !strings.HasSuffix(got, "...") {
		t.Errorf("long bash command should be truncated with ellipsis: %q", got)
	}

	if len(got) > 60 {
		t.Errorf("truncated summary too long: %d chars", len(got))
	}
}

func TestFormatToolDetailVariants2(t *testing.T) {
	tests := []struct {
		name     string
		info     protocol.ApprovalInfo
		contains []string
	}{
		{
			"bash with agent",
			protocol.ApprovalInfo{SessionName: "braw", Agent: "claude", ToolName: "Bash", ToolInput: `{"command":"go test ./..."}`},
			[]string{"braw", "claude", "go test"},
		},
		{
			"no input",
			protocol.ApprovalInfo{SessionName: "canny", ToolName: "Bash"},
			[]string{"canny"},
		},
		{
			"invalid json",
			protocol.ApprovalInfo{SessionName: "dreich", ToolName: "Bash", ToolInput: "garbage{"},
			[]string{"Input"},
		},
		{
			"write with content",
			protocol.ApprovalInfo{SessionName: "whin", ToolName: "Write", ToolInput: `{"file_path":"/x.go","content":"line1\nline2"}`},
			[]string{"/x.go", "line1"},
		},
		{
			"edit old/new",
			protocol.ApprovalInfo{SessionName: "glen", ToolName: "Edit", ToolInput: `{"file_path":"/x.go","old_string":"a","new_string":"b"}`},
			[]string{"/x.go", "old", "new"},
		},
		{
			"skill with args",
			protocol.ApprovalInfo{SessionName: "kirk", ToolName: "Skill", ToolInput: `{"skill":"ship-it","args":"--fast"}`},
			[]string{"ship-it", "--fast"},
		},
		{
			"agent with prompt",
			protocol.ApprovalInfo{SessionName: "ben", ToolName: "Agent", ToolInput: `{"description":"scout","prompt":"go find things"}`},
			[]string{"scout", "go find"},
		},
		{
			"default renders keys",
			protocol.ApprovalInfo{SessionName: "haar", ToolName: "Custom", ToolInput: `{"weird":"value\nwith newline"}`},
			[]string{"weird", "value"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolDetail(tt.info, 80)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("detail for %s missing %q:\n%s", tt.name, want, got)
				}
			}
		})
	}
}

func TestFormatToolDetailWriteTruncatesManyLines2(t *testing.T) {
	// JSON-encode the content so the embedded newlines are valid.
	content := strings.TrimRight(strings.Repeat("x\n", 30), "\n")

	encoded, err := json.Marshal(map[string]string{"file_path": "/f", "content": content})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}

	info := protocol.ApprovalInfo{SessionName: "loch", ToolName: "Write", ToolInput: string(encoded)}

	got := formatToolDetail(info, 80)
	if !strings.Contains(got, "more lines") {
		t.Errorf("expected a 'more lines' truncation marker:\n%s", got)
	}
}

func TestTruncateBlock2(t *testing.T) {
	if got := truncateBlock("a\nb", 5); got != "a\nb" {
		t.Errorf("under limit should be unchanged: %q", got)
	}

	got := truncateBlock("a\nb\nc\nd", 2)
	if !strings.HasPrefix(got, "a\nb") || !strings.Contains(got, "more lines") {
		t.Errorf("truncateBlock over limit wrong: %q", got)
	}
}

func TestShortPath2(t *testing.T) {
	if got := shortPath("a/b/c"); got != "a/b/c" {
		t.Errorf("<=3 segments unchanged, got %q", got)
	}

	if got := shortPath("/one/two/three/four/five"); got != ".../three/four/five" {
		t.Errorf("shortPath long = %q", got)
	}
}

func TestFirstLine2(t *testing.T) {
	if got := firstLine("no newline"); got != "no newline" {
		t.Errorf("firstLine no-newline = %q", got)
	}

	if got := firstLine("first\nsecond"); got != "first" {
		t.Errorf("firstLine = %q", got)
	}
}

func TestWrapLinesLongWord2(t *testing.T) {
	// A single word longer than maxWidth is hard-split.
	out := wrapLines(strings.Repeat("z", 25), 10)
	for _, line := range out {
		if len(line) > 10 {
			t.Errorf("wrapped line exceeds maxWidth: %q (%d)", line, len(line))
		}
	}

	// Zero maxWidth falls back to 80 and doesn't panic.
	if got := wrapLines("hello world", 0); len(got) == 0 {
		t.Error("wrapLines with zero width returned nothing")
	}
}
