package daemon

import (
	"testing"
	"time"
)

// addStoppedSession inserts a stopped, no-repo session directly into state so
// soft-delete unit tests need no PTY or real process (PID stays 0, so the kill
// path is a no-op).
func addStoppedSession(t *testing.T, sm *SessionManager, id, name string) *SessionState {
	t.Helper()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	s := &SessionState{
		ID:              id,
		Name:            name,
		Status:          StatusStopped,
		StatusChangedAt: time.Now(),
		CreatedAt:       time.Now(),
	}
	sm.state.Sessions[id] = s

	return s
}

func TestShouldPurge(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name     string
		deleted  *time.Time
		expires  *time.Time
		fallback time.Time
		want     bool
	}{
		{"not deleted", nil, nil, now, false},
		{"deleted, expires in future", &past, &future, now, false},
		{"deleted, expires in past", &past, &past, now, true},
		{"deleted, expires exactly now", &past, &now, now, true},
		{"deleted, nil expiry uses fallback (future)", &past, nil, future, false},
		{"deleted, nil expiry uses fallback (past)", &past, nil, past, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &SessionState{DeletedAt: tt.deleted, ExpiresAt: tt.expires}
			if got := shouldPurge(s, now, tt.fallback); got != tt.want {
				t.Errorf("shouldPurge = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSoftDeleteMarksAndPreserves(t *testing.T) {
	sm := newTestSessionManager(t)
	addStoppedSession(t, sm, "braw-id", "braw")

	snap, err := sm.SoftDelete("braw-id")
	if err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	if snap.DeletedAt == nil || snap.ExpiresAt == nil {
		t.Fatal("snapshot missing DeletedAt/ExpiresAt")
	}

	// ExpiresAt is frozen to DeletedAt + retention (24h default).
	want := snap.DeletedAt.Add(24 * time.Hour)
	if !snap.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v (DeletedAt + 24h)", *snap.ExpiresAt, want)
	}

	// The session must still exist in state, marked soft-deleted and stopped.
	s, ok := sm.Get("braw-id")
	if !ok {
		t.Fatal("session removed from state after soft delete")
	}

	if !s.IsSoftDeleted() {
		t.Error("session not marked soft-deleted")
	}

	if s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", s.Status)
	}
}

func TestSoftDeleteRejections(t *testing.T) {
	t.Run("starred", func(t *testing.T) {
		sm := newTestSessionManager(t)
		s := addStoppedSession(t, sm, "canny-id", "canny")
		s.Starred = true

		if _, err := sm.SoftDelete("canny-id"); err == nil {
			t.Error("expected error soft-deleting a starred session")
		}
	})

	t.Run("already soft-deleted", func(t *testing.T) {
		sm := newTestSessionManager(t)
		addStoppedSession(t, sm, "dreich-id", "dreich")

		if _, err := sm.SoftDelete("dreich-id"); err != nil {
			t.Fatalf("first SoftDelete error = %v", err)
		}

		if _, err := sm.SoftDelete("dreich-id"); err == nil {
			t.Error("expected error soft-deleting an already-deleted session")
		}
	})

	t.Run("creating", func(t *testing.T) {
		sm := newTestSessionManager(t)
		s := addStoppedSession(t, sm, "haar-id", "haar")
		s.Status = StatusCreating

		if _, err := sm.SoftDelete("haar-id"); err == nil {
			t.Error("expected error soft-deleting a creating session")
		}
	})

	t.Run("not found", func(t *testing.T) {
		sm := newTestSessionManager(t)

		if _, err := sm.SoftDelete("thrawn-id"); err == nil {
			t.Error("expected error soft-deleting a missing session")
		}
	})
}

func TestRestoreClearsMarker(t *testing.T) {
	sm := newTestSessionManager(t)
	addStoppedSession(t, sm, "bide-id", "bide")

	if _, err := sm.SoftDelete("bide-id"); err != nil {
		t.Fatalf("SoftDelete error = %v", err)
	}

	restored, err := sm.Restore("bide-id")
	if err != nil {
		t.Fatalf("Restore error = %v", err)
	}

	if restored.DeletedAt != nil || restored.ExpiresAt != nil {
		t.Error("Restore did not clear DeletedAt/ExpiresAt")
	}

	if restored.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", restored.Status)
	}
}

func TestRestoreRejections(t *testing.T) {
	t.Run("not deleted", func(t *testing.T) {
		sm := newTestSessionManager(t)
		addStoppedSession(t, sm, "bonnie-id", "bonnie")

		if _, err := sm.Restore("bonnie-id"); err == nil {
			t.Error("expected error restoring a session that is not deleted")
		}
	})

	t.Run("not found", func(t *testing.T) {
		sm := newTestSessionManager(t)

		if _, err := sm.Restore("scunner-id"); err == nil {
			t.Error("expected error restoring a missing session")
		}
	})

	t.Run("expired window", func(t *testing.T) {
		sm := newTestSessionManager(t)
		s := addStoppedSession(t, sm, "thrawn-id", "thrawn")

		// Mark soft-deleted with an already-past expiry.
		past := time.Now().Add(-2 * time.Hour)
		expired := time.Now().Add(-time.Hour)
		s.DeletedAt = &past
		s.ExpiresAt = &expired

		if _, err := sm.Restore("thrawn-id"); err == nil {
			t.Error("expected error restoring a session past its recovery window")
		}
	})
}

func TestSoftDeleteWithChildren(t *testing.T) {
	sm := newTestSessionManager(t)
	addStoppedSession(t, sm, "ben-root", "ben")
	c1 := addStoppedSession(t, sm, "bairn-1", "bairn-one")
	c1.ParentID = "ben-root"
	c2 := addStoppedSession(t, sm, "bairn-2", "bairn-two")
	c2.ParentID = "bairn-1"

	deleted, err := sm.SoftDeleteWithChildren("ben-root", false)
	if err != nil {
		t.Fatalf("SoftDeleteWithChildren error = %v", err)
	}

	if len(deleted) != 3 {
		t.Errorf("deleted %d sessions, want 3", len(deleted))
	}

	for _, id := range []string{"ben-root", "bairn-1", "bairn-2"} {
		s, ok := sm.Get(id)
		if !ok || !s.IsSoftDeleted() {
			t.Errorf("session %q not soft-deleted", id)
		}
	}
}

func TestRestoreWithChildren(t *testing.T) {
	sm := newTestSessionManager(t)
	addStoppedSession(t, sm, "brae-root", "brae")
	c1 := addStoppedSession(t, sm, "bairn-a", "bairn-a")
	c1.ParentID = "brae-root"

	if _, err := sm.SoftDeleteWithChildren("brae-root", false); err != nil {
		t.Fatalf("SoftDeleteWithChildren error = %v", err)
	}

	restored, err := sm.RestoreWithChildren("brae-root")
	if err != nil {
		t.Fatalf("RestoreWithChildren error = %v", err)
	}

	if len(restored) != 2 {
		t.Errorf("restored %d sessions, want 2", len(restored))
	}

	for _, id := range []string{"brae-root", "bairn-a"} {
		s, ok := sm.Get(id)
		if !ok || s.IsSoftDeleted() {
			t.Errorf("session %q still soft-deleted after restore", id)
		}
	}
}

func TestSoftDeletedDescendantCount(t *testing.T) {
	sm := newTestSessionManager(t)
	addStoppedSession(t, sm, "ben-p", "ben-parent")
	c1 := addStoppedSession(t, sm, "bairn-x", "bairn-x")
	c1.ParentID = "ben-p"
	c2 := addStoppedSession(t, sm, "bairn-y", "bairn-y")
	c2.ParentID = "ben-p"

	// Soft-delete only the children (exclude root).
	if _, err := sm.SoftDeleteWithChildren("ben-p", true); err != nil {
		t.Fatalf("SoftDeleteWithChildren error = %v", err)
	}

	if got := sm.softDeletedDescendantCount("ben-p"); got != 2 {
		t.Errorf("softDeletedDescendantCount = %d, want 2", got)
	}
}

func TestPurgeExpired(t *testing.T) {
	sm := newTestSessionManager(t)
	now := time.Now()

	// Expired soft-deleted session.
	exp := addStoppedSession(t, sm, "auld-id", "auld")
	dPast := now.Add(-48 * time.Hour)
	ePast := now.Add(-24 * time.Hour)
	exp.DeletedAt = &dPast
	exp.ExpiresAt = &ePast

	// Fresh soft-deleted session (still within window).
	fresh := addStoppedSession(t, sm, "bonnie-id", "bonnie")
	dNow := now
	eFuture := now.Add(24 * time.Hour)
	fresh.DeletedAt = &dNow
	fresh.ExpiresAt = &eFuture

	// Live session, untouched.
	addStoppedSession(t, sm, "braw-id", "braw")

	sm.purgeExpired(now)

	if _, ok := sm.Get("auld-id"); ok {
		t.Error("expired session was not purged")
	}

	if s, ok := sm.Get("bonnie-id"); !ok || !s.IsSoftDeleted() {
		t.Error("fresh soft-deleted session should survive purge")
	}

	if _, ok := sm.Get("braw-id"); !ok {
		t.Error("live session should never be purged")
	}
}

// TestPurgeSkipsRestored covers the compare-and-delete guarantee at the logical
// level: a session restored (marker cleared) before the sweep acts is not
// purged, even if it was expired when soft-deleted.
func TestPurgeSkipsRestored(t *testing.T) {
	sm := newTestSessionManager(t)
	now := time.Now()

	s := addStoppedSession(t, sm, "ken-id", "ken")
	dPast := now.Add(-48 * time.Hour)
	ePast := now.Add(-time.Hour)
	s.DeletedAt = &dPast
	s.ExpiresAt = &ePast

	// Simulate a restore clearing the marker before the sweep.
	sm.mu.Lock()
	s.DeletedAt = nil
	s.ExpiresAt = nil
	sm.mu.Unlock()

	sm.purgeExpired(now)

	if _, ok := sm.Get("ken-id"); !ok {
		t.Error("restored session must not be purged")
	}
}
