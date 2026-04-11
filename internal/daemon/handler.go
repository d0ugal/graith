package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/dougalmatthews/graith/internal/version"
)

const protocolVersion = "1.0"

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
				sendControl("handshake_ok", protocol.HandshakeOkMsg{Version: protocolVersion, DaemonVersion: version.Version})
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
				sess, err := sm.Create(c.Name, agentName, c.RepoPath, c.Base, c.Prompt, clientRows, clientCols)
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

				sess, _ := sm.Get(a.SessionID)
				sendControl("attached", toSessionInfo(sess))

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
				if err := sm.Delete(d.SessionID); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("deleted", struct {
						SessionID string `json:"session_id"`
					}{d.SessionID})
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
	return protocol.SessionInfo{
		ID:             s.ID,
		Name:           s.Name,
		RepoPath:       s.RepoPath,
		RepoName:       s.RepoName,
		WorktreePath:   s.WorktreePath,
		Branch:         s.Branch,
		Agent:          s.Agent,
		AgentSessionID: s.AgentSessionID,
		Status:         string(s.Status),
		ExitCode:       s.ExitCode,
		CreatedAt:      s.CreatedAt.Format(time.RFC3339),
	}
}
