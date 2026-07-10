package daemon

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// softDeleteViaHandler soft-deletes a stopped session through the delete handler
// and returns the parsed result.
func softDeleteViaHandler(t *testing.T, h *testHarness, id string) protocol.DeleteResultMsg {
	t.Helper()

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: id})

	env := h.readControlMsg(t)
	if env.Type != "deleted" {
		t.Fatalf("expected deleted, got %q", env.Type)
	}

	var r protocol.DeleteResultMsg

	_ = protocol.DecodePayload(env, &r)

	return r
}

func TestListFiltersDeleted(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "braw-live", "braw", 0, "")
	h.addStoppedSession(t, "dreich-del", "dreich", 0, "")

	softDeleteViaHandler(t, h, "dreich-del")

	// Default list (Deleted=false) shows only the live session.
	h.sendControl(t, "list", protocol.ListMsg{})

	env := h.readControlMsg(t)

	var live protocol.SessionListMsg

	_ = protocol.DecodePayload(env, &live)

	if len(live.Sessions) != 1 || live.Sessions[0].ID != "braw-live" {
		t.Fatalf("live list = %+v, want only braw-live", live.Sessions)
	}

	// Deleted list shows only the soft-deleted session, with expiry populated.
	h.sendControl(t, "list", protocol.ListMsg{Deleted: true})

	env = h.readControlMsg(t)

	var deleted protocol.SessionListMsg

	_ = protocol.DecodePayload(env, &deleted)

	if len(deleted.Sessions) != 1 || deleted.Sessions[0].ID != "dreich-del" {
		t.Fatalf("deleted list = %+v, want only dreich-del", deleted.Sessions)
	}

	d := deleted.Sessions[0]
	if d.DeletedAt == "" || d.DeleteExpiresAt == "" {
		t.Errorf("deleted session missing DeletedAt/DeleteExpiresAt: %+v", d)
	}
}

func TestHandleDeleteSoftDefault(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "braw-id", "braw", 0, "")

	r := softDeleteViaHandler(t, h, "braw-id")

	if !r.Soft {
		t.Error("expected Soft=true for default delete")
	}

	if r.ExpiresAt == "" {
		t.Error("expected ExpiresAt on a soft delete result")
	}

	s, ok := h.sm.Get("braw-id")
	if !ok || !s.IsSoftDeleted() {
		t.Error("session should remain in state, soft-deleted")
	}
}

func TestHandleRestore(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "bide-id", "bide", 0, "")
	softDeleteViaHandler(t, h, "bide-id")

	h.sendControl(t, "restore", protocol.RestoreMsg{SessionID: "bide-id"})

	env := h.readControlMsg(t)
	if env.Type != "restored" {
		t.Fatalf("expected restored, got %q", env.Type)
	}

	var r protocol.RestoreResultMsg

	_ = protocol.DecodePayload(env, &r)

	if len(r.Sessions) != 1 || r.Sessions[0].ID != "bide-id" {
		t.Fatalf("restore result = %+v, want bide-id", r.Sessions)
	}

	s, ok := h.sm.Get("bide-id")
	if !ok || s.IsSoftDeleted() {
		t.Error("session should be live (not soft-deleted) after restore")
	}
}

func TestHandlePurgeHardDeletes(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "auld-id", "auld", 0, "")

	// gr purge sets Purge=true → hard delete regardless of retention.
	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "auld-id", Purge: true})

	env := h.readControlMsg(t)
	if env.Type != "deleted" {
		t.Fatalf("expected deleted, got %q", env.Type)
	}

	var r protocol.DeleteResultMsg

	_ = protocol.DecodePayload(env, &r)

	if r.Soft {
		t.Error("expected Soft=false for a purge")
	}

	if _, ok := h.sm.Get("auld-id"); ok {
		t.Error("purged session should be removed from state")
	}
}

// zeroRetentionConfig returns a Default config with soft delete disabled.
func zeroRetentionConfig() *config.Config {
	cfg := config.Default()
	cfg.Delete.Retention = "0"

	return cfg
}

// TestHandleDeleteRejectedWhenRetentionZero pins the settled retention==0
// routing: gr delete (Purge=false) is rejected — it never destroys the session.
func TestHandleDeleteRejectedWhenRetentionZero(t *testing.T) {
	h := newTestHarnessWithConfig(t, zeroRetentionConfig())
	h.addStoppedSession(t, "thrawn-id", "thrawn", 0, "")

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "thrawn-id"})
	h.expectError(t, "soft delete is disabled")

	if _, ok := h.sm.Get("thrawn-id"); !ok {
		t.Error("rejected delete must not destroy the session")
	}
}

// TestHandlePurgeWorksWhenRetentionZero confirms the destructive verb still
// works with soft delete disabled: gr purge (Purge=true) hard-deletes.
func TestHandlePurgeWorksWhenRetentionZero(t *testing.T) {
	h := newTestHarnessWithConfig(t, zeroRetentionConfig())
	h.addStoppedSession(t, "scunner-id", "scunner", 0, "")

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "scunner-id", Purge: true})

	env := h.readControlMsg(t)
	if env.Type != "deleted" {
		t.Fatalf("expected deleted, got %q", env.Type)
	}

	if _, ok := h.sm.Get("scunner-id"); ok {
		t.Error("purge with retention=0 should hard-delete")
	}

	// TODO(#994): a matching batch reject case (`gr delete --stopped` with
	// retention=0) belongs here once batch routing is exercised end-to-end; the
	// single-session reject above pins the settled predicate.
}
