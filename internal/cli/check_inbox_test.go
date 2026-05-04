package cli

import "testing"

func TestInboxSubMsgAcksMessages(t *testing.T) {
	msg := inboxSubMsg("sess-123")

	if msg.Stream != "inbox:sess-123" {
		t.Errorf("Stream = %q, want %q", msg.Stream, "inbox:sess-123")
	}
	if msg.Subscriber != "sess-123" {
		t.Errorf("Subscriber = %q, want %q", msg.Subscriber, "sess-123")
	}
	if !msg.OnlyUnread {
		t.Error("OnlyUnread = false, want true")
	}
	if !msg.Ack {
		t.Error("Ack = false, want true — messages must be acked to prevent duplicates on restart (issue #277)")
	}
}
