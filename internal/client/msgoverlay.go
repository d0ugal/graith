package client

import (
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	sender    string // display name of the author
	body      string
	createdAt time.Time
	outbound  bool // true if authored by self (sent), false if received
	system    bool // true for _system.* / orchestrator notifications
}

type msgFetchedMsg struct {
	conversations []msgConversation
}

type msgTickMsg struct{}

type messageOverlayModel struct {
	selfID string
	fetch  func() []protocol.ConversationMessage
	names  map[string]string

	conversations []msgConversation
	cursor        int // selected conversation in the left rail
	scroll        int // thread scroll offset (lines from top)
	loaded        bool
	width         int
	height        int
}

func newMessageOverlayModel(selfID string, fetch func() []protocol.ConversationMessage, names map[string]string) messageOverlayModel {
	return messageOverlayModel{
		selfID: selfID,
		fetch:  fetch,
		names:  names,
	}
}

func (m messageOverlayModel) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), m.tickCmd())
}

func (m messageOverlayModel) tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return msgTickMsg{}
	})
}

func (m messageOverlayModel) fetchCmd() tea.Cmd {
	fetch := m.fetch
	selfID := m.selfID
	names := m.names
	return func() tea.Msg {
		if fetch == nil {
			return msgFetchedMsg{}
		}
		return msgFetchedMsg{conversations: groupConversations(selfID, fetch(), names)}
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
	return strings.HasPrefix(cm.Stream, "_system.") || cm.SenderName == "orchestrator"
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
		return m, tea.Batch(m.fetchCmd(), m.tickCmd())

	case msgFetchedMsg:
		// Preserve the selected peer across refreshes where possible.
		var selectedPeer string
		if m.cursor >= 0 && m.cursor < len(m.conversations) {
			selectedPeer = m.conversations[m.cursor].peerID
		}
		m.conversations = msg.conversations
		m.loaded = true
		m.cursor = 0
		for i, c := range m.conversations {
			if c.peerID == selectedPeer {
				m.cursor = i
				break
			}
		}
		if m.cursor >= len(m.conversations) {
			m.cursor = max(0, len(m.conversations)-1)
		}
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.conversations)-1 {
				m.cursor++
				m.scroll = 0
			}
			return m, nil
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
				m.scroll = 0
			}
			return m, nil
		case "ctrl+d", "pgdown", " ":
			m.scroll += 5
			return m, nil
		case "ctrl+u", "pgup", "b":
			m.scroll -= 5
			if m.scroll < 0 {
				m.scroll = 0
			}
			return m, nil
		case "g":
			m.scroll = 0
			return m, nil
		}
	}
	return m, nil
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
	helpLine := help.Render("↑/↓ conversation  PgUp/PgDn scroll  q close")

	// Body area between title (1 line + blank) and help (blank + 1 line).
	bodyH := max(1, h-4)

	railW := 26
	if w < 70 {
		railW = max(16, w/3)
	}
	threadW := max(10, w-railW-3)

	rail := m.renderRail(railW, bodyH)
	thread := m.renderThread(threadW, bodyH)

	railStyle := lipgloss.NewStyle().Width(railW).Height(bodyH)
	threadStyle := lipgloss.NewStyle().Width(threadW).Height(bodyH).
		BorderLeft(true).Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colorFaint).PaddingLeft(1)

	body := lipgloss.JoinHorizontal(lipgloss.Top, railStyle.Render(rail), threadStyle.Render(thread))

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

	var lines []string
	for i, c := range m.conversations {
		if i >= height {
			break
		}
		name := c.peerName
		prefix := "  "
		style := lipgloss.NewStyle()
		if i == m.cursor {
			prefix = "> "
			style = style.Bold(true).Foreground(colorPurple)
		}
		count := dim.Render(" (" + strconv.Itoa(len(c.messages)) + ")")
		label := truncate(name, max(1, width-len(prefix)-5))
		lines = append(lines, prefix+style.Render(label)+count)
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

	var lines []string
	for _, e := range conv.messages {
		header := e.sender
		if e.outbound {
			header = "me → " + conv.peerName
		}
		hs := peerStyle
		switch {
		case e.system:
			hs = sysStyle
			header = "⚙ " + header
		case e.outbound:
			hs = meStyle
		}
		ts := ""
		if !e.createdAt.IsZero() {
			ts = "  " + dim.Render(relTime(e.createdAt))
		}
		lines = append(lines, hs.Render(header)+ts)
		body := bodyStyle.Render(strings.TrimRight(e.body, "\n"))
		lines = append(lines, strings.Split(body, "\n")...)
		lines = append(lines, "")
	}

	// Default scroll to the bottom (most recent) unless the user scrolled up.
	total := len(lines)
	maxStart := max(0, total-height)
	start := maxStart - m.scroll
	if start < 0 {
		start = 0
	}
	if start > maxStart {
		start = maxStart
	}
	end := min(total, start+height)
	return strings.Join(lines[start:end], "\n")
}

func relTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	return ShortDuration(d) + " ago"
}

// RunMessageOverlay displays the chatroom-style message viewer for sessionID,
// showing direct messages to and from that session grouped by peer. It is
// read-only in v1 and refreshes every 2 seconds. Returns when the user closes
// the overlay; the caller then reattaches.
func RunMessageOverlay(sessionID string, fetch func() []protocol.ConversationMessage, names map[string]string) {
	m := newMessageOverlayModel(sessionID, fetch, names)
	p := tea.NewProgram(m)
	_, _ = p.Run()
}
