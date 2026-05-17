package agent

import (
	"os"
	"strings"
)

var agentDetected bool

func init() {
	agentDetected = detect(os.LookupEnv)
}

// Detected returns true if the process is running inside an AI agent environment.
func Detected() bool {
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
