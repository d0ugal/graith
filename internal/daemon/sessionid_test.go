package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

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

// TestCreateRejectsSoftDeletedID checks that a supplied ID belonging to a
// soft-deleted session is still rejected: the session retains its state entry
// (its worktree/branch persist pending purge), so its ID must stay reserved.
func TestCreateRejectsSoftDeletedID(t *testing.T) {
	sm := newTestSessionManager(t)

	deletedAt := time.Now().UTC()
	expiresAt := deletedAt.Add(24 * time.Hour)

	sm.mu.Lock()
	sm.state.Sessions["deadbeef"] = &SessionState{
		ID:        "deadbeef",
		Name:      "dreich-session",
		Status:    StatusStopped,
		DeletedAt: &deletedAt,
		ExpiresAt: &expiresAt,
	}
	sm.mu.Unlock()

	if !sm.state.Sessions["deadbeef"].IsSoftDeleted() {
		t.Fatal("seeded session should be soft-deleted")
	}

	_, err := sm.Create(CreateOpts{
		ID:        "deadbeef",
		Name:      "fash-session",
		AgentName: "claude",
		NoRepo:    true,
		Rows:      24,
		Cols:      80,
	})
	assertErrContains(t, err, "already in use")

	if len(sm.state.Sessions) != 1 {
		t.Errorf("collision with a soft-deleted session must not create one, got %d", len(sm.state.Sessions))
	}

	if got := sm.state.Sessions["deadbeef"].Name; got != "dreich-session" {
		t.Errorf("soft-deleted session was clobbered: name = %q, want %q", got, "dreich-session")
	}
}

// TestCreateConcurrentSuppliedIDRace checks that when two Create calls race on
// the same supplied ID, exactly one wins and the other is rejected — the
// collision check and reservation are atomic under sm.mu. Runs meaningfully
// under -race.
func TestCreateConcurrentSuppliedIDRace(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: false}
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := newSMWithConfig(t, cfg)

	const wantID = "beefcafe"

	names := []string{"whin-racer-yin", "whin-racer-twa"}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		errs    int
	)

	for i := 0; i < len(names); i++ {
		wg.Add(1)

		go func(name string) {
			defer wg.Done()

			_, err := sm.Create(CreateOpts{
				ID:        wantID,
				Name:      name,
				AgentName: "sleeper",
				NoRepo:    true,
				Rows:      24,
				Cols:      80,
			})

			mu.Lock()
			defer mu.Unlock()

			if err == nil {
				okCount++
			} else if strings.Contains(err.Error(), "already in use") {
				errs++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}(names[i])
	}

	wg.Wait()

	defer stopAndClosePTY(sm, wantID)

	if okCount != 1 || errs != 1 {
		t.Errorf("want exactly one success and one collision, got %d ok / %d collision", okCount, errs)
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
