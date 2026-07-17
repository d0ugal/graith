//go:build integration

package daemon

import "context"

// CreateOrchestratorForIntegration synchronously creates the authenticated
// orchestrator used by cross-package wire integration tests. Production boot
// continues to use ensureOrchestrator and its supervisor.
func (sm *SessionManager) CreateOrchestratorForIntegration(ctx context.Context) (SessionState, error) {
	return sm.createOrchestrator(ctx)
}
