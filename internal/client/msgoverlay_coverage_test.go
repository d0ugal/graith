package client

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/d0ugal/graith/internal/protocol"
)

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
