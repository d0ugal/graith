package daemon

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

const (
	eventTypeStatusChange   = "status_change"
	eventTypeMessage        = "message"
	eventTypeSessionDeleted = "session_deleted"

	eventStatusKindAgent   = "agent"
	eventStatusKindSession = "session"
)

type eventBroker struct {
	mu     sync.Mutex
	subs   map[chan protocol.EventMsg]struct{}
	buffer int
}

type pendingStatusChangeEvent struct {
	sessionID   string
	sessionName string
	from        SessionStatus
	to          SessionStatus
	at          time.Time
	ok          bool
}

func newEventBroker(buffer int) *eventBroker {
	if buffer < 1 {
		buffer = 1
	}

	return &eventBroker{
		subs:   make(map[chan protocol.EventMsg]struct{}),
		buffer: buffer,
	}
}

func (b *eventBroker) Subscribe() (<-chan protocol.EventMsg, func()) {
	ch := make(chan protocol.EventMsg, b.buffer)

	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once

	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
		})
	}

	return ch, unsub
}

func (b *eventBroker) Publish(event protocol.EventMsg) {
	b.mu.Lock()

	subs := make([]chan protocol.EventMsg, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func eventAt(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}

	return t.UTC().Format(time.RFC3339Nano)
}

func pendingSessionStatusChangeEvent(id string, s *SessionState, from SessionStatus) pendingStatusChangeEvent {
	if s == nil || from == s.Status {
		return pendingStatusChangeEvent{}
	}

	return pendingStatusChangeEvent{
		sessionID:   id,
		sessionName: s.Name,
		from:        from,
		to:          s.Status,
		at:          s.StatusChangedAt,
		ok:          true,
	}
}

func (sm *SessionManager) publishPendingStatusChangeEvent(event pendingStatusChangeEvent) {
	if !event.ok {
		return
	}

	sm.publishStatusChangeEvent(
		event.sessionID,
		event.sessionName,
		eventStatusKindSession,
		string(event.from),
		string(event.to),
		event.at,
	)
}

func (sm *SessionManager) publishStatusChangeEvent(sessionID, sessionName, statusKind, from, to string, at time.Time) {
	if statusKind == eventStatusKindSession {
		sm.observeSessionLifecycleTransition(from, to)
	}

	if sm.events == nil || from == to {
		return
	}

	sm.events.Publish(protocol.EventMsg{
		Type:       eventTypeStatusChange,
		At:         eventAt(at),
		SessionID:  sessionID,
		Session:    sessionName,
		StatusKind: statusKind,
		From:       from,
		To:         to,
	})
}

func (sm *SessionManager) publishSessionDeletedEvent(sessionID, sessionName string, at time.Time) {
	if sm.events == nil {
		return
	}

	sm.events.Publish(protocol.EventMsg{
		Type:      eventTypeSessionDeleted,
		At:        eventAt(at),
		SessionID: sessionID,
		Session:   sessionName,
	})
}

func (sm *SessionManager) publishMessage(opts PublishOpts) (Message, error) {
	msg, err := sm.messages.Publish(opts)
	if err != nil {
		return Message{}, err
	}

	sm.observeMessagePublished(msg)
	sm.publishMessageEvent(msg)

	return msg, nil
}

func (sm *SessionManager) publishMessageEvent(msg Message) {
	if sm.events == nil || strings.HasPrefix(msg.Stream, "_system.") {
		return
	}

	if _, isInbox := parseInboxStream(msg.Stream); isInbox {
		return
	}

	sender := msg.SenderName
	if sender == "" {
		sender = msg.SenderID
	}

	sm.events.Publish(protocol.EventMsg{
		Type:      eventTypeMessage,
		At:        msg.CreatedAt,
		Topic:     msg.Stream,
		MessageID: msg.ID,
		Seq:       msg.Seq,
		SenderID:  msg.SenderID,
		Sender:    sender,
		System:    msg.System,
		Body:      msg.Body,
	})
}

func (sm *SessionManager) handleEventsSub(
	ctx context.Context,
	sendControl func(string, any),
	sendControlResult func(string, any) error,
	reader *protocol.FrameReader,
) bool {
	if sm.events == nil {
		sendControl("error", protocol.ErrorMsg{Message: "event stream unavailable"})
		return false
	}

	sub, unsub := sm.events.Subscribe()
	defer unsub()

	sendControl("events_following", struct{}{})

	detachCh := readForDetach(reader)

	for {
		select {
		case event := <-sub:
			if err := sendControlResult("event", event); err != nil {
				return true
			}
		case <-detachCh:
			sendControl("events_done", struct{}{})
			return true
		case <-ctx.Done():
			return true
		}
	}
}
