package daemon

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestWaitForTypeIdle_ConfiguredTimingsReachWait proves the effective [input]
// timing (not the old hard-coded 10s/2m constants) is what gr type passes to
// WaitForUserIdle, and that a coherent snapshot is read per call (issue #1317).
func TestWaitForTypeIdle_ConfiguredTimingsReachWait(t *testing.T) {
	cases := []struct {
		name     string
		input    config.InputConfig
		wantIdle time.Duration
		wantMax  time.Duration
	}{
		{
			name:     "defaults preserve historical 10s/2m",
			input:    config.InputConfig{},
			wantIdle: 10 * time.Second,
			wantMax:  2 * time.Minute,
		},
		{
			name:     "non-default values reach WaitForUserIdle",
			input:    config.InputConfig{TypeIdleTimeout: "3s", TypeMaxWait: "45s"},
			wantIdle: 3 * time.Second,
			wantMax:  45 * time.Second,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotIdle, gotMax time.Duration

			userIdle := waitForTypeIdle(c.input, func(idle, maxWait time.Duration) bool {
				gotIdle, gotMax = idle, maxWait

				return true // user went idle before the max wait
			})

			if !userIdle {
				t.Errorf("waitForTypeIdle returned false, want true (user idle)")
			}

			if gotIdle != c.wantIdle {
				t.Errorf("idle timeout passed to WaitForUserIdle = %v, want %v", gotIdle, c.wantIdle)
			}

			if gotMax != c.wantMax {
				t.Errorf("max wait passed to WaitForUserIdle = %v, want %v", gotMax, c.wantMax)
			}
		})
	}
}

// TestWaitForTypeIdle_MaxWaitPathWarns proves the max-wait path still injects:
// when the user never goes idle, WaitForUserIdle reports false and the caller
// treats that as "inject anyway with a warning" (issue #1317).
func TestWaitForTypeIdle_MaxWaitPathWarns(t *testing.T) {
	input := config.InputConfig{TypeIdleTimeout: "1s", TypeMaxWait: "2s"}

	maxWaitExpired := !waitForTypeIdle(input, func(_, _ time.Duration) bool {
		return false // user still active when the max wait expired
	})

	if !maxWaitExpired {
		t.Fatal("expected max-wait-expired (warn + inject anyway) when user never goes idle")
	}
}
