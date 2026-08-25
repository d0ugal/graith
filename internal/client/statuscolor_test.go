package client

import (
	"image/color"
	"testing"
)

func TestStatusColor(t *testing.T) {
	tests := []struct {
		status string
		want   any
	}{
		{"running", colorGreen},
		{"errored", colorRed},
		{"stopped", colorDim},
		{"unknown", colorDim},
		{"", colorDim},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := StatusColor(tt.status); got != tt.want {
				t.Errorf("StatusColor(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestAgentStatusColor(t *testing.T) {
	tests := []struct {
		status string
		want   any
	}{
		{"active", colorGreen},
		{"running", colorGreen},
		{"ready", colorBlue},
		{"idle", colorDim},
		{"", colorDim},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := AgentStatusColor(tt.status); got != tt.want {
				t.Errorf("AgentStatusColor(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestDependencyHealthColors(t *testing.T) {
	tests := map[string]struct {
		value string
		color func(string) color.Color
		want  color.Color
	}{
		"operational state": {"operational", DependencyStateColor, colorGreen},
		"degraded state":    {"degraded", DependencyStateColor, colorYellow},
		"down state":        {"down", DependencyStateColor, colorRed},
		"unknown state":     {"unknown", DependencyStateColor, colorDim},
		"empty state":       {"", DependencyStateColor, colorDim},
		"fresh source":      {"fresh", DependencySourceHealthColor, colorGreen},
		"stale source":      {"stale", DependencySourceHealthColor, colorYellow},
		"failed source":     {"failed", DependencySourceHealthColor, colorRed},
		"unknown source":    {"unknown", DependencySourceHealthColor, colorDim},
		"empty source":      {"", DependencySourceHealthColor, colorDim},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := test.color(test.value); got != test.want {
				t.Errorf("color(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
