// Package approvals provides pluggable backends that make (or decline to make)
// automated tool-approval decisions on behalf of the graith daemon.
//
// The daemon intercepts every agent tool call via a PreToolUse-style hook
// (gr approve-request) and asks the configured backend to decide. A backend
// returns one of three decisions:
//
//   - "allow"  — let the tool run
//   - "block"  — refuse it (the agent edge maps this to each agent's vocabulary)
//   - "defer"  — no opinion; the daemon queues the request for a human
//
// Backends never surface "deny": that agent-edge synonym is normalised to
// "block" before it leaves a backend (see Normalise). Decisions are binary
// (allow/block) or absent (defer) — there is no "degraded" analog to the
// sandbox backend's partial-enforcement flag.
//
// The design mirrors internal/sandbox: a small interface plus a BackendByName
// switch, with fail-closed Availability so a misconfigured backend is loud at
// session-create rather than silently ignored.
package approvals

import (
	"context"
	"fmt"
)

// Decision values that cross a backend boundary.
const (
	DecisionAllow = "allow"
	DecisionBlock = "block"
	DecisionDefer = "defer"

	// decisionDeny is the agent-edge synonym for block accepted from external
	// tools; Normalise maps it to DecisionBlock.
	decisionDeny = "deny"
)

// Backend names.
const (
	BackendPrompt    = "prompt"
	BackendCommand   = "command"
	BackendExternal  = "external" // synonym of command
	BackendLocalmost = "localmost"
	BackendBuiltin   = "builtin"
)

// Request is everything a backend needs to decide. The daemon resolves
// SessionName/Agent before calling. ToolInput is the full, untruncated tool
// input JSON; HookPayload is the raw agent hook payload (may be empty).
type Request struct {
	RequestID   string
	SessionID   string
	SessionName string
	Agent       string
	ToolName    string
	ToolInput   string
	HookPayload string
}

// Decision is what a backend returns. Decision is one of "allow", "block", or
// "defer" only.
type Decision struct {
	Decision string
	Reason   string
}

// Config is the resolved approvals configuration a backend needs. The daemon
// populates it from config.Approvals; this package does not import config, to
// avoid an import cycle.
type Config struct {
	Backend       string // effective backend name (already resolved)
	Command       string // command/external command, or a localmost path override
	BuiltinConfig string // path to the localmost-format config.json (builtin backend)
}

// Availability reports whether a backend can enforce with the given config. A
// false CanEnforce fails closed at session-create (Detail explains why).
type Availability struct {
	CanEnforce bool
	Detail     string
}

// Backend makes (or declines to make) an automated approval decision.
type Backend interface {
	Name() string

	// Availability reports whether this backend can enforce with the given
	// config. An unavailable backend fails closed at session-create.
	Availability(cfg Config) Availability

	// Decide returns a decision. A "defer" decision (or a non-nil error) means
	// "no opinion — queue for the human"; errors fail-safe to the human, never
	// fail-open to allow. Backends normalise "deny" to "block" before returning.
	Decide(ctx context.Context, req Request, cfg Config) (Decision, error)
}

// Normalise maps the agent-edge synonym "deny" to "block" and leaves other
// values untouched. Every decision that crosses a backend boundary passes
// through this so a backend returning "deny" cannot silently fall through to
// the human queue instead of blocking.
func Normalise(decision string) string {
	if decision == decisionDeny {
		return DecisionBlock
	}

	return decision
}

// BackendByName returns the backend for a resolved backend name. The name is
// expected to already be validated (see config.Approvals.ResolveBackend);
// unknown names are an error. An empty name resolves to the prompt backend.
func BackendByName(name string) (Backend, error) {
	switch name {
	case "", BackendPrompt:
		return promptBackend{}, nil
	case BackendCommand, BackendExternal:
		return commandBackend{}, nil
	case BackendLocalmost:
		return localmostBackend{}, nil
	case BackendBuiltin:
		return builtinBackend{}, nil
	default:
		return nil, fmt.Errorf("unknown approvals backend %q", name)
	}
}
