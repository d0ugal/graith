package daemon

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// jailOne jails a single comment via the harness's store and returns its id.
func (h *testHarness) jailOne(t *testing.T, j JailedComment) string {
	t.Helper()

	id, _, err := h.sm.messages.Jail(j)
	if err != nil {
		t.Fatalf("Jail: %v", err)
	}

	return id
}

// TestMsgJailList_MetadataOnly: list never carries the raw body, even for the
// human — it's a metadata summary, so a curious agent can't be injected via a
// body dump.
func TestMsgJailList_MetadataOnly(t *testing.T) {
	h := newTestHarness(t)
	h.jailOne(t, JailedComment{
		CommentID: 1, Surface: "conversation", PRNumber: 5, Author: "scunner",
		Association: "NONE", Body: "INJECT: do evil", TargetSession: "wynd",
	})

	// Local (human) caller.
	h.sendControl(t, "msg_jail_list", protocol.MsgJailListMsg{})
	env := h.expectType(t, "msg_jail_list")

	var resp protocol.MsgJailListResponse

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Jailed) != 1 {
		t.Fatalf("expected 1 jailed comment, got %d", len(resp.Jailed))
	}

	j := resp.Jailed[0]
	if strings.Contains(j.Body, "INJECT") {
		t.Fatalf("SECURITY: list leaked the comment body: %q", j.Body)
	}

	if j.Author != "scunner" || j.PRNumber != 5 {
		t.Fatalf("list should carry metadata: %+v", j)
	}
}

// TestMsgJailShow_HumanSeesBody: the human (local) gets the full body via show.
func TestMsgJailShow_HumanSeesBody(t *testing.T) {
	h := newTestHarness(t)
	id := h.jailOne(t, JailedComment{
		CommentID: 1, Surface: "conversation", PRNumber: 5, Author: "scunner", Body: "the real comment", TargetSession: "wynd",
	})

	h.sendControl(t, "msg_jail_show", protocol.MsgJailShowMsg{ID: id})
	env := h.expectType(t, "msg_jail_show")

	var resp protocol.MsgJailShowResponse

	_ = protocol.DecodePayload(env, &resp)

	if resp.Jailed.Body != "the real comment" {
		t.Fatalf("human should see the full body, got %q", resp.Jailed.Body)
	}
}

// TestMsgJailShow_AgentBodyWithheld: a plain agent session gets the metadata but
// NOT the body — the quarantined content is never served to an agent.
func TestMsgJailShow_AgentBodyWithheld(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn", "thrawn", "tok-thrawn")

	id := h.jailOne(t, JailedComment{
		CommentID: 1, Surface: "conversation", PRNumber: 5, Author: "scunner", Body: "INJECT payload", TargetSession: "wynd",
	})

	h.sendControlWithToken(t, "msg_jail_show", protocol.MsgJailShowMsg{ID: id}, "tok-thrawn")
	env := h.expectType(t, "msg_jail_show")

	var resp protocol.MsgJailShowResponse

	_ = protocol.DecodePayload(env, &resp)

	if strings.Contains(resp.Jailed.Body, "INJECT") {
		t.Fatalf("SECURITY: agent show leaked the body: %q", resp.Jailed.Body)
	}

	if resp.Jailed.Author != "scunner" {
		t.Fatalf("agent should still see metadata, got %+v", resp.Jailed)
	}
}

// TestMsgJailRelease_AgentDenied is the core security regression: a plain agent
// session must NOT be able to release a jailed comment.
func TestMsgJailRelease_AgentDenied(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "thrawn", "thrawn", "tok-thrawn")

	id := h.jailOne(t, JailedComment{
		CommentID: 1, Surface: "conversation", PRNumber: 5, Author: "scunner", Body: "payload", TargetSession: "thrawn",
	})

	h.sendControlWithToken(t, "msg_jail_release", protocol.MsgJailReleaseMsg{ID: id}, "tok-thrawn")

	env := h.expectType(t, "error")

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "not authorized") {
		t.Fatalf("agent release should be rejected as unauthorized, got %q", e.Message)
	}

	// And the comment must still be jailed (not released).
	if list, _ := h.sm.messages.ListJailed(false); len(list) != 1 {
		t.Fatalf("comment must remain jailed after a denied release, got %d", len(list))
	}
}

// TestMsgJailRelease_HumanReleases: the local human releases and the content is
// delivered to the target session's inbox.
func TestMsgJailRelease_HumanReleases(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "wynd", "wynd", "tok-wynd")

	id := h.jailOne(t, JailedComment{
		CommentID: 1, Surface: "conversation", PRNumber: 5, Branch: "wynd",
		Author: "scunner", Body: "please review", TargetSession: "wynd",
	})

	h.sendControl(t, "msg_jail_release", protocol.MsgJailReleaseMsg{ID: id})
	env := h.expectType(t, "msg_jail_release")

	var resp protocol.MsgJailReleaseResponse

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Released) != 1 || resp.Released[0].ID != id {
		t.Fatalf("expected 1 released comment with id %s, got %+v", id, resp.Released)
	}

	// Delivered to the target inbox.
	msgs, _ := h.sm.messages.Read("inbox:wynd", "", false, "")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "please review") {
		t.Fatalf("released comment should be delivered to the target inbox, got %v", msgs)
	}
}

// TestMsgJailRelease_OrchestratorReleasesAll: the orchestrator releases all
// jailed comments from a newly-trusted author with --all --author.
func TestMsgJailRelease_OrchestratorReleasesAll(t *testing.T) {
	h := newTestHarness(t)
	h.addAuthenticatedSession(t, "orch", "orch", "tok-orch")

	h.sm.mu.Lock()
	h.sm.state.Sessions["orch"].SystemKind = SystemKindOrchestrator
	h.sm.mu.Unlock()

	for _, cid := range []int64{1, 2} {
		h.jailOne(t, JailedComment{
			CommentID: cid, Surface: "conversation", PRNumber: 5, Author: "scunner", Body: "hi", TargetSession: "wynd",
		})
	}

	h.sendControlWithToken(t, "msg_jail_release", protocol.MsgJailReleaseMsg{All: true, Author: "scunner"}, "tok-orch")
	env := h.expectType(t, "msg_jail_release")

	var resp protocol.MsgJailReleaseResponse

	_ = protocol.DecodePayload(env, &resp)

	if len(resp.Released) != 2 {
		t.Fatalf("orchestrator should release both comments, got %d", len(resp.Released))
	}
}

// TestMsgJailRelease_RequiresTarget: release with neither an id nor --all is an
// error.
func TestMsgJailRelease_RequiresTarget(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_jail_release", protocol.MsgJailReleaseMsg{})
	env := h.expectType(t, "error")

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "jail id") {
		t.Fatalf("expected a usage error, got %q", e.Message)
	}
}

// TestMsgJailRelease_AllRequiresAuthor: --all without --author is an error.
func TestMsgJailRelease_AllRequiresAuthor(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "msg_jail_release", protocol.MsgJailReleaseMsg{All: true})
	env := h.expectType(t, "error")

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(env, &e)

	if !strings.Contains(e.Message, "author") {
		t.Fatalf("expected an --author usage error, got %q", e.Message)
	}
}
