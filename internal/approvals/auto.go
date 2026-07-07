package approvals

import "context"

// autoBackend auto-approves every tool request. It is the engine behind the
// per-session "yolo" mode (gr new --yolo) and the [approvals] backend = "auto"
// config: an unattended or background agent runs without blocking on human
// approval prompts until they time out.
//
// This is the intended composition point for future safety guardrails. A
// dangerous-command blocklist (#595) would veto here — returning "block" for a
// hard-blocked command — before the default allow. Until that exists, every
// request is allowed. Availability never fails: unlike the command/localmost
// backends there is no external binary to find.
type autoBackend struct{}

func (autoBackend) Name() string { return BackendAuto }

func (autoBackend) Availability(Config) Availability {
	return Availability{CanEnforce: true}
}

func (autoBackend) Decide(context.Context, Request, Config) (Decision, error) {
	// Reachable via both per-session yolo and the [approvals] backend = "auto"
	// config, so keep the reason backend-neutral rather than yolo-specific.
	return Decision{Decision: DecisionAllow, Reason: "auto-approved"}, nil
}
