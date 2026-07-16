package daemon

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// --- attach_convert -------------------------------------------------------

// TestCoverAttachConvertInvalidPayload verifies a malformed attach_convert is
// rejected before touching the manager.
func TestCoverAttachConvertInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "attach_convert")

	h.expectError(t, "invalid attach_convert")
}

// TestCoverAttachConvertUnknownSession verifies attach_convert on an unknown
// session passes the human auth gate then errors from ConvertToInteractive.
func TestCoverAttachConvertUnknownSession(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "attach_convert", protocol.AttachConvertMsg{SessionID: "haar"})

	h.expectError(t, "not found")
}

// --- restore variants -----------------------------------------------------

// TestCoverRestoreInvalidPayload verifies a malformed restore message errors.
func TestCoverRestoreInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "restore")

	h.expectError(t, "invalid restore")
}

// TestCoverRestoreUnknownSession verifies restore of an unknown session errors.
func TestCoverRestoreUnknownSession(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "restore", protocol.RestoreMsg{SessionID: "haar"})

	h.expectError(t, "not found")
}

// TestCoverRestoreWithChildren verifies the with-children restore path returns
// the whole restored subtree.
func TestCoverRestoreWithChildren(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "ben-id", "ben", 0, "")
	h.addStoppedSession(t, "bairn-id", "bairn", 0, "")
	h.setParent(t, "bairn-id", "ben-id")

	// Soft-delete the whole subtree, then restore it with children.
	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "ben-id", Children: true})
	h.expectType(t, "deleted")

	h.sendControl(t, "restore", protocol.RestoreMsg{SessionID: "ben-id", Children: true})

	env := h.expectType(t, "restored")

	var r protocol.RestoreResultMsg
	if err := protocol.DecodePayload(env, &r); err != nil {
		t.Fatal(err)
	}

	if len(r.Sessions) != 2 {
		t.Fatalf("expected 2 restored sessions, got %d (%+v)", len(r.Sessions), r.Sessions)
	}

	for _, id := range []string{"ben-id", "bairn-id"} {
		s, ok := h.sm.Get(id)
		if !ok || s.IsSoftDeleted() {
			t.Errorf("session %s should be live after restore", id)
		}
	}
}

// --- soft delete with children --------------------------------------------

// TestCoverSoftDeleteWithChildren verifies the default (soft) with-children
// delete marks the whole subtree deleted, reporting each affected session.
func TestCoverSoftDeleteWithChildren(t *testing.T) {
	h := newTestHarness(t)
	h.addStoppedSession(t, "ben-id", "ben", 0, "")
	h.addStoppedSession(t, "bairn-id", "bairn", 0, "")
	h.setParent(t, "bairn-id", "ben-id")

	h.sendControl(t, "delete", protocol.DeleteMsg{SessionID: "ben-id", Children: true})

	env := h.expectType(t, "deleted")

	var r protocol.DeleteResultMsg
	if err := protocol.DecodePayload(env, &r); err != nil {
		t.Fatal(err)
	}

	if !r.Soft {
		t.Error("expected Soft=true for default with-children delete")
	}

	if len(r.Affected) != 2 {
		t.Fatalf("expected 2 affected sessions, got %d (%+v)", len(r.Affected), r.Affected)
	}

	for _, id := range []string{"ben-id", "bairn-id"} {
		s, ok := h.sm.Get(id)
		if !ok || !s.IsSoftDeleted() {
			t.Errorf("session %s should be soft-deleted", id)
		}
	}
}

// --- type -----------------------------------------------------------------

// TestCoverTypeSessionNotFound verifies gr type to a session with no live PTY
// errors.
func TestCoverTypeSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "type", protocol.TypeMsg{SessionID: "haar", Input: "y"})

	h.expectError(t, "session not found")
}

// TestCoverTypeInvalidPayload verifies a malformed type message errors.
func TestCoverTypeInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "type")

	h.expectError(t, "invalid type")
}

// TestCoverTypeInjectsInput verifies gr type into a live (unattached) PTY writes
// the input and replies "typed".
func TestCoverTypeInjectsInput(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-id", "braw")

	h.sendControl(t, "type", protocol.TypeMsg{SessionID: "braw-id", Input: "help", NoNewline: true})

	env := h.expectType(t, "typed")

	var got struct {
		SessionID string `json:"session_id"`
	}
	if err := protocol.DecodePayload(env, &got); err != nil {
		t.Fatal(err)
	}

	if got.SessionID != "braw-id" {
		t.Errorf("typed session_id = %q, want braw-id", got.SessionID)
	}
}

// --- interrupt ------------------------------------------------------------

// TestCoverInterruptSessionNotFound verifies interrupt of a session with no live
// PTY errors.
func TestCoverInterruptSessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "interrupt", protocol.InterruptMsg{SessionID: "haar"})

	h.expectError(t, "no live process to interrupt")
}

// TestCoverInterruptInvalidPayload verifies a malformed interrupt message errors.
func TestCoverInterruptInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "interrupt")

	h.expectError(t, "invalid interrupt")
}

// --- create / fork / migrate error paths ----------------------------------

// TestCoverForkInvalidPayload verifies a malformed fork message errors.
func TestCoverForkInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "fork")

	h.expectError(t, "invalid fork")
}

// TestCoverForkUnknownSource verifies forking an unknown source session errors.
func TestCoverForkUnknownSource(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "fork", protocol.ForkMsg{Name: "canny", SourceSessionID: "haar"})

	h.expectError(t, "not found")
}

// TestCoverMigrateInvalidPayload verifies a malformed migrate message errors.
func TestCoverMigrateInvalidPayload(t *testing.T) {
	h := newTestHarness(t)

	h.sendWrongShapePayload(t, "migrate")

	h.expectError(t, "invalid migrate")
}

// TestCoverMigrateUnknownSession verifies migrating an unknown session errors.
func TestCoverMigrateUnknownSession(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "migrate", protocol.MigrateMsg{SessionID: "haar", Agent: "codex"})

	h.expectError(t, "not found")
}
