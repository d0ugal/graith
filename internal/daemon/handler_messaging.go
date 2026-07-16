package daemon

import (
	"github.com/d0ugal/graith/internal/protocol"
)

// filterInboxStreams drops inbox streams from a stream listing. An authenticated
// session must not discover other sessions' private inbox streams via msg_topics
// (it reads its own inbox with gr msg inbox instead).
func filterInboxStreams(streams []StreamInfo) []StreamInfo {
	filtered := streams[:0]

	for _, s := range streams {
		if _, isInbox := parseInboxStream(s.Name); !isInbox {
			filtered = append(filtered, s)
		}
	}

	return filtered
}

// applyMsgPubSenderIdentity forces the publish sender identity by role so it
// can't be spoofed. A session publishes as itself; the local human (CLI) may
// address on behalf of a named session (trusted local use); a remote human
// publishes as its device and CANNOT claim to be a session. It returns false
// (after sending an error) when the caller's role may not publish at all.
func applyMsgPubSenderIdentity(sm *SessionManager, auth authContext, m *protocol.MsgPubMsg, send func(string, any)) bool {
	switch auth.role {
	case roleSession, roleOrchestrator:
		m.SenderID = auth.sessionID
		if sess, ok := sm.Get(auth.sessionID); ok {
			m.SenderName = sess.Name
		}
	case roleLocalHuman:
		if m.SenderID != "" {
			if sess, ok := sm.Get(m.SenderID); ok {
				m.SenderName = sess.Name
			}
		}
	case roleRemoteHuman:
		m.SenderID = "device:" + auth.deviceID
		m.SenderName = "remote device"
	default:
		send("error", protocol.ErrorMsg{Message: "not authorized to publish"})
		return false
	}

	return true
}

// handleMsgPub publishes a message to a stream, forcing sender identity by role
// and (unless quiet) notifying the target's inbox when the stream is an inbox.
func handleMsgPub(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.MsgPubMsg](msg, send, "invalid msg_pub message")
	if !ok {
		return
	}

	if !applyMsgPubSenderIdentity(sm, auth, &m, send) {
		return
	}

	published, err := sm.messages.Publish(PublishOpts{Stream: m.Stream, SenderID: m.SenderID, SenderName: m.SenderName, Body: m.Body, ThreadID: m.ThreadID, ReplyTo: m.ReplyTo})
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("msg_published", published)

	if !m.Quiet {
		if targetID, isInbox := parseInboxStream(m.Stream); isInbox {
			go sm.notifyInbox(targetID, m.SenderID, m.SenderName)
		}
	}
}

// handleMsgAck acknowledges the latest message on a stream for a subscriber.
func handleMsgAck(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.MsgAckMsg](msg, send, "invalid msg_ack message")
	if !ok {
		return
	}

	if auth.authenticated {
		m.Subscriber = auth.sessionID
		if _, isInbox := parseInboxStream(m.Stream); isInbox {
			send("error", protocol.ErrorMsg{
				Message: "inbox streams cannot be acked via msg_ack; use gr msg inbox --ack instead",
			})

			return
		}
	}

	if err := sm.messages.AckLatest(m.Stream, m.Subscriber); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("msg_acked", struct{}{})
	}
}

// handleMsgTopics lists the streams visible to a subscriber, hiding inbox
// streams from authenticated sessions.
func handleMsgTopics(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.MsgTopicsMsg](msg, send, "invalid msg_topics message")
	if !ok {
		return
	}

	if auth.authenticated {
		m.Subscriber = auth.sessionID
	}

	streams, err := sm.messages.ListStreams(m.Subscriber, m.IncludeSystem)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	if auth.authenticated {
		streams = filterInboxStreams(streams)
	}

	send("msg_topics_list", struct {
		Streams []StreamInfo `json:"streams"`
	}{streams})
}

// handleMsgConversation returns the threaded conversation for a target session.
// The self-or-descendant rule lets the human CLI, the session itself, an
// ancestor agent, or the orchestrator read it; the by-sender filter inside
// Conversation keeps the cross-inbox scan safe.
func handleMsgConversation(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.MsgConversationMsg](msg, send, "invalid msg_conversation message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, m.SessionID, authSelfOrDescendant, send) {
		return
	}

	if sm.messages == nil {
		send("msg_conversation_list", protocol.MsgConversationListMsg{})

		return
	}

	convo, err := sm.messages.Conversation(m.SessionID, sm.Config().Messages.ClampConversationLimit(m.Limit))
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	out := make([]protocol.ConversationMessage, len(convo))
	for i, cm := range convo {
		out[i] = protocol.ConversationMessage{
			ID:         cm.ID,
			Seq:        cm.Seq,
			Stream:     cm.Stream,
			SenderID:   cm.SenderID,
			SenderName: cm.SenderName,
			Body:       cm.Body,
			ThreadID:   cm.ThreadID,
			ReplyTo:    cm.ReplyTo,
			CreatedAt:  cm.CreatedAt,
			System:     cm.System,
		}
	}

	send("msg_conversation_list", protocol.MsgConversationListMsg{Messages: out})
}

// handleMsgJailList returns a metadata-only summary of jailed (quarantined) PR
// comments — the raw untrusted body is never included here, even for the human,
// so merely listing the jail can't be used to inject a body dump.
func handleMsgJailList(sm *SessionManager, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.MsgJailListMsg](msg, send, "invalid msg_jail_list message")
	if !ok {
		return
	}

	if sm.messages == nil {
		send("msg_jail_list", protocol.MsgJailListResponse{})

		return
	}

	jailed, err := sm.messages.ListJailed(m.IncludeReleased)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("msg_jail_list", protocol.MsgJailListResponse{Jailed: jailedToWire(jailed, false)})
}

// handleMsgJailShow returns one jailed comment. The raw body is revealed only to
// a release-authorized role (human/orchestrator); an ordinary agent/guest gets
// the metadata with the body withheld.
func handleMsgJailShow(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.MsgJailShowMsg](msg, send, "invalid msg_jail_show message")
	if !ok {
		return
	}

	if sm.messages == nil {
		send("error", protocol.ErrorMsg{Message: "no jailed comments"})

		return
	}

	j, found, err := sm.messages.GetJailed(m.ID)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	if !found {
		send("error", protocol.ErrorMsg{Message: "no jailed comment with id " + m.ID})

		return
	}

	send("msg_jail_show", protocol.MsgJailShowResponse{Jailed: jailedOneToWire(j, auth.mayReadJailBody(sm))})
}

// handleMsgJailRelease releases quarantined untrusted content to a working
// agent, so it is restricted to a human operator or the orchestrator — a plain
// agent session is rejected (issue #1082).
func handleMsgJailRelease(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.MsgJailReleaseMsg](msg, send, "invalid msg_jail_release message")
	if !ok {
		return
	}

	if !auth.authorizeJailRelease(sm, send) {
		return
	}

	var (
		released []JailedComment
		err      error
	)

	switch {
	case m.All:
		if m.Author == "" {
			send("error", protocol.ErrorMsg{Message: "--all requires --author <login>"})

			return
		}

		released, err = sm.ReleaseJailedByAuthor(m.Author)
	case m.ID != "":
		var one JailedComment

		one, err = sm.ReleaseJailed(m.ID)
		if err == nil {
			released = []JailedComment{one}
		}
	default:
		send("error", protocol.ErrorMsg{Message: "specify a jail id, or --all --author <login>"})

		return
	}

	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("msg_jail_release", protocol.MsgJailReleaseResponse{Released: jailedToWire(released, true)})
}
