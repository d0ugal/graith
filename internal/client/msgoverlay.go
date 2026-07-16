package client

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
)

// msgConversation is one peer's thread: every message exchanged between self
// and that peer, in chronological order.
type msgConversation struct {
	peerID   string
	peerName string
	messages []msgEntry
	lastAt   time.Time
}

// msgEntry is a single rendered message in a thread.
type msgEntry struct {
	id        string // stable message id (for collapse state across refreshes)
	sender    string // display name of the author
	body      string
	createdAt time.Time
	outbound  bool // true if authored by self (sent), false if received
	system    bool // true for _system.* / orchestrator notifications
}

type msgFetchedMsg struct {
	conversations []msgConversation
	ok            bool // false if the fetch failed (keep the last good snapshot)
}

type msgTickMsg struct{}

// scheduleMessageTick is a test seam around Bubble Tea's timer. Production
// uses tea.Tick; tests replace it to observe the configured cadence without
// sleeping.
var scheduleMessageTick = tea.Tick

type messageOverlayModel struct {
	selfID string
	// fetch returns the conversation messages and ok=false on a transient
	// fetch error (so the model can keep the last good snapshot).
	fetch func() ([]protocol.ConversationMessage, bool)
	names map[string]string

	conversations []msgConversation
	cursor        int // selected conversation in the left rail
	msgCursor     int // selected message within the current thread
	// lineScroll pages within the focused message when it's taller than the
	// viewport; reset to 0 whenever the message cursor moves.
	lineScroll int
	// The focused message (msgCursor) is always shown expanded; every other
	// message is collapsed to a single header line. pinned holds messages the
	// user has explicitly kept open (via enter) so they stay expanded even when
	// not focused — keyed by message id.
	pinned   map[string]bool
	loaded   bool
	fetching bool // a fetch is in flight; don't stack another
	width    int
	height   int
	keys     MessageKeys
}

func newMessageOverlayModel(selfID string, fetch func() ([]protocol.ConversationMessage, bool), names map[string]string) messageOverlayModel {
	return messageOverlayModel{
		selfID: selfID,
		fetch:  fetch,
		names:  names,
		pinned: map[string]bool{},
		keys:   DefaultMessageKeys(),
	}
}

func (m messageOverlayModel) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), m.tickCmd())
}

func (m messageOverlayModel) tickCmd() tea.Cmd {
	return scheduleMessageTick(refreshInterval, func(time.Time) tea.Msg {
		return msgTickMsg{}
	})
}

func (m messageOverlayModel) fetchCmd() tea.Cmd {
	fetch := m.fetch
	selfID := m.selfID
	names := m.names

	return func() tea.Msg {
		if fetch == nil {
			return msgFetchedMsg{ok: true}
		}

		msgs, ok := fetch()
		if !ok {
			return msgFetchedMsg{ok: false}
		}

		return msgFetchedMsg{conversations: groupConversations(selfID, msgs, names), ok: true}
	}
}

// groupConversations turns a flat, chronologically-ordered message list into
// per-peer threads. The peer is the sender for received messages and the inbox
// owner (stream suffix) for sent messages.
func groupConversations(selfID string, msgs []protocol.ConversationMessage, names map[string]string) []msgConversation {
	selfInbox := "inbox:" + selfID
	byPeer := map[string]*msgConversation{}
	order := []string{}

	for _, cm := range msgs {
		var peerID string

		outbound := false

		if cm.Stream == selfInbox {
			// Received by self; peer is the sender.
			peerID = cm.SenderID
			if cm.SenderID == selfID {
				// Self-message (sent to own inbox); treat as outbound.
				outbound = true
			}
		} else {
			// Sent by self to peer's inbox; peer is the inbox owner.
			peerID = strings.TrimPrefix(cm.Stream, "inbox:")
			outbound = true
		}

		conv, ok := byPeer[peerID]
		if !ok {
			conv = &msgConversation{peerID: peerID, peerName: resolvePeerName(peerID, cm, names)}
			byPeer[peerID] = conv
			order = append(order, peerID)
		} else if conv.peerName == "" || conv.peerName == shortID(peerID) {
			// Prefer a real name if a later message carries one.
			if n := resolvePeerName(peerID, cm, names); n != "" {
				conv.peerName = n
			}
		}

		sender := cm.SenderName
		if sender == "" {
			sender = shortID(cm.SenderID)
		}

		created := parseMsgTime(cm.CreatedAt)

		conv.messages = append(conv.messages, msgEntry{
			id:        cm.ID,
			sender:    sender,
			body:      cm.Body,
			createdAt: created,
			outbound:  outbound,
			system:    isSystemMessage(cm),
		})
		if created.After(conv.lastAt) {
			conv.lastAt = created
		}
	}

	convs := make([]msgConversation, 0, len(order))
	for _, id := range order {
		convs = append(convs, *byPeer[id])
	}
	// Most recently active conversation first.
	sort.SliceStable(convs, func(i, j int) bool {
		return convs[i].lastAt.After(convs[j].lastAt)
	})

	return convs
}

// resolvePeerName resolves a peer's display name, preferring the live session
// list, then the sender name carried on a received message, then a short id.
func resolvePeerName(peerID string, cm protocol.ConversationMessage, names map[string]string) string {
	if n, ok := names[peerID]; ok && n != "" {
		return n
	}

	if cm.SenderID == peerID && cm.SenderName != "" {
		return cm.SenderName
	}

	return shortID(peerID)
}

func isSystemMessage(cm protocol.ConversationMessage) bool {
	// cm.System flags automated daemon notifications (issue #887), which arrive
	// on a session's normal inbox stream rather than a "_system." stream — so
	// the stream/sender heuristics below miss them without this check.
	return cm.System || strings.HasPrefix(cm.Stream, "_system.") || cm.SenderName == "orchestrator"
}

func shortID(id string) string {
	if id == "" {
		return "(unknown)"
	}

	if len(id) > 8 {
		return id[:8]
	}

	return id
}

func parseMsgTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}

	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}

	return time.Time{}
}

func (m messageOverlayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		return m, nil

	case msgTickMsg:
		// Don't stack fetches: if one is already in flight (e.g. a slow or
		// stalled daemon), just reschedule the tick. This also means only one
		// response is ever outstanding, so a late response can't overwrite a
		// newer one.
		if m.fetching {
			return m, m.tickCmd()
		}

		m.fetching = true

		return m, tea.Batch(m.fetchCmd(), m.tickCmd())

	case msgFetchedMsg:
		m.fetching = false
		m.loaded = true
		// On a transient fetch error, keep the last good snapshot rather than
		// blanking the view to "No messages".
		if !msg.ok {
			return m, nil
		}
		// Preserve the selected peer and focused message across refreshes.
		var selectedPeer, focusedMsgID string
		if m.cursor >= 0 && m.cursor < len(m.conversations) {
			selectedPeer = m.conversations[m.cursor].peerID
		}

		if e := m.currentEntry(); e != nil {
			focusedMsgID = e.id
		}

		prevAtLast := m.msgCursor >= m.msgCount()-1
		peerFound := false
		m.conversations = msg.conversations

		m.cursor = 0
		for i, c := range m.conversations {
			if c.peerID == selectedPeer {
				m.cursor = i
				peerFound = true

				break
			}
		}

		if m.cursor >= len(m.conversations) {
			m.cursor = max(0, len(m.conversations)-1)
		}

		switch {
		case peerFound && prevAtLast:
			// Reader was at the tail: follow the newest message.
			m.msgCursor = max(0, m.msgCount()-1)
		case peerFound && focusedMsgID != "":
			// Re-find the focused message by id so inserts/removals before it
			// don't shift the cursor onto a different message; fall back to
			// clamping if it vanished.
			m.msgCursor = max(0, min(m.msgCursor, m.msgCount()-1))
			for i, e := range m.conversations[m.cursor].messages {
				if e.id == focusedMsgID {
					m.msgCursor = i
					break
				}
			}
		default:
			// Peer vanished (or no prior focus): land on the newest message.
			m.msgCursor = max(0, m.msgCount()-1)
		}

		// If the refresh moved focus to a different message, reset the
		// intra-message scroll so the new message opens at its header. Key-driven
		// cursor moves already do this; the refresh path previously did not,
		// leaving lineScroll pointing partway down an unrelated message.
		if e := m.currentEntry(); e == nil || e.id != focusedMsgID {
			m.lineScroll = 0
		}

		return m, nil

	case tea.KeyPressMsg:
		s := msg.String()

		switch {
		case matchKey(m.keys.Cancel, s):
			return m, tea.Quit
		// Vertical: move the message cursor within the current thread. The
		// viewport follows it (this is the scroll).
		case matchKey(m.keys.Down, s):
			if m.msgCursor < m.msgCount()-1 {
				m.msgCursor++
				m.lineScroll = 0
			}

			return m, nil
		case matchKey(m.keys.Up, s):
			if m.msgCursor > 0 {
				m.msgCursor--
				m.lineScroll = 0
			}

			return m, nil
		// Page within the focused message when it's taller than the viewport.
		case matchKey(m.keys.PageDown, s):
			// Clamp to the focused block's scrollable height so the stored value
			// can't accumulate past the real maximum (which would make later pgup
			// presses appear to do nothing until it drops back under the clamp).
			m.lineScroll = min(m.lineScroll+m.pageStep(), m.maxLineScroll())
			return m, nil
		case matchKey(m.keys.PageUp, s):
			// Clamp to the current max before subtracting: a viewport resize can
			// shrink maxLineScroll below a previously-valid lineScroll, and
			// without this the first pgup(s) would subtract from the stale larger
			// value and appear to do nothing (the same symptom bug 2 fixed for
			// pgdown accumulation).
			m.lineScroll = max(0, min(m.lineScroll, m.maxLineScroll())-m.pageStep())
			return m, nil
		// Horizontal: switch conversation in the rail.
		case matchKey(m.keys.NextConv, s):
			if m.cursor < len(m.conversations)-1 {
				m.cursor++
				m.msgCursor = max(0, m.msgCountAt(m.cursor)-1)
				m.lineScroll = 0
			}

			return m, nil
		case matchKey(m.keys.PrevConv, s):
			if m.cursor > 0 {
				m.cursor--
				m.msgCursor = max(0, m.msgCountAt(m.cursor)-1)
				m.lineScroll = 0
			}

			return m, nil
		// Pin/unpin the focused message so it stays expanded even when not
		// focused (the focused message is always expanded regardless).
		case matchKey(m.keys.Pin, s):
			if e := m.currentEntry(); e != nil && e.id != "" {
				m.pinned[e.id] = !m.pinned[e.id]
			}

			return m, nil
		// Pin-all / unpin-all in the current thread.
		case matchKey(m.keys.ExpandAll, s):
			m.setAllPinned(true)
			return m, nil
		case matchKey(m.keys.CollapseAll, s):
			m.setAllPinned(false)
			return m, nil
		case matchKey(m.keys.Top, s):
			m.msgCursor = 0
			m.lineScroll = 0

			return m, nil
		case matchKey(m.keys.Bottom, s):
			m.msgCursor = max(0, m.msgCount()-1)
			m.lineScroll = 0

			return m, nil
		}
	}

	return m, nil
}

// pageStep is the number of lines a page key scrolls within a tall message —
// roughly one viewport, leaving a line of overlap.
func (m messageOverlayModel) pageStep() int {
	return max(1, m.height-5)
}

// threadViewport returns the (width, height) the thread pane is rendered at,
// mirroring View()'s layout math so scroll clamping matches what's displayed.
func (m messageOverlayModel) threadViewport() (int, int) {
	bodyH := max(1, m.height-4)
	if m.width < 36 {
		return max(1, m.width-1), bodyH
	}

	railW := 26
	if m.width < 70 {
		railW = max(16, m.width/3)
	}

	return max(10, m.width-railW-3), bodyH
}

// maxLineScroll is the furthest lineScroll can advance for the focused message:
// 0 unless its rendered block is taller than the thread viewport. It mirrors the
// render-time clamp in renderThread so the two never disagree.
func (m messageOverlayModel) maxLineScroll() int {
	e := m.currentEntry()
	if e == nil {
		return 0
	}

	width, height := m.threadViewport()
	body := strings.TrimRight(sanitizeMessageBody(e.body), "\n")
	bodyStyle := lipgloss.NewStyle().Width(width)
	// Block layout matches renderThread: header (1) + wrapped body + trailing blank (1).
	blockH := 1 + len(strings.Split(bodyStyle.Render(body), "\n")) + 1

	return max(0, blockH-height)
}

// msgCount returns the number of messages in the selected conversation.
func (m messageOverlayModel) msgCount() int { return m.msgCountAt(m.cursor) }

func (m messageOverlayModel) msgCountAt(conv int) int {
	if conv < 0 || conv >= len(m.conversations) {
		return 0
	}

	return len(m.conversations[conv].messages)
}

// currentEntry returns the message under the cursor, or nil.
func (m messageOverlayModel) currentEntry() *msgEntry {
	if m.cursor < 0 || m.cursor >= len(m.conversations) {
		return nil
	}

	msgs := m.conversations[m.cursor].messages
	if m.msgCursor < 0 || m.msgCursor >= len(msgs) {
		return nil
	}

	return &msgs[m.msgCursor]
}

func (m messageOverlayModel) setAllPinned(v bool) {
	if m.cursor < 0 || m.cursor >= len(m.conversations) {
		return
	}

	for _, e := range m.conversations[m.cursor].messages {
		if e.id != "" {
			m.pinned[e.id] = v
		}
	}
}

func (m messageOverlayModel) View() tea.View {
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		return tea.NewView("")
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
	dim := lipgloss.NewStyle().Foreground(colorDim)
	help := lipgloss.NewStyle().Foreground(colorFaint)

	title := titleStyle.Render("Messages")

	// Body area between title (1 line + blank) and help (blank + 1 line).
	bodyH := max(1, h-4)

	// Below a threshold there isn't room for two panes; fall back to a
	// single-column thread view so the overlay stays usable (and exitable).
	var body, helpLine string
	if w < 36 {
		helpLine = help.Render(fmt.Sprintf("%s/%s msg  %s/%s scroll  %s pin  %s close",
			primaryKey(m.keys.Up), primaryKey(m.keys.Down),
			primaryKey(m.keys.PageDown), primaryKey(m.keys.PageUp),
			primaryKey(m.keys.Pin), primaryKey(m.keys.Cancel)))
		body = m.renderThread(max(1, w-1), bodyH)
	} else {
		helpLine = help.Render(fmt.Sprintf(
			"%s/%s message (auto-expands)  %s/%s scroll long msg  %s pin  %s/%s all  %s/%s first/last  %s/%s conversation  %s close",
			primaryKey(m.keys.Up), primaryKey(m.keys.Down),
			primaryKey(m.keys.PageUp), primaryKey(m.keys.PageDown),
			primaryKey(m.keys.Pin),
			primaryKey(m.keys.ExpandAll), primaryKey(m.keys.CollapseAll),
			primaryKey(m.keys.Top), primaryKey(m.keys.Bottom),
			primaryKey(m.keys.PrevConv), primaryKey(m.keys.NextConv),
			primaryKey(m.keys.Cancel)))

		railW := 26
		if w < 70 {
			railW = max(16, w/3)
		}
		// Reserve 3 columns for the thread pane's border + padding.
		threadW := max(10, w-railW-3)

		rail := m.renderRail(railW, bodyH)
		thread := m.renderThread(threadW, bodyH)

		railStyle := lipgloss.NewStyle().Width(railW).Height(bodyH)
		threadStyle := lipgloss.NewStyle().Width(threadW).Height(bodyH).
			BorderLeft(true).Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorFaint).PaddingLeft(1)

		body = lipgloss.JoinHorizontal(lipgloss.Top, railStyle.Render(rail), threadStyle.Render(thread))
	}

	var b strings.Builder
	b.WriteString(title)

	if !m.loaded {
		b.WriteString("  ")
		b.WriteString(dim.Render("loading…"))
	}

	b.WriteString("\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	b.WriteString(helpLine)

	v := tea.NewView(b.String())
	v.AltScreen = true

	return v
}

func (m messageOverlayModel) renderRail(width, height int) string {
	dim := lipgloss.NewStyle().Foreground(colorDim)
	if m.loaded && len(m.conversations) == 0 {
		return dim.Render("No messages")
	}

	// Scroll the rail so the selected conversation stays visible when there
	// are more peers than fit.
	start := 0

	if len(m.conversations) > height {
		if m.cursor >= height {
			start = m.cursor - height + 1
		}

		if start > len(m.conversations)-height {
			start = len(m.conversations) - height
		}

		if start < 0 {
			start = 0
		}
	}

	end := min(len(m.conversations), start+height)

	var lines []string

	for i := start; i < end; i++ {
		c := m.conversations[i]
		prefix := "  "
		style := lipgloss.NewStyle()

		if i == m.cursor {
			prefix = "> "
			style = style.Bold(true).Foreground(colorPurple)
		}

		countStr := " (" + strconv.Itoa(len(c.messages)) + ")"
		label := truncate(c.peerName, max(1, width-len(prefix)-lipgloss.Width(countStr)))
		lines = append(lines, prefix+style.Render(label)+dim.Render(countStr))
	}

	return strings.Join(lines, "\n")
}

func (m messageOverlayModel) renderThread(width, height int) string {
	dim := lipgloss.NewStyle().Foreground(colorDim)

	if m.cursor < 0 || m.cursor >= len(m.conversations) {
		if m.loaded {
			return dim.Render("Select a conversation")
		}

		return ""
	}

	conv := m.conversations[m.cursor]

	meStyle := lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
	peerStyle := lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
	sysStyle := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	bodyStyle := lipgloss.NewStyle().Width(width)
	selStyle := lipgloss.NewStyle().Foreground(colorPurple).Bold(true)

	// Build each message as a block of lines; collapsed messages are a single
	// header line, expanded ones add the (sanitized) body. Track where the
	// selected message's block starts/ends so we can scroll it into view.
	var lines []string

	selStart, selEnd := 0, 0

	for i, e := range conv.messages {
		// The focused message is always expanded; others only if pinned (or if
		// they have no id to track collapse state).
		expanded := i == m.msgCursor || e.id == "" || m.pinned[e.id]

		marker := "▸"
		if expanded {
			marker = "▾"
		}

		who := e.sender
		if e.outbound {
			who = "me → " + conv.peerName
		}

		hs := peerStyle

		switch {
		case e.system:
			hs = sysStyle
			who = "⚙ " + who
		case e.outbound:
			hs = meStyle
		}

		markerStyle := dim
		if i == m.msgCursor {
			markerStyle = selStyle
		}

		body := strings.TrimRight(sanitizeMessageBody(e.body), "\n")

		header := markerStyle.Render(marker) + " " + hs.Render(who) + dim.Render(msgTimestamp(e.createdAt))
		if !expanded {
			// Show a one-line snippet so a collapsed thread is still scannable.
			// Budget by display width; skip if there's no room.
			if budget := width - lipgloss.Width(header) - 2; budget > 0 {
				if snippet := strings.TrimSpace(firstLine(body)); snippet != "" {
					header += "  " + dim.Render(ansi.Truncate(snippet, budget, "…"))
				}
			}
		}
		// Bound the header to one physical row so renderThread's line counting
		// stays in sync with what the terminal displays (no wrap desync).
		header = ansi.Truncate(header, width, "…")

		blockStart := len(lines)

		lines = append(lines, header)
		if expanded {
			lines = append(lines, strings.Split(bodyStyle.Render(body), "\n")...)
			lines = append(lines, "")
		}

		if i == m.msgCursor {
			selStart, selEnd = blockStart, len(lines)
		}
	}

	// Scroll so the selected message's block is visible.
	total := len(lines)
	start := 0

	if total > height {
		if selEnd-selStart > height {
			// Focused block is taller than the viewport: anchor at its top and
			// let lineScroll page down through it (clamped so its top can't
			// scroll past the viewport bottom).
			start = selStart + min(m.lineScroll, max(0, (selEnd-selStart)-height))
		} else {
			// Block fits: show it fully, scrolling up only as needed.
			if selEnd > start+height {
				start = selEnd - height
			}

			if selStart < start {
				start = selStart
			}
		}

		if start > total-height {
			start = total - height
		}

		if start < 0 {
			start = 0
		}
	}

	end := min(total, start+height)

	return strings.Join(lines[start:end], "\n")
}

// msgTimestamp renders an absolute time plus a relative delta, e.g.
// "  15:04 (3m ago)" — date is shown when the message isn't from today.
func msgTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	lt := t.Local()
	now := time.Now()

	layout := "15:04"
	if lt.Year() != now.Year() || lt.YearDay() != now.YearDay() {
		layout = "Jan 2 15:04"
	}

	return "  " + lt.Format(layout) + " (" + relTime(t) + ")"
}

func relTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}

	return ShortDuration(d) + " ago"
}

// sanitizeMessageBody removes ANSI escape sequences and stray control
// characters from an (agent-controlled) message body, keeping only newlines and
// tabs. This prevents a message from emitting terminal control sequences that
// could spoof or corrupt the operator's overlay.
func sanitizeMessageBody(s string) string {
	s = ansi.Strip(s)

	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}

		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, s)
}

// RunMessageOverlay displays the chatroom-style message viewer for sessionID,
// showing direct messages to and from that session grouped by peer. It is
// read-only in v1 and refreshes at the configured terminal.refresh_interval.
// fetch returns the conversation and ok=false on a transient error (so the
// last good snapshot is kept).
// Returns when the user closes the overlay; the caller then reattaches.
func RunMessageOverlay(sessionID string, keys MessageKeys, fetch func() ([]protocol.ConversationMessage, bool), names map[string]string) {
	m := newMessageOverlayModel(sessionID, fetch, names)
	m.keys = keys
	p := tea.NewProgram(m)
	_, _ = p.Run()
}
