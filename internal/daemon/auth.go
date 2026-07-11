package daemon

import (
	"crypto/subtle"
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
		if token != "" && sm.humanToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(sm.humanToken)) == 1 {
			return authContext{role: roleLocalHuman, origin: origin}, nil
		}

		return authContext{role: roleNone, origin: origin}, fmt.Errorf("invalid token")
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
			return fmt.Errorf("operation not permitted for agent sessions")
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
			return fmt.Errorf("not authorized: can only target own session")
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

		return fmt.Errorf("not authorized: target session is not self or descendant")
	}

	return fmt.Errorf("unknown auth rule")
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
		return fmt.Errorf("not authorized to manage scenarios")
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
		return fmt.Errorf("not authorized to manage triggers")
	}

	orchID := sm.findOrchestratorID()
	if orchID == "" {
		return fmt.Errorf("not authorized: no orchestrator session to authorize against")
	}

	if ac.sessionID == orchID || sm.isDescendantOf(ac.sessionID, orchID) {
		return nil
	}

	return fmt.Errorf("not authorized: only the orchestrator or its descendants can manage triggers")
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
