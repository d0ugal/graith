package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestResolveWaitMode(t *testing.T) {
	tests := []struct {
		name        string
		contains    string
		status      string
		idle        bool
		timeout     time.Duration
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
		{name: "unknown status", status: "stoped", wantErr: true},
		{name: "negative timeout", idle: true, timeout: -time.Second, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waitContains = tt.contains
			waitStatus = tt.status
			waitIdle = tt.idle
			waitTimeout = tt.timeout

			t.Cleanup(func() {
				waitContains = ""
				waitStatus = ""
				waitIdle = false
				waitTimeout = 0
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

func TestTimeoutMillis(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want int
	}{
		{"zero means forever", 0, 0},
		{"negative means forever", -time.Second, 0},
		{"whole ms", 250 * time.Millisecond, 250},
		{"seconds", 3 * time.Second, 3000},
		{"sub-ms floors to 1", 500 * time.Microsecond, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeoutMillis(tt.in); got != tt.want {
				t.Errorf("timeoutMillis(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// withJSONCov2 flips the package-global jsonOutput flag (which captureStdout
// reads when rebinding `out`) and restores it afterwards.
func withJSONCov2(t *testing.T, on bool, fn func()) {
	t.Helper()

	prev := jsonOutput
	jsonOutput = on

	defer func() { jsonOutput = prev }()

	fn()
}

func TestReportWaitMatchedCov2MatchedLine(t *testing.T) {
	withJSONCov2(t, false, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{MatchedLine: "tests passed"})
		})

		if !strings.Contains(got, "matched: tests passed") {
			t.Errorf("output %q should report the matched line", got)
		}
	})
}

func TestReportWaitMatchedCov2Status(t *testing.T) {
	withJSONCov2(t, false, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{Status: "stopped"})
		})

		if !strings.Contains(got, "reached status: stopped") {
			t.Errorf("output %q should report the reached status", got)
		}
	})
}

func TestReportWaitMatchedCov2Default(t *testing.T) {
	withJSONCov2(t, false, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{})
		})

		if !strings.Contains(got, "condition met") {
			t.Errorf("output %q should report a generic condition-met message", got)
		}
	})
}

func TestReportWaitMatchedCov2JSON(t *testing.T) {
	withJSONCov2(t, true, func() {
		got := captureStdout(t, func() {
			reportWaitMatched(protocol.WaitMatchedMsg{MatchedLine: "ci green"})
		})

		if !strings.Contains(got, `"matched": true`) {
			t.Errorf("JSON output %q should carry matched=true", got)
		}

		if !strings.Contains(got, "ci green") {
			t.Errorf("JSON output %q should include the matched line", got)
		}
	})
}
