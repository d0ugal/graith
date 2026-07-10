package daemon

import (
	"strings"
	"testing"
	"time"
)

// softDelete a freshly-added stopped session and return the manager, ready for a
// guard assertion. The session has no repo, so ID-addressable operations reach
// their soft-delete guard without doing git/PTY work.
func newSoftDeletedSession(t *testing.T, id, name string) *SessionManager {
	t.Helper()

	sm := newTestSessionManager(t)
	addStoppedSession(t, sm, id, name)

	if _, err := sm.SoftDelete(id); err != nil {
		t.Fatalf("SoftDelete(%q) error = %v", id, err)
	}

	return sm
}

func assertSoftDeletedError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected a soft-deleted rejection error, got nil")
	}

	if !strings.Contains(err.Error(), "soft-deleted") {
		t.Errorf("error %q does not mention soft-deleted", err.Error())
	}
}

func TestGuardResumeRejectsSoftDeleted(t *testing.T) {
	sm := newSoftDeletedSession(t, "dreich-id", "dreich")

	_, err := sm.Resume("dreich-id", 24, 80)
	assertSoftDeletedError(t, err)
}

func TestGuardRestartRejectsSoftDeleted(t *testing.T) {
	sm := newSoftDeletedSession(t, "fash-id", "fash")

	_, err := sm.Restart("fash-id", 24, 80)
	assertSoftDeletedError(t, err)
}

func TestGuardRenameRejectsSoftDeleted(t *testing.T) {
	sm := newSoftDeletedSession(t, "auld-id", "auld")

	assertSoftDeletedError(t, sm.Rename("auld-id", "bonnie"))
}

func TestGuardStarRejectsSoftDeleted(t *testing.T) {
	sm := newSoftDeletedSession(t, "kirk-id", "kirk")

	assertSoftDeletedError(t, sm.Star("kirk-id"))
	assertSoftDeletedError(t, sm.Unstar("kirk-id"))
}

func TestGuardUpdateRejectsSoftDeleted(t *testing.T) {
	sm := newSoftDeletedSession(t, "wynd-id", "wynd")

	newName := "glen"
	assertSoftDeletedError(t, sm.Update("wynd-id", &newName, nil))
}

func TestGuardForkRejectsSoftDeleted(t *testing.T) {
	sm := newSoftDeletedSession(t, "whin-id", "whin")

	_, err := sm.Fork("neep", "whin-id", 24, 80)
	assertSoftDeletedError(t, err)
}

func TestGuardMigrateRejectsSoftDeleted(t *testing.T) {
	sm := newTestSessionManager(t)
	s := addStoppedSession(t, sm, "clachan-id", "clachan")
	s.Agent = "claude"

	if _, err := sm.SoftDelete("clachan-id"); err != nil {
		t.Fatalf("SoftDelete error = %v", err)
	}

	// Migrate must refuse before any transcript I/O or metadata mutation.
	_, err := sm.Migrate("clachan-id", "codex", "", 24, 80)
	assertSoftDeletedError(t, err)

	// The soft-delete metadata must be untouched (no partial migration).
	got, _ := sm.Get("clachan-id")
	if got.MigratedFrom != nil || got.Agent != "claude" {
		t.Errorf("migrate mutated a soft-deleted session: agent=%q migratedFrom=%v", got.Agent, got.MigratedFrom)
	}
}

func TestGuardSetSummaryRejectsSoftDeleted(t *testing.T) {
	sm := newSoftDeletedSession(t, "speir-id", "speir")

	assertSoftDeletedError(t, sm.SetSummary("speir-id", "working", 0))
	assertSoftDeletedError(t, sm.ClearSummary("speir-id"))

	// The soft-delete summary must survive the rejected overwrite.
	got, _ := sm.Get("speir-id")
	if !strings.Contains(got.SummaryText, "Soft-deleted") {
		t.Errorf("soft-delete summary was overwritten: %q", got.SummaryText)
	}
}

// TestReconcileSoftDeletedOrphansClearsPID verifies the startup crash-recovery
// sweep zeroes a stale PID left on a soft-deleted session (the process is long
// gone, so killVerifiedProcess is a no-op, but the PID must be cleared).
func TestReconcileSoftDeletedOrphansClearsPID(t *testing.T) {
	sm := newTestSessionManager(t)
	s := addStoppedSession(t, sm, "haar-id", "haar")

	now := time.Now()
	future := now.Add(24 * time.Hour)
	s.DeletedAt = &now
	s.ExpiresAt = &future
	// A dead PID that no longer maps to a live process (well above any real PID).
	s.PID = 2147483646

	sm.reconcileSoftDeletedOrphans()

	got, ok := sm.Get("haar-id")
	if !ok {
		t.Fatal("session missing after reconcile")
	}

	if got.PID != 0 {
		t.Errorf("PID = %d, want 0 after orphan reconcile", got.PID)
	}
}
