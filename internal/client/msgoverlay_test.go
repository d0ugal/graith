package client

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func cm(stream, senderID, senderName, body, createdAt string) protocol.ConversationMessage {
	return protocol.ConversationMessage{
		Stream:     stream,
		SenderID:   senderID,
		SenderName: senderName,
		Body:       body,
		CreatedAt:  createdAt,
	}
}

func findConv(convs []msgConversation, peerID string) *msgConversation {
	for i := range convs {
		if convs[i].peerID == peerID {
			return &convs[i]
		}
	}
	return nil
}

func TestGroupConversationsDirections(t *testing.T) {
	self := "ben"
	names := map[string]string{"bairn": "wee-bairn"}
	msgs := []protocol.ConversationMessage{
		cm("inbox:ben", "bairn", "wee-bairn", "task done", "2026-06-25T10:00:00Z"), // received
		cm("inbox:bairn", "ben", "ben", "review please", "2026-06-25T10:00:01Z"),   // sent
	}

	convs := groupConversations(self, msgs, names)
	if len(convs) != 1 {
		t.Fatalf("got %d conversations, want 1", len(convs))
	}
	c := convs[0]
	if c.peerID != "bairn" {
		t.Fatalf("peerID = %q, want bairn", c.peerID)
	}
	if c.peerName != "wee-bairn" {
		t.Errorf("peerName = %q, want wee-bairn (from names map)", c.peerName)
	}
	if len(c.messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(c.messages))
	}
	if c.messages[0].outbound {
		t.Error("received message marked outbound")
	}
	if !c.messages[1].outbound {
		t.Error("sent message not marked outbound")
	}
}

func TestGroupConversationsSelfMessage(t *testing.T) {
	convs := groupConversations("kirk", []protocol.ConversationMessage{
		cm("inbox:kirk", "kirk", "kirk", "note to self", "2026-06-25T10:00:00Z"),
	}, nil)
	c := findConv(convs, "kirk")
	if c == nil {
		t.Fatal("self-conversation not found")
	}
	if len(c.messages) != 1 || !c.messages[0].outbound {
		t.Errorf("self-message should appear once as outbound, got %+v", c.messages)
	}
}

func TestGroupConversationsNameFallback(t *testing.T) {
	// No names map; received message carries a sender name, sent message does
	// not — the peer name should come from the received message.
	convs := groupConversations("ben", []protocol.ConversationMessage{
		cm("inbox:bairn", "ben", "ben", "hi", "2026-06-25T10:00:00Z"),          // sent: peer=bairn, no name
		cm("inbox:ben", "bairn", "wee-bairn", "hello", "2026-06-25T10:00:01Z"), // received: carries name
	}, nil)
	c := findConv(convs, "bairn")
	if c == nil {
		t.Fatal("conversation with bairn not found")
	}
	if c.peerName != "wee-bairn" {
		t.Errorf("peerName = %q, want wee-bairn (from received sender_name)", c.peerName)
	}
}

func TestGroupConversationsShortIDFallback(t *testing.T) {
	// Unknown peer with no name anywhere falls back to a short id.
	convs := groupConversations("ben", []protocol.ConversationMessage{
		cm("inbox:abcdef1234567890", "ben", "ben", "hi", "2026-06-25T10:00:00Z"),
	}, nil)
	c := findConv(convs, "abcdef1234567890")
	if c == nil {
		t.Fatal("conversation not found")
	}
	if c.peerName != "abcdef12" {
		t.Errorf("peerName = %q, want short id abcdef12", c.peerName)
	}
}

func TestGroupConversationsSystemClassification(t *testing.T) {
	convs := groupConversations("ben", []protocol.ConversationMessage{
		cm("inbox:ben", "orch-1", "orchestrator", "manifest", "2026-06-25T10:00:00Z"),
	}, nil)
	c := findConv(convs, "orch-1")
	if c == nil || len(c.messages) != 1 {
		t.Fatalf("conversation/messages missing: %+v", convs)
	}
	if !c.messages[0].system {
		t.Error("orchestrator message not classified as system")
	}
}

func TestGroupConversationsSortedByActivity(t *testing.T) {
	convs := groupConversations("ben", []protocol.ConversationMessage{
		cm("inbox:ben", "auld", "auld", "old", "2026-06-25T10:00:00Z"),
		cm("inbox:ben", "bonnie", "bonnie", "new", "2026-06-25T11:00:00Z"),
	}, nil)
	if len(convs) != 2 {
		t.Fatalf("got %d, want 2", len(convs))
	}
	if convs[0].peerID != "bonnie" {
		t.Errorf("most recent conversation first: got %q, want bonnie", convs[0].peerID)
	}
}

func TestSanitizeMessageBody(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"newlines and tabs kept", "a\nb\tc", "a\nb\tc"},
		{"ansi color stripped", "\x1b[31mred\x1b[0m", "red"},
		{"cursor move stripped", "before\x1b[2Jafter", "beforeafter"},
		{"bare control chars stripped", "a\x07\x00b", "ab"},
		{"osc clipboard stripped", "\x1b]52;c;Zm9v\x07x", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeMessageBody(tc.in); got != tc.want {
				t.Errorf("sanitizeMessageBody(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
