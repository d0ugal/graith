package daemon

import "testing"

// publishDM is a small helper that mimics a direct message: a publish to the
// recipient's inbox stream with the sender recorded.
func publishDM(t *testing.T, s *MsgStore, from, to, body string) {
	t.Helper()
	if _, err := s.Publish("inbox:"+to, from, from, body, "", ""); err != nil {
		t.Fatalf("Publish %s->%s: %v", from, to, err)
	}
}

func TestConversationBothDirections(t *testing.T) {
	s := testStore(t)

	// ben is the parent, bairn the child. They exchange DMs; a third session
	// (whin) and a topic should be excluded from ben's conversation.
	publishDM(t, s, "bairn", "ben", "task done")      // received by ben
	publishDM(t, s, "ben", "bairn", "review please")  // sent by ben
	publishDM(t, s, "bairn", "ben", "fixed")          // received by ben
	publishDM(t, s, "whin", "clachan", "not for ben") // third-party DM
	if _, err := s.Publish("blether", "ben", "ben", "topic chatter", "", ""); err != nil {
		t.Fatalf("Publish topic: %v", err)
	}

	convo, err := s.Conversation("ben", 0)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(convo) != 3 {
		t.Fatalf("got %d messages, want 3 (both directions, no third-party, no topic)", len(convo))
	}

	// Ordered chronologically.
	wantBodies := []string{"task done", "review please", "fixed"}
	for i, want := range wantBodies {
		if convo[i].Body != want {
			t.Errorf("convo[%d].Body = %q, want %q", i, convo[i].Body, want)
		}
	}

	// Outbound message is in the peer's inbox with ben as sender.
	if convo[1].Stream != "inbox:bairn" || convo[1].SenderID != "ben" {
		t.Errorf("outbound msg stream=%q sender=%q, want inbox:bairn/ben", convo[1].Stream, convo[1].SenderID)
	}
	// Inbound messages land in ben's inbox.
	if convo[0].Stream != "inbox:ben" {
		t.Errorf("inbound msg stream=%q, want inbox:ben", convo[0].Stream)
	}
}

func TestConversationExcludesThirdPartyAndTopics(t *testing.T) {
	s := testStore(t)

	publishDM(t, s, "canny", "braw", "hello braw")   // braw's conversation
	publishDM(t, s, "dreich", "thrawn", "elsewhere") // unrelated DM
	if _, err := s.Publish("blether", "braw", "braw", "broadcast", "", ""); err != nil {
		t.Fatalf("Publish topic: %v", err)
	}

	convo, err := s.Conversation("braw", 0)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(convo) != 1 {
		t.Fatalf("got %d, want 1 (only the DM to braw)", len(convo))
	}
	if convo[0].Body != "hello braw" {
		t.Errorf("body = %q, want %q", convo[0].Body, "hello braw")
	}
}

func TestConversationSelfMessageAppearsOnce(t *testing.T) {
	s := testStore(t)

	// A message a session sends to its own inbox matches both the received
	// branch (stream = inbox:self) and conceptually the sent branch; it must
	// appear exactly once.
	publishDM(t, s, "kirk", "kirk", "note to self")

	convo, err := s.Conversation("kirk", 0)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(convo) != 1 {
		t.Fatalf("got %d, want 1 (self-message counted once)", len(convo))
	}
}

func TestConversationLimitReturnsMostRecent(t *testing.T) {
	s := testStore(t)

	// Interleave directions so the LIMIT selection exercises cross-stream
	// ordering (sent lands in inbox:bairn, received in inbox:ben), not just a
	// single stream.
	publishDM(t, s, "bairn", "ben", "one")   // received
	publishDM(t, s, "ben", "bairn", "two")   // sent
	publishDM(t, s, "bairn", "ben", "three") // received
	publishDM(t, s, "ben", "bairn", "four")  // sent
	publishDM(t, s, "bairn", "ben", "five")  // received

	convo, err := s.Conversation("ben", 3)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(convo) != 3 {
		t.Fatalf("got %d, want 3 (limit)", len(convo))
	}
	// Most recent 3, still in ascending order, spanning both directions.
	want := []string{"three", "four", "five"}
	for i, w := range want {
		if convo[i].Body != w {
			t.Errorf("convo[%d].Body = %q, want %q", i, convo[i].Body, w)
		}
	}
}

// TestConversationPeerPrefixCollision guards the GLOB + "stream <> inbox:self"
// logic against a peer whose id is a prefix of, or shares a prefix with, self.
func TestConversationPeerPrefixCollision(t *testing.T) {
	s := testStore(t)

	// self = "ben"; peers "ben2" and "benji" have inbox streams that share the
	// "inbox:ben" prefix. A naive LIKE 'inbox:ben%' would wrongly include them.
	publishDM(t, s, "ben2", "ben", "to ben")    // received by ben
	publishDM(t, s, "ben", "ben2", "to ben2")   // sent by ben -> ben2
	publishDM(t, s, "whin", "ben2", "not bens") // unrelated DM to ben2

	convo, err := s.Conversation("ben", 0)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(convo) != 2 {
		t.Fatalf("got %d, want 2 (the DM to ben + ben's DM to ben2)", len(convo))
	}
	bodies := map[string]bool{}
	for _, m := range convo {
		bodies[m.Body] = true
	}
	if !bodies["to ben"] || !bodies["to ben2"] {
		t.Errorf("unexpected bodies: %+v", bodies)
	}
	if bodies["not bens"] {
		t.Error("conversation leaked an unrelated third-party DM to ben2")
	}
}

func TestConversationEmpty(t *testing.T) {
	s := testStore(t)
	convo, err := s.Conversation("haar", 0)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(convo) != 0 {
		t.Fatalf("got %d, want 0 for a session with no messages", len(convo))
	}
}

// TestConversationAuthRule locks the deliberate access decision for the
// msg_conversation control message: it authorises with authSelfOrDescendant, so
// the human CLI (unauthenticated), the session itself, and an ancestor reading a
// descendant are all permitted, while a sibling is denied. If msg_conversation
// is ever changed to a stricter rule (e.g. self-only), this test should fail —
// the cross-inbox "to and from" view depends on this rule. Keep it in sync with
// the rule used in handler.go's "msg_conversation" case.
func TestConversationAuthRule(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"ben":     {ID: "ben"},
		"bairn":   {ID: "bairn", ParentID: "ben"},
		"sibling": {ID: "sibling"},
	})

	const rule = authSelfOrDescendant // the rule handler.go uses for msg_conversation

	// Human CLI (no token) may read any session's conversation.
	human := authContext{}
	if err := human.checkTarget(sm, "ben", rule); err != nil {
		t.Errorf("human reading ben: unexpected error %v", err)
	}

	// A session may read its own conversation.
	self := authContext{sessionID: "ben", authenticated: true}
	if err := self.checkTarget(sm, "ben", rule); err != nil {
		t.Errorf("ben reading self: unexpected error %v", err)
	}

	// An ancestor may read a descendant's conversation (the decided behaviour).
	if err := self.checkTarget(sm, "bairn", rule); err != nil {
		t.Errorf("ben reading descendant bairn: unexpected error %v", err)
	}

	// A sibling/unrelated session may NOT read another's conversation.
	if err := self.checkTarget(sm, "sibling", rule); err == nil {
		t.Error("ben reading unrelated sibling: expected authorization error, got nil")
	}
}
