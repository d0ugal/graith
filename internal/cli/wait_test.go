package cli

import "testing"

func TestResolveWaitMode(t *testing.T) {
	tests := []struct {
		name        string
		contains    string
		status      string
		idle        bool
		wantMode    string
		wantPattern string
		wantErr     bool
	}{
		{name: "contains", contains: "bonnie.*ready", wantMode: "contains", wantPattern: "bonnie.*ready"},
		{name: "status", status: "stopped", wantMode: "status"},
		{name: "idle", idle: true, wantMode: "idle"},
		{name: "none set", wantErr: true},
		{name: "contains and status", contains: "x", status: "stopped", wantErr: true},
		{name: "status and idle", status: "running", idle: true, wantErr: true},
		{name: "bad regexp", contains: "[unterminated", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waitContains = tt.contains
			waitStatus = tt.status
			waitIdle = tt.idle

			t.Cleanup(func() {
				waitContains = ""
				waitStatus = ""
				waitIdle = false
			})

			mode, pattern, err := resolveWaitMode()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got mode=%q pattern=%q", mode, pattern)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}

			if pattern != tt.wantPattern {
				t.Errorf("pattern = %q, want %q", pattern, tt.wantPattern)
			}
		})
	}
}
