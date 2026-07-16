package daemon

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// TestCreateOptsFromMsgMapsEveryField locks the CreateMsg→CreateOpts field
// mapping: it's the highest-risk "silently drop a field" spot in the refactor.
// Every non-Codex CreateMsg field is set to a distinctive value and asserted to
// land on the right CreateOpts field, with agentName/rows/cols passed through.
func TestCreateOptsFromMsgMapsEveryField(t *testing.T) {
	c := protocol.CreateMsg{
		Name:                "braw",
		ParentID:            "ben-id",
		Agent:               "ignored-here", // agentName is resolved by the caller
		RepoPath:            "/croft/graith",
		Base:                "main",
		Prompt:              "forge the brig",
		Model:               "claude-opus-4-8",
		NoRepo:              true,
		Mirror:              "bothy",
		AgentHooks:          true,
		InPlace:             true,
		AllowConcurrent:     true,
		SkipModelValidation: true,
		Yolo:                true,
		Headless:            true,
		NoFetch:             true,
		Codex:               &config.CodexOptions{Profile: "canny"},
	}

	got := createOptsFromMsg(c, "codex", 40, 120)

	checks := []struct {
		field string
		ok    bool
	}{
		{"Name", got.Name == "braw"},
		{"AgentName(resolved)", got.AgentName == "codex"},
		{"RepoPath", got.RepoPath == "/croft/graith"},
		{"BaseBranch", got.BaseBranch == "main"},
		{"Prompt", got.Prompt == "forge the brig"},
		{"Model", got.Model == "claude-opus-4-8"},
		{"ParentID", got.ParentID == "ben-id"},
		{"NoRepo", got.NoRepo},
		{"Mirror", got.Mirror == "bothy"},
		{"AgentHooks", got.AgentHooks},
		{"InPlace", got.InPlace},
		{"AllowConcurrent", got.AllowConcurrent},
		{"SkipModelValidation", got.SkipModelValidation},
		{"Yolo", got.Yolo},
		{"Headless", got.Headless},
		{"NoFetch", got.NoFetch},
		{"Codex", got.Codex.Profile == "canny"},
		{"Rows", got.Rows == 40},
		{"Cols", got.Cols == 120},
	}

	for _, ch := range checks {
		if !ch.ok {
			t.Errorf("createOptsFromMsg: field %s did not map correctly (got %+v)", ch.field, got)
		}
	}
}

// TestCreateOptsFromMsgNilCodex verifies a nil Codex pointer maps to the zero
// CodexOptions (not a nil deref).
func TestCreateOptsFromMsgNilCodex(t *testing.T) {
	got := createOptsFromMsg(protocol.CreateMsg{Name: "neep"}, "claude", 24, 80)

	if got.Codex != (config.CodexOptions{}) {
		t.Errorf("nil Codex should map to zero CodexOptions, got %+v", got.Codex)
	}
}

// TestClampConversationLimit exercises the pure limit-normalisation logic
// extracted from the msg_conversation handler: non-positive defaults, the
// pass-through band, and the upper cap.
func TestClampConversationLimit(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero defaults to page size", 0, 500},
		{"negative defaults to page size", -7, 500},
		{"one passes through", 1, 1},
		{"mid passes through", 750, 750},
		{"exactly max passes through", maxConversationLimit, maxConversationLimit},
		{"above max is capped", maxConversationLimit + 1, maxConversationLimit},
		{"huge is capped", 1 << 20, maxConversationLimit},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampConversationLimit(tc.in); got != tc.want {
				t.Errorf("clampConversationLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestFilterInboxStreams verifies inbox streams are dropped while topics and
// other streams are preserved in order.
func TestFilterInboxStreams(t *testing.T) {
	in := []StreamInfo{
		{Name: "blether"},
		{Name: "inbox:braw-id"},
		{Name: "topic:ken"},
		{Name: "inbox:canny-id"},
		{Name: "loch"},
	}

	got := filterInboxStreams(in)

	want := []string{"blether", "topic:ken", "loch"}
	if len(got) != len(want) {
		t.Fatalf("filtered length = %d, want %d (%+v)", len(got), len(want), got)
	}

	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("filtered[%d] = %q, want %q", i, got[i].Name, w)
		}
	}
}

// TestFilterInboxStreamsAllInbox confirms an all-inbox slice filters to empty
// (not nil-panicking) and a no-inbox slice is untouched.
func TestFilterInboxStreamsAllInbox(t *testing.T) {
	if got := filterInboxStreams([]StreamInfo{{Name: "inbox:a"}, {Name: "inbox:b"}}); len(got) != 0 {
		t.Errorf("all-inbox filtered length = %d, want 0", len(got))
	}

	none := []StreamInfo{{Name: "glen"}, {Name: "brae"}}
	if got := filterInboxStreams(none); len(got) != 2 {
		t.Errorf("no-inbox filtered length = %d, want 2", len(got))
	}
}

// TestConfigFileExists checks the config-presence interpretation: nil means
// present, ErrNotExist means absent, and any other stat error is ambiguous and
// treated as present (don't claim defaults when we can't tell).
func TestConfigFileExists(t *testing.T) {
	tests := []struct {
		name    string
		statErr error
		want    bool
	}{
		{"nil means present", nil, true},
		{"not-exist means absent", os.ErrNotExist, false},
		{"wrapped not-exist means absent", &fs.PathError{Op: "stat", Path: "x", Err: os.ErrNotExist}, false},
		{"permission error is ambiguous, treat as present", os.ErrPermission, true},
		{"generic error is ambiguous, treat as present", errors.New("dreich"), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := configFileExists(tc.statErr); got != tc.want {
				t.Errorf("configFileExists(%v) = %v, want %v", tc.statErr, got, tc.want)
			}
		})
	}
}

// TestApplyMsgPubSenderIdentityRemoteHuman verifies a paired remote human
// publishes as its device and can never claim to be a session.
func TestApplyMsgPubSenderIdentityRemoteHuman(t *testing.T) {
	h := newTestHarness(t)

	m := protocol.MsgPubMsg{SenderID: "spoofed-session", SenderName: "impostor"}
	auth := authContext{role: roleRemoteHuman, deviceID: "dev-braw"}

	if ok := applyMsgPubSenderIdentity(h.sm, auth, &m, func(string, any) {}); !ok {
		t.Fatal("remote human should be authorized to publish")
	}

	if m.SenderID != "device:dev-braw" {
		t.Errorf("SenderID = %q, want %q", m.SenderID, "device:dev-braw")
	}

	if m.SenderName != "remote device" {
		t.Errorf("SenderName = %q, want %q", m.SenderName, "remote device")
	}
}

// TestApplyMsgPubSenderIdentitySession verifies a session's publish is forced to
// its own identity, overriding any client-supplied sender.
func TestApplyMsgPubSenderIdentitySession(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "braw-id", "bonnie", "tok-braw")

	m := protocol.MsgPubMsg{SenderID: "spoofed", SenderName: "impostor"}
	auth := authContext{role: roleSession, sessionID: "braw-id"}

	if ok := applyMsgPubSenderIdentity(h.sm, auth, &m, func(string, any) {}); !ok {
		t.Fatal("session should be authorized to publish")
	}

	if m.SenderID != "braw-id" {
		t.Errorf("SenderID = %q, want %q", m.SenderID, "braw-id")
	}

	if m.SenderName != "bonnie" {
		t.Errorf("SenderName = %q, want %q (should resolve from session)", m.SenderName, "bonnie")
	}
}

// TestApplyMsgPubSenderIdentityUnauthorized verifies a role with no publish
// rights (an unpaired remote / read-only guest) is rejected with an error and
// the sender identity is left untouched (no mutation on the reject path).
func TestApplyMsgPubSenderIdentityUnauthorized(t *testing.T) {
	h := newTestHarness(t)

	for _, role := range []authRole{roleNone, roleRemoteGuest} {
		var sent string

		// Seed distinctive sender fields so we can prove they are not rewritten.
		m := protocol.MsgPubMsg{SenderID: "untouched-id", SenderName: "untouched-name"}
		auth := authContext{role: role}

		ok := applyMsgPubSenderIdentity(h.sm, auth, &m, func(msgType string, _ any) { sent = msgType })
		if ok {
			t.Errorf("role %d should not be authorized to publish", role)
		}

		if sent != "error" {
			t.Errorf("role %d: expected error message, got %q", role, sent)
		}

		if m.SenderID != "untouched-id" || m.SenderName != "untouched-name" {
			t.Errorf("role %d: reject path mutated identity: SenderID=%q SenderName=%q", role, m.SenderID, m.SenderName)
		}
	}
}

// TestApplyMsgPubSenderIdentityLocalHumanNoSender verifies the local human with
// an empty SenderID (not addressing on behalf of anyone) is authorized and no
// name is resolved.
func TestApplyMsgPubSenderIdentityLocalHumanNoSender(t *testing.T) {
	h := newTestHarness(t)

	m := protocol.MsgPubMsg{}
	auth := authContext{role: roleLocalHuman}

	if ok := applyMsgPubSenderIdentity(h.sm, auth, &m, func(string, any) {}); !ok {
		t.Fatal("local human should be authorized to publish without a sender")
	}

	if m.SenderID != "" || m.SenderName != "" {
		t.Errorf("expected empty sender identity, got SenderID=%q SenderName=%q", m.SenderID, m.SenderName)
	}
}

// TestApplyMsgPubSenderIdentityLocalHumanOnBehalf verifies the local human (CLI)
// may address on behalf of a named session, resolving its display name.
func TestApplyMsgPubSenderIdentityLocalHumanOnBehalf(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "canny-id", "canny", "tok-canny")

	m := protocol.MsgPubMsg{SenderID: "canny-id"}
	auth := authContext{role: roleLocalHuman}

	if ok := applyMsgPubSenderIdentity(h.sm, auth, &m, func(string, any) {}); !ok {
		t.Fatal("local human should be authorized to publish")
	}

	if m.SenderID != "canny-id" || m.SenderName != "canny" {
		t.Errorf("got SenderID=%q SenderName=%q, want canny-id/canny", m.SenderID, m.SenderName)
	}
}

// TestAuthorizeUpdateOrphanRequiresOrchestrator verifies clearing a session's
// parent (orphaning) is denied for an ordinary session but allowed for the
// human CLI.
func TestAuthorizeUpdateOrphanRequiresOrchestrator(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "bairn-id", "bairn", "tok-bairn")

	empty := ""

	// An authenticated non-orchestrator session may not orphan itself.
	sessionAuth := authContext{role: roleSession, authenticated: true, sessionID: "bairn-id"}
	if err := authorizeUpdate(h.sm, sessionAuth, protocol.UpdateMsg{SessionID: "bairn-id", ParentID: &empty}); err == nil {
		t.Fatal("expected orphan by non-orchestrator session to be denied")
	}

	// The human CLI (unauthenticated local) is exempt and may orphan.
	humanAuth := authContext{role: roleLocalHuman}
	if err := authorizeUpdate(h.sm, humanAuth, protocol.UpdateMsg{SessionID: "bairn-id", ParentID: &empty}); err != nil {
		t.Fatalf("human CLI should be allowed to orphan, got %v", err)
	}
}

// TestAuthorizeUpdateReparentRequiresAuthorityOverNewParent covers the
// anti-privilege-escalation guard: adopting a NEW parent (non-empty ParentID)
// must be authorized over that parent too, so a session can't reparent an
// unrelated session under itself to manufacture a descendant relationship.
func TestAuthorizeUpdateReparentRequiresAuthorityOverNewParent(t *testing.T) {
	h := newTestHarness(t)
	// Two unrelated authenticated sessions; neither is an ancestor of the other.
	h.addAuthenticatedSession(t, "bairn-id", "bairn", "tok-bairn")
	h.addAuthenticatedSession(t, "stranger-id", "stranger", "tok-stranger")

	// bairn (a plain session) tries to adopt stranger — a session it has no
	// authority over — as its new parent. Must be denied.
	stranger := "stranger-id"
	sessionAuth := authContext{role: roleSession, authenticated: true, sessionID: "bairn-id"}

	err := authorizeUpdate(h.sm, sessionAuth, protocol.UpdateMsg{SessionID: "bairn-id", ParentID: &stranger})
	if err == nil {
		t.Fatal("expected reparent under an unauthorized new parent to be denied")
	}

	// The human CLI is exempt and may reparent freely.
	humanAuth := authContext{role: roleLocalHuman}
	if err := authorizeUpdate(h.sm, humanAuth, protocol.UpdateMsg{SessionID: "bairn-id", ParentID: &stranger}); err != nil {
		t.Fatalf("human CLI should be allowed to reparent, got %v", err)
	}
}
