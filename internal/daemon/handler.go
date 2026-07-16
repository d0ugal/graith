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
		return "killed by signal " + s.ExitSignal
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
// origin describes where the connection came from (local Unix socket vs remote
// tailnet listener) and carries the tailnet identity for remote connections;
// it is threaded to resolveAuth so authorization is origin-aware. The zero
// ConnOrigin{} is a local connection, preserving the existing trust model.
//
// The control-message dispatch below is a thin router: most cases delegate to a
// handle* function grouped by concern in handler_lifecycle.go /
// handler_messaging.go / handler_scenario.go / handler_trigger.go /
// handler_todo.go / handler_query.go. Cases that mutate connection-local state
// (attach/detach/resize/handshake), block the read loop, or return from the
// loop (logs --follow, wait, msg_inbox/sub, approval_request, mcp_connect,
// upgrade, pair_request) stay inline here because they are coupled to this
// connection's lifecycle.
func HandleConnection(ctx context.Context, conn net.Conn, origin ConnOrigin, sm *SessionManager, log *slog.Logger) {
	reader := protocol.NewFrameReader(conn)
	writer := &safeFrameWriter{writer: protocol.NewFrameWriter(conn)}

	var (
		attachedSessionID  string
		attachedDataWriter *frameDataWriter
		// attachedReadOnly drops this connection's input frames when the current
		// attach was requested read-only (issue #31). Set on attach, cleared on
		// detach; it is the server-side backstop to the client's input gate.
		attachedReadOnly bool
		// The default geometry used until the client reports its own (or for a
		// session created over a connection that never sends geometry) comes from
		// the [lifecycle] policy.
		clientRows = sm.Config().Lifecycle.DefaultRowsOrDefault()
		clientCols = sm.Config().Lifecycle.DefaultColsOrDefault()
		// poppedDeviceID is the device ID proven via proof-of-possession on this
		// connection (set once a valid auth_proof is received); empty means the
		// remote caller has not completed PoP and stays roleNone.
		poppedDeviceID string
		// challengeNonce is the per-connection PoP nonce issued after a remote
		// handshake; the client must sign it in auth_proof. Single-use.
		challengeNonce string
		// attachedAuth records the role that authorized the current attach. Data
		// frames carry no token, so the live remote-policy backstop uses this to
		// re-check write authority after a pairing-policy reload.
		attachedAuth authContext
	)

	// connDone is closed when this connection's handler returns (for any
	// reason). Background helpers spawned for this connection (e.g. a blocked
	// pair_request) select on it to stop when the client disconnects.
	connDone := make(chan struct{})

	// Centralized cleanup: runs exactly once on every return path (read error,
	// or an early return from logs --follow / wait / msg_sub / approval_request
	// / upgrade), so per-connection registrations never leak.
	defer func() {
		close(connDone)

		if attachedSessionID != "" {
			sm.ClearAttachedClient(attachedSessionID, conn)

			if pty, ok := sm.GetPTY(attachedSessionID); ok {
				pty.DetachWriter(attachedDataWriter)
			}
		}

		if poppedDeviceID != "" {
			sm.UnregisterDeviceConn(poppedDeviceID, conn)
		}

		sm.RemoveApprovalSubscriber(conn)
	}()

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

			return // deferred cleanup handles attach/device/subscriber teardown
		}

		// Gate 1 is live policy, not a listener-generation snapshot. Check every
		// inbound frame (control and raw terminal data) so disabling remote access
		// or removing this WhoIs identity revokes an already-open connection at
		// its next message even if generation shutdown is racing the read loop.
		if origin.Remote && !sm.remotePolicyAllows(origin.Identity) {
			sendControl("error", protocol.ErrorMsg{Message: "remote access disabled or tailnet identity no longer allowed"})
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
			auth, authErr := resolveAuth(sm, msg.Token, origin, poppedDeviceID)
			sm.mu.RUnlock()

			// The handshake is protocol negotiation and confers no authority —
			// every privileged message re-runs resolveAuth on its own token in
			// the cases below, so a client that authenticates here still cannot
			// act without a valid credential. It must therefore always receive a
			// handshake_ok/handshake_err, never the generic auth "error" frame.
			// Gating it broke the tokenless liveness probe in EnsureDaemon
			// (daemonResponds), which read the "error" reply as "not a graith
			// daemon" and triggered a doomed autostart — wedging every CLI
			// command except `gr doctor` once a human token was provisioned.
			if authErr != nil && msg.Type != "handshake" {
				sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
				continue
			}

			// Gate A (design §B.4): for remote (tailnet) connections, reject any
			// message the caller's role may not use before it reaches dispatch.
			// A roleNone remote may reach only the pairing lane; a read-only
			// guest only observational messages; local connections are never
			// gated here. This is the choke point that keeps the network surface
			// fail-closed independent of the per-case handler checks below.
			if origin.Remote && !remoteAllowed(auth.role, msg.Type) {
				sendControl("error", protocol.ErrorMsg{Message: "not authorized over remote connection"})
				continue
			}

			switch msg.Type {
			case "handshake":
				// NOTE: the client's liveness probe (client.daemonResponds) treats a
				// handshake reply as proof a graith daemon is present, but only for an
				// explicit allowlist of first-frame reply types: handshake_ok,
				// handshake_err, and the generic auth "error". If this case ever grows
				// a new first-frame reply type, add it to that allowlist too, or the
				// probe will misread a live daemon as dead and trigger a doomed
				// autostart (the v0.67.1 regression fixed in PR #1066).
				h, ok := decodePayload[protocol.HandshakeMsg](msg, sendControl, "invalid handshake")
				if !ok {
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

				// On a remote connection, issue a proof-of-possession challenge.
				// The client must return a valid auth_proof (signing this nonce
				// with its paired device key) before it advances past roleNone.
				if origin.Remote {
					nonce, nErr := randomHex(32)
					if nErr != nil {
						sendControl("error", protocol.ErrorMsg{Message: "failed to issue auth challenge"})
						return
					}

					challengeNonce = nonce
					sendControl("auth_challenge", protocol.AuthChallengeMsg{Nonce: nonce})
				}

			case "auth_proof":
				// Proof-of-possession: verify the client signed the issued nonce
				// bound to this daemon's TLS SPKI pin (issue #886), with the
				// private key of the device its token resolves to, and that the
				// connection's tailnet identity still matches. Binding to the
				// server cert defeats a MITM relaying the challenge/response. On
				// success the connection advances to its paired role.
				ap, ok := decodePayload[protocol.AuthProofMsg](msg, sendControl, "invalid auth_proof")
				if !ok {
					continue
				}

				sm.mu.RLock()
				dev := sm.DeviceForToken(msg.Token)
				remoteTLSPin := sm.remoteTLSPin
				sm.mu.RUnlock()

				if dev == nil || challengeNonce == "" || !verifyPoP(dev.PubKey, challengeNonce, remoteTLSPin, ap.Signature) ||
					(origin.Remote && !identityMatchesDevice(origin.Identity, dev)) {
					sendControl("error", protocol.ErrorMsg{Message: "proof of possession failed"})
					continue
				}

				poppedDeviceID = dev.ID
				challengeNonce = "" // single-use

				sm.RegisterDeviceConn(dev.ID, conn)
				sendControl("auth_ok", protocol.HandshakeOkMsg{Version: protocol.Version, DaemonVersion: version.Version})

			case "pair_request":
				// A device requests pairing. Queue it for local human approval;
				// a background waiter delivers the minted token when approved.
				// The read loop is NOT blocked — so a client disconnect is
				// noticed promptly (connDone) and the waiter is cleaned up.
				pr, ok := decodePayload[protocol.PairRequestMsg](msg, sendControl, "invalid pair_request")
				if !ok {
					continue
				}

				var identity TailnetIdentity
				if origin.Identity != nil {
					identity = *origin.Identity
				}

				rid, waiter, err := sm.AddPendingPairing(pr.DeviceLabel, pr.DevicePubKey, identity, time.Now(), connDone)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				log.Info("pairing requested", "request_id", rid, "label", pr.DeviceLabel, "tailnet_user", identity.User)

				go func() {
					timer := time.NewTimer(time.Until(waiter.expiresAt))
					defer timer.Stop()

					select {
					case appr := <-waiter.approval:
						sendControl("pair_response", protocol.PairResponseMsg{
							DeviceID:      appr.DeviceID,
							ClientToken:   appr.Token,
							DaemonProfile: appr.Profile,
							TLSPinSPKI:    appr.TLSPin,
						})
					case <-timer.C:
						sm.unregisterPairWaiter(rid)
						sendControl("error", protocol.ErrorMsg{Message: "pairing request timed out"})
					case <-connDone:
						sm.unregisterPairWaiter(rid)
					}
				}()

			case "pair_approve":
				handlePairApprove(sm, auth, sendControl, msg, log)

			case "pair_list":
				handlePairList(sm, auth, sendControl)

			case "pair_revoke":
				handlePairRevoke(sm, auth, sendControl, msg, log)

			case "approval_subscribe":
				handleApprovalSubscribe(sm, auth, sendControl, conn)

			case "repo_list":
				handleRepoList(sm, sendControl)

			case "store_list":
				handleStoreList(sm, auth, sendControl, msg)

			case "store_get":
				handleStoreGet(sm, auth, sendControl, msg)

			case "list":
				handleList(sm, sendControl, msg)

			case "create":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleCreate(sm, auth, sendControl, msg, clientRows, clientCols)

			case "fork":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleFork(sm, auth, sendControl, msg, clientRows, clientCols)

			case "migrate":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleMigrate(sm, auth, sendControl, msg, clientRows, clientCols)

			case "attach":
				a, ok := decodePayload[protocol.AttachMsg](msg, sendControl, "invalid attach message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, a.SessionID, authSelfOrDescendant, sendControl) {
					continue
				}

				if attachedSessionID != "" {
					sm.ClearAttachedClient(attachedSessionID, conn)

					if pty, ok := sm.GetPTY(attachedSessionID); ok {
						pty.DetachWriter(attachedDataWriter)
					}
				}

				// Headless sessions have no interactive TUI to stream. Attaching
				// converts them to interactive (stop → `claude --resume` in a PTY),
				// which restarts the agent, so the daemon asks the client to confirm
				// first via convert_required; the client answers with attach_convert
				// (issue #1137). Read-only inspection stays available via `gr logs -f`.
				if sess, exists := sm.Get(a.SessionID); exists && sess.DriverKind == DriverHeadless {
					sendControl("convert_required", protocol.ConvertRequiredMsg{SessionID: sess.ID, Name: sess.Name})
					continue
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
				attachedReadOnly = a.ReadOnly
				attachedAuth = auth

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

				if tail, err := ptySess.ScrollbackFile().Tail(sm.Config().Limits.LogLinesOrDefault()); err == nil && len(tail) > 0 {
					_ = writer.WriteFrame(protocol.ChannelData, tail)
				}

				ptySess.Attach(attachedDataWriter)

			case "attach_convert":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleAttachConvert(sm, auth, sendControl, msg, clientRows, clientCols)

			case "detach":
				if attachedSessionID != "" {
					sm.ClearAttachedClient(attachedSessionID, conn)

					if pty, ok := sm.GetPTY(attachedSessionID); ok {
						pty.DetachWriter(attachedDataWriter)
					}

					attachedSessionID = ""
					attachedDataWriter = nil
					attachedReadOnly = false
					attachedAuth = authContext{}
				}

				sendControl("detached", protocol.DetachedMsg{Reason: "user"})

			case "delete":
				d, ok := decodePayload[protocol.DeleteMsg](msg, sendControl, "invalid delete message")
				if !ok {
					continue
				}

				handleDelete(ctx, sm, auth, sendControl, d)

			case "restore":
				var r protocol.RestoreMsg
				if err := protocol.DecodePayload(msg, &r); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid restore message"})
					continue
				}

				handleRestore(sm, auth, sendControl, r)

			case "stop":
				s, ok := decodePayload[protocol.StopMsg](msg, sendControl, "invalid stop message")
				if !ok {
					continue
				}

				sm.log.Debug("control request",
					"op", "stop", "caller", auth.describe(),
					"target", s.SessionID, "children", s.Children)

				handleSessionLifecycle(sm, auth, sendControl,
					lifecycleRequest{SessionID: s.SessionID, Children: s.Children, ExcludeRoot: s.ExcludeRoot},
					"stopped", "stopped", sm.StopWithChildren, sm.Stop)

			case "rename":
				handleRename(sm, auth, sendControl, msg)

			case "update":
				handleUpdate(sm, auth, sendControl, msg)

			case "star":
				handleStar(sm, auth, sendControl, msg)

			case "unstar":
				handleUnstar(sm, auth, sendControl, msg)

			case "set_status":
				handleSetStatus(sm, auth, sendControl, msg)

			case "resume":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleResume(sm, auth, sendControl, msg, clientRows, clientCols)

			case "restart":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleRestart(sm, auth, sendControl, msg, clientRows, clientCols)

			case "logs":
				l, ok := decodePayload[protocol.LogsMsg](msg, sendControl, "invalid logs message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, l.SessionID, authSelfOrDescendant, sendControl) {
					continue
				}

				lines := l.Lines
				if lines <= 0 {
					lines = sm.Config().Limits.LogLinesOrDefault()
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

				if tail, err := ptySess.ScrollbackFile().Tail(lines); err == nil && len(tail) > 0 {
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

			case "wait":
				w, ok := decodePayload[protocol.WaitMsg](msg, sendControl, "invalid wait message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, w.SessionID, authSelfOrDescendant, sendControl) {
					continue
				}

				if sm.handleWait(ctx, sendControl, reader, w) {
					return
				}

			case "msg_pub":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleMsgPub(sm, auth, sendControl, msg)

			case "msg_inbox":
				m, ok := decodePayload[protocol.MsgInboxMsg](msg, sendControl, "invalid msg_inbox message")
				if !ok {
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
				m, ok := decodePayload[protocol.MsgSubMsg](msg, sendControl, "invalid msg_sub message")
				if !ok {
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
				handleMsgAck(sm, auth, sendControl, msg)

			case "msg_topics":
				handleMsgTopics(sm, auth, sendControl, msg)

			case "msg_conversation":
				handleMsgConversation(sm, auth, sendControl, msg)

			case "msg_jail_list":
				handleMsgJailList(sm, sendControl, msg)

			case "msg_jail_show":
				handleMsgJailShow(sm, auth, sendControl, msg)

			case "msg_jail_release":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleMsgJailRelease(sm, auth, sendControl, msg)

			case "approval_request":
				req, ok := decodePayload[protocol.ApprovalRequestMsg](msg, sendControl, "invalid approval_request")
				if !ok {
					continue
				}

				if auth.authenticated {
					req.SessionID = auth.sessionID
				}

				if !auth.authorizeTarget(sm, req.SessionID, authSelfOnly, sendControl) {
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
				handleApprovalRespond(sm, auth, sendControl, msg)

			case "approval_list":
				handleApprovalList(sm, sendControl)

			case "type":
				handleType(sm, auth, sendControl, msg, log)

			case "interrupt":
				handleInterrupt(sm, auth, sendControl, msg)

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
				handleScreenPreview(sm, auth, sendControl, msg)

			case "screen_snapshot":
				handleScreenSnapshot(sm, auth, sendControl, msg)

			case "reload":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleReload(sm, auth, sendControl)

			case "status":
				handleStatus(sm, auth, sendControl, msg)

			case "status_report":
				handleStatusReport(sm, auth, sendControl, msg)

			case "diagnostics":
				handleDiagnostics(sm, sendControl)

			case "config":
				handleConfig(sm, sendControl)

			case "agent_catalog":
				handleAgentCatalog(sm, sendControl)

			case "gc":
				handleGC(sm, sendControl, msg)

			case "mcp_connect":
				mc, ok := decodePayload[protocol.MCPConnectMsg](msg, sendControl, "invalid mcp_connect")
				if !ok {
					continue
				}

				if auth.authenticated {
					mc.SessionID = auth.sessionID
				}

				if !auth.authorizeTarget(sm, mc.SessionID, authSelfOnly, sendControl) {
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
					defer sm.mcpManager.Disconnect(proxyID, proc)

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

			case "mcp_list":
				handleMCPList(sm, sendControl)

			case "mcp_restart":
				handleMCPRestart(sm, auth, sendControl, msg)

			case "mcp_logs":
				handleMCPLogs(sm, sendControl, msg)

			case "scenario_start":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleScenarioStart(sm, auth, sendControl, msg, clientRows, clientCols)

			case "scenario_stop":
				handleScenarioStop(sm, auth, sendControl, msg)

			case "scenario_delete":
				handleScenarioDelete(ctx, sm, auth, sendControl, msg)

			case "scenario_status":
				handleScenarioStatus(sm, sendControl, msg)

			case "trigger_list":
				handleTriggerList(sm, sendControl)

			case "trigger_status":
				handleTriggerStatus(sm, sendControl, msg)

			case "trigger_run":
				handleTriggerRun(ctx, sm, auth, sendControl, msg)

			case "trigger_pause":
				handleTriggerPause(sm, auth, sendControl, msg)

			case "notify":
				handleNotify(sm, auth, sendControl, msg)

			case "scenario_resume":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleScenarioResume(sm, auth, sendControl, msg, clientRows, clientCols)

			case "todo_add":
				handleTodoAdd(sm, auth, sendControl, msg)

			case "todo_list":
				handleTodoList(sm, auth, sendControl, msg)

			case "todo_claim":
				handleTodoClaim(sm, auth, sendControl, msg)

			case "todo_transition":
				handleTodoTransition(sm, auth, sendControl, msg)

			case "todo_update":
				handleTodoUpdate(sm, auth, sendControl, msg)

			case "todo_assign":
				handleTodoAssign(sm, auth, sendControl, msg)

			case "todo_remove":
				handleTodoRemove(sm, auth, sendControl, msg)

			case "todo_export":
				handleTodoExport(sm, auth, sendControl, msg)

			case "scenario_add":
				//nolint:contextcheck // session-lifecycle work is intentionally detached from the client connection: it uses its own bounded background timeouts so it survives client disconnect, not the request ctx.
				handleScenarioAdd(sm, auth, sendControl, msg, clientRows, clientCols)

			case "scenario_list":
				handleScenarioList(sm, sendControl)

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
			if origin.Remote && !sm.remoteInputAllowed(attachedAuth) {
				sendControl("error", protocol.ErrorMsg{Message: "remote connection no longer authorized for terminal input"})
				return
			}

			// Read-only attaches never inject input — drop the frame outright so a
			// read-only observer cannot mutate the session even if a client fails to
			// gate input locally (issue #31).
			if attachedReadOnly {
				continue
			}

			if attachedSessionID != "" && sm.IsAttachedClient(attachedSessionID, conn) {
				if pty, ok := sm.GetPTY(attachedSessionID); ok {
					pty.NotifyUserInput()
					_ = pty.WriteInput(frame.Payload)
				}
			}
		}
	}
}

// decodePayload decodes the control message payload into a value of type T. On
// failure it sends an "error" control message with errMsg and returns ok=false,
// so callers can uniformly `if !ok { continue }`.
func decodePayload[T any](msg protocol.Envelope, send func(string, any), errMsg string) (T, bool) {
	var v T
	if err := protocol.DecodePayload(msg, &v); err != nil {
		send("error", protocol.ErrorMsg{Message: errMsg})

		return v, false
	}

	return v, true
}

// authorizeTarget checks whether the caller may act on target session id under
// the given rule, taking sm.mu around the check. On failure it sends an "error"
// control message and returns false, so callers can uniformly
// `if !auth.authorizeTarget(...) { continue }`.
func (ac authContext) authorizeTarget(sm *SessionManager, id string, rule authRule, send func(string, any)) bool {
	sm.mu.RLock()
	err := ac.checkTarget(sm, id, rule)
	sm.mu.RUnlock()

	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return false
	}

	return true
}

// authorizeScenarioOp checks whether the caller may operate on the named
// scenario, taking sm.mu around the check. On failure it sends an "error"
// control message and returns false, so callers can uniformly
// `if !auth.authorizeScenarioOp(...) { continue }`.
func (ac authContext) authorizeScenarioOp(sm *SessionManager, name string, send func(string, any)) bool {
	sm.mu.RLock()
	err := ac.checkScenarioOp(sm, name)
	sm.mu.RUnlock()

	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return false
	}

	return true
}

func (ac authContext) authorizeTriggerOp(sm *SessionManager, send func(string, any)) bool {
	sm.mu.RLock()
	err := ac.checkTriggerOp(sm)
	sm.mu.RUnlock()

	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return false
	}

	return true
}

func (ac authContext) authorizeNotify(sm *SessionManager, send func(string, any)) bool {
	sm.mu.RLock()
	err := ac.checkNotifyOp(sm)
	sm.mu.RUnlock()

	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return false
	}

	return true
}

func (ac authContext) authorizeJailRelease(sm *SessionManager, send func(string, any)) bool {
	sm.mu.RLock()
	err := ac.checkJailRelease(sm)
	sm.mu.RUnlock()

	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return false
	}

	return true
}

// mayReadJailBody reports whether the caller may see a jailed comment's raw
// body — the same release-authorized set (human/orchestrator). An ordinary
// agent or a read-only guest gets metadata with the body withheld.
func (ac authContext) mayReadJailBody(sm *SessionManager) bool {
	sm.mu.RLock()
	err := ac.checkJailRelease(sm)
	sm.mu.RUnlock()

	return err == nil
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
		ID:              s.ID,
		ParentID:        s.ParentID,
		Name:            s.Name,
		RepoPath:        s.RepoPath,
		RepoName:        s.RepoName,
		WorktreePath:    s.WorktreePath,
		Branch:          s.Branch,
		BaseBranch:      s.BaseBranch,
		Agent:           s.Agent,
		AgentSessionID:  s.AgentSessionID,
		Status:          string(s.Status),
		AgentStatus:     s.AgentStatus,
		ExitCode:        s.ExitCode,
		ExitSignal:      s.ExitSignal,
		CreatedAt:       s.CreatedAt.Format(time.RFC3339),
		Dirty:           s.GitDirty,
		UnpushedCount:   s.GitUnpushed,
		PullRequest:     prInfo(s.PullRequest),
		CI:              ciInfo(s.CI),
		Tokens:          tokenInfo(s.Tokens),
		Sandboxed:       s.Sandboxed,
		Mirror:          s.Mirror,
		InPlace:         s.InPlace,
		Yolo:            s.Yolo,
		Model:           s.Model,
		ToolName:        s.HookToolName,
		ConfigStale:     isConfigStale(s, cfg),
		Starred:         s.Starred,
		SystemKind:      s.SystemKind,
		ScenarioID:      s.ScenarioID,
		ScenarioName:    s.ScenarioName,
		ContextPressure: s.ContextPressure,
		SubAgentCount:   len(s.SubAgents),
	}
	if s.MigratedFrom != nil {
		info.MigratedFrom = s.MigratedFrom.Agent
	}

	if s.DeletedAt != nil {
		info.DeletedAt = s.DeletedAt.Format(time.RFC3339)

		// ExpiresAt is frozen at delete time; surface it verbatim rather than
		// recomputing from current retention (which could disagree with the
		// deadline the user was shown).
		if s.ExpiresAt != nil {
			info.DeleteExpiresAt = s.ExpiresAt.Format(time.RFC3339)
		}
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
	if info.SummaryText == "" && hr != nil {
		info.SummaryText = hookDerivedStatus(*hr, time.Now())
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

	// interrupt_count / interrupt_delay_ms are read live at interrupt-delivery
	// time (see InterruptSession), so changing them never requires a restart.
	// Exclude them from the comparison so retuning interrupts — or the new
	// Claude defaults after an upgrade — doesn't flag existing sessions stale.
	created := s.CreationCfg.Agent
	created.InterruptCount, created.InterruptDelayMs = nil, nil
	current := agent
	current.InterruptCount, current.InterruptDelayMs = nil, nil

	if !reflect.DeepEqual(created, current) {
		return true
	}

	currentSandbox := cfg.Sandbox.Merge(agent.Sandbox)
	if s.SystemKind == SystemKindOrchestrator {
		currentSandbox = cfg.OrchestratorSandboxMerged(s.Agent)
	}

	return !reflect.DeepEqual(s.CreationCfg.SandboxConfig, currentSandbox)
}
