package client

import "testing"

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
		{"approval", colorRed},
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
