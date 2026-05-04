package client

import (
	"testing"
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
	for _, width := range []int{0, 1, 2, 3, 4, 5, 8} {
		m := approvalModel{width: width, height: 20}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("View() panicked at width=%d: %v", width, r)
			}
		}()
		m.View()
	}
}
