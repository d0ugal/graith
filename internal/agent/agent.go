package agent

import (
	"os"
	"strings"
	"sync"
)

var (
	agentDetectedOnce sync.Once
	agentDetected     bool
)

// Detected returns true if the process is running inside an AI agent environment.
// GR_AGENT_MODE=0/false/no always takes priority, even after the first call.
//
// The environment-based detection is computed once, lazily, on the first call
// and cached — a CLI process's environment does not change at runtime, so this
// matches the previous package-init behaviour without a package-level init.
func Detected() bool {
	if v, ok := os.LookupEnv("GR_AGENT_MODE"); ok {
		switch strings.ToLower(v) {
		case "0", "false", "no":
			return false
		}
	}

	agentDetectedOnce.Do(func() {
		agentDetected = detect(os.LookupEnv)
	})

	return agentDetected
}

// DetectedEnviron applies the same canonical agent-mode rules to an explicit
// environment snapshot. Lifecycle code uses this instead of maintaining a
// second, inevitably divergent list of agent markers.
func DetectedEnviron(environ []string) bool {
	values := environValues(environ)

	return detect(func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	})
}

// SecurityBoundaryDetectedEnviron detects agent and Graith-session callers for
// trust-boundary decisions. Unlike DetectedEnviron, a negative GR_AGENT_MODE
// presentation override cannot hide concrete session or external-agent
// markers from a security check.
func SecurityBoundaryDetectedEnviron(environ []string) bool {
	values := environValues(environ)
	lookup := func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}

	if value, ok := lookup("GR_AGENT_MODE"); ok {
		switch strings.ToLower(value) {
		case "1", "true", "yes":
			return true
		}
	}

	return detectMarkers(lookup)
}

func environValues(environ []string) map[string]string {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}

	return values
}

var agentEnvVars = []string{
	"GRAITH_SESSION_ID",
	"GRAITH_SESSION_NAME",
	"GRAITH_AGENT_TYPE",
	"GRAITH_TOKEN",
	"GRAITH_WORKTREE_PATH",
	"GRAITH_REPO_PATH",
	"CLAUDECODE",
	"CLAUDE_CODE",
	"CURSOR_AGENT",
	"GITHUB_COPILOT",
	"AMAZON_Q",
	"OPENCODE",
}

func detect(lookupEnv func(string) (string, bool)) bool {
	if v, ok := lookupEnv("GR_AGENT_MODE"); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes":
			return true
		case "0", "false", "no":
			return false
		}
		// Invalid value — fall through to auto-detection
	}

	return detectMarkers(lookupEnv)
}

func detectMarkers(lookupEnv func(string) (string, bool)) bool {
	for _, key := range agentEnvVars {
		if _, ok := lookupEnv(key); ok {
			return true
		}
	}

	return false
}
