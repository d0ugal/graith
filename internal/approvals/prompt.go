package approvals

import "context"

// promptBackend performs no automation: it always defers, so the daemon queues
// the request for a human. This is the default (backend = "").
type promptBackend struct{}

func (promptBackend) Name() string { return BackendPrompt }

func (promptBackend) Availability(Config) Availability {
	return Availability{CanEnforce: true}
}

func (promptBackend) Decide(context.Context, Request, Config) (Decision, error) {
	return Decision{Decision: DecisionDefer}, nil
}
