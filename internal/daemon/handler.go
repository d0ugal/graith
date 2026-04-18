package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
)

// HandleConnection processes the frame protocol for a single client connection.
func HandleConnection(ctx context.Context, conn net.Conn, sm *SessionManager, log *slog.Logger) {
	reader := protocol.NewFrameReader(conn)
	writer := &safeFrameWriter{writer: protocol.NewFrameWriter(conn)}

	var attachedSessionID string
	var attachedDataWriter *frameDataWriter
	var clientRows, clientCols uint16 = 24, 80

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

			switch msg.Type {
			case "handshake":
				var h protocol.HandshakeMsg
				if err := protocol.DecodePayload(msg, &h); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid handshake"})
					continue
				}
				clientCols = h.TerminalSize[0]
				clientRows = h.TerminalSize[1]
				sendControl("handshake_ok", protocol.HandshakeOkMsg{Version: protocol.Version, DaemonVersion: version.Version})
				log.Info("client connected", "client_id", h.ClientID, "cwd", h.Cwd)

			case "list":
				sessions := sm.List()
				infos := make([]protocol.SessionInfo, 0, len(sessions))
				for _, s := range sessions {
					infos = append(infos, toSessionInfo(s))
				}
				sendControl("session_list", protocol.SessionListMsg{Sessions: infos})

			case "create":
				var c protocol.CreateMsg
				if err := protocol.DecodePayload(msg, &c); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid create message"})
					continue
				}
				agentName := c.Agent
				if agentName == "" {
					agentName = sm.cfg.DefaultAgent
				}
				sess, err := sm.Create(c.Name, agentName, c.RepoPath, c.Base, c.Prompt, c.NoRepo, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("created", toSessionInfo(sess))
				}

			case "attach":
				var a protocol.AttachMsg
				if err := protocol.DecodePayload(msg, &a); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid attach message"})
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
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
				}

				attachedSessionID = a.SessionID
				attachedDataWriter = &frameDataWriter{writer: writer}

				sm.KickAttachedClient(a.SessionID)
				sm.SetAttachedClient(a.SessionID, conn, func() {
					data, _ := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "replaced"})
					_ = writer.WriteFrame(protocol.ChannelControl, data)
				})

				now := time.Now().UTC()
				sm.mu.Lock()
				if s, ok := sm.state.Sessions[a.SessionID]; ok {
					s.LastAttachedAt = &now
				}
				sm.mu.Unlock()

				_ = ptySess.Resize(clientRows, clientCols)

				sess, _ := sm.Get(a.SessionID)
				sendControl("attached", toSessionInfo(sess))

				ptySess.Attach(attachedDataWriter)

				if tail, err := ptySess.Scrollback.Tail(300); err == nil && len(tail) > 0 {
					_ = writer.WriteFrame(protocol.ChannelData, tail)
				}

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
				if err := sm.Delete(d.SessionID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("deleted", struct {
						SessionID string `json:"session_id"`
					}{d.SessionID})
				}

			case "stop":
				var s protocol.StopMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid stop message"})
					continue
				}
				if err := sm.Stop(s.SessionID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("stopped", struct {
						SessionID string `json:"session_id"`
					}{s.SessionID})
				}

			case "rename":
				var r protocol.RenameMsg
				if err := protocol.DecodePayload(msg, &r); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid rename message"})
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

			case "resume":
				var r protocol.ResumeMsg
				if err := protocol.DecodePayload(msg, &r); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid resume message"})
					continue
				}
				sess, err := sm.Resume(r.SessionID, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("resumed", toSessionInfo(sess))
				}

			case "logs":
				var l protocol.LogsMsg
				if err := protocol.DecodePayload(msg, &l); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid logs message"})
					continue
				}
				ptySess, ok := sm.GetPTY(l.SessionID)
				if !ok {
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
				}
				lines := l.Lines
				if lines == 0 {
					lines = 300
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
				published, err := sm.messages.Publish(m.Stream, m.SenderID, m.SenderName, m.Body, m.ThreadID, m.ReplyTo)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("msg_published", published)
				}

			case "msg_sub":
				var m protocol.MsgSubMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_sub message"})
					continue
				}

				// Subscribe before reading to avoid missing messages published
				// between Read and Subscribe.
				var sub chan Message
				var unsub func()
				if m.Wait || m.Follow {
					sub, unsub = sm.messages.Subscribe(m.Stream)
				}

				msgs, err := sm.messages.Read(m.Stream, m.Subscriber, m.OnlyUnread, m.ThreadID)
				if err != nil {
					if unsub != nil {
						unsub()
					}
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}
				for _, msg := range msgs {
					sendControl("msg_message", msg)
				}

				var lastDeliveredSeq int64
				if len(msgs) > 0 {
					lastDeliveredSeq = msgs[len(msgs)-1].Seq
				}
				if m.Ack && m.Subscriber != "" && lastDeliveredSeq > 0 {
					sm.messages.Ack(m.Stream, m.Subscriber, lastDeliveredSeq)
				}

				if !m.Wait && !m.Follow {
					if unsub != nil {
						unsub()
					}
					sendControl("msg_done", struct{}{})
					continue
				}
				if m.Wait && len(msgs) > 0 {
					unsub()
					sendControl("msg_done", struct{}{})
					continue
				}

				sendControl("msg_following", struct{}{})
				detachCh := make(chan struct{})
				go func() {
					for {
						f, err := reader.ReadFrame()
						if err != nil {
							close(detachCh)
							return
						}
						if f.Channel == protocol.ChannelControl {
							ctrl, _ := protocol.DecodeControl(f.Payload)
							if ctrl.Type == "detach" {
								close(detachCh)
								return
							}
						}
					}
				}()
				func() {
					defer unsub()
					for {
						select {
						case tmsg := <-sub:
							if m.ThreadID != "" && tmsg.ThreadID != m.ThreadID {
								continue
							}
							sendControl("msg_message", tmsg)
							if m.Ack && m.Subscriber != "" {
								sm.messages.Ack(m.Stream, m.Subscriber, tmsg.Seq)
							}
							if m.Wait {
								sendControl("msg_done", struct{}{})
								return
							}
						case <-detachCh:
							sendControl("msg_done", struct{}{})
							return
						case <-ctx.Done():
							return
						}
					}
				}()
				// The goroutine above owns the reader — returning prevents
				// the outer loop from racing on ReadFrame.
				return

			case "msg_ack":
				var m protocol.MsgAckMsg
				if err := protocol.DecodePayload(msg, &m); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid msg_ack message"})
					continue
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
				streams, err := sm.messages.ListStreams(m.Subscriber)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("msg_topics_list", struct {
						Streams []StreamInfo `json:"streams"`
					}{streams})
				}

			case "type":
				var t protocol.TypeMsg
				if err := protocol.DecodePayload(msg, &t); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid type message"})
					continue
				}
				pty, ok := sm.GetPTY(t.SessionID)
				if !ok {
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
				}
				if err := pty.WriteInput([]byte(t.Input)); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "write failed: " + err.Error()})
					continue
				}
				if !t.NoNewline {
					_ = pty.WriteInput([]byte("\r"))
				}
				sendControl("typed", struct {
					SessionID string `json:"session_id"`
				}{t.SessionID})

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
				if err := sm.ReloadConfig(); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("reloaded", struct{}{})
				}

			case "upgrade":
				select {
				case sm.upgradeCh <- struct{}{}:
					sendControl("upgrading", struct{}{})
				default:
					sendControl("error", protocol.ErrorMsg{Message: "upgrade already in progress"})
				}
				return
			}

		case protocol.ChannelData:
			if attachedSessionID != "" {
				if pty, ok := sm.GetPTY(attachedSessionID); ok {
					_ = pty.WriteInput(frame.Payload)
				}
			}
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

func toSessionInfo(s SessionState) protocol.SessionInfo {
	info := protocol.SessionInfo{
		ID:             s.ID,
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
		CreatedAt:      s.CreatedAt.Format(time.RFC3339),
		Dirty:          s.GitDirty,
		UnpushedCount:  s.GitUnpushed,
	}
	if s.LastAttachedAt != nil {
		info.LastAttachedAt = s.LastAttachedAt.Format(time.RFC3339)
	}
	return info
}
