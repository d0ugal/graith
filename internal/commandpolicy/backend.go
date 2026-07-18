// Package commandpolicy provides an optional, synchronous restriction layer
// for shell commands. An allow decision only continues to the OS sandbox; it
// never grants capabilities or bypasses sandbox enforcement.
package commandpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const defaultExecTimeout = 5 * time.Second

const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"

	BackendBuiltin   = "builtin"
	BackendLocalmost = "localmost"
)

// Request is the normalized command-policy input. ToolInput contains the full,
// untruncated JSON supplied by the agent hook.
type Request struct {
	SessionID   string
	SessionName string
	Agent       string
	ToolName    string
	ToolInput   string
	HookPayload string
}

type Decision struct {
	Decision string
	Reason   string
}

type Config struct {
	Command       string
	BuiltinConfig string
	BuiltinInline []byte
	ExecTimeout   time.Duration
}

func (c Config) execTimeout() time.Duration {
	if c.ExecTimeout > 0 {
		return c.ExecTimeout
	}
	return defaultExecTimeout
}

type Availability struct {
	CanEnforce bool
	Detail     string
}

type Backend interface {
	Name() string
	Availability(Config) Availability
	Evaluate(context.Context, Request, Config) (Decision, error)
}

func BackendByName(name string) (Backend, error) {
	switch name {
	case BackendBuiltin:
		return builtinBackend{}, nil
	case BackendLocalmost:
		return localmostBackend{}, nil
	default:
		return nil, fmt.Errorf("unknown command policy backend %q", name)
	}
}

// shellCommand returns the command for a supported shell tool. Tools outside
// this deliberately narrow scope proceed directly to sandbox enforcement.
func shellCommand(toolName, toolInput string) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "shell", "run_shell_command", "exec_command":
	default:
		return "", false, nil
	}

	var input struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(toolInput), &input); err != nil {
		return "", true, fmt.Errorf("parse shell tool input: %w", err)
	}
	command := input.Command
	if command == "" {
		command = input.Cmd
	}
	if strings.TrimSpace(command) == "" {
		return "", true, fmt.Errorf("shell tool input has no command")
	}
	return command, true, nil
}
