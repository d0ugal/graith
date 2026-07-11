package daemon

import (
	"os"
	"regexp"
	"testing"
)

// handlerCaseRe matches the control-message `case "x":` labels in the
// HandleConnection switch.
var handlerCaseRe = regexp.MustCompile(`case "([a-z_]+)":`)

// handlerMessageCases extracts every control-message case label from
// handler.go. The frame-channel switch uses `case protocol.ChannelControl`
// (no string literal) so it is not matched.
func handlerMessageCases(t *testing.T) map[string]bool {
	t.Helper()

	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}

	cases := map[string]bool{}
	for _, m := range handlerCaseRe.FindAllStringSubmatch(string(src), -1) {
		cases[m[1]] = true
	}

	if len(cases) < 20 {
		t.Fatalf("only found %d handler cases — regex likely broken", len(cases))
	}

	return cases
}

// TestRemoteMatrixCompleteness is the guard the design promises: every control
// message the handler dispatches must have a remote-policy classification, and
// the policy table must not carry stale entries. If a new `case "x":` is added
// to HandleConnection without a remoteMessagePolicy row (or vice versa), this
// fails — so no message can silently bypass Gate A.
func TestRemoteMatrixCompleteness(t *testing.T) {
	cases := handlerMessageCases(t)

	// Messages classified for the pairing lane but whose handler cases are added
	// with the listener work (not yet present in handler.go). Allowed to be in
	// the policy table without a case for now.
	pendingCases := map[string]bool{}

	for msgType := range cases {
		if _, ok := remoteMessagePolicy[msgType]; !ok {
			t.Errorf("handler case %q has no remoteMessagePolicy entry — classify it in authmatrix.go", msgType)
		}
	}

	for msgType := range remoteMessagePolicy {
		if !cases[msgType] && !pendingCases[msgType] {
			t.Errorf("remoteMessagePolicy has entry %q with no handler case — stale entry", msgType)
		}
	}
}

func TestRemoteAllowed(t *testing.T) {
	tests := []struct {
		name    string
		role    authRole
		msgType string
		want    bool
	}{
		// roleNone (unpaired remote): only the pairing lane.
		{"none can handshake", roleNone, "handshake", true},
		{"none can pair_request", roleNone, "pair_request", true},
		{"none can auth_proof", roleNone, "auth_proof", true},
		{"none cannot list", roleNone, "list", false},
		{"none cannot create", roleNone, "create", false},
		{"none cannot msg_pub", roleNone, "msg_pub", false},
		{"none cannot scenario_stop", roleNone, "scenario_stop", false},
		{"none cannot approval_list", roleNone, "approval_list", false},
		{"none cannot upgrade", roleNone, "upgrade", false},

		// roleRemoteGuest: read-only.
		{"guest can list", roleRemoteGuest, "list", true},
		{"guest can logs", roleRemoteGuest, "logs", true},
		{"guest can approval_list", roleRemoteGuest, "approval_list", true},
		{"guest can screen_snapshot", roleRemoteGuest, "screen_snapshot", true},
		{"guest cannot attach", roleRemoteGuest, "attach", false},
		{"guest cannot create", roleRemoteGuest, "create", false},
		{"guest cannot msg_pub", roleRemoteGuest, "msg_pub", false},
		{"guest cannot approval_respond", roleRemoteGuest, "approval_respond", false},
		{"guest cannot scenario_stop", roleRemoteGuest, "scenario_stop", false},
		{"guest cannot upgrade", roleRemoteGuest, "upgrade", false},
		// Tightened: these sensitive reads are NOT guest-visible.
		{"guest cannot read DMs", roleRemoteGuest, "msg_conversation", false},
		{"guest cannot scenario_status", roleRemoteGuest, "scenario_status", false},
		{"guest cannot wait", roleRemoteGuest, "wait", false},
		{"guest cannot approval_request", roleRemoteGuest, "approval_request", false},

		// roleRemoteHuman: everything except local-only.
		{"human can list", roleRemoteHuman, "list", true},
		{"human can attach", roleRemoteHuman, "attach", true},
		{"human can create", roleRemoteHuman, "create", true},
		{"human can approval_respond", roleRemoteHuman, "approval_respond", true},
		{"human can approval_subscribe", roleRemoteHuman, "approval_subscribe", true},
		{"human can msg_pub", roleRemoteHuman, "msg_pub", true},
		{"human can scenario_stop", roleRemoteHuman, "scenario_stop", true},
		{"human cannot upgrade", roleRemoteHuman, "upgrade", false},
		{"human cannot reload", roleRemoteHuman, "reload", false},
		{"human cannot mcp_connect", roleRemoteHuman, "mcp_connect", false},
		{"human cannot diagnostics (until redaction)", roleRemoteHuman, "diagnostics", false},
		// Session-originated: a human must NOT be able to impersonate a session.
		{"human cannot approval_request", roleRemoteHuman, "approval_request", false},
		{"human cannot status_report", roleRemoteHuman, "status_report", false},
		{"human can read DMs", roleRemoteHuman, "msg_conversation", true},
		{"human can wait", roleRemoteHuman, "wait", true},

		// Remote sessions: everything except local-only (self/descendant applied later).
		{"session can attach", roleSession, "attach", true},
		{"session can approval_request", roleSession, "approval_request", true},
		{"session can status_report", roleSession, "status_report", true},
		{"session cannot upgrade", roleSession, "upgrade", false},
		{"session cannot reload", roleSession, "reload", false},

		// Remote orchestrator: same reach as a plain session (everything but local-only).
		{"orchestrator can attach", roleOrchestrator, "attach", true},
		{"orchestrator can status_report", roleOrchestrator, "status_report", true},
		{"orchestrator can scenario_start", roleOrchestrator, "scenario_start", true},
		{"orchestrator cannot upgrade", roleOrchestrator, "upgrade", false},

		// roleLocalHuman is never gated by this table (the default branch fails
		// closed here); the local 0700 socket is governed by the handler checks.
		{"local human is not gated here (fails closed)", roleLocalHuman, "list", false},
		{"local human fails closed on mutating too", roleLocalHuman, "create", false},

		// Unknown message fails closed for everyone.
		{"unknown denied for human", roleRemoteHuman, "wheesht", false},
		{"unknown denied for none", roleNone, "wheesht", false},
		{"unknown denied for orchestrator", roleOrchestrator, "wheesht", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remoteAllowed(tt.role, tt.msgType); got != tt.want {
				t.Errorf("remoteAllowed(%v, %q) = %v, want %v", tt.role, tt.msgType, got, tt.want)
			}
		})
	}
}
