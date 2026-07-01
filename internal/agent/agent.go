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

var agentEnvVars = []string{
	"GRAITH_SESSION_ID",
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

	for _, key := range agentEnvVars {
		if _, ok := lookupEnv(key); ok {
			return true
		}
	}

	return false
}
