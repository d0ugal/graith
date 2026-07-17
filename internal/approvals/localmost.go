package approvals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// localmostBackend invokes the real federicotdn/localmost binary using its
// native Claude Code PreToolUse protocol.
//
// graith is itself the PreToolUse hook (gr approve-request), so the most
// faithful integration forwards the original agent hook payload to
// `localmost check`. When no raw payload is available (e.g. a non-Claude
// agent), a minimal PreToolUse envelope is reconstructed from the tool name and
// input.
//
// localmost's decision is read from Claude Code's hook-output schema
// (hookSpecificOutput.permissionDecision ∈ allow|deny|ask), mapped to
// allow|block|defer. Anything unparseable, or a non-zero exit, defers to the
// human (fail-safe). Only Bash tool calls are evaluated; other tools defer,
// matching localmost's Bash-matcher scope.
//
// NOTE: localmost's exact stdout contract is not exhaustively documented, so
// the output is parsed defensively (hookSpecificOutput first, then a top-level
// decision field). This is a known compatibility-risk surface, validated by the
// conformance corpus when a real binary is available.
type localmostBackend struct{}

func (localmostBackend) Name() string { return BackendLocalmost }

// localmostCommand is the binary to invoke: the configured override, or the
// default "localmost".
func localmostCommand(cfg Config) string {
	if c := strings.TrimSpace(cfg.Command); c != "" {
		return c
	}

	return "localmost"
}

func (localmostBackend) Availability(cfg Config) Availability {
	command := localmostCommand(cfg)
	if _, err := exec.LookPath(command); err != nil {
		return Availability{
			CanEnforce: false,
			Detail:     fmt.Sprintf("approvals backend %q requires the %q binary on PATH: %v", BackendLocalmost, command, err),
		}
	}

	return Availability{CanEnforce: true}
}

// localmostHookOutput models the parts of Claude Code's PreToolUse hook output
// that carry a permission decision.
type localmostHookOutput struct {
	HookSpecificOutput struct {
		PermissionDecision string `json:"permissionDecision"`
		Reason             string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`

	// Fallbacks for simpler/older output shapes.
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (localmostBackend) Decide(ctx context.Context, req Request, cfg Config) (Decision, error) {
	// localmost only reasons about shell commands (Bash matcher); defer others.
	if req.ToolName != "Bash" {
		return Decision{Decision: DecisionDefer}, nil
	}

	command := localmostCommand(cfg)

	stdin, err := localmostStdin(req)
	if err != nil {
		return Decision{Decision: DecisionDefer}, err
	}

	cmdCtx, cancel := context.WithTimeout(ctx, cfg.execTimeout())
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, "check")
	cmd.Stdin = bytes.NewReader(stdin)

	out, err := cmd.Output()
	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return Decision{Decision: DecisionDefer}, fmt.Errorf("localmost backend execution deadline (%s) expired: %w", cfg.execTimeout(), cmdCtx.Err())
		}

		return Decision{Decision: DecisionDefer}, fmt.Errorf("localmost check failed: %w", err)
	}

	var parsed localmostHookOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("localmost returned unparseable output: %w", err)
	}

	decision := parsed.HookSpecificOutput.PermissionDecision
	reason := parsed.HookSpecificOutput.Reason

	if decision == "" {
		decision = parsed.Decision
		reason = parsed.Reason
	}

	switch decision {
	case "allow", "approve":
		return Decision{Decision: DecisionAllow, Reason: reason}, nil
	case "deny", "block":
		return Decision{Decision: DecisionBlock, Reason: reason}, nil
	default: // "ask", "", or unknown -> defer to the human
		return Decision{Decision: DecisionDefer, Reason: reason}, nil
	}
}

// localmostStdin returns the PreToolUse payload to feed localmost: the original
// hook payload when present, else a reconstructed minimal envelope.
func localmostStdin(req Request) ([]byte, error) {
	if strings.TrimSpace(req.HookPayload) != "" {
		return []byte(req.HookPayload), nil
	}

	toolInput := json.RawMessage(req.ToolInput)
	if len(toolInput) == 0 || !json.Valid(toolInput) {
		toolInput = json.RawMessage(`{}`)
	}

	return json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       req.ToolName,
		"tool_input":      toolInput,
	})
}
