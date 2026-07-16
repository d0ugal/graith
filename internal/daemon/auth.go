package daemon

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
)

// authRole is the resolved privilege class of a connection (design §B.3). It
// makes authorization origin-aware so that "no token" over the network is never
// treated as the local human.
type authRole int

const (
	roleNone         authRole = iota // remote, unpaired/invalid — only handshake + pairing
	roleLocalHuman                   // local Unix socket (the 0700 trust boundary)
	roleRemoteHuman                  // paired remote device, PoP-verified, WhoIs matches
	roleRemoteGuest                  // paired under require_pairing=false — read-only
	roleSession                      // per-session agent token
	roleOrchestrator                 // session token whose SystemKind == orchestrator
)

// authContext holds the resolved identity for a request after token validation.
type authContext struct {
	// role is the resolved privilege class (design §B.3).
	role authRole
	// sessionID is set for roleSession/roleOrchestrator (empty otherwise).
	sessionID string
	// deviceID is set for roleRemoteHuman/roleRemoteGuest (empty otherwise).
	deviceID string
	// authenticated is true when a valid session token was presented. Retained
	// so the existing handler checks keep working unchanged; the exhaustive
	// role-based matrix (design §B.4, Task 6) replaces those call sites and
	// makes the handler fully role-aware before any network listener lands.
	authenticated bool
	// origin describes where the connection came from (local vs remote tailnet).
	origin ConnOrigin
}

// isHuman reports whether the caller is a human operator (local socket or a
// PoP-verified paired remote human), as opposed to a session/agent.
// roleRemoteGuest is intentionally excluded — it is read-only, not a full human.
func (ac authContext) isHuman() bool {
	return ac.role == roleLocalHuman || ac.role == roleRemoteHuman
}

// isLocalHuman reports whether the caller is on the local Unix socket. Used to
// gate local-only operations (upgrade, reload, pairing approval).
func (ac authContext) isLocalHuman() bool {
	return ac.role == roleLocalHuman
}

// describe renders the caller's identity for audit logging (issue #1104): the
// privilege class plus the session or device id that scopes it. It never
// includes the token itself. Used by the lifecycle handlers to make "who asked
// for this stop/delete/restart?" answerable from the daemon log.
func (ac authContext) describe() string {
	switch ac.role {
	case roleLocalHuman:
		return "local-human"
	case roleRemoteHuman:
		return "remote-human(" + ac.deviceID + ")"
	case roleRemoteGuest:
		return "remote-guest(" + ac.deviceID + ")"
	case roleOrchestrator:
		return "orchestrator(" + ac.sessionID + ")"
	case roleSession:
		return "session(" + ac.sessionID + ")"
	case roleNone:
		return "unauthenticated"
	default:
		return "unknown"
	}
}

// resolveAuth resolves the connection's role from its token, origin, and — for
// remote connections — the device ID proven via proof-of-possession on this
// connection (poppedDeviceID, empty until a valid auth_proof).
// Must be called with sm.mu at least RLocked.
func resolveAuth(sm *SessionManager, token string, origin ConnOrigin, poppedDeviceID string) (authContext, error) {
	// A session token authenticates an agent regardless of origin; the
	// self/descendant limits are enforced downstream.
	if token != "" {
		if sid := sm.SessionForToken(token); sid != "" {
			ac := authContext{role: roleSession, sessionID: sid, authenticated: true, origin: origin}
			if ac.isOrchestrator(sm) {
				ac.role = roleOrchestrator
			}

			return ac, nil
		}
	}

	if !origin.Remote {
		// When a human credential is provisioned, the local caller must present
		// it: this is the fail-closed boundary that stops a token-stripping agent
		// from being treated as the human. A daemon started via Run always has one
		// (startup fails closed if it cannot be established), so a serving
		// production daemon is always in this branch.
		if sm.humanToken != "" {
			if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(sm.humanToken)) == 1 {
				return authContext{role: roleLocalHuman, origin: origin}, nil
			}

			return authContext{role: roleNone, origin: origin}, errors.New("invalid token")
		}

		// No human credential provisioned. This is only reachable for a
		// SessionManager constructed without startup provisioning (tests,
		// embedders) — never a served production daemon. Preserve the legacy
		// 0700-socket trust boundary: an empty token is the local human, a stray
		// token is invalid.
		if token == "" {
			return authContext{role: roleLocalHuman, origin: origin}, nil
		}

		return authContext{role: roleNone, origin: origin}, errors.New("invalid token")
	}

	// With pairing disabled, Gate 1 (the live WhoIs allowlist enforced by the
	// remote connection boundary) is the whole human-authentication policy. It
	// deliberately grants only the read-only guest role, even to a device that
	// was previously paired for human read/write access. Re-enabling pairing
	// restores that device's recorded role on its next message/connection.
	requirePairing := true
	if sm.cfg != nil {
		requirePairing = sm.cfg.Remote.RequirePairing
	}

	if !requirePairing {
		return authContext{role: roleRemoteGuest, origin: origin}, nil
	}

	// Remote connection: require proof-of-possession AND a client token that
	// resolves to that same device, whose recorded tailnet identity matches the
	// current WhoIs. Anything short of that is roleNone (Gate A restricts
	// roleNone to the pairing messages).
	if poppedDeviceID == "" {
		return authContext{role: roleNone, origin: origin}, nil
	}

	d := sm.DeviceForToken(token)
	if d == nil || d.ID != poppedDeviceID || !identityMatchesDevice(origin.Identity, d) {
		return authContext{role: roleNone, origin: origin}, nil
	}

	role := roleRemoteHuman
	if d.ReadOnly {
		role = roleRemoteGuest
	}

	return authContext{role: role, deviceID: d.ID, origin: origin}, nil
}

// identityMatchesDevice reports whether the connection's current tailnet
// identity matches the identity recorded when the device was paired. Fail
// closed when the connection has no resolved identity, and require a non-empty
// Node on both sides so a degenerate zero-value identity (e.g. a &TailnetIdentity{}
// from a degraded WhoIs) can never match a device record with an empty Node.
func identityMatchesDevice(id *TailnetIdentity, d *PairedDevice) bool {
	if id == nil || id.Node == "" || d.TailnetNode == "" {
		return false
	}

	return id.User == d.TailnetUser && id.Node == d.TailnetNode
}

type authRule int

const (
	authAlwaysAllowed authRule = iota
	authSelfOnly
	authSelfOrDescendant
	authHumanOnly
)

// isOrchestrator reports whether the authenticated session is the system
// orchestrator. The orchestrator is the fleet control plane and is granted
// elevated privileges to manage any session regardless of parentage.
// Must be called with sm.mu at least RLocked.
func (ac authContext) isOrchestrator(sm *SessionManager) bool {
	if !ac.authenticated {
		return false
	}

	sess, ok := sm.state.Sessions[ac.sessionID]

	return ok && sess.SystemKind == SystemKindOrchestrator
}

// checkTarget verifies that the authenticated session is authorized to act on
// the target session, according to the given rule. Must be called with sm.mu
// at least RLocked.
func (ac authContext) checkTarget(sm *SessionManager, targetID string, rule authRule) error {
	switch rule {
	case authAlwaysAllowed:
		return nil

	case authHumanOnly:
		if ac.authenticated {
			return errors.New("operation not permitted for agent sessions")
		}

		return nil

	case authSelfOnly:
		if !ac.authenticated {
			return nil
		}
		// The orchestrator is the fleet control plane and may target any session.
		if ac.isOrchestrator(sm) {
			return nil
		}

		if targetID != ac.sessionID {
			return errors.New("not authorized: can only target own session")
		}

		return nil

	case authSelfOrDescendant:
		if !ac.authenticated {
			return nil
		}
		// The orchestrator is the fleet control plane and may target any session.
		if ac.isOrchestrator(sm) {
			return nil
		}

		if targetID == ac.sessionID {
			return nil
		}

		if sm.isDescendantOf(targetID, ac.sessionID) {
			return nil
		}

		return errors.New("not authorized: target session is not self or descendant")
	}

	return errors.New("unknown auth rule")
}

func parseInboxStream(stream string) (string, bool) {
	if !strings.HasPrefix(stream, "inbox:") {
		return "", false
	}

	return strings.TrimPrefix(stream, "inbox:"), true
}

// checkScenarioOp validates that the caller is authorized to operate on a
// scenario. A human operator — the local CLI or a paired remote human — may
// manage any scenario. A session must be the scenario's orchestrator or a
// descendant of it. roleNone / read-only guests are rejected (Gate A also
// blocks them from scenario_* over the network).
// Must be called with sm.mu at least RLocked.
func (ac authContext) checkScenarioOp(sm *SessionManager, scenarioName string) error {
	if ac.isHuman() {
		return nil
	}

	if ac.role != roleSession && ac.role != roleOrchestrator {
		return errors.New("not authorized to manage scenarios")
	}

	for _, sc := range sm.state.Scenarios {
		if sc.Name == scenarioName {
			if ac.sessionID == sc.OrchestratorID {
				return nil
			}

			if sm.isDescendantOf(ac.sessionID, sc.OrchestratorID) {
				return nil
			}

			return fmt.Errorf("not authorized: only the scenario orchestrator or its descendants can manage scenario %q", scenarioName)
		}
	}

	return fmt.Errorf("scenario %q not found", scenarioName)
}

// checkTriggerOp authorizes a mutating trigger op (run/pause/resume). Triggers
// are daemon-owned (no per-trigger owner), so the rule is: the caller must be the
// system orchestrator session or a descendant of it. Humans are always allowed.
// Must be called with sm.mu at least RLocked.
func (ac authContext) checkTriggerOp(sm *SessionManager) error {
	if ac.isHuman() {
		return nil
	}

	if ac.role != roleSession && ac.role != roleOrchestrator {
		return errors.New("not authorized to manage triggers")
	}

	orchID := sm.findOrchestratorID()
	if orchID == "" {
		return errors.New("not authorized: no orchestrator session to authorize against")
	}

	if ac.sessionID == orchID || sm.isDescendantOf(ac.sessionID, orchID) {
		return nil
	}

	return errors.New("not authorized: only the orchestrator or its descendants can manage triggers")
}

// checkNotifyOp authorizes a proactive push notification (`gr notify`). To stop
// individual agents spamming the human, only a human operator or the system
// orchestrator session may send one — a plain agent session is rejected. (Note
// this is stricter than triggers, which also allow orchestrator descendants:
// the whole point of push is that it is a scarce, human-facing channel.)
// Triggers fire notifications daemon-internally and never reach this gate.
// Must be called with sm.mu at least RLocked.
func (ac authContext) checkNotifyOp(sm *SessionManager) error {
	if ac.isHuman() {
		return nil
	}

	if ac.isOrchestrator(sm) {
		return nil
	}

	return errors.New("not authorized: only the orchestrator or the human may send notifications")
}

// checkJailRelease authorizes releasing a jailed PR comment (issue #1082).
// Releasing delivers quarantined, untrusted content to a working agent, so it
// must be restricted to a human operator or the system orchestrator — a plain
// agent session is rejected. If it weren't, a compromised agent could release
// its own prompt-injection payload out of the jail, defeating the quarantine.
// (This mirrors checkNotifyOp: human or orchestrator only, no descendants.)
// Must be called with sm.mu at least RLocked.
func (ac authContext) checkJailRelease(sm *SessionManager) error {
	if ac.isHuman() {
		return nil
	}

	if ac.isOrchestrator(sm) {
		return nil
	}

	return errors.New("not authorized: only the orchestrator or the human may release jailed comments")
}

// isDescendantOf checks whether targetID is a transitive descendant of rootID.
// Must be called with sm.mu at least RLocked.
func (sm *SessionManager) isDescendantOf(targetID, rootID string) bool {
	visited := make(map[string]bool)

	current := targetID
	for {
		if current == rootID {
			return true
		}

		if visited[current] {
			return false
		}

		visited[current] = true

		sess, ok := sm.state.Sessions[current]
		if !ok || sess.ParentID == "" {
			return false
		}

		current = sess.ParentID
	}
}
