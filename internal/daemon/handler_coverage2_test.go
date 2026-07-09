package daemon

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
)

// This file covers handler-dispatch branches round 1 did not reach: the
// attach guards for transient session states, the handshake profile-mismatch
// rejection, the per-case authorization checks that stop a session from acting
// on a target it doesn't own, and the two pure helpers that format a session's
// exit for logs/errors.

// --- attach transient-state guards --------------------------------------

func TestCoverAttachSessionCreating(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["haar-creating"] = &SessionState{
		ID: "haar-creating", Name: "haar-creating", Status: StatusCreating,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "haar-creating"})
	h.expectError(t, "is being created")
}

func TestCoverAttachSessionDeleting(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	h.sm.state.Sessions["haar-deleting"] = &SessionState{
		ID: "haar-deleting", Name: "haar-deleting", Status: StatusDeleting,
		CreatedAt: time.Now().UTC(),
	}
	h.sm.mu.Unlock()

	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "haar-deleting"})
	h.expectError(t, "is being deleted")
}

// --- handshake profile mismatch -----------------------------------------

// TestCoverHandshakeProfileMismatch asserts a client whose profile differs from
// the daemon's is rejected with a handshake_err, not silently accepted. The
// test harness daemon runs with an empty profile, so any non-empty client
// profile trips the guard.
func TestCoverHandshakeProfileMismatch(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version:      protocol.Version,
		Profile:      "thrawn",
		TerminalSize: [2]uint16{80, 24},
	})

	env := h.readControlMsg(t)
	if env.Type != "handshake_err" {
		t.Fatalf("expected handshake_err for profile mismatch, got %q", env.Type)
	}

	var he protocol.HandshakeErrMsg
	if err := protocol.DecodePayload(env, &he); err != nil {
		t.Fatal(err)
	}

	if he.Reason == "" {
		t.Fatal("handshake_err reason should explain the profile mismatch")
	}
}

// TestCoverHandshakeVersionOkThenAuthOk is a positive control confirming the
// harness handshake itself succeeds with matching version/profile, so the
// mismatch test above is isolating the profile check rather than a broken
// handshake.
func TestCoverHandshakeOkBaseline(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "handshake", protocol.HandshakeMsg{
		Version:      protocol.Version,
		TerminalSize: [2]uint16{80, 24},
	})

	env := h.readControlMsg(t)
	if env.Type != "handshake_ok" {
		t.Fatalf("expected handshake_ok, got %q", env.Type)
	}

	var ok protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(env, &ok); err != nil {
		t.Fatal(err)
	}

	if ok.DaemonVersion != version.Version {
		t.Errorf("daemon version = %q, want %q", ok.DaemonVersion, version.Version)
	}
}

// --- per-case authorization: session targeting a foreign session --------

// TestCoverSessionCannotTargetForeignSession drives each session-scoped control
// message from an authenticated session whose target is an unrelated session
// (not self, not a descendant). Every case must reject with "not authorized",
// proving the per-case checkTarget gate — the descendant-based authority model
// — holds across the dispatch table rather than only on the paths round 1
// happened to exercise.
func TestCoverSessionCannotTargetForeignSession(t *testing.T) {
	const callerToken = "tok-bairn"

	msgTypes := []string{
		"rename", "star", "unstar", "resume", "restart",
		"logs", "wait", "stop", "delete",
	}

	for _, mt := range msgTypes {
		t.Run(mt, func(t *testing.T) {
			h := newTestHarness(t)
			h.addAuthenticatedSession(t, "bairn-caller", "bairn-caller", callerToken)

			// An unrelated session the caller has no authority over.
			h.sm.mu.Lock()
			h.sm.state.Sessions["ben-foreign"] = &SessionState{
				ID: "ben-foreign", Name: "ben-foreign", Status: StatusRunning,
				CreatedAt: time.Now().UTC(),
			}
			h.sm.mu.Unlock()

			// A generic payload carrying only session_id; every targeted message
			// type decodes this into its SessionID field.
			payload := map[string]any{"session_id": "ben-foreign", "new_name": "scunner"}
			h.sendControlWithToken(t, mt, payload, callerToken)
			h.expectError(t, "not authorized")
		})
	}
}

// --- exit-description helpers -------------------------------------------

func TestCoverDescribeSessionExit(t *testing.T) {
	code := 137

	cases := []struct {
		name  string
		state SessionState
		want  string
	}{
		{"signal", SessionState{ExitSignal: "SIGKILL"}, "killed by signal SIGKILL"},
		{"exit code", SessionState{ExitCode: &code}, "exited with code 137"},
		{"status fallback", SessionState{Status: StatusStopped}, "status: stopped"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeSessionExit(tc.state); got != tc.want {
				t.Errorf("describeSessionExit() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCoverSessionLabel(t *testing.T) {
	if got := sessionLabel(SessionState{ID: "abc123", Name: "bonnie"}); got != "bonnie" {
		t.Errorf("sessionLabel with name = %q, want bonnie", got)
	}

	if got := sessionLabel(SessionState{ID: "abc123"}); got != "abc123" {
		t.Errorf("sessionLabel without name = %q, want id abc123", got)
	}
}

// TestCoverExitDescriptionSignalPrecedence pins that ExitSignal wins over
// ExitCode when both are set — the order the switch relies on.
func TestCoverExitDescriptionSignalPrecedence(t *testing.T) {
	code := 1
	got := describeSessionExit(SessionState{ExitSignal: "SIGTERM", ExitCode: &code, Status: StatusStopped})

	if got != "killed by signal SIGTERM" {
		t.Errorf("describeSessionExit precedence = %q, want signal to win", got)
	}
}
