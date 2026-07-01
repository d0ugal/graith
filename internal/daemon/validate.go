package daemon

import (
	"fmt"
	"regexp"
	"strings"
)

var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

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
		return fmt.Errorf("session name must not be empty")
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
