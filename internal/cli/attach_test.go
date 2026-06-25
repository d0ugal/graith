package cli

import (
	"reflect"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestOrderAgents(t *testing.T) {
	agents := map[string]config.Agent{
		"codex":  {},
		"claude": {},
		"cursor": {},
	}

	tests := []struct {
		name   string
		agents map[string]config.Agent
		def    string
		want   []string
	}{
		{
			name:   "sorted with default hoisted to front",
			agents: agents,
			def:    "cursor",
			want:   []string{"cursor", "claude", "codex"},
		},
		{
			name:   "default already first stays first",
			agents: agents,
			def:    "claude",
			want:   []string{"claude", "codex", "cursor"},
		},
		{
			name:   "empty default leaves plain sorted order",
			agents: agents,
			def:    "",
			want:   []string{"claude", "codex", "cursor"},
		},
		{
			name:   "default absent from map leaves plain sorted order",
			agents: agents,
			def:    "thrawn",
			want:   []string{"claude", "codex", "cursor"},
		},
		{
			name:   "empty map yields empty list",
			agents: map[string]config.Agent{},
			def:    "claude",
			want:   []string{},
		},
		{
			name:   "single agent",
			agents: map[string]config.Agent{"claude": {}},
			def:    "claude",
			want:   []string{"claude"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orderAgents(tt.agents, tt.def)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("orderAgents(%v, %q) = %v, want %v", tt.agents, tt.def, got, tt.want)
			}
		})
	}
}
