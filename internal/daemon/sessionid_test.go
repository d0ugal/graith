package daemon

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestCreateRejectsMalformedID checks that a caller-supplied ID that doesn't
// match the generated format is rejected before any state is touched.
func TestCreateRejectsMalformedID(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.Create(CreateOpts{
		ID:        "NOT-hex!",
		Name:      "braw-session",
		AgentName: "claude",
		NoRepo:    true,
		Rows:      24,
		Cols:      80,
	})
	assertErrContains(t, err, "invalid")

	if len(sm.state.Sessions) != 0 {
		t.Errorf("no session should be created for a malformed ID, got %d", len(sm.state.Sessions))
	}
}

// TestCreateRejectsInUseID checks that supplying an ID already held by a live
// session fails closed and leaves the existing session untouched.
func TestCreateRejectsInUseID(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.mu.Lock()
	sm.state.Sessions["abcdef12"] = &SessionState{
		ID:     "abcdef12",
		Name:   "auld-session",
		Status: StatusRunning,
	}
	sm.mu.Unlock()

	_, err := sm.Create(CreateOpts{
		ID:        "abcdef12",
		Name:      "thrawn-session",
		AgentName: "claude",
		NoRepo:    true,
		Rows:      24,
		Cols:      80,
	})
	assertErrContains(t, err, "already in use")

	if len(sm.state.Sessions) != 1 {
		t.Errorf("collision must not create a session, got %d sessions", len(sm.state.Sessions))
	}

	if got := sm.state.Sessions["abcdef12"].Name; got != "auld-session" {
		t.Errorf("existing session was clobbered: name = %q, want %q", got, "auld-session")
	}
}

// TestCreateUsesSuppliedID checks the happy path: a valid, unused caller ID is
// adopted as the session's real ID rather than a freshly generated one. This
// is the behaviour the scenario reserve flow relies on so a reserved
// placeholder ID stays equal to the final session ID.
func TestCreateUsesSuppliedID(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: false}
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := newSMWithConfig(t, cfg)

	const wantID = "0a1b2c3d"

	sess, err := sm.Create(CreateOpts{
		ID:        wantID,
		Name:      "bide-session",
		AgentName: "sleeper",
		NoRepo:    true,
		Rows:      24,
		Cols:      80,
	})
	if err != nil {
		t.Fatalf("Create with a valid supplied ID failed: %v", err)
	}

	defer stopAndClosePTY(sm, wantID)

	if sess.ID != wantID {
		t.Errorf("session ID = %q, want the supplied %q", sess.ID, wantID)
	}

	sm.mu.RLock()
	stored, ok := sm.state.Sessions[wantID]
	sm.mu.RUnlock()

	if !ok {
		t.Fatalf("session not indexed under the supplied ID %q", wantID)
	}

	if stored.Name != "bide-session" {
		t.Errorf("stored session name = %q, want %q", stored.Name, "bide-session")
	}
}

// TestCreateGeneratesIDWhenUnset checks the default path is unchanged: an empty
// CreateOpts.ID still yields a generated 8-hex-char ID.
func TestCreateGeneratesIDWhenUnset(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: false}
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := newSMWithConfig(t, cfg)

	sess, err := sm.Create(CreateOpts{
		Name:      "canny-session",
		AgentName: "sleeper",
		NoRepo:    true,
		Rows:      24,
		Cols:      80,
	})
	if err != nil {
		t.Fatalf("Create without a supplied ID failed: %v", err)
	}

	defer stopAndClosePTY(sm, sess.ID)

	if err := validateSessionID(sess.ID); err != nil {
		t.Errorf("generated ID %q does not match expected format: %v", sess.ID, err)
	}

	if strings.TrimSpace(sess.ID) == "" {
		t.Error("generated session ID is empty")
	}
}
