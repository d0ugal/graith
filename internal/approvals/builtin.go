package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/approvals/localmost"
)

// builtinBackend is graith's own clean-room, behaviourally-compatible
// reimplementation of localmost's rule engine (see internal/approvals/localmost).
// It evaluates Bash commands against a localmost-format config.json; other tools
// defer to the human, matching localmost's Bash-matcher scope.
type builtinBackend struct{}

func (builtinBackend) Name() string { return BackendBuiltin }

func (builtinBackend) Availability(cfg Config) Availability {
	path := strings.TrimSpace(cfg.BuiltinConfig)
	if path == "" {
		return Availability{
			CanEnforce: false,
			Detail:     `approvals backend "builtin" requires [approvals.builtin] config to be set`,
		}
	}

	if _, err := localmost.Load(path); err != nil {
		return Availability{
			CanEnforce: false,
			Detail:     fmt.Sprintf(`approvals backend "builtin" config is invalid: %v`, err),
		}
	}

	return Availability{CanEnforce: true}
}

func (builtinBackend) Decide(_ context.Context, req Request, cfg Config) (Decision, error) {
	// localmost only reasons about shell commands; defer other tools.
	if req.ToolName != "Bash" {
		return Decision{Decision: DecisionDefer}, nil
	}

	path := strings.TrimSpace(cfg.BuiltinConfig)
	if path == "" {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("no builtin approvals config configured")
	}

	engine, err := localmost.Load(path)
	if err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("load builtin approvals config: %w", err)
	}

	command := bashCommand(req.ToolInput)
	if command == "" {
		return Decision{Decision: DecisionDefer}, nil
	}

	policy, err := engine.Evaluate(command)
	if err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("evaluate command: %w", err)
	}

	switch policy {
	case localmost.PolicyAllow:
		return Decision{Decision: DecisionAllow}, nil
	case localmost.PolicyDeny:
		return Decision{Decision: DecisionBlock, Reason: "blocked by approvals rules"}, nil
	default: // ask
		// Documented divergence: localmost's askNoninteractive (map ask->deny
		// when no human is present) is not enforced here. A pure backend can't
		// observe whether a client is attached, so "ask" always defers to the
		// human queue; an unattended session still ends in block via the
		// approval timeout, just not immediately.
		return Decision{Decision: DecisionDefer}, nil
	}
}

// bashCommand extracts the command string from a Bash tool's input JSON.
func bashCommand(toolInput string) string {
	if toolInput == "" {
		return ""
	}

	var ti struct {
		Command string `json:"command"`
	}

	if json.Unmarshal([]byte(toolInput), &ti) != nil {
		return ""
	}

	return ti.Command
}
