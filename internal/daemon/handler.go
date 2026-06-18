package daemon

import (
	"cmp"
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
				agentName := c.Agent
				if agentName == "" {
					agentName = sm.Config().DefaultAgent
				}
				sess, err := sm.Create(c.Name, agentName, c.RepoPath, c.Base, c.Prompt, c.Model, c.ParentID, c.NoRepo, c.ShareWorktree, c.AgentHooks, c.InPlace, c.AllowConcurrent, clientRows, clientCols)
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
				sess, err := sm.Fork(f.Name, f.SourceSessionID, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("created", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
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
				sm.SetAttachedClient(a.SessionID, conn,
					func() {
						conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
						data, _ := protocol.EncodeControl("detached", protocol.DetachedMsg{Reason: "replaced"})
						_ = writer.WriteFrame(protocol.ChannelControl, data)
						conn.Close()
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
				if d.Children {
					deleted, err := sm.DeleteWithChildren(d.SessionID, d.ExcludeRoot)
					if err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("deleted", struct {
							SessionID string   `json:"session_id"`
							Deleted   []string `json:"deleted"`
						}{d.SessionID, deleted})
					}
				} else {
					if err := sm.Delete(d.SessionID); err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("deleted", struct {
							SessionID string `json:"session_id"`
						}{d.SessionID})
					}
				}

			case "stop":
				var s protocol.StopMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid stop message"})
					continue
				}
				if s.Children {
					stopped, err := sm.StopWithChildren(s.SessionID, s.ExcludeRoot)
					if err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("stopped", struct {
							SessionID string   `json:"session_id"`
							Stopped   []string `json:"stopped"`
						}{s.SessionID, stopped})
					}
				} else {
					if err := sm.Stop(s.SessionID); err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					} else {
						sendControl("stopped", struct {
							SessionID string `json:"session_id"`
						}{s.SessionID})
					}
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

			case "star":
				var s protocol.StarMsg
				if err := protocol.DecodePayload(msg, &s); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid star message"})
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
				sess, err := sm.Restart(r.SessionID, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("restarted", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
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
				if m.SenderID != "" {
					if sess, ok := sm.Get(m.SenderID); ok {
						m.SenderName = sess.Name
					}
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

				if m.Ack && m.Subscriber != "" && len(msgs) > 0 {
					if m.ThreadID != "" {
						seqs := make([]int64, len(msgs))
						for i, msg := range msgs {
							seqs[i] = msg.Seq
						}
						sm.messages.AckMessages(m.Stream, m.Subscriber, seqs)
					} else {
						sm.messages.Ack(m.Stream, m.Subscriber, msgs[len(msgs)-1].Seq)
					}
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
								if m.ThreadID != "" {
									sm.messages.AckMessages(m.Stream, m.Subscriber, []int64{tmsg.Seq})
								} else {
									sm.messages.Ack(m.Stream, m.Subscriber, tmsg.Seq)
								}
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
				streams, err := sm.messages.ListStreams(m.Subscriber, m.IncludeSystem)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("msg_topics_list", struct {
						Streams []StreamInfo `json:"streams"`
					}{streams})
				}

			case "approval_request":
				var req protocol.ApprovalRequestMsg
				if err := protocol.DecodePayload(msg, &req); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid approval_request"})
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
				pty, ok := sm.GetPTY(t.SessionID)
				if !ok {
					sendControl("error", protocol.ErrorMsg{Message: "session not found"})
					continue
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

			case "status":
				var sr protocol.StatusRequestMsg
				if err := protocol.DecodePayload(msg, &sr); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid status message"})
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
				if sm.mcpManager == nil {
					sendControl("error", protocol.ErrorMsg{Message: "MCP manager not initialized"})
					return
				}

				// Verify the requesting session's agent is allowed to use this server.
				if mc.SessionID != "" {
					if sess, ok := sm.Get(mc.SessionID); ok {
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
				}

				if !sm.mcpManager.HasServer(mc.Server) {
					sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("unknown MCP server %q", mc.Server)})
					return
				}
				proxyID := fmt.Sprintf("%s-%s", mc.SessionID, mc.Server)
				proc, err := sm.mcpManager.Connect(mc.Server, proxyID)
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
					conn.Close()
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

			case "upgrade":
				var u protocol.UpgradeMsg
				_ = protocol.DecodePayload(msg, &u)
				if u.ClientVersion != "" && u.ClientVersion == version.Version {
					log.Info("ignoring same-version upgrade request", "version", u.ClientVersion)
					sendControl("error", protocol.ErrorMsg{Message: "already running " + version.Version})
					return
				}
				select {
				case sm.upgradeCh <- u.ExecPath:
					sendControl("upgrading", struct{}{})
				default:
					sendControl("error", protocol.ErrorMsg{Message: "upgrade already in progress"})
				}
				return
			}

		case protocol.ChannelData:
			if attachedSessionID != "" && sm.IsAttachedClient(attachedSessionID, conn) {
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
		CreatedAt:      s.CreatedAt.Format(time.RFC3339),
		Dirty:          s.GitDirty,
		UnpushedCount:  s.GitUnpushed,
		Sandboxed:      s.Sandboxed,
		SharedWorktree: s.SharedWorktree,
		InPlace:        s.InPlace,
		Model:          cmp.Or(s.HookModel, s.Model),
		ToolName:       s.HookToolName,
		CostUSD:        s.HookCostUSD,
		ContextPercent: s.HookContextPercent,
		ConfigStale:    isConfigStale(s, cfg),
		Starred:        s.Starred,
		SystemKind:     s.SystemKind,
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
