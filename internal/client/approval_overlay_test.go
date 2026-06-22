package client

import (
	"fmt"
	"testing"

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
