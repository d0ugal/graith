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

type msgBrowserMode int

const (
	msgModeDirect msgBrowserMode = iota
	msgModeTopics
)

// MsgTopicInfo is the client-side shape for a message topic listed by
// msg_topics. It mirrors the daemon response without importing daemon types into
// the client package.
type MsgTopicInfo struct {
	Name     string `json:"name"`
	Total    int64  `json:"total"`
	Unread   int64  `json:"unread"`
	LatestAt string `json:"latest_at,omitempty"`
}

// MessageFetchRequest describes the optional topic payload requested by a
// message-browser refresh.
type MessageFetchRequest struct {
	Topic string
}

// MessageFetchResult is one snapshot of direct messages, topic metadata, and
// the selected topic's messages.
type MessageFetchResult struct {
	DirectMessages []protocol.ConversationMessage
	Topics         []MsgTopicInfo
	TopicMessages  []protocol.ConversationMessage
}

// MessageBrowserFetch loads one message-browser snapshot. ok=false means the
// caller should keep its previous snapshot.
type MessageBrowserFetch func(MessageFetchRequest) (MessageFetchResult, bool)

// msgConversation is one direct peer's thread: every message exchanged between
// self and that peer, in chronological order.
type msgConversation struct {
	peerID   string
	peerName string
	messages []msgEntry
	lastAt   time.Time
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}

	return s
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	if len(s) <= maxLen {
		return s
	}

	if maxLen < 4 {
		return s[:maxLen]
	}

	return s[:maxLen-3] + "..."
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
	topics        []MsgTopicInfo
	topicName     string
	topicMessages []msgEntry
	ok            bool // false if the fetch failed (keep the last good snapshot)
}

type msgTickMsg struct{}

type messageOverlayModel struct {
	selfID string
	// fetch returns the browser snapshot and ok=false on a transient
	// fetch error (so the model can keep the last good snapshot).
	fetch MessageBrowserFetch
	names map[string]string

	mode          msgBrowserMode
	conversations []msgConversation
	cursor        int // selected direct conversation in the left rail
	topics        []MsgTopicInfo
	topicCursor   int
	topicMessages []msgEntry
	topicLoaded   string
	topicTotal    int64
	topicLatestAt string
	msgCursor     int // selected message within the current direct thread/topic
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
	// refresh is the daemon re-poll cadence, snapshotted from the shared
	// configurable refreshInterval (issue #1315) when the overlay opens so the
	// message viewer tracks terminal.refresh_interval like the picker and status
	// bar rather than a private hard-coded cadence.
	refresh time.Duration
}

func newMessageOverlayModel(selfID string, fetch func() ([]protocol.ConversationMessage, bool), names map[string]string) messageOverlayModel {
	// Keep the original direct-message constructor as a compatibility shim for
	// tests and any external callers using the package-level overlay API.
	var browserFetch MessageBrowserFetch
	if fetch != nil {
		browserFetch = func(MessageFetchRequest) (MessageFetchResult, bool) {
			msgs, ok := fetch()
			if !ok {
				return MessageFetchResult{}, false
			}

			return MessageFetchResult{DirectMessages: msgs}, true
		}
	}

	return newMessageBrowserModel(selfID, browserFetch, names)
}

func newMessageBrowserModel(selfID string, fetch MessageBrowserFetch, names map[string]string) messageOverlayModel {
	return messageOverlayModel{
		selfID:  selfID,
		fetch:   fetch,
		names:   names,
		pinned:  map[string]bool{},
		keys:    DefaultMessageKeys(),
		refresh: refreshInterval,
	}
}

func (m messageOverlayModel) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), m.tickCmd())
}

// tickFn constructs the recurring refresh timer. It is a package var (defaulting
// to tea.Tick) purely so tests can capture the interval tickCmd schedules
// without a real sleep; production always uses tea.Tick.
var tickFn = tea.Tick

func (m messageOverlayModel) tickCmd() tea.Cmd {
	// Use the configured shared cadence; fall back to the package default if a
	// model was built without the constructor (e.g. a bare struct literal in a
	// test) so a zero can't turn tea.Tick into a busy loop.
	interval := m.refresh
	if interval <= 0 {
		interval = refreshInterval
	}

	return tickFn(interval, func(time.Time) tea.Msg {
		return msgTickMsg{}
	})
}

func (m messageOverlayModel) fetchCmd() tea.Cmd {
	fetch := m.fetch
	selfID := m.selfID
	names := m.names

	topic := ""
	if m.needsTopicFetch() {
		topic = m.selectedTopicName()
	}

	return func() tea.Msg {
		if fetch == nil {
			return msgFetchedMsg{ok: true}
		}

		result, ok := fetch(MessageFetchRequest{Topic: topic})
		if !ok {
			return msgFetchedMsg{ok: false}
		}

		return msgFetchedMsg{
			conversations: groupConversations(selfID, result.DirectMessages, names),
			topics:        result.Topics,
			topicName:     topic,
			topicMessages: topicEntries(selfID, result.TopicMessages),
			ok:            true,
		}
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

func topicEntries(selfID string, msgs []protocol.ConversationMessage) []msgEntry {
	entries := make([]msgEntry, 0, len(msgs))
	for _, cm := range msgs {
		sender := cm.SenderName
		if sender == "" {
			sender = shortID(cm.SenderID)
		}

		entries = append(entries, msgEntry{
			id:        cm.ID,
			sender:    sender,
			body:      cm.Body,
			createdAt: parseMsgTime(cm.CreatedAt),
			outbound:  cm.SenderID == selfID,
			system:    isSystemMessage(cm),
		})
	}

	return entries
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

func (m messageOverlayModel) selectedTopicName() string {
	topic, ok := m.selectedTopicInfo()
	if !ok {
		return ""
	}

	return topic.Name
}

func (m messageOverlayModel) selectedTopicInfo() (MsgTopicInfo, bool) {
	if m.topicCursor < 0 || m.topicCursor >= len(m.topics) {
		return MsgTopicInfo{}, false
	}

	return m.topics[m.topicCursor], true
}

func (m messageOverlayModel) needsTopicFetch() bool {
	if m.mode != msgModeTopics {
		return false
	}

	topic, ok := m.selectedTopicInfo()
	if !ok || topic.Name == "" {
		return false
	}

	return topic.Name != m.topicLoaded || topic.Total != m.topicTotal || topic.LatestAt != m.topicLatestAt
}

func (m messageOverlayModel) currentEntries() []msgEntry {
	if m.mode == msgModeTopics {
		if m.selectedTopicName() == "" || m.selectedTopicName() != m.topicLoaded {
			return nil
		}

		return m.topicMessages
	}

	if m.cursor < 0 || m.cursor >= len(m.conversations) {
		return nil
	}

	return m.conversations[m.cursor].messages
}

func restoreDirectCursor(conversations []msgConversation, selectedPeer string) (int, bool) {
	for i, c := range conversations {
		if c.peerID == selectedPeer {
			return i, true
		}
	}

	return 0, false
}

func restoreTopicCursor(topics []MsgTopicInfo, selectedTopic string) (int, bool) {
	for i, t := range topics {
		if t.Name == selectedTopic {
			return i, true
		}
	}

	return 0, false
}

func restoreMessageCursor(m messageOverlayModel, sourceFound bool, focusedMsgID string, prevAtLast bool) int {
	count := m.msgCount()

	switch {
	case sourceFound && prevAtLast:
		// Reader was at the tail: follow the newest message.
		return max(0, count-1)
	case sourceFound && focusedMsgID != "":
		// Re-find the focused message by id so inserts/removals before it don't
		// shift the cursor onto a different message; fall back to clamping if it
		// vanished.
		cursor := max(0, min(m.msgCursor, count-1))
		for i, e := range m.currentEntries() {
			if e.id == focusedMsgID {
				cursor = i
				break
			}
		}

		return cursor
	default:
		// Source vanished (or no prior focus): land on the newest message.
		return max(0, count-1)
	}
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
		// Preserve the selected source and focused message across refreshes.
		var selectedPeer, selectedTopic, focusedMsgID string
		if m.cursor >= 0 && m.cursor < len(m.conversations) {
			selectedPeer = m.conversations[m.cursor].peerID
		}

		selectedTopic = m.selectedTopicName()
		if e := m.currentEntry(); e != nil {
			focusedMsgID = e.id
		}

		prevAtLast := m.msgCursor >= m.msgCount()-1

		m.conversations = msg.conversations
		m.topics = msg.topics

		if msg.topicName != "" {
			m.topicMessages = msg.topicMessages
			m.topicLoaded = msg.topicName
			m.topicTotal = 0
			m.topicLatestAt = ""

			for _, topic := range m.topics {
				if topic.Name == msg.topicName {
					m.topicTotal = topic.Total
					m.topicLatestAt = topic.LatestAt

					break
				}
			}
		}

		var peerFound, topicFound bool

		m.cursor, peerFound = restoreDirectCursor(m.conversations, selectedPeer)

		m.topicCursor, topicFound = restoreTopicCursor(m.topics, selectedTopic)
		if currentTopic := m.selectedTopicName(); currentTopic != m.topicLoaded {
			m.topicMessages = nil
			m.topicLoaded = ""
			m.topicTotal = 0
			m.topicLatestAt = ""
		}

		sourceFound := peerFound
		if m.mode == msgModeTopics {
			sourceFound = topicFound
		}

		m.msgCursor = restoreMessageCursor(m, sourceFound, focusedMsgID, prevAtLast)

		// If the refresh moved focus to a different message, reset the
		// intra-message scroll so the new message opens at its header. Key-driven
		// cursor moves already do this; the refresh path previously did not,
		// leaving lineScroll pointing partway down an unrelated message.
		if e := m.currentEntry(); e == nil || e.id != focusedMsgID {
			m.lineScroll = 0
		}

		if m.needsTopicFetch() {
			m.fetching = true

			return m, m.fetchCmd()
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
		// Horizontal: switch conversation/topic in the rail.
		case matchKey(m.keys.NextConv, s):
			if m.mode == msgModeTopics {
				if m.topicCursor < len(m.topics)-1 {
					m.topicCursor++
					m.msgCursor = max(0, m.msgCount()-1)
					m.lineScroll = 0

					if m.needsTopicFetch() && !m.fetching {
						m.fetching = true

						return m, m.fetchCmd()
					}
				}

				return m, nil
			}

			if m.cursor < len(m.conversations)-1 {
				m.cursor++
				m.msgCursor = max(0, m.msgCountAt(m.cursor)-1)
				m.lineScroll = 0
			}

			return m, nil
		case matchKey(m.keys.PrevConv, s):
			if m.mode == msgModeTopics {
				if m.topicCursor > 0 {
					m.topicCursor--
					m.msgCursor = max(0, m.msgCount()-1)
					m.lineScroll = 0

					if m.needsTopicFetch() && !m.fetching {
						m.fetching = true

						return m, m.fetchCmd()
					}
				}

				return m, nil
			}

			if m.cursor > 0 {
				m.cursor--
				m.msgCursor = max(0, m.msgCountAt(m.cursor)-1)
				m.lineScroll = 0
			}

			return m, nil
		case matchKey(m.keys.Topics, s):
			if m.mode != msgModeTopics {
				m.mode = msgModeTopics
				m.msgCursor = max(0, m.msgCount()-1)
				m.lineScroll = 0

				if m.needsTopicFetch() && !m.fetching {
					m.fetching = true

					return m, m.fetchCmd()
				}
			}

			return m, nil
		case matchKey(m.keys.Direct, s):
			if m.mode != msgModeDirect {
				m.mode = msgModeDirect
				m.msgCursor = max(0, m.msgCount()-1)
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

// msgCount returns the number of messages in the selected conversation or topic.
func (m messageOverlayModel) msgCount() int {
	if m.mode == msgModeTopics {
		return len(m.currentEntries())
	}

	return m.msgCountAt(m.cursor)
}

func (m messageOverlayModel) msgCountAt(conv int) int {
	if conv < 0 || conv >= len(m.conversations) {
		return 0
	}

	return len(m.conversations[conv].messages)
}

// currentEntry returns the message under the cursor, or nil.
func (m messageOverlayModel) currentEntry() *msgEntry {
	msgs := m.currentEntries()
	if m.msgCursor < 0 || m.msgCursor >= len(msgs) {
		return nil
	}

	return &msgs[m.msgCursor]
}

func (m messageOverlayModel) setAllPinned(v bool) {
	for _, e := range m.currentEntries() {
		if e.id != "" {
			m.pinned[e.id] = v
		}
	}
}

func (m messageOverlayModel) contextLabel() string {
	source := "Direct"
	if m.mode == msgModeTopics {
		source = "Topics"
		if topic := m.selectedTopicName(); topic != "" {
			source = "Topic " + topic
		}
	} else if m.cursor >= 0 && m.cursor < len(m.conversations) {
		source = "Direct " + m.conversations[m.cursor].peerName
	}

	count := m.msgCount()
	if count <= 0 {
		return source
	}

	pos := min(max(0, m.msgCursor)+1, count)

	label := fmt.Sprintf("%s  %d/%d", source, pos, count)
	if pos < count {
		if key := primaryKey(m.keys.Bottom); key != "" {
			label += "  " + key + " latest"
		} else {
			label += "  latest"
		}
	}

	return label
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
	if context := m.contextLabel(); context != "" {
		title += "  " + dim.Render(context)
	}

	if !m.loaded {
		title += "  " + dim.Render("loading…")
	}

	title = ansi.Truncate(title, w, "…")

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
		helpLine = ansi.Truncate(helpLine, w, "…")
		body = m.renderThread(max(1, w-1), bodyH)
	} else {
		helpLine = help.Render(fmt.Sprintf(
			"%s/%s older/newer  %s/%s scroll long msg  %s latest  %s/%s source  %s topics  %s direct  %s pin  %s/%s all  %s close",
			primaryKey(m.keys.Up), primaryKey(m.keys.Down),
			primaryKey(m.keys.PageUp), primaryKey(m.keys.PageDown),
			primaryKey(m.keys.Bottom),
			primaryKey(m.keys.PrevConv), primaryKey(m.keys.NextConv),
			primaryKey(m.keys.Topics), primaryKey(m.keys.Direct),
			primaryKey(m.keys.Pin),
			primaryKey(m.keys.ExpandAll), primaryKey(m.keys.CollapseAll),
			primaryKey(m.keys.Cancel)))
		helpLine = ansi.Truncate(helpLine, w, "…")

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

	b.WriteString("\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	b.WriteString(helpLine)

	v := tea.NewView(b.String())
	v.AltScreen = true

	return v
}

func (m messageOverlayModel) renderRail(width, height int) string {
	if m.mode == msgModeTopics {
		return m.renderTopicRail(width, height)
	}

	dim := lipgloss.NewStyle().Foreground(colorDim)
	if m.loaded && len(m.conversations) == 0 {
		return dim.Render("No direct messages")
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

func (m messageOverlayModel) renderTopicRail(width, height int) string {
	dim := lipgloss.NewStyle().Foreground(colorDim)
	if m.loaded && len(m.topics) == 0 {
		return dim.Render("No available topics")
	}

	start := 0

	if len(m.topics) > height {
		if m.topicCursor >= height {
			start = m.topicCursor - height + 1
		}

		if start > len(m.topics)-height {
			start = len(m.topics) - height
		}

		if start < 0 {
			start = 0
		}
	}

	end := min(len(m.topics), start+height)

	var lines []string

	for i := start; i < end; i++ {
		topic := m.topics[i]
		prefix := "  "
		style := lipgloss.NewStyle()

		if i == m.topicCursor {
			prefix = "> "
			style = style.Bold(true).Foreground(colorPurple)
		}

		countStr := " (" + strconv.FormatInt(topic.Total, 10) + ")"
		if topic.Unread > 0 {
			countStr = " (" + strconv.FormatInt(topic.Total, 10) + "/" + strconv.FormatInt(topic.Unread, 10) + " new)"
		}

		label := truncate(topic.Name, max(1, width-len(prefix)-lipgloss.Width(countStr)))
		lines = append(lines, prefix+style.Render(label)+dim.Render(countStr))
	}

	return strings.Join(lines, "\n")
}

func (m messageOverlayModel) renderThread(width, height int) string {
	dim := lipgloss.NewStyle().Foreground(colorDim)

	var (
		entries    []msgEntry
		directPeer string
		topicName  string
	)

	if m.mode == msgModeTopics {
		if m.loaded && len(m.topics) == 0 {
			return dim.Render("No available topics")
		}

		topicName = m.selectedTopicName()
		if topicName == "" {
			if m.loaded {
				return dim.Render("Select a topic")
			}

			return ""
		}

		if topicName != m.topicLoaded {
			return dim.Render("Loading topic messages…")
		}

		entries = m.topicMessages
		if m.loaded && len(entries) == 0 {
			return dim.Render("No messages in " + topicName)
		}
	} else {
		if m.cursor < 0 || m.cursor >= len(m.conversations) {
			if m.loaded {
				if len(m.conversations) == 0 {
					return dim.Render("No direct messages")
				}

				return dim.Render("Select a conversation")
			}

			return ""
		}

		directPeer = m.conversations[m.cursor].peerName
		entries = m.currentEntries()
	}

	if len(entries) == 0 {
		if m.loaded {
			if m.mode == msgModeDirect {
				return dim.Render("No direct messages")
			}

			return dim.Render("Select a topic")
		}

		return ""
	}

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

	for i, e := range entries {
		// The focused message is always expanded; others only if pinned (or if
		// they have no id to track collapse state).
		expanded := i == m.msgCursor || e.id == "" || m.pinned[e.id]

		marker := "▸"
		if expanded {
			marker = "▾"
		}

		who := e.sender
		if m.mode == msgModeTopics {
			who = "#" + topicName + "  " + who
			if e.outbound {
				who = "me → #" + topicName
			}
		} else if e.outbound {
			who = "me → " + directPeer
		} else {
			who = who + " → me"
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
// read-only in v1 and re-polls the daemon at the configured shared refresh
// interval (terminal.refresh_interval), matching the picker and status bar.
// fetch returns the conversation and ok=false on a transient error
// (so the last good snapshot is kept).
// Returns when the user closes the overlay; the caller then reattaches.
func RunMessageOverlay(sessionID string, keys MessageKeys, fetch func() ([]protocol.ConversationMessage, bool), names map[string]string) {
	m := newMessageOverlayModel(sessionID, fetch, names)
	m.keys = keys
	p := tea.NewProgram(m)
	_, _ = p.Run()
}

// RunMessageBrowserOverlay displays the bounded direct/topic message browser
// for sessionID. It opens on the newest direct message and lets the user switch
// to visible topics with the configured message source keys.
func RunMessageBrowserOverlay(sessionID string, keys MessageKeys, fetch MessageBrowserFetch, names map[string]string) {
	m := newMessageBrowserModel(sessionID, fetch, names)
	m.keys = keys
	p := tea.NewProgram(m)
	_, _ = p.Run()
}
