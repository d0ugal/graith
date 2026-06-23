package daemon

import (
	"fmt"
	"strings"
)

// authContext holds the resolved identity for a request after token validation.
type authContext struct {
	// sessionID is the authenticated session ID, or empty for unauthenticated
	// (human) connections.
	sessionID string
	// authenticated is true when a valid token was presented.
	authenticated bool
}

// resolveAuth validates the token from the envelope and returns an authContext.
// Must be called with sm.mu at least RLocked.
func resolveAuth(sm *SessionManager, token string) (authContext, error) {
	if token == "" {
		return authContext{}, nil
	}
	sid := sm.SessionForToken(token)
	if sid == "" {
		return authContext{}, fmt.Errorf("invalid token")
	}
	return authContext{sessionID: sid, authenticated: true}, nil
}

type authRule int

const (
	authAlwaysAllowed authRule = iota
	authSelfOnly
	authSelfOrDescendant
	authHumanOnly
)

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
		if targetID != ac.sessionID {
			return fmt.Errorf("not authorized: can only target own session")
		}
		return nil

	case authSelfOrDescendant:
		if !ac.authenticated {
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

// checkMsgPub validates msg_pub authorization. The sender_id is forced to the
// authenticated session, so the recipient always knows who sent the message.
// Any authenticated session may send to any inbox.
func (ac authContext) checkMsgPub(_ *SessionManager, _ string) error {
	return nil
}

func parseInboxStream(stream string) (string, bool) {
	if !strings.HasPrefix(stream, "inbox:") {
		return "", false
	}
	return strings.TrimPrefix(stream, "inbox:"), true
}

// checkScenarioOp validates that the caller is authorized to operate on a
// scenario. Unauthenticated callers (human CLI) are always allowed. Authenticated
// callers must be the scenario's orchestrator or a descendant of it.
// Must be called with sm.mu at least RLocked.
func (ac authContext) checkScenarioOp(sm *SessionManager, scenarioName string) error {
	if !ac.authenticated {
		return nil
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
