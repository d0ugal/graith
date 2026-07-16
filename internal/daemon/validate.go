package daemon

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// validSessionID matches the shape produced by generateID: four random bytes
// hex-encoded to eight lowercase hex characters. Caller-supplied IDs are held
// to the generator's own format so nothing downstream (branch names, worktree
// and scratch paths) ever sees an ID shape it wasn't built to handle.
var validSessionID = regexp.MustCompile(`^[0-9a-f]{8}$`)

// validateSessionID checks that a caller-supplied session ID matches the
// format Create would otherwise generate. It does not check uniqueness —
// Create does that under its lock, atomically with reserving the session.
func validateSessionID(id string) error {
	if !validSessionID.MatchString(id) {
		return fmt.Errorf("session id %q is invalid: must be 8 lowercase hex characters", id)
	}

	return nil
}

const OrchestratorSessionName = "orchestrator"
const SystemKindOrchestrator = "orchestrator"

var reservedSessionNames = map[string]bool{
	OrchestratorSessionName: true,
}

// ValidateSessionName checks that a session name is safe for use in git branch
// names, shell commands, osascript, template expansion, and environment variables.
func ValidateSessionName(name string) error {
	return validateSessionName(name, false)
}

func validateSessionName(name string, allowReserved bool) error {
	if name == "" {
		return errors.New("session name must not be empty")
	}

	if len(name) > 128 {
		return fmt.Errorf("session name must be 128 characters or fewer (got %d)", len(name))
	}

	if strings.Contains(name, "..") {
		return fmt.Errorf("session name must not contain %q", "..")
	}

	if !validSessionName.MatchString(name) {
		return fmt.Errorf("session name %q is invalid: must start with an alphanumeric character and contain only alphanumeric characters, hyphens, underscores, or dots", name)
	}

	if !allowReserved && reservedSessionNames[name] {
		return fmt.Errorf("session name %q is reserved for system use", name)
	}

	return nil
}

func IsSystemSession(s *SessionState) bool {
	return s.SystemKind != ""
}
