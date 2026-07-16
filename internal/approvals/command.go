package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// commandBackend delegates the decision to an external command over graith's
// own JSON approval protocol. This is graith's contract, NOT the wire format of
// any particular tool:
//
//   - graith writes one JSON object to the command's stdin:
//     {"tool_name","tool_input","session_id","session_name"}
//   - the command writes one JSON object to stdout:
//     {"decision":"allow"|"block"|"deny"|"defer","reason":"..."}
//
// A "defer"/empty decision, a non-zero exit, unreadable/non-JSON output, or a
// cancelled context all defer to the human. "deny" is normalised to "block".
// The command is executed directly (no shell); use a wrapper script if you need
// arguments or a pipeline.
type commandBackend struct{}

func (commandBackend) Name() string { return BackendCommand }

func (commandBackend) Availability(cfg Config) Availability {
	if strings.TrimSpace(cfg.Command) == "" {
		return Availability{
			CanEnforce: false,
			Detail:     `approvals backend "command" requires [approvals] command to be set`,
		}
	}

	return Availability{CanEnforce: true}
}

type commandRequest struct {
	ToolName    string `json:"tool_name"`
	ToolInput   string `json:"tool_input,omitempty"`
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
}

type commandResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func (commandBackend) Decide(ctx context.Context, req Request, cfg Config) (Decision, error) {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return Decision{Decision: DecisionDefer}, errors.New("no approvals command configured")
	}

	input := commandRequest{
		ToolName:    req.ToolName,
		ToolInput:   req.ToolInput,
		SessionID:   req.SessionID,
		SessionName: req.SessionName,
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("marshal approval input: %w", err)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, cfg.execTimeout())
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command)
	cmd.Stdin = strings.NewReader(string(inputJSON))

	out, err := cmd.Output()
	if err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("approvals command %q failed: %w", command, err)
	}

	var resp commandResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("approvals command %q returned invalid JSON: %w", command, err)
	}

	decision := Normalise(resp.Decision)
	if decision == "" || decision == DecisionDefer {
		return Decision{Decision: DecisionDefer}, nil
	}

	return Decision{Decision: decision, Reason: resp.Reason}, nil
}
