package daemon

// This file implements Gate A of the design's §B.4 authorization matrix: the
// per-message policy for REMOTE (tailnet) connections. Local Unix-socket
// connections (roleLocalHuman) are the existing 0700 trust boundary and are
// never gated by this table — the established handler checks (checkTarget,
// the self/descendant rules, the local-only `authenticated` guards) continue to
// govern them unchanged. This table governs only what a remote caller may reach.

// remotePolicy classifies how a control message may be used over a remote
// connection.
type remotePolicy int

const (
	// remoteDenied: never allowed over the network, even for a paired human or a
	// session — local Unix socket only (e.g. upgrade, reload, pairing approval,
	// and, until redaction lands, diagnostics). This is the zero value so any
	// message missing from the table fails closed.
	remoteDenied remotePolicy = iota
	// remotePreAuth: the pairing lane — allowed for a roleNone remote connection
	// that has not yet completed pairing/PoP (handshake + pair_request +
	// auth_proof).
	remotePreAuth
	// remoteReadOnly: observational; allowed for a read-only guest, a paired
	// human, and sessions.
	remoteReadOnly
	// remoteHumanRW: mutating/operator actions; allowed for a paired human and
	// sessions, but not a read-only guest.
	remoteHumanRW
	// remoteSessionOnly: session-originated messages (a session acting on
	// itself — e.g. requesting approval, reporting hook status). Allowed for
	// sessions/orchestrator only; NOT paired humans (who could otherwise
	// impersonate a session) or guests.
	remoteSessionOnly
)

// remoteMessagePolicy classifies EVERY control message the handler dispatches.
// A message missing here defaults to remoteDenied (fail closed). The
// completeness test in auth_matrix_test.go fails if the handler grows a `case`
// without a matching entry here (or vice versa), so no message can silently
// bypass Gate A. When a new control message is added to HandleConnection, add a
// row here in the same change.
var remoteMessagePolicy = map[string]remotePolicy{
	// Pairing lane (reachable before authentication). The pair_* / auth_proof
	// handler cases are added with the listener work; their policy is fixed here.
	"handshake":    remotePreAuth,
	"pair_request": remotePreAuth,
	"auth_proof":   remotePreAuth,

	// Read-only / observational — the guest subset (design §B.4). Kept tight:
	// only fleet/session observation, no private DMs or scenario/wait targeting.
	"list":            remoteReadOnly,
	"status":          remoteReadOnly,
	"logs":            remoteReadOnly,
	"screen_preview":  remoteReadOnly,
	"screen_snapshot": remoteReadOnly,
	"approval_list":   remoteReadOnly,

	// Session-originated (a session acting on itself); not humans/guests.
	"approval_request": remoteSessionOnly,
	"status_report":    remoteSessionOnly,

	// Mutating / operator actions (paired human + sessions; not guests).
	"msg_conversation": remoteHumanRW,  // reads private DMs — human, not guest
	"msg_jail_list":    remoteReadOnly, // inspect quarantined comments — agents may see, not release
	"msg_jail_show":    remoteReadOnly,
	"msg_jail_release": remoteHumanRW, // releasing untrusted content: human/orchestrator only (handler re-checks)
	"scenario_status":  remoteHumanRW, // design denies scenario_* to guests
	"scenario_list":    remoteHumanRW,
	"trigger_list":     remoteHumanRW, // triggers gated to human + sessions, not guests
	"trigger_status":   remoteHumanRW,
	"trigger_run":      remoteHumanRW,
	"trigger_pause":    remoteHumanRW,
	"mcp_list":         remoteHumanRW, // daemon-level MCP management: human + sessions, not guests
	"mcp_restart":      remoteHumanRW, // mutating; the handler re-checks via authorizeTriggerOp
	"mcp_logs":         remoteHumanRW, // MCP stderr may carry sensitive output
	"notify":           remoteHumanRW, // orchestrator/human only; the handler re-checks

	// Read-only host introspection for the paired human/sessions only (not a
	// read-only guest). diagnostics carries only host-shaped detail (PIDs,
	// worktree paths, token-*presence*, never token values). config's secret
	// env-map values are redacted before they cross the wire (config.RedactSecrets),
	// so neither serves a guest a secret the human/guest split would need to guard.
	"config":      remoteHumanRW, // effective config (env secrets redacted) + diff (GUI config viewer, #904)
	"diagnostics": remoteHumanRW, // health/doctor payload for the GUI diagnostics panel (#904)

	"wait":               remoteHumanRW, // targets arbitrary sessions
	"repo_list":          remoteHumanRW, // only useful for create, which guests can't do
	"store_list":         remoteHumanRW, // store contents may be sensitive: human + sessions, not guests
	"store_get":          remoteHumanRW, // reads a document body; same sensitivity as store_list
	"attach":             remoteHumanRW,
	"attach_convert":     remoteHumanRW, // convert a headless session to interactive on attach (#1137)
	"detach":             remoteHumanRW,
	"resize":             remoteHumanRW,
	"type":               remoteHumanRW,
	"create":             remoteHumanRW,
	"delete":             remoteHumanRW,
	"restore":            remoteHumanRW,
	"stop":               remoteHumanRW,
	"resume":             remoteHumanRW,
	"restart":            remoteHumanRW,
	"rename":             remoteHumanRW,
	"star":               remoteHumanRW,
	"unstar":             remoteHumanRW,
	"update":             remoteHumanRW,
	"fork":               remoteHumanRW,
	"migrate":            remoteHumanRW,
	"set_status":         remoteHumanRW,
	"interrupt":          remoteHumanRW,
	"msg_pub":            remoteHumanRW,
	"msg_inbox":          remoteHumanRW,
	"msg_sub":            remoteHumanRW,
	"msg_ack":            remoteHumanRW,
	"msg_topics":         remoteHumanRW,
	"approval_respond":   remoteHumanRW,
	"approval_subscribe": remoteHumanRW,
	"scenario_start":     remoteHumanRW,
	"scenario_stop":      remoteHumanRW,
	"scenario_delete":    remoteHumanRW,
	"scenario_resume":    remoteHumanRW,
	"scenario_add":       remoteHumanRW,
	"scenario_task_done": remoteHumanRW,

	// Local-only — never over the network, for any remote role.
	"upgrade":      remoteDenied,
	"reload":       remoteDenied,
	"gc":           remoteDenied, // host-level data-dir maintenance; local Unix socket only
	"mcp_connect":  remoteDenied,
	"pair_approve": remoteDenied, // pairing approval is an out-of-band local human action
	"pair_list":    remoteDenied,
	"pair_revoke":  remoteDenied,
}

// remoteAllowed reports whether a control message of type msgType may be
// processed for a connection with the given role. It is consulted ONLY for
// remote (tailnet) connections; local connections are never gated here.
//
// roleNone (unpaired remote) may reach only the pairing lane. roleRemoteGuest
// (paired under require_pairing=false) is read-only. roleRemoteHuman and remote
// sessions/orchestrators may reach everything except the local-only messages;
// their finer-grained authorization (self/descendant, etc.) is then applied by
// the existing handler checks.
func remoteAllowed(role authRole, msgType string) bool {
	pol := remoteMessagePolicy[msgType] // unknown → remoteDenied (fail closed)

	switch role {
	case roleNone:
		return pol == remotePreAuth
	case roleRemoteGuest:
		return pol == remotePreAuth || pol == remoteReadOnly
	case roleRemoteHuman:
		// Everything a human operator may do — but NOT session-only or
		// local-only messages (so a paired human cannot impersonate a session).
		return pol == remotePreAuth || pol == remoteReadOnly || pol == remoteHumanRW
	case roleSession, roleOrchestrator:
		return pol != remoteDenied
	default:
		// roleLocalHuman is never gated here; any other value fails closed.
		return false
	}
}
