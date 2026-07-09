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

func TestGroupConversationsAutomatedNotificationIsSystem(t *testing.T) {
	// An automated daemon notification (issue #887) arrives on a session's
	// normal inbox stream, not a "_system." stream, so it must be classified
	// as system via the System flag alone.
	convs := groupConversations("ben", []protocol.ConversationMessage{
		{
			Stream:     "inbox:ben",
			SenderID:   "graith:system",
			SenderName: "graith notifications",
			Body:       "PR #884 was merged.",
			CreatedAt:  "2026-06-25T10:00:00Z",
			System:     true,
		},
	}, nil)

	c := findConv(convs, "graith:system")
	if c == nil || len(c.messages) != 1 {
		t.Fatalf("conversation/messages missing: %+v", convs)
	}

	if !c.messages[0].system {
		t.Error("automated notification not classified as system")
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

// A message taller than the viewport can be scrolled through with the page
// keys (space / PgDn), so its tail is reachable.
func TestMessageOverlayLongMessageScroll(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}

	m := newMessageOverlayModel("ben", nil, nil)
	m.conversations = groupConversations("ben", []protocol.ConversationMessage{
		{ID: "m0", Stream: "inbox:ben", SenderID: "bairn", Body: sb.String(), CreatedAt: "2026-06-25T10:00:00Z"},
		{ID: "m1", Stream: "inbox:ben", SenderID: "bairn", Body: "short tail", CreatedAt: "2026-06-25T10:00:01Z"},
	}, nil)
	m.loaded = true
	m.msgCursor = 0 // focus the long message
	m.width, m.height = 80, 10

	out := m.renderThread(78, 10)
	if !strings.Contains(out, "line 0") {
		t.Fatalf("top of long message should be visible initially:\n%s", out)
	}

	if strings.Contains(out, "line 29") {
		t.Fatalf("tail should NOT be visible before scrolling:\n%s", out)
	}
	// Page down a few times; the tail should come into view.
	for i := 0; i < 6; i++ {
		mm, _ := m.Update(keyPress(" "))
		m = mm.(messageOverlayModel)
	}

	if m.lineScroll == 0 {
		t.Fatal("paging did not advance lineScroll")
	}

	out = m.renderThread(78, 10)
	if !strings.Contains(out, "line 29") {
		t.Errorf("after paging down, tail of long message should be reachable:\n%s", out)
	}
	// Moving the message cursor (down to the short message) resets the scroll.
	mm, _ := m.Update(keyPress("j"))

	m = mm.(messageOverlayModel)
	if m.lineScroll != 0 {
		t.Errorf("lineScroll = %d, want 0 after cursor move", m.lineScroll)
	}
}

// longMessageModel builds a loaded model whose first message is far taller than
// the viewport (so lineScroll is live) followed by a short tail message.
func longMessageModel() messageOverlayModel {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}

	m := newMessageOverlayModel("ben", nil, nil)
	m.conversations = groupConversations("ben", []protocol.ConversationMessage{
		{ID: "m0", Stream: "inbox:ben", SenderID: "bairn", Body: sb.String(), CreatedAt: "2026-06-25T10:00:00Z"},
		{ID: "m1", Stream: "inbox:ben", SenderID: "bairn", Body: "short tail", CreatedAt: "2026-06-25T10:00:01Z"},
	}, nil)
	m.loaded = true
	m.msgCursor = 0 // focus the long message
	m.width, m.height = 80, 10

	return m
}

// Paging down on a tall message clamps lineScroll to the block's real maximum
// instead of accumulating unbounded (issue #774, bug 2). Otherwise a later pgup
// appears to do nothing until the inflated value drops back under the clamp.
func TestMessageOverlayLineScrollClampedOnPageDown(t *testing.T) {
	m := longMessageModel()

	maxScroll := m.maxLineScroll()
	if maxScroll <= 0 {
		t.Fatalf("expected a scrollable long message, maxLineScroll = %d", maxScroll)
	}
	// Page down far more times than needed to reach the end.
	for i := 0; i < 50; i++ {
		mm, _ := m.Update(keyPress(" "))
		m = mm.(messageOverlayModel)
	}

	if m.lineScroll != maxScroll {
		t.Fatalf("lineScroll = %d after paging past the end, want clamp at %d", m.lineScroll, maxScroll)
	}
	// A single pgup must now produce a visible change (the bug: it wouldn't,
	// because lineScroll had accumulated far past the clamp).
	mm, _ := m.Update(keyPress("pgup"))
	m = mm.(messageOverlayModel)

	if m.lineScroll != maxScroll-m.pageStep() {
		t.Errorf("lineScroll = %d after one pgup, want %d", m.lineScroll, maxScroll-m.pageStep())
	}
}

// After a resize shrinks the scrollable height, pgup must clamp to the new max
// before subtracting, so the first press produces a visible change rather than
// silently decrementing a stale (too-large) value (issue #774, resize edge).
func TestMessageOverlayPageUpClampsAfterResize(t *testing.T) {
	m := longMessageModel()
	// Scroll to the bottom of the tall message at the small viewport.
	for i := 0; i < 50; i++ {
		mm, _ := m.Update(keyPress(" "))
		m = mm.(messageOverlayModel)
	}

	stale := m.lineScroll
	// Grow the viewport taller (but still shorter than the message): this shrinks
	// maxLineScroll below `stale` while keeping the message scrollable.
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = mm.(messageOverlayModel)

	newMax := m.maxLineScroll()
	if newMax <= 0 || newMax >= stale {
		t.Fatalf("resize should shrink maxLineScroll but keep it scrollable: stale=%d newMax=%d", stale, newMax)
	}
	// One pgup must move relative to the new max, not the stale value.
	mm, _ = m.Update(keyPress("pgup"))
	m = mm.(messageOverlayModel)

	if want := max(0, newMax-m.pageStep()); m.lineScroll != want {
		t.Errorf("lineScroll = %d after resize+pgup, want %d (clamped to new max)", m.lineScroll, want)
	}
}

// A refresh that follows the newest message (prevAtLast) must reset lineScroll
// so the newly focused message opens at its header rather than partway down
// (issue #774, bug 1).
func TestMessageOverlayRefreshResetsLineScrollOnFollow(t *testing.T) {
	m := longMessageModel()
	// Sit at the tail with a non-zero intra-message scroll, so the next refresh
	// takes the prevAtLast (follow-newest) branch.
	m.msgCursor = m.msgCount() - 1 // at last message (m1)
	m.lineScroll = 7               // pretend we had paged down a tall focused message

	// Refresh appends a new, tall newest message; prevAtLast is true so focus
	// follows to it.
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("fresh ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}

	refreshed := groupConversations("ben", []protocol.ConversationMessage{
		{ID: "m0", Stream: "inbox:ben", SenderID: "bairn", Body: "old long", CreatedAt: "2026-06-25T10:00:00Z"},
		{ID: "m1", Stream: "inbox:ben", SenderID: "bairn", Body: "short tail", CreatedAt: "2026-06-25T10:00:01Z"},
		{ID: "m2", Stream: "inbox:ben", SenderID: "bairn", Body: sb.String(), CreatedAt: "2026-06-25T10:00:02Z"},
	}, nil)

	mm, _ := m.Update(msgFetchedMsg{conversations: refreshed, ok: true})
	m = mm.(messageOverlayModel)

	if got := m.currentEntry(); got == nil || got.id != "m2" {
		t.Fatalf("expected focus to follow to newest m2, got %+v", got)
	}

	if m.lineScroll != 0 {
		t.Errorf("lineScroll = %d after refresh moved focus, want 0", m.lineScroll)
	}
}

// A refresh that keeps focus on the same message id must preserve lineScroll so
// the reader's scroll position within a still-focused message isn't lost.
func TestMessageOverlayRefreshPreservesLineScrollOnSameMessage(t *testing.T) {
	m := longMessageModel()
	// Focus the tall message (not at last, so the re-find-by-id branch runs) and
	// scroll into it.
	m.msgCursor = 0
	m.lineScroll = 5

	// Refresh with the same messages plus a newer one, but focus stays on m0 by
	// id (we're not at the tail).
	refreshed := groupConversations("ben", []protocol.ConversationMessage{
		{ID: "m0", Stream: "inbox:ben", SenderID: "bairn", Body: "old long", CreatedAt: "2026-06-25T10:00:00Z"},
		{ID: "m1", Stream: "inbox:ben", SenderID: "bairn", Body: "short tail", CreatedAt: "2026-06-25T10:00:01Z"},
		{ID: "m2", Stream: "inbox:ben", SenderID: "bairn", Body: "brand new", CreatedAt: "2026-06-25T10:00:05Z"},
	}, nil)

	mm, _ := m.Update(msgFetchedMsg{conversations: refreshed, ok: true})
	m = mm.(messageOverlayModel)

	if got := m.currentEntry(); got == nil || got.id != "m0" {
		t.Fatalf("expected focus to stay on m0 by id, got %+v", got)
	}

	if m.lineScroll != 5 {
		t.Errorf("lineScroll = %d after refresh kept focus on same message, want 5 (preserved)", m.lineScroll)
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

// --- shortID / parseMsgTime / relTime edge cases ---

func TestShortID_Cases(t *testing.T) {
	if got := shortID(""); got != "(unknown)" {
		t.Errorf("shortID(empty) = %q, want (unknown)", got)
	}

	if got := shortID("abcd"); got != "abcd" {
		t.Errorf("shortID(short) = %q, want abcd", got)
	}

	if got := shortID("abcdefghijkl"); got != "abcdefgh" {
		t.Errorf("shortID(long) = %q, want abcdefgh", got)
	}
}

func TestParseMsgTime_Formats(t *testing.T) {
	if parseMsgTime("2026-06-25T10:00:00.123456789Z").IsZero() {
		t.Error("RFC3339Nano should parse")
	}

	if parseMsgTime("2026-06-25T10:00:00Z").IsZero() {
		t.Error("RFC3339 should parse")
	}

	if !parseMsgTime("not a time").IsZero() {
		t.Error("garbage should return zero time")
	}
}

func TestRelTime_NegativeClampedToZero(t *testing.T) {
	// A future timestamp yields a negative duration; relTime clamps to 0s.
	future := time.Now().Add(10 * time.Minute)
	if got := relTime(future); got != "0s ago" {
		t.Errorf("relTime(future) = %q, want 0s ago", got)
	}
}

// --- msgCountAt / currentEntry out-of-range ---

func TestMsgCountAt_OutOfRange(t *testing.T) {
	m := testModel(2)
	if got := m.msgCountAt(-1); got != 0 {
		t.Errorf("msgCountAt(-1) = %d, want 0", got)
	}

	if got := m.msgCountAt(99); got != 0 {
		t.Errorf("msgCountAt(99) = %d, want 0", got)
	}
}

func TestCurrentEntry_OutOfRange(t *testing.T) {
	m := testModel(2)

	m.cursor = 99
	if m.currentEntry() != nil {
		t.Error("currentEntry should be nil when cursor is out of range")
	}

	m.cursor = 0
	m.msgCursor = 99

	if m.currentEntry() != nil {
		t.Error("currentEntry should be nil when msgCursor is out of range")
	}
}

// --- Init / tickCmd / fetchCmd ---

func TestMsgOverlay_InitReturnsBatch(t *testing.T) {
	m := newMessageOverlayModel("ben", func() ([]protocol.ConversationMessage, bool) {
		return []protocol.ConversationMessage{
			cm("inbox:ben", "bairn", "wee-bairn", "hi", "2026-06-25T10:00:00Z"),
		}, true
	}, nil)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init should return a command")
	}

	msg := cmd()
	if _, ok := msg.(tea.BatchMsg); !ok {
		t.Fatalf("Init command should produce a BatchMsg, got %T", msg)
	}
}

func TestMsgOverlay_FetchCmdGroupsConversations(t *testing.T) {
	m := newMessageOverlayModel("ben", func() ([]protocol.ConversationMessage, bool) {
		return []protocol.ConversationMessage{
			cm("inbox:ben", "bairn", "wee-bairn", "hi", "2026-06-25T10:00:00Z"),
		}, true
	}, nil)

	msg := m.fetchCmd()()

	fetched, ok := msg.(msgFetchedMsg)
	if !ok {
		t.Fatalf("fetchCmd should produce msgFetchedMsg, got %T", msg)
	}

	if !fetched.ok || len(fetched.conversations) != 1 {
		t.Errorf("expected 1 conversation and ok=true, got %+v", fetched)
	}
}

func TestMsgOverlay_FetchCmdNilFetch(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)

	fetched, ok := m.fetchCmd()().(msgFetchedMsg)
	if !ok || !fetched.ok {
		t.Fatalf("nil fetch should still produce ok msgFetchedMsg, got %+v", fetched)
	}

	if len(fetched.conversations) != 0 {
		t.Errorf("nil fetch should produce no conversations, got %d", len(fetched.conversations))
	}
}

func TestMsgOverlay_FetchCmdTransientError(t *testing.T) {
	m := newMessageOverlayModel("ben", func() ([]protocol.ConversationMessage, bool) {
		return nil, false
	}, nil)

	fetched := m.fetchCmd()().(msgFetchedMsg)
	if fetched.ok {
		t.Error("fetch returning ok=false should yield msgFetchedMsg{ok:false}")
	}
}

// Note: tickCmd's timer behavior is covered structurally via the msgTickMsg
// handler tests below (TickStartsFetch / TickSkipsWhenFetching) rather than by
// executing the real 2-second tea.Tick, which would add a wall-clock delay to
// the suite.

// --- Update: tick fetching guard ---

func TestMsgOverlay_TickSkipsWhenFetching(t *testing.T) {
	m := testModel(2)
	m.fetching = true

	updated, cmd := m.Update(msgTickMsg{})
	mm := updated.(messageOverlayModel)

	if !mm.fetching {
		t.Error("fetching should remain true when a fetch is in flight")
	}

	if cmd == nil {
		t.Error("tick should still reschedule itself")
	}
}

func TestMsgOverlay_TickStartsFetch(t *testing.T) {
	m := testModel(2)
	m.fetching = false

	updated, cmd := m.Update(msgTickMsg{})
	mm := updated.(messageOverlayModel)

	if !mm.fetching {
		t.Error("tick should mark a fetch in flight")
	}

	if cmd == nil {
		t.Error("tick should return a batch command")
	}
}

// --- Update: transient fetch error keeps snapshot ---

func TestMsgOverlay_FetchErrorKeepsSnapshot(t *testing.T) {
	m := testModel(3)
	before := m.conversations

	updated, _ := m.Update(msgFetchedMsg{ok: false})
	mm := updated.(messageOverlayModel)

	if len(mm.conversations) != len(before) {
		t.Errorf("transient error should keep %d conversations, got %d", len(before), len(mm.conversations))
	}

	if !mm.loaded {
		t.Error("a fetch response should mark the model loaded")
	}
}

// --- Update: conversation navigation (h/l) ---

func TestMsgOverlay_ConversationSwitch(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)
	m.conversations = groupConversations("ben", []protocol.ConversationMessage{
		cm("inbox:ben", "aaa", "canny", "one", "2026-06-25T10:00:02Z"),
		cm("inbox:ben", "bbb", "bonnie", "two", "2026-06-25T10:00:01Z"),
	}, nil)
	m.loaded = true
	m.width, m.height = 100, 24

	if len(m.conversations) != 2 {
		t.Fatalf("expected 2 conversations, got %d", len(m.conversations))
	}

	// Right/l moves to the next conversation.
	updated, _ := m.Update(keyPress("l"))
	mm := updated.(messageOverlayModel)

	if mm.cursor != 1 {
		t.Errorf("cursor after right = %d, want 1", mm.cursor)
	}

	// Right again clamps.
	updated, _ = mm.Update(keyPress("right"))
	mm = updated.(messageOverlayModel)

	if mm.cursor != 1 {
		t.Errorf("cursor should clamp at last conversation, got %d", mm.cursor)
	}

	// Left/h moves back.
	updated, _ = mm.Update(keyPress("h"))
	mm = updated.(messageOverlayModel)

	if mm.cursor != 0 {
		t.Errorf("cursor after left = %d, want 0", mm.cursor)
	}

	// Left again clamps at 0.
	updated, _ = mm.Update(keyPress("left"))
	mm = updated.(messageOverlayModel)

	if mm.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", mm.cursor)
	}
}

// --- Update: g / G jump to first/last ---

func TestMsgOverlay_HomeEnd(t *testing.T) {
	m := testModel(5)
	m.msgCursor = 2

	updated, _ := m.Update(keyPress("g"))
	mm := updated.(messageOverlayModel)

	if mm.msgCursor != 0 {
		t.Errorf("g should jump to first, got %d", mm.msgCursor)
	}

	updated, _ = mm.Update(keyPress("G"))
	mm = updated.(messageOverlayModel)

	if mm.msgCursor != mm.msgCount()-1 {
		t.Errorf("G should jump to last (%d), got %d", mm.msgCount()-1, mm.msgCursor)
	}
}

// --- Update: enter pins / O / C pin-all ---

func TestMsgOverlay_PinToggle(t *testing.T) {
	m := testModel(3)
	m.msgCursor = 1
	id := m.currentEntry().id

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(messageOverlayModel)

	if !mm.pinned[id] {
		t.Errorf("enter should pin message %q", id)
	}

	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm = updated.(messageOverlayModel)

	if mm.pinned[id] {
		t.Errorf("enter again should unpin message %q", id)
	}
}

func TestMsgOverlay_PinAllUnpinAll(t *testing.T) {
	m := testModel(3)

	updated, _ := m.Update(keyPress("O"))
	mm := updated.(messageOverlayModel)

	for _, e := range mm.conversations[mm.cursor].messages {
		if !mm.pinned[e.id] {
			t.Errorf("O should pin every message; %q not pinned", e.id)
		}
	}

	updated, _ = mm.Update(keyPress("C"))
	mm = updated.(messageOverlayModel)

	for _, e := range mm.conversations[mm.cursor].messages {
		if mm.pinned[e.id] {
			t.Errorf("C should unpin every message; %q still pinned", e.id)
		}
	}
}

func TestSetAllPinned_NoConversation(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)
	m.cursor = 5 // out of range
	// Must not panic and must be a no-op.
	m.setAllPinned(true)

	if len(m.pinned) != 0 {
		t.Error("setAllPinned with out-of-range cursor should not pin anything")
	}
}

// --- Update: quit + window size ---

func TestMsgOverlay_QuitKeys(t *testing.T) {
	for _, k := range []string{"q", "esc"} {
		m := testModel(2)

		_, cmd := m.Update(keyPress(k))
		if cmd == nil {
			t.Fatalf("key %q should return a command", k)
		}

		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("key %q should return tea.Quit, got %T", k, cmd())
		}
	}
}

func TestMsgOverlay_WindowSize(t *testing.T) {
	m := testModel(2)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	mm := updated.(messageOverlayModel)

	if mm.width != 200 || mm.height != 50 {
		t.Errorf("window size not applied: got %dx%d", mm.width, mm.height)
	}
}

// --- threadViewport: narrow and mid widths ---

func TestThreadViewport_Widths(t *testing.T) {
	// Very narrow: single-column fallback.
	narrow := messageOverlayModel{width: 30, height: 20}

	w, h := narrow.threadViewport()
	if w != 29 || h != 16 {
		t.Errorf("narrow viewport = %dx%d, want 29x16", w, h)
	}

	// Mid width (<70): rail is width/3.
	mid := messageOverlayModel{width: 60, height: 20}

	wMid, _ := mid.threadViewport()
	// railW = max(16, 20) = 20; threadW = max(10, 60-20-3) = 37
	if wMid != 37 {
		t.Errorf("mid viewport width = %d, want 37", wMid)
	}

	// Wide (>=70): rail is fixed 26.
	wide := messageOverlayModel{width: 100, height: 20}

	wWide, _ := wide.threadViewport()
	if wWide != 71 {
		t.Errorf("wide viewport width = %d, want 71", wWide)
	}
}

// --- View: renders at wide, narrow, and zero sizes ---

func TestMsgOverlay_ViewZeroSize(t *testing.T) {
	m := testModel(2)
	m.width, m.height = 0, 0

	if got := m.View().Content; got != "" {
		t.Errorf("zero-size View should be empty, got %q", got)
	}
}

func TestMsgOverlay_ViewWideRendersRail(t *testing.T) {
	m := testModel(3)
	m.width, m.height = 120, 30

	out := m.View().Content
	if !strings.Contains(out, "Messages") {
		t.Errorf("wide View should show the title:\n%s", out)
	}
	// The rail shows the peer name.
	if !strings.Contains(out, "bairn") {
		t.Errorf("wide View should render the conversation rail:\n%s", out)
	}
}

func TestMsgOverlay_ViewNarrowSingleColumn(t *testing.T) {
	m := testModel(3)
	m.width, m.height = 30, 20 // below the 36 threshold

	out := m.View().Content
	if !strings.Contains(out, "Messages") {
		t.Errorf("narrow View should still render the title:\n%s", out)
	}
}

func TestMsgOverlay_ViewLoadingIndicator(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)
	m.width, m.height = 120, 30
	m.loaded = false

	out := m.View().Content
	if !strings.Contains(out, "loading") {
		t.Errorf("unloaded View should show loading indicator:\n%s", out)
	}
}

// --- renderRail: empty state and scrolling ---

func TestRenderRail_EmptyLoaded(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)
	m.loaded = true

	if got := m.renderRail(26, 10); !strings.Contains(got, "No messages") {
		t.Errorf("empty loaded rail should say No messages, got %q", got)
	}
}

func TestRenderRail_ScrollsToSelected(t *testing.T) {
	msgs := make([]protocol.ConversationMessage, 0, 10)

	for i := 0; i < 10; i++ {
		// Distinct peers, decreasing time so order is stable.
		peer := string(rune('a' + i))
		msgs = append(msgs, protocol.ConversationMessage{
			Stream:    "inbox:ben",
			SenderID:  peer,
			Body:      "msg",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}

	m := newMessageOverlayModel("ben", nil, nil)
	m.conversations = groupConversations("ben", msgs, nil)
	m.loaded = true
	m.cursor = 9 // select the last, forcing the rail to scroll

	out := m.renderRail(26, 3)
	lines := strings.Split(out, "\n")

	if len(lines) > 3 {
		t.Errorf("rail height 3 should render at most 3 lines, got %d", len(lines))
	}
	// The selected conversation is marked with "> ".
	if !strings.Contains(out, "> ") {
		t.Errorf("rail should mark the selected conversation:\n%s", out)
	}
}

// --- renderThread: no-conversation states ---

func TestRenderThread_NoConversationLoaded(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)
	m.loaded = true
	m.cursor = -1

	if got := m.renderThread(80, 20); !strings.Contains(got, "Select a conversation") {
		t.Errorf("loaded with no selection should prompt to select, got %q", got)
	}
}

func TestRenderThread_NoConversationNotLoaded(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)
	m.cursor = -1

	if got := m.renderThread(80, 20); got != "" {
		t.Errorf("unloaded with no selection should render empty, got %q", got)
	}
}

// --- renderThread: system + outbound header rendering ---

func TestRenderThread_SystemAndOutbound(t *testing.T) {
	m := newMessageOverlayModel("ben", nil, nil)
	m.conversations = groupConversations("ben", []protocol.ConversationMessage{
		{ID: "s1", Stream: "_system.notify", SenderID: "daemon", SenderName: "orchestrator", Body: "system note", CreatedAt: "2026-06-25T10:00:00Z"},
		{ID: "o1", Stream: "inbox:bairn", SenderID: "ben", SenderName: "ben", Body: "sent by me", CreatedAt: "2026-06-25T10:00:01Z"},
	}, nil)
	m.loaded = true
	m.width, m.height = 100, 24

	// Select the conversation that holds the system message.
	for i, c := range m.conversations {
		for _, e := range c.messages {
			if e.system {
				m.cursor = i
			}
		}
	}

	out := m.renderThread(80, 20)
	if !strings.Contains(out, "⚙") {
		t.Errorf("system messages should carry the ⚙ marker:\n%s", out)
	}

	// Also render the outbound-only conversation for the "me → peer" header path.
	for i, c := range m.conversations {
		for _, e := range c.messages {
			if e.outbound && !e.system {
				m.cursor = i
			}
		}
	}

	if outbound := m.renderThread(80, 20); !strings.Contains(outbound, "me →") {
		t.Errorf("outbound messages should carry the 'me →' header:\n%s", outbound)
	}
}
