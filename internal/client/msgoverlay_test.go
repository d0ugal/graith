package client

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

// testModel builds a loaded overlay model with one conversation of n messages.
func testModel(n int) messageOverlayModel {
	msgs := make([]protocol.ConversationMessage, n)
	for i := 0; i < n; i++ {
		// Two-line body: the first line shows as the collapsed snippet; the
		// "detail N" line only renders when the message is expanded.
		msgs[i] = protocol.ConversationMessage{
			ID:        "m" + strconv.Itoa(i),
			Stream:    "inbox:ben",
			SenderID:  "bairn",
			Body:      "summary " + strconv.Itoa(i) + "\ndetail " + strconv.Itoa(i),
			CreatedAt: "2026-06-25T10:00:0" + strconv.Itoa(i) + "Z",
		}
	}
	m := newMessageOverlayModel("ben", nil, nil)
	m.conversations = groupConversations("ben", msgs, nil)
	m.loaded = true
	m.msgCursor = m.msgCount() - 1 // start on the most recent, as the UI does
	m.width, m.height = 100, 24
	return m
}

func TestMessageOverlayMessageNavigation(t *testing.T) {
	m := testModel(4) // msgCursor starts at 3 (last)
	if m.msgCursor != 3 {
		t.Fatalf("initial msgCursor = %d, want 3", m.msgCursor)
	}
	// Up moves toward older messages; clamps at 0.
	for i := 0; i < 10; i++ {
		mm, _ := m.Update(keyPress("k"))
		m = mm.(messageOverlayModel)
	}
	if m.msgCursor != 0 {
		t.Errorf("after many ups, msgCursor = %d, want 0", m.msgCursor)
	}
	// Down clamps at last.
	for i := 0; i < 10; i++ {
		mm, _ := m.Update(keyPress("j"))
		m = mm.(messageOverlayModel)
	}
	if m.msgCursor != 3 {
		t.Errorf("after many downs, msgCursor = %d, want 3", m.msgCursor)
	}
}

// The focused message is expanded (its body rendered); the others are collapsed
// to a single header line.
func TestMessageOverlayFocusedMessageExpanded(t *testing.T) {
	m := testModel(3)
	m.msgCursor = 1
	out := m.renderThread(80, 40)
	if !strings.Contains(out, "detail 1") {
		t.Errorf("focused message body should be visible:\n%s", out)
	}
	if strings.Contains(out, "detail 0") || strings.Contains(out, "detail 2") {
		t.Errorf("non-focused message bodies should be collapsed:\n%s", out)
	}
	// Moving the cursor expands a different message.
	mm, _ := m.Update(keyPress("k")) // to index 0
	m = mm.(messageOverlayModel)
	out = m.renderThread(80, 40)
	if !strings.Contains(out, "detail 0") {
		t.Errorf("after moving up, message 0 should expand:\n%s", out)
	}
	if strings.Contains(out, "detail 1") {
		t.Errorf("message 1 should collapse after moving away:\n%s", out)
	}
}

// Enter pins a message so it stays expanded even after the cursor moves away.
func TestMessageOverlayPinKeepsExpanded(t *testing.T) {
	m := testModel(3)
	m.msgCursor = 0
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // pin message 0
	m = mm.(messageOverlayModel)
	mm, _ = m.Update(keyPress("j")) // move to message 1
	m = mm.(messageOverlayModel)
	out := m.renderThread(80, 40)
	if !strings.Contains(out, "detail 0") {
		t.Errorf("pinned message 0 should stay expanded after moving away:\n%s", out)
	}
	if !strings.Contains(out, "detail 1") {
		t.Errorf("focused message 1 should be expanded:\n%s", out)
	}
}

func TestMessageOverlayRenderShowsTimeAndDelta(t *testing.T) {
	m := testModel(2)
	out := m.renderThread(80, 20)
	// Render shows the relative delta ("ago") and a collapse marker.
	if !strings.Contains(out, "ago") {
		t.Errorf("thread render missing relative delta:\n%s", out)
	}
	if !strings.Contains(out, "▸") {
		t.Errorf("non-focused messages should show the collapsed ▸ marker:\n%s", out)
	}
}

func TestMsgTimestampTodayHasTimeAndDelta(t *testing.T) {
	ts := msgTimestamp(time.Now().Add(-3 * time.Minute))
	if !strings.Contains(ts, "ago") || !strings.Contains(ts, ":") {
		t.Errorf("msgTimestamp = %q, want an absolute HH:MM and a delta", ts)
	}
	if msgTimestamp(time.Time{}) != "" {
		t.Error("zero time should render empty")
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
