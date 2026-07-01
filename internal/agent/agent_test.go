package agent

import (
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "no env vars",
			env:  nil,
			want: false,
		},
		{
			name: "GR_AGENT_MODE=1 enables",
			env:  map[string]string{"GR_AGENT_MODE": "1"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=true enables",
			env:  map[string]string{"GR_AGENT_MODE": "true"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=yes enables case-insensitive",
			env:  map[string]string{"GR_AGENT_MODE": "YES"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=0 disables even with other vars",
			env:  map[string]string{"GR_AGENT_MODE": "0", "GRAITH_SESSION_ID": "canny-abc"},
			want: false,
		},
		{
			name: "GR_AGENT_MODE=false disables",
			env:  map[string]string{"GR_AGENT_MODE": "false"},
			want: false,
		},
		{
			name: "GR_AGENT_MODE=no disables",
			env:  map[string]string{"GR_AGENT_MODE": "NO"},
			want: false,
		},
		{
			name: "GR_AGENT_MODE=invalid treated as not set",
			env:  map[string]string{"GR_AGENT_MODE": "maybe"},
			want: false,
		},
		{
			name: "GRAITH_SESSION_ID enables",
			env:  map[string]string{"GRAITH_SESSION_ID": "canny-123"},
			want: true,
		},
		{
			name: "CLAUDECODE enables",
			env:  map[string]string{"CLAUDECODE": "1"},
			want: true,
		},
		{
			name: "CLAUDE_CODE enables",
			env:  map[string]string{"CLAUDE_CODE": "1"},
			want: true,
		},
		{
			name: "CURSOR_AGENT enables",
			env:  map[string]string{"CURSOR_AGENT": "1"},
			want: true,
		},
		{
			name: "GITHUB_COPILOT enables",
			env:  map[string]string{"GITHUB_COPILOT": "1"},
			want: true,
		},
		{
			name: "AMAZON_Q enables",
			env:  map[string]string{"AMAZON_Q": "1"},
			want: true,
		},
		{
			name: "OPENCODE enables",
			env:  map[string]string{"OPENCODE": "1"},
			want: true,
		},
		{
			name: "GR_AGENT_MODE=0 overrides CLAUDECODE",
			env:  map[string]string{"GR_AGENT_MODE": "0", "CLAUDECODE": "1"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				v, ok := tt.env[key]
				return v, ok
			}

			got := detect(lookup)
			if got != tt.want {
				t.Errorf("detect() = %v, want %v", got, tt.want)
			}
		})
	}
}
