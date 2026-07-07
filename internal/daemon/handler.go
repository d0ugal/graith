package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/version"
)

// sessionLabel returns a human-friendly identifier for a session, preferring
// its name and falling back to its ID.
func sessionLabel(s SessionState) string {
	if s.Name != "" {
		return s.Name
	}

	return s.ID
}

// describeSessionExit returns a concise description of why a session is no
// longer running, for use in log/error messages.
func describeSessionExit(s SessionState) string {
	switch {
	case s.ExitSignal != "":
		return fmt.Sprintf("killed by signal %s", s.ExitSignal)
	case s.ExitCode != nil:
		return fmt.Sprintf("exited with code %d", *s.ExitCode)
	default:
		return fmt.Sprintf("status: %s", s.Status)
	}
}

const (
	typeIdleTimeout = 10 * time.Second
	typeMaxWait     = 2 * time.Minute
)

// HandleConnection processes the frame protocol for a single client connection.
func HandleConnection(ctx context.Context, conn net.Conn, sm *SessionManager, log *slog.Logger) {
	reader := protocol.NewFrameReader(conn)
	writer := &safeFrameWriter{writer: protocol.NewFrameWriter(conn)}

	var (
		attachedSessionID      string
		attachedDataWriter     *frameDataWriter
		clientRows, clientCols uint16 = 24, 80
	)

	sendControl := func(msgType string, payload any) {
		data, err := protocol.EncodeControl(msgType, payload)
		if err != nil {
			log.Error("encode control", "err", err)
			return
		}

		_ = writer.WriteFrame(protocol.ChannelControl, data)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		frame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				log.Debug("client read error", "err", err)
			}

			if attachedSessionID != "" {
				sm.ClearAttachedClient(attachedSessionID, conn)

				if pty, ok := sm.GetPTY(attachedSessionID); ok {
					pty.DetachWriter(attachedDataWriter)
				}
			}

			return
		}

		switch frame.Channel {
		case protocol.ChannelControl:
			msg, err := protocol.DecodeControl(frame.Payload)
			if err != nil {
				sendControl("error", protocol.ErrorMsg{Message: "malformed message"})
				continue
			}

			sm.mu.RLock()
			auth, authErr := resolveAuth(sm, msg.Token)
			sm.mu.RUnlock()

			if authErr != nil {
				sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
				continue
			}

			switch msg.Type {
			case "handshake":
				var h protocol.HandshakeMsg
				if err := protocol.DecodePayload(msg, &h); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid handshake"})
					continue
				}

				if !protocol.VersionCompatible(h.Version) {
					sendControl("handshake_err", protocol.HandshakeErrMsg{
						Reason: fmt.Sprintf("protocol version mismatch: client=%s, server=%s; try upgrading the client and running: gr daemon restart", h.Version, protocol.Version),
					})

					return
				}

				if h.Profile != sm.paths.Profile {
					sendControl("handshake_err", protocol.HandshakeErrMsg{
						Reason: fmt.Sprintf("profile mismatch: client is %q but daemon is %q", h.Profile, sm.paths.Profile),
					})

					return
				}

				clientCols = h.TerminalSize[0]
				clientRows = h.TerminalSize[1]

				sendControl("handshake_ok", protocol.HandshakeOkMsg{Version: protocol.Version, DaemonVersion: version.Version})
				log.Info("client connected", "client_id", h.ClientID, "cwd", h.Cwd)

			case "list":
				sessions := sm.List()
				cfg := sm.Config()

				infos := make([]protocol.SessionInfo, 0, len(sessions))
				for _, s := range sessions {
					infos = append(infos, toSessionInfo(s, cfg, sm.getHookReport(s.ID)))
				}

				sendControl("session_list", protocol.SessionListMsg{Sessions: infos})

			case "create":
				var c protocol.CreateMsg
				if err := protocol.DecodePayload(msg, &c); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid create message"})
					continue
				}

				if auth.authenticated {
					c.ParentID = auth.sessionID
				}

				agentName := c.Agent
				if agentName == "" {
					agentName = sm.Config().DefaultAgent
				}

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: sessions must survive client disconnect (see architecture note), so Create uses its own bounded background timeouts rather than the request ctx.
				sess, err := sm.Create(c.Name, agentName, c.RepoPath, c.Base, c.Prompt, c.Model, c.ParentID, c.NoRepo, c.ShareWorktree, c.AgentHooks, c.InPlace, c.AllowConcurrent, c.SkipModelValidation, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("created", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
				}

			case "fork":
				var f protocol.ForkMsg
				if err := protocol.DecodePayload(msg, &f); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid fork message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, f.SourceSessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: forked sessions must survive client disconnect, so Fork uses its own bounded background timeouts rather than the request ctx.
				sess, err := sm.Fork(f.Name, f.SourceSessionID, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("created", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
				}

			case "migrate":
				var m protocol.MigrateMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid migrate message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, m.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: migration must survive client disconnect, so Migrate uses its own bounded background timeouts rather than the request ctx.
				sess, err := sm.Migrate(m.SessionID, m.Agent, m.Model, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("migrated", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
				}

			case "attach":
				var a protocol.AttachMsg
				if err := protocol.DecodePayload(msg, &a); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid attach message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, a.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if attachedSessionID != "" {
					sm.ClearAttachedClient(attachedSessionID, conn)

					if pty, ok := sm.GetPTY(attachedSessionID); ok {
						pty.DetachWriter(attachedDataWriter)
					}
				}

				ptySess, ok := sm.GetPTY(a.SessionID)
				if !ok {
					sess, exists := sm.Get(a.SessionID)
					if !exists {
						sendControl("error", protocol.ErrorMsg{Message: "session not found"})
						continue
					}

					switch sess.Status {
					case StatusStopped, StatusErrored:
						log.Info("auto-resuming session on attach", "session", a.SessionID, "status", sess.Status)

						//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: resume must survive client disconnect, so Resume uses its own bounded background timeouts rather than the request ctx.
						if _, err := sm.Resume(a.SessionID, clientRows, clientCols); err != nil {
							sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("auto-resume failed: %v", err)})
							continue
						}

						ptySess, ok = sm.GetPTY(a.SessionID)
						if !ok {
							sendControl("error", protocol.ErrorMsg{Message: "session not found after resume"})
							continue
						}
					case StatusCreating:
						sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("session %q is being created", a.SessionID)})
						continue
					case StatusDeleting:
						sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("session %q is being deleted", a.SessionID)})
						continue
					default:
						sendControl("error", protocol.ErrorMsg{Message: "session not found"})
						continue
					}
				}

				attachedSessionID = a.SessionID
				attachedDataWriter = &frameDataWriter{writer: writer}

				sm.KickAttachedClient(a.SessionID)
				sm.SetAttachedClient(a.SessionID, conn,
					func() {
						_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))

						data, _ := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "replaced"})
						_ = writer.WriteFrame(protocol.ChannelControl, data)

						_ = conn.Close()
					},
					sendControl,
				)

				now := time.Now().UTC()

				sm.mu.Lock()
				if s, ok := sm.state.Sessions[a.SessionID]; ok {
					s.LastAttachedAt = &now
				}

				if err := sm.saveState(); err != nil {
					log.Error("failed to save state after attach", "session", a.SessionID, "err", err)
				}
				sm.mu.Unlock()

				_ = ptySess.Resize(clientRows, clientCols)

				sess, _ := sm.Get(a.SessionID)
				sendControl("attached", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))

				if tail, err := ptySess.Scrollback.Tail(300); err == nil && len(tail) > 0 {
					_ = writer.WriteFrame(protocol.ChannelData, tail)
				}

				ptySess.Attach(attachedDataWriter)

			case "detach":
				if attachedSessionID != "" {
					sm.ClearAttachedClient(attachedSessionID, conn)

					if pty, ok := sm.GetPTY(attachedSessionID); ok {
						pty.DetachWriter(attachedDataWriter)
					}

					attachedSessionID = ""
					attachedDataWriter = nil
				}

				sendControl("detached", protocol.DetachedMsg{Reason: "user"})

			case "delete":
				var d protocol.DeleteMsg
				if err := protocol.DecodePayload(msg, &d); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid delete message"})
					continue
				}

				handleSessionLifecycle(sm, auth, sendControl,
					lifecycleRequest{SessionID: d.SessionID, Children: d.Children, ExcludeRoot: d.ExcludeRoot},
					"deleted", "deleted", sm.DeleteWithChildren, sm.Delete)

			case "stop":
				var s protocol.StopMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid stop message"})
					continue
				}

				handleSessionLifecycle(sm, auth, sendControl,
					lifecycleRequest{SessionID: s.SessionID, Children: s.Children, ExcludeRoot: s.ExcludeRoot},
					"stopped", "stopped", sm.StopWithChildren, sm.Stop)

			case "rename":
				var r protocol.RenameMsg
				if err := protocol.DecodePayload(msg, &r); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid rename message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, r.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if err := sm.Rename(r.SessionID, r.NewName); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("renamed", struct {
						SessionID string `json:"session_id"`
						NewName   string `json:"new_name"`
					}{r.SessionID, r.NewName})
				}

			case "update":
				var u protocol.UpdateMsg
				if err := protocol.DecodePayload(msg, &u); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid update message"})
					continue
				}
				// Authorize the update. The caller must have authority over the
				// target session, and — when adopting a new parent — over that
				// parent too. This prevents a session from reparenting an
				// unrelated session under itself to manufacture a descendant
				// relationship and bypass the descendant-based auth model.
				// Clearing the parent ("") removes the session from its
				// ancestors' authority, so it is treated as a privileged
				// reparent: only the orchestrator and the human CLI may orphan a
				// session, otherwise a child could orphan itself to escape its
				// parent's control. The orchestrator and human CLI are exempt
				// from the target/new-parent checks via checkTarget.
				sm.mu.RLock()

				authErr := auth.checkTarget(sm, u.SessionID, authSelfOrDescendant)
				if authErr == nil && u.ParentID != nil {
					if *u.ParentID == "" {
						if auth.authenticated && !auth.isOrchestrator(sm) {
							authErr = fmt.Errorf("not authorized: only the orchestrator may orphan a session")
						}
					} else {
						authErr = auth.checkTarget(sm, *u.ParentID, authSelfOrDescendant)
					}
				}

				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if err := sm.Update(u.SessionID, u.Name, u.ParentID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("updated", struct {
						SessionID string `json:"session_id"`
					}{u.SessionID})
				}

			case "star":
				var s protocol.StarMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid star message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, s.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if err := sm.Star(s.SessionID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("starred", struct {
						SessionID string `json:"session_id"`
					}{s.SessionID})
				}

			case "unstar":
				var u protocol.UnstarMsg
				if err := protocol.DecodePayload(msg, &u); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid unstar message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, u.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if err := sm.Unstar(u.SessionID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("unstarred", struct {
						SessionID string `json:"session_id"`
					}{u.SessionID})
				}

			case "set_status":
				var m protocol.SetStatusMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid set_status message"})
					continue
				}

				if auth.authenticated {
					m.SessionID = auth.sessionID
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, m.SessionID, authSelfOnly)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if m.Clear {
					if err := sm.ClearSummary(m.SessionID); err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("status_set", protocol.StatusSetMsg{SessionID: m.SessionID})
					}
				} else {
					if err := sm.SetSummary(m.SessionID, m.Text, m.TTLSeconds); err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("status_set", protocol.StatusSetMsg{SessionID: m.SessionID})
					}
				}

			case "resume":
				var r protocol.ResumeMsg
				if err := protocol.DecodePayload(msg, &r); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid resume message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, r.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: resume must survive client disconnect, so Resume uses its own bounded background timeouts rather than the request ctx.
				sess, err := sm.Resume(r.SessionID, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("resumed", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
				}

			case "restart":
				var r protocol.RestartMsg
				if err := protocol.DecodePayload(msg, &r); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid restart message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, r.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if r.Children {
					//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: restart must survive client disconnect, so RestartWithChildren uses its own bounded background timeouts rather than the request ctx.
					restarted, err := sm.RestartWithChildren(r.SessionID, r.ExcludeRoot, clientRows, clientCols)
					if err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("restarted", struct {
							SessionID string   `json:"session_id"`
							Restarted []string `json:"restarted"`
						}{r.SessionID, restarted})
					}
				} else {
					//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: restart must survive client disconnect, so Restart uses its own bounded background timeouts rather than the request ctx.
					sess, err := sm.Restart(r.SessionID, clientRows, clientCols)
					if err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("restarted", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
					}
				}

			case "logs":
				var l protocol.LogsMsg
				if err := protocol.DecodePayload(msg, &l); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid logs message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, l.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				lines := l.Lines
				if lines == 0 {
					lines = 300
				}

				ptySess, ok := sm.GetPTY(l.SessionID)
				if !ok {
					// No live PTY. The session may still exist in state (stopped
					// or crashed) with its scrollback log on disk — distinguish
					// "no such session" from "session exists but has no output".
					sess, exists := sm.Get(l.SessionID)
					if !exists {
						sendControl("error", protocol.ErrorMsg{Message: "session not found"})
						continue
					}

					tail, err := grpty.TailFile(sm.scrollbackLogPath(l.SessionID), lines)
					if err == nil && len(tail) > 0 {
						_ = writer.WriteFrame(protocol.ChannelData, tail)

						sendControl("logs_done", struct{}{})

						continue
					}

					sendControl("error", protocol.ErrorMsg{
						Message: fmt.Sprintf("no output captured for session %s (%s)", sessionLabel(sess), describeSessionExit(sess)),
					})

					continue
				}

				if tail, err := ptySess.Scrollback.Tail(lines); err == nil && len(tail) > 0 {
					_ = writer.WriteFrame(protocol.ChannelData, tail)
				}

				if !l.Follow {
					sendControl("logs_done", struct{}{})
					continue
				}

				logsWriter := &frameDataWriter{writer: writer}
				ptySess.Attach(logsWriter)
				sendControl("logs_following", struct{}{})

				for {
					f, err := reader.ReadFrame()
					if err != nil {
						ptySess.DetachWriter(logsWriter)
						return
					}

					if f.Channel == protocol.ChannelControl {
						ctrl, _ := protocol.DecodeControl(f.Payload)
						if ctrl.Type == "detach" {
							ptySess.DetachWriter(logsWriter)
							sendControl("logs_done", struct{}{})

							break
						}
					}
				}

			case "msg_pub":
				var m protocol.MsgPubMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_pub message"})
					continue
				}

				if auth.authenticated {
					m.SenderID = auth.sessionID
					if sess, ok := sm.Get(auth.sessionID); ok {
						m.SenderName = sess.Name
					}

					sm.mu.RLock()
					authErr := auth.checkMsgPub(sm, m.Stream)
					sm.mu.RUnlock()

					if authErr != nil {
						sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
						continue
					}
				} else if m.SenderID != "" {
					if sess, ok := sm.Get(m.SenderID); ok {
						m.SenderName = sess.Name
					}
				}

				published, err := sm.messages.Publish(m.Stream, m.SenderID, m.SenderName, m.Body, m.ThreadID, m.ReplyTo)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("msg_published", published)

					if !m.Quiet {
						if targetID, isInbox := parseInboxStream(m.Stream); isInbox {
							//nolint:contextcheck // launched as a detached goroutine that may auto-resume a stopped session; it must outlive this request, so it deliberately does not inherit the connection ctx.
							go sm.notifyInbox(targetID, m.SenderID, m.SenderName)
						}
					}
				}

			case "msg_inbox":
				var m protocol.MsgInboxMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_inbox message"})
					continue
				}

				if !auth.authenticated {
					sendControl("error", protocol.ErrorMsg{
						Message: "msg_inbox requires an authenticated session — use gr msg sub --topic inbox:<id> for debugging",
					})

					continue
				}

				stream := "inbox:" + auth.sessionID
				if sm.handleMsgStreamRead(ctx, sendControl, reader, stream, auth.sessionID, m.OnlyUnread, m.ThreadID, m.Wait, m.Follow, m.Ack) {
					return
				}

			case "msg_sub":
				var m protocol.MsgSubMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_sub message"})
					continue
				}

				if auth.authenticated {
					m.Subscriber = auth.sessionID
					if _, isInbox := parseInboxStream(m.Stream); isInbox {
						sendControl("error", protocol.ErrorMsg{
							Message: "inbox streams cannot be read via msg_sub; use gr msg inbox instead",
						})

						continue
					}
				}

				if sm.handleMsgStreamRead(ctx, sendControl, reader, m.Stream, m.Subscriber, m.OnlyUnread, m.ThreadID, m.Wait, m.Follow, m.Ack) {
					return
				}

			case "msg_ack":
				var m protocol.MsgAckMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_ack message"})
					continue
				}

				if auth.authenticated {
					m.Subscriber = auth.sessionID
					if _, isInbox := parseInboxStream(m.Stream); isInbox {
						sendControl("error", protocol.ErrorMsg{
							Message: "inbox streams cannot be acked via msg_ack; use gr msg inbox --ack instead",
						})

						continue
					}
				}

				if err := sm.messages.AckLatest(m.Stream, m.Subscriber); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("msg_acked", struct{}{})
				}

			case "msg_topics":
				var m protocol.MsgTopicsMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_topics message"})
					continue
				}

				if auth.authenticated {
					m.Subscriber = auth.sessionID
				}

				streams, err := sm.messages.ListStreams(m.Subscriber, m.IncludeSystem)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					if auth.authenticated {
						filtered := streams[:0]
						for _, s := range streams {
							if _, isInbox := parseInboxStream(s.Name); !isInbox {
								filtered = append(filtered, s)
							}
						}

						streams = filtered
					}

					sendControl("msg_topics_list", struct {
						Streams []StreamInfo `json:"streams"`
					}{streams})
				}

			case "msg_conversation":
				var m protocol.MsgConversationMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_conversation message"})
					continue
				}
				// Authorise the target session. The self-or-descendant rule lets
				// the human CLI (unauthenticated), the session itself, an ancestor
				// agent, or the orchestrator read the conversation. The by-sender
				// filter inside Conversation keeps the cross-inbox scan safe: for
				// the target session, outbound results are limited to messages it
				// authored, plus everything in its own inbox.
				sm.mu.RLock()
				authErr := auth.checkTarget(sm, m.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if sm.messages == nil {
					sendControl("msg_conversation_list", protocol.MsgConversationListMsg{})
					continue
				}
				// Clamp the limit: default unset/non-positive to a sensible page
				// size, and cap large values so a client can't ask the daemon to
				// sort an unbounded result set (local perf/DoS footgun).
				const maxConversationLimit = 2000

				limit := m.Limit
				if limit <= 0 {
					limit = 500
				}

				if limit > maxConversationLimit {
					limit = maxConversationLimit
				}

				convo, err := sm.messages.Conversation(m.SessionID, limit)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
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
					}
				}

				sendControl("msg_conversation_list", protocol.MsgConversationListMsg{Messages: out})

			case "approval_request":
				var req protocol.ApprovalRequestMsg
				if err := protocol.DecodePayload(msg, &req); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid approval_request"})
					continue
				}

				if auth.authenticated {
					req.SessionID = auth.sessionID
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, req.SessionID, authSelfOnly)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				log.Info("approval request received", "request_id", req.RequestID, "session_id", req.SessionID, "tool", req.ToolName)
				// Monitor connection close so we can cancel if the hook dies.
				connCtx, connCancel := context.WithCancel(ctx)

				go func() {
					for {
						f, err := reader.ReadFrame()
						if err != nil {
							connCancel()
							return
						}

						if f.Channel == protocol.ChannelControl {
							ctrl, _ := protocol.DecodeControl(f.Payload)
							if ctrl.Type == "detach" {
								connCancel()
								return
							}
						}
					}
				}()

				decision := sm.SubmitApproval(connCtx, req)
				sendControl("approval_decision", decision)

				return

			case "approval_respond":
				if auth.authenticated {
					sendControl("error", protocol.ErrorMsg{Message: "operation not permitted for agent sessions"})
					continue
				}

				var resp protocol.ApprovalRespondMsg
				if err := protocol.DecodePayload(msg, &resp); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid approval_respond"})
					continue
				}

				if err := sm.RespondToApproval(resp.RequestID, resp.Decision, resp.Reason); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("approval_responded", struct{}{})
				}

			case "approval_list":
				pending := sm.PendingApprovals()
				sendControl("approval_notification", protocol.ApprovalNotificationMsg{
					Pending: pending,
				})

			case "type":
				var t protocol.TypeMsg
				if err := protocol.DecodePayload(msg, &t); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid type message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, t.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				pty, ok := sm.GetPTY(t.SessionID)
				if !ok {
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
				}

				if sm.HasAttachedClient(t.SessionID) {
					if !pty.WaitForUserIdle(typeIdleTimeout, typeMaxWait) {
						log.Warn("gr type: max wait expired, injecting while user may still be active",
							"session", t.SessionID)
					}
				}

				var writeErr error
				if t.NoNewline {
					writeErr = pty.WriteInput([]byte(t.Input))
				} else {
					writeErr = pty.WriteInputAndSubmit([]byte(t.Input))
				}

				if writeErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: "write failed: " + writeErr.Error()})
					continue
				}

				pty.Poke()
				sendControl("typed", struct {
					SessionID string `json:"session_id"`
				}{t.SessionID})

			case "interrupt":
				var in protocol.InterruptMsg
				if err := protocol.DecodePayload(msg, &in); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid interrupt message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, in.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if err := sm.InterruptSession(in.SessionID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				sendControl("interrupted", struct {
					SessionID string `json:"session_id"`
				}{in.SessionID})

			case "resize":
				var r protocol.ResizeMsg
				if err := protocol.DecodePayload(msg, &r); err != nil {
					continue
				}

				if attachedSessionID != "" {
					if pty, ok := sm.GetPTY(attachedSessionID); ok {
						_ = pty.Resize(r.Rows, r.Cols)
					}
				}

			case "screen_preview":
				var sp protocol.ScreenPreviewMsg
				if err := protocol.DecodePayload(msg, &sp); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid screen_preview message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, sp.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				ptySess, ok := sm.GetPTY(sp.SessionID)
				if !ok {
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
				}

				sendControl("screen_preview_response", protocol.ScreenPreviewResponseMsg{
					SessionID: sp.SessionID,
					Preview:   ptySess.ScreenPreview(),
				})

			case "screen_snapshot":
				var ss protocol.ScreenSnapshotMsg
				if err := protocol.DecodePayload(msg, &ss); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid screen_snapshot message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, ss.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				ptySess, ok := sm.GetPTY(ss.SessionID)
				if !ok {
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
				}

				snap := ptySess.ScreenSnapshot()
				sendControl("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
					SessionID:     ss.SessionID,
					Frame:         snap.Frame,
					CursorX:       snap.CursorX,
					CursorY:       snap.CursorY,
					CursorVisible: snap.CursorVisible,
					Cols:          snap.Cols,
					Rows:          snap.Rows,
				})

			case "reload":
				if auth.authenticated {
					sendControl("error", protocol.ErrorMsg{Message: "operation not permitted for agent sessions"})
					continue
				}

				//nolint:contextcheck // applyConfig may spawn a detached orchestrator goroutine (context.Background) that must outlive this reload request, so it deliberately does not inherit the connection ctx.
				if err := sm.ReloadConfig(); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("reloaded", struct{}{})
				}

			case "status":
				var sr protocol.StatusRequestMsg
				if err := protocol.DecodePayload(msg, &sr); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid status message"})
					continue
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, sr.SessionID, authSelfOrDescendant)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				sess, ok := sm.Get(sr.SessionID)
				if !ok {
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
				}

				unread := 0
				if sm.messages != nil {
					unread = sm.messages.TotalUnread(sr.SessionID)
				}

				fleet := sm.fleetSummary()
				sendControl("status_response", protocol.StatusResponseMsg{
					Session:     toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)),
					UnreadCount: unread,
					Fleet:       fleet,
				})

			case "status_report":
				var sr protocol.StatusReportMsg
				if err := protocol.DecodePayload(msg, &sr); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid status_report"})
					continue
				}

				if auth.authenticated {
					sr.SessionID = auth.sessionID
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, sr.SessionID, authSelfOnly)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				sm.HandleHookReport(sr)
				sendControl("status_reported", struct{}{})

			case "diagnostics":
				sendControl("diagnostics", sm.Diagnostics())

			case "mcp_connect":
				var mc protocol.MCPConnectMsg
				if err := protocol.DecodePayload(msg, &mc); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid mcp_connect"})
					continue
				}

				if auth.authenticated {
					mc.SessionID = auth.sessionID
				}

				sm.mu.RLock()
				authErr := auth.checkTarget(sm, mc.SessionID, authSelfOnly)
				sm.mu.RUnlock()

				if authErr != nil {
					sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
					continue
				}

				if sm.mcpManager == nil {
					sendControl("error", protocol.ErrorMsg{Message: "MCP manager not initialized"})
					return
				}

				// Verify the requesting session's agent is allowed to use this
				// server, and gather its template vars for per-session isolation.
				mcpVars := config.TemplateVars{SessionID: mc.SessionID}
				if mc.SessionID != "" {
					sess, ok := sm.Get(mc.SessionID)
					if !ok {
						// A non-empty session ID that doesn't resolve is an
						// error — don't spawn a process with partially populated
						// (empty {session_name}/{worktree_path}) template vars.
						sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("session %q not found", mc.SessionID)})
						return
					}

					mcpVars.SessionName = sess.Name
					mcpVars.WorktreePath = sess.WorktreePath
					allowed := sm.resolveMCPServersFromConfig(sm.Config(), sess.Agent)
					found := false

					for _, s := range allowed {
						if s.Name == mc.Server {
							found = true
							break
						}
					}

					if !found {
						sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("MCP server %q is not enabled for agent %q", mc.Server, sess.Agent)})
						return
					}
				}

				if !sm.mcpManager.HasServer(mc.Server) {
					sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("unknown MCP server %q", mc.Server)})
					return
				}

				proxyID := fmt.Sprintf("%s-%s", mc.SessionID, mc.Server)

				proc, err := sm.mcpManager.Connect(mc.Server, proxyID, mcpVars)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					return
				}

				channelID := protocol.ChannelMCP
				sendControl("mcp_connect_ok", protocol.MCPConnectOkMsg{
					Server:  mc.Server,
					Channel: channelID,
				})

				// Bridge: proxy stdin (from frames) → MCP server stdin,
				//         MCP server stdout → proxy (as frames).
				bridgeDone := make(chan struct{})
				go func() {
					defer close(bridgeDone)

					buf := make([]byte, 32*1024)
					for {
						n, err := proc.stdout.Read(buf)
						if n > 0 {
							if werr := writer.WriteFrame(channelID, buf[:n]); werr != nil {
								return
							}
						}

						if err != nil {
							return
						}
					}
				}()

				// If the MCP server dies, force the connection closed so the
				// read loop unblocks and the proxy can reconnect.
				go func() {
					select {
					case <-bridgeDone:
					case <-proc.done:
					}

					_ = conn.Close()
				}()

				// Read frames from proxy and write to MCP server stdin.
				func() {
					defer sm.mcpManager.Disconnect(proxyID)

					for {
						f, err := reader.ReadFrame()
						if err != nil {
							return
						}

						switch f.Channel {
						case channelID:
							if _, err := proc.stdin.Write(f.Payload); err != nil {
								return
							}
						case protocol.ChannelControl:
							ctrl, _ := protocol.DecodeControl(f.Payload)
							if ctrl.Type == "detach" {
								return
							}
						}
					}
				}()
				<-bridgeDone

				return

			case "scenario_start":
				var s protocol.ScenarioStartMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid scenario_start message"})
					continue
				}

				if !auth.authenticated {
					sendControl("error", protocol.ErrorMsg{Message: "scenario_start requires authentication"})
					continue
				}

				s.CallerSessionID = auth.sessionID

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: scenario sessions must survive client disconnect, so StartScenario uses its own bounded background timeouts rather than the request ctx.
				scenario, err := sm.StartScenario(s, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					record, _ := sm.ScenarioStatus(scenario.Name)
					sendControl("scenario_started", record)
				}

			case "scenario_stop":
				var s protocol.ScenarioStopMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid scenario_stop message"})
					continue
				}

				sm.mu.RLock()

				if err := auth.checkScenarioOp(sm, s.Name); err != nil {
					sm.mu.RUnlock()
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})

					continue
				}

				sm.mu.RUnlock()

				stopped, err := sm.StopScenario(s.Name)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("scenario_stopped", struct {
						Name    string   `json:"name"`
						Stopped []string `json:"stopped"`
					}{s.Name, stopped})
				}

			case "scenario_delete":
				var s protocol.ScenarioDeleteMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid scenario_delete message"})
					continue
				}

				sm.mu.RLock()

				if err := auth.checkScenarioOp(sm, s.Name); err != nil {
					sm.mu.RUnlock()
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})

					continue
				}

				sm.mu.RUnlock()

				deleted, err := sm.DeleteScenario(s.Name)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("scenario_deleted", struct {
						Name    string   `json:"name"`
						Deleted []string `json:"deleted"`
					}{s.Name, deleted})
				}

			case "scenario_status":
				var s protocol.ScenarioStatusMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid scenario_status message"})
					continue
				}

				record, err := sm.ScenarioStatus(s.Name)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("scenario_status", protocol.ScenarioStatusResponse{Scenario: *record})
				}

			case "scenario_resume":
				var s protocol.ScenarioResumeMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid scenario_resume message"})
					continue
				}

				sm.mu.RLock()

				if err := auth.checkScenarioOp(sm, s.Name); err != nil {
					sm.mu.RUnlock()
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})

					continue
				}

				sm.mu.RUnlock()

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: resumed scenario sessions must survive client disconnect, so ResumeScenario uses its own bounded background timeouts rather than the request ctx.
				resumed, err := sm.ResumeScenario(s.Name, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("scenario_resumed", struct {
						Name    string   `json:"name"`
						Resumed []string `json:"resumed"`
					}{s.Name, resumed})
				}

			case "scenario_task_done":
				var s protocol.ScenarioTaskDoneMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid scenario_task_done message"})
					continue
				}

				callerID := ""
				if auth.authenticated {
					callerID = auth.sessionID
				}

				if callerID == "" {
					sendControl("error", protocol.ErrorMsg{Message: "scenario task-done requires an authenticated session"})
					continue
				}

				if err := sm.ScenarioTaskDone(s.Name, callerID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("scenario_task_done", struct {
						Name string `json:"name"`
					}{s.Name})
				}

			case "scenario_add":
				var s protocol.ScenarioAddMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid scenario_add message"})
					continue
				}

				sm.mu.RLock()

				if err := auth.checkScenarioOp(sm, s.Name); err != nil {
					sm.mu.RUnlock()
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})

					continue
				}

				sm.mu.RUnlock()

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: the added session must survive client disconnect, so AddToScenario uses its own bounded background timeouts rather than the request ctx.
				sess, err := sm.AddToScenario(s.Name, s.Session, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("scenario_added", struct {
						Name      string `json:"name"`
						SessionID string `json:"session_id"`
					}{s.Name, sess.ID})
				}

			case "scenario_list":
				records := sm.ListScenarios()
				sendControl("scenario_list", protocol.ScenarioListResponse{Scenarios: records})

			case "upgrade":
				if auth.authenticated {
					sendControl("error", protocol.ErrorMsg{Message: "operation not permitted for agent sessions"})
					continue
				}

				var u protocol.UpgradeMsg

				_ = protocol.DecodePayload(msg, &u)
				select {
				case sm.upgradeCh <- u.ExecPath:
					sendControl("upgrading", struct{}{})
				default:
					sendControl("error", protocol.ErrorMsg{Message: "upgrade already in progress"})
				}

				return

			default:
				sendControl("error", protocol.ErrorMsg{Message: "unsupported control message: " + msg.Type})
			}

		case protocol.ChannelData:
			if attachedSessionID != "" && sm.IsAttachedClient(attachedSessionID, conn) {
				if pty, ok := sm.GetPTY(attachedSessionID); ok {
					pty.NotifyUserInput()
					_ = pty.WriteInput(frame.Payload)
				}
			}
		}
	}
}

// lifecycleRequest holds the fields shared by the stop and delete control
// messages, both of which target a session (optionally with descendants).
type lifecycleRequest struct {
	SessionID   string
	Children    bool
	ExcludeRoot bool
}

// handleSessionLifecycle implements the shared stop/delete dispatch: authorize
// the target, then run either the with-children batch operation or the single
// operation and report the result. event is the success message type
// ("stopped"/"deleted") and resultKey is the JSON field holding the affected
// session names for the with-children response.
func handleSessionLifecycle(
	sm *SessionManager,
	auth authContext,
	sendControl func(string, any),
	req lifecycleRequest,
	event, resultKey string,
	batchFn func(sessionID string, excludeRoot bool) ([]string, error),
	singleFn func(sessionID string) error,
) {
	sm.mu.RLock()
	authErr := auth.checkTarget(sm, req.SessionID, authSelfOrDescendant)
	sm.mu.RUnlock()

	if authErr != nil {
		sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
		return
	}

	if req.Children {
		affected, err := batchFn(req.SessionID, req.ExcludeRoot)
		if err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		} else {
			sendControl(event, map[string]any{
				"session_id": req.SessionID,
				resultKey:    affected,
			})
		}
	} else {
		if err := singleFn(req.SessionID); err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		} else {
			sendControl(event, map[string]any{
				"session_id": req.SessionID,
			})
		}
	}
}

// safeFrameWriter wraps a FrameWriter with a mutex so multiple goroutines
// (handler loop + PTY readLoop) can write frames concurrently.
type safeFrameWriter struct {
	mu     sync.Mutex
	writer *protocol.FrameWriter
}

func (w *safeFrameWriter) WriteFrame(channel byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.writer.WriteFrame(channel, payload)
}

// frameDataWriter adapts a safeFrameWriter into an io.Writer that sends data frames.
type frameDataWriter struct {
	writer *safeFrameWriter
}

func (w *frameDataWriter) Write(p []byte) (int, error) {
	if err := w.writer.WriteFrame(protocol.ChannelData, p); err != nil {
		return 0, err
	}

	return len(p), nil
}

func toSessionInfo(s SessionState, cfg *config.Config, hr *hookReport) protocol.SessionInfo {
	info := protocol.SessionInfo{
		ID:             s.ID,
		ParentID:       s.ParentID,
		Name:           s.Name,
		RepoPath:       s.RepoPath,
		RepoName:       s.RepoName,
		WorktreePath:   s.WorktreePath,
		Branch:         s.Branch,
		BaseBranch:     s.BaseBranch,
		Agent:          s.Agent,
		AgentSessionID: s.AgentSessionID,
		Status:         string(s.Status),
		AgentStatus:    s.AgentStatus,
		ExitCode:       s.ExitCode,
		ExitSignal:     s.ExitSignal,
		CreatedAt:      s.CreatedAt.Format(time.RFC3339),
		Dirty:          s.GitDirty,
		UnpushedCount:  s.GitUnpushed,
		PullRequest:    prInfo(s.PullRequest),
		CI:             ciInfo(s.CI),
		Sandboxed:      s.Sandboxed,
		SharedWorktree: s.SharedWorktree,
		InPlace:        s.InPlace,
		Model:          s.Model,
		ToolName:       s.HookToolName,
		ConfigStale:    isConfigStale(s, cfg),
		Starred:        s.Starred,
		SystemKind:     s.SystemKind,
		ScenarioID:     s.ScenarioID,
		ScenarioName:   s.ScenarioName,
	}
	if s.MigratedFrom != nil {
		info.MigratedFrom = s.MigratedFrom.Agent
	}

	if s.LastAttachedAt != nil {
		info.LastAttachedAt = s.LastAttachedAt.Format(time.RFC3339)
	}

	if !s.StatusChangedAt.IsZero() {
		info.StatusChangedAt = s.StatusChangedAt.Format(time.RFC3339)
	}

	for _, inc := range s.Includes {
		info.Includes = append(info.Includes, protocol.IncludedRepoInfo{
			RepoName:     inc.RepoName,
			WorktreePath: inc.WorktreePath,
			Branch:       inc.Branch,
			BaseBranch:   inc.BaseBranch,
			Dirty:        inc.dirty,
			Unpushed:     inc.unpushed,
		})
	}

	// Summary resolution
	if s.SummaryText != "" && s.SummarySetAt != nil {
		ttl := cfg.Status.TTLDuration()
		if s.SummaryTTL > 0 {
			ttl = time.Duration(s.SummaryTTL) * time.Second
		}

		age := time.Since(*s.SummarySetAt)

		recentOutput := s.LastOutputAt != nil && time.Since(*s.LastOutputAt) < ttl
		active := s.Status == StatusRunning && recentOutput

		switch {
		case age > ttl && active:
			// Expired: agent is active but hasn't updated status — clear it
		case age > ttl:
			info.SummaryText = s.SummaryText
			info.SummaryFaded = true
		default:
			info.SummaryText = s.SummaryText
		}
	}

	// Fallback to hook-derived status when explicit is absent/expired
	if info.SummaryText == "" && hr != nil && hr.ToolName != "" && time.Now().Before(hr.AuthoritativeUntil) {
		info.SummaryText = "Using " + hr.ToolName
	}

	// LastOutputAt — use runtime value, fall back to StatusChangedAt
	if s.LastOutputAt != nil {
		info.LastOutputAt = s.LastOutputAt.Format(time.RFC3339)
	} else if !s.StatusChangedAt.IsZero() {
		info.LastOutputAt = s.StatusChangedAt.Format(time.RFC3339)
	}

	return info
}

func isConfigStale(s SessionState, cfg *config.Config) bool {
	if s.CreationCfg == nil {
		return false
	}

	agent, ok := cfg.Agents[s.Agent]
	if !ok {
		return true
	}

	if !reflect.DeepEqual(s.CreationCfg.Agent, agent) {
		return true
	}

	currentSandbox := cfg.Sandbox.Merge(agent.Sandbox)
	if s.SystemKind == SystemKindOrchestrator {
		currentSandbox = cfg.OrchestratorSandboxMerged(s.Agent)
	}

	return !reflect.DeepEqual(s.CreationCfg.SandboxConfig, currentSandbox)
}
