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
		{"none cannot upgrade", roleNone, "upgrade", false},

		// roleRemoteGuest: read-only.
		{"guest can list", roleRemoteGuest, "list", true},
		{"guest can logs", roleRemoteGuest, "logs", true},
		{"guest can screen_snapshot", roleRemoteGuest, "screen_snapshot", true},
		{"guest cannot attach", roleRemoteGuest, "attach", false},
		{"guest cannot create", roleRemoteGuest, "create", false},
		{"guest cannot msg_pub", roleRemoteGuest, "msg_pub", false},
		{"guest cannot scenario_stop", roleRemoteGuest, "scenario_stop", false},
		{"guest cannot upgrade", roleRemoteGuest, "upgrade", false},
		// Tightened: these sensitive reads are NOT guest-visible.
		{"guest cannot read DMs", roleRemoteGuest, "msg_conversation", false},
		{"guest cannot scenario_status", roleRemoteGuest, "scenario_status", false},
		{"guest cannot wait", roleRemoteGuest, "wait", false},
		{"guest cannot events_sub", roleRemoteGuest, "events_sub", false},
		{"guest cannot agent_info", roleRemoteGuest, "agent_info", false},
		{"guest cannot search conversations", roleRemoteGuest, "search", false},

		// roleRemoteHuman: everything except local-only.
		{"user can list", roleRemoteHuman, "list", true},
		{"user can attach", roleRemoteHuman, "attach", true},
		{"user can create", roleRemoteHuman, "create", true},
		{"user can msg_pub", roleRemoteHuman, "msg_pub", true},
		{"user can scenario_stop", roleRemoteHuman, "scenario_stop", true},
		{"user cannot upgrade", roleRemoteHuman, "upgrade", false},
		{"user cannot reload", roleRemoteHuman, "reload", false},
		// Removed message names are absent from the matrix and remain fail-closed.
		{"user cannot use removed tool-server connect message", roleRemoteHuman, "mcp_connect", false},
		// #904: the GUI config viewer + diagnostics panel — paired user only.
		{"user can diagnostics", roleRemoteHuman, "diagnostics", true},
		{"user can config", roleRemoteHuman, "config", true},
		{"user can agent_info", roleRemoteHuman, "agent_info", true},
		{"guest cannot diagnostics", roleRemoteGuest, "diagnostics", false},
		{"guest cannot config", roleRemoteGuest, "config", false},
		// Session-originated: a user must NOT be able to impersonate a session.
		{"user cannot status_report", roleRemoteHuman, "status_report", false},
		{"user cannot publish scenario result", roleRemoteHuman, "scenario_result_publish", false},
		{"user can read DMs", roleRemoteHuman, "msg_conversation", true},
		{"user can wait", roleRemoteHuman, "wait", true},
		{"user can search conversations", roleRemoteHuman, "search", true},
		{"user can events_sub", roleRemoteHuman, "events_sub", true},

		// Remote sessions: everything except local-only (self/descendant applied later).
		{"session can attach", roleSession, "attach", true},
		{"session can agent_info", roleSession, "agent_info", true},
		{"session can reach search handler for user-only denial", roleSession, "search", true},
		{"session can events_sub", roleSession, "events_sub", true},
		{"session can status_report", roleSession, "status_report", true},
		{"session can publish own scenario result", roleSession, "scenario_result_publish", true},
		{"session cannot upgrade", roleSession, "upgrade", false},
		{"session cannot reload", roleSession, "reload", false},

		// Remote orchestrator: same reach as a plain session (everything but local-only).
		{"orchestrator can attach", roleOrchestrator, "attach", true},
		{"orchestrator can status_report", roleOrchestrator, "status_report", true},
		{"orchestrator can scenario_start", roleOrchestrator, "scenario_start", true},
		{"orchestrator cannot upgrade", roleOrchestrator, "upgrade", false},

		// roleLocalHuman is never gated by this table (the default branch fails
		// closed here); the local 0700 socket is governed by the handler checks.
		{"local user is not gated here (fails closed)", roleLocalHuman, "list", false},
		{"local user fails closed on mutating too", roleLocalHuman, "create", false},

		// Unknown message fails closed for everyone.
		{"unknown denied for user", roleRemoteHuman, "wheesht", false},
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
