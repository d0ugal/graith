package commandpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const maxLocalmostOutput = 64 << 10
const localmostWaitDelay = 250 * time.Millisecond

var errLocalmostOutputLimit = errors.New("localmost command policy output exceeded 64 KiB")

type limitedOutput struct {
	bytes.Buffer

	remaining int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		return w.Buffer.Write(p)
	}

	if w.remaining > 0 {
		n := w.remaining
		_, _ = w.Buffer.Write(p[:n])
		w.remaining = 0

		return n, errLocalmostOutputLimit
	}

	return 0, errLocalmostOutputLimit
}

type localmostBackend struct{}

func (localmostBackend) Name() string { return BackendLocalmost }

func localmostCommand(cfg Config) string {
	if command := strings.TrimSpace(cfg.Command); command != "" {
		return command
	}

	return "localmost"
}

func (localmostBackend) Availability(cfg Config) Availability {
	command := localmostCommand(cfg)
	if _, err := exec.LookPath(command); err != nil {
		return Availability{CanEnforce: false, Detail: fmt.Sprintf("command policy backend %q requires %q on PATH: %v", BackendLocalmost, command, err)}
	}

	return Availability{CanEnforce: true}
}

type localmostHookOutput struct {
	HookSpecificOutput struct {
		PermissionDecision string `json:"permissionDecision"`
		Reason             string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (localmostBackend) Evaluate(ctx context.Context, req Request, cfg Config) (Decision, error) {
	command, inScope, err := shellCommand(req.ToolName, req.ToolInput)
	if err != nil {
		return Decision{}, err
	}

	if !inScope {
		return Decision{Decision: DecisionAllow}, nil
	}

	stdin, err := localmostStdin(req, command)
	if err != nil {
		return Decision{}, err
	}

	cmdCtx, cancel := context.WithTimeout(ctx, cfg.execTimeout())
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, localmostCommand(cfg), "check")
	// CommandContext kills the direct child at the deadline, but a descendant
	// can keep inherited stdout/stderr pipes open after that child exits. Bound
	// Wait as well so a backend cannot extend a synchronous policy check by
	// leaking a pipe-holding process.
	cmd.WaitDelay = localmostWaitDelay
	cmd.Stdin = bytes.NewReader(stdin)
	// Keep one byte beyond the limit so an exact-limit response remains valid
	// while an oversized response is detected even when the pipe copier's last
	// read lands exactly on its internal buffer boundary.
	stdout := &limitedOutput{remaining: maxLocalmostOutput + 1}
	stderr := &limitedOutput{remaining: maxLocalmostOutput + 1}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	if err != nil {
		if cmdCtx.Err() != nil {
			return Decision{}, fmt.Errorf("localmost command policy timed out: %w", cmdCtx.Err())
		}

		if errors.Is(err, errLocalmostOutputLimit) {
			return Decision{}, errLocalmostOutputLimit
		}

		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return Decision{}, fmt.Errorf("localmost command policy failed: %w: %s", err, detail)
		}

		return Decision{}, fmt.Errorf("localmost command policy failed: %w", err)
	}

	if stdout.Len() > maxLocalmostOutput || stderr.Len() > maxLocalmostOutput {
		return Decision{}, errLocalmostOutputLimit
	}

	var parsed localmostHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return Decision{}, fmt.Errorf("localmost returned malformed output: %w", err)
	}

	decision := parsed.HookSpecificOutput.PermissionDecision

	reason := parsed.HookSpecificOutput.Reason
	if decision == "" {
		decision, reason = parsed.Decision, parsed.Reason
	}

	switch strings.ToLower(decision) {
	case "allow", "approve":
		return Decision{Decision: DecisionAllow, Reason: reason}, nil
	case "deny", "block":
		return Decision{Decision: DecisionDeny, Reason: reason}, nil
	case "ask", "defer":
		return Decision{Decision: DecisionDeny, Reason: "command policy requested interaction; command policy has no human decision path"}, nil
	default:
		return Decision{}, fmt.Errorf("localmost returned unknown decision %q", decision)
	}
}

func localmostStdin(req Request, command string) ([]byte, error) {
	// Claude's payload is already localmost's native format. Other agents use
	// different hook schemas, so normalize them to a Claude-style Bash event.
	if strings.EqualFold(req.Agent, "claude") && strings.TrimSpace(req.HookPayload) != "" {
		return []byte(req.HookPayload), nil
	}

	return json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": command},
	})
}
