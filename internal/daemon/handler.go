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
// origin describes where the connection came from (local Unix socket vs remote
// tailnet listener) and carries the tailnet identity for remote connections;
// it is threaded to resolveAuth so authorization is origin-aware. The zero
// ConnOrigin{} is a local connection, preserving the existing trust model.
func HandleConnection(ctx context.Context, conn net.Conn, origin ConnOrigin, sm *SessionManager, log *slog.Logger) {
	reader := protocol.NewFrameReader(conn)
	writer := &safeFrameWriter{writer: protocol.NewFrameWriter(conn)}

	var (
		attachedSessionID      string
		attachedDataWriter     *frameDataWriter
		clientRows, clientCols uint16 = 24, 80
		// poppedDeviceID is the device ID proven via proof-of-possession on this
		// connection (set once a valid auth_proof is received); empty means the
		// remote caller has not completed PoP and stays roleNone.
		poppedDeviceID string
		// challengeNonce is the per-connection PoP nonce issued after a remote
		// handshake; the client must sign it in auth_proof. Single-use.
		challengeNonce string
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
				sm.mu.RUnlock()

				if dev == nil || challengeNonce == "" || !verifyPoP(dev.PubKey, challengeNonce, sm.remoteTLSPin, ap.Signature) ||
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

				rid, waitCh, err := sm.AddPendingPairing(pr.DeviceLabel, pr.DevicePubKey, identity, time.Now())
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				log.Info("pairing requested", "request_id", rid, "label", pr.DeviceLabel, "tailnet_user", identity.User)

				go func() {
					select {
					case appr := <-waitCh:
						sendControl("pair_response", protocol.PairResponseMsg{
							DeviceID:      appr.DeviceID,
							ClientToken:   appr.Token,
							DaemonProfile: appr.Profile,
							TLSPinSPKI:    appr.TLSPin,
						})
					case <-time.After(pendingPairingTTL):
						sm.unregisterPairWaiter(rid)
						sendControl("error", protocol.ErrorMsg{Message: "pairing request timed out"})
					case <-connDone:
						sm.unregisterPairWaiter(rid)
					}
				}()

			case "pair_approve":
				if auth.role != roleLocalHuman {
					sendControl("error", protocol.ErrorMsg{Message: "pairing approval is local-only"})
					continue
				}

				pa, ok := decodePayload[protocol.PairApproveMsg](msg, sendControl, "invalid pair_approve")
				if !ok {
					continue
				}

				// A device paired while require_pairing=false gets the read-only
				// guest role (design §B.2).
				readOnly := !sm.Config().Remote.RequirePairing

				deviceID, token, err := sm.ApprovePairing(pa.RequestID, readOnly, time.Now())
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				log.Info("device paired", "device", deviceID, "read_only", readOnly)
				sendControl("pair_approved", protocol.PairResponseMsg{DeviceID: deviceID, ClientToken: token, DaemonProfile: sm.paths.Profile, TLSPinSPKI: sm.remoteTLSPin})

			case "pair_list":
				if auth.role != roleLocalHuman {
					sendControl("error", protocol.ErrorMsg{Message: "pair list is local-only"})
					continue
				}

				pending, paired := sm.ListPairings()
				resp := protocol.PairListResponseMsg{}

				for _, p := range pending {
					resp.Pending = append(resp.Pending, protocol.PairPending{
						RequestID:   p.RequestID,
						DeviceLabel: p.DeviceLabel,
						TailnetUser: p.Identity.User,
						TailnetNode: p.Identity.Node,
						RequestedAt: p.RequestedAt.Format(time.RFC3339),
					})
				}

				for _, d := range paired {
					lastSeen := ""
					if !d.LastSeenAt.IsZero() {
						lastSeen = d.LastSeenAt.Format(time.RFC3339)
					}

					resp.Paired = append(resp.Paired, protocol.PairedDeviceInfo{
						DeviceID:    d.ID,
						Label:       d.Label,
						TailnetUser: d.TailnetUser,
						TailnetNode: d.TailnetNode,
						CreatedAt:   d.CreatedAt.Format(time.RFC3339),
						LastSeenAt:  lastSeen,
					})
				}

				sendControl("pair_list", resp)

			case "pair_revoke":
				if auth.role != roleLocalHuman {
					sendControl("error", protocol.ErrorMsg{Message: "pair revoke is local-only"})
					continue
				}

				pv, ok := decodePayload[protocol.PairRevokeMsg](msg, sendControl, "invalid pair_revoke")
				if !ok {
					continue
				}

				n, err := sm.RevokeDevice(pv.DeviceID)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				log.Info("device revoked", "device", pv.DeviceID, "connections_closed", n)
				sendControl("pair_revoked", protocol.PairRevokeMsg{DeviceID: pv.DeviceID})

			case "approval_subscribe":
				// Register this connection to receive approval notifications
				// without attaching to a session (design §C.6). Human operators
				// only. Cleaned up on disconnect.
				if !auth.isHuman() {
					sendControl("error", protocol.ErrorMsg{Message: "approval_subscribe requires a human operator"})
					continue
				}

				sm.AddApprovalSubscriber(conn, sendControl)
				// Send the current pending set immediately so the subscriber
				// starts with fleet state, not just future changes.
				sendControl("approval_notification", protocol.ApprovalNotificationMsg{Pending: sm.PendingApprovals()})

			case "repo_list":
				// Return the repos the daemon knows about so a remote client can
				// offer a picker (it has no local cwd — design §C.4).
				sendControl("repo_list", protocol.RepoListResponseMsg{Repos: sm.availableRepos()})

			case "list":
				var lm protocol.ListMsg

				_ = protocol.DecodePayload(msg, &lm) // empty/legacy payload → live sessions

				sessions := sm.List()
				cfg := sm.Config()

				infos := make([]protocol.SessionInfo, 0, len(sessions))
				for _, s := range sessions {
					if s.IsSoftDeleted() != lm.Deleted {
						continue
					}

					infos = append(infos, toSessionInfo(s, cfg, sm.getHookReport(s.ID)))
				}

				sendControl("session_list", protocol.SessionListMsg{Sessions: infos})

			case "create":
				c, ok := decodePayload[protocol.CreateMsg](msg, sendControl, "invalid create message")
				if !ok {
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
				sess, err := sm.Create(CreateOpts{
					Name:                c.Name,
					AgentName:           agentName,
					RepoPath:            c.RepoPath,
					BaseBranch:          c.Base,
					Prompt:              c.Prompt,
					Model:               c.Model,
					ParentID:            c.ParentID,
					NoRepo:              c.NoRepo,
					Mirror:              c.Mirror,
					AgentHooks:          c.AgentHooks,
					InPlace:             c.InPlace,
					AllowConcurrent:     c.AllowConcurrent,
					SkipModelValidation: c.SkipModelValidation,
					Yolo:                c.Yolo,
					Headless:            c.Headless,
					NoFetch:             c.NoFetch,
					Rows:                clientRows,
					Cols:                clientCols,
				})
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("created", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
				}

			case "fork":
				f, ok := decodePayload[protocol.ForkMsg](msg, sendControl, "invalid fork message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, f.SourceSessionID, authSelfOrDescendant, sendControl) {
					continue
				}

				//nolint:contextcheck // session lifecycle is intentionally detached from the client connection: forked sessions must survive client disconnect, so Fork uses its own bounded background timeouts rather than the request ctx.
				sess, err := sm.ForkWithAgent(f.Name, f.SourceSessionID, f.Agent, f.Model, clientRows, clientCols)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("created", toSessionInfo(sess, sm.Config(), sm.getHookReport(sess.ID)))
				}

			case "migrate":
				m, ok := decodePayload[protocol.MigrateMsg](msg, sendControl, "invalid migrate message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, m.SessionID, authSelfOrDescendant, sendControl) {
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

				// Headless sessions have no interactive TUI to stream. Convert-on-
				// attach (stop → `claude --resume` in a PTY) is a planned follow-up
				// (issue #1075); until then, direct the user to read-only logs
				// rather than attaching to a stream they can't drive.
				if sess, exists := sm.Get(a.SessionID); exists && sess.DriverKind == DriverHeadless {
					sendControl("error", protocol.ErrorMsg{Message: "session is headless; interactive attach is not yet supported — use `gr logs -f " + sess.Name + "` to watch it read-only"})
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

				if tail, err := ptySess.ScrollbackFile().Tail(300); err == nil && len(tail) > 0 {
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
				d, ok := decodePayload[protocol.DeleteMsg](msg, sendControl, "invalid delete message")
				if !ok {
					continue
				}

				handleDelete(sm, auth, sendControl, d)

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
				r, ok := decodePayload[protocol.RenameMsg](msg, sendControl, "invalid rename message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, r.SessionID, authSelfOrDescendant, sendControl) {
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
				u, ok := decodePayload[protocol.UpdateMsg](msg, sendControl, "invalid update message")
				if !ok {
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
				s, ok := decodePayload[protocol.StarMsg](msg, sendControl, "invalid star message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, s.SessionID, authSelfOrDescendant, sendControl) {
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
				u, ok := decodePayload[protocol.UnstarMsg](msg, sendControl, "invalid unstar message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, u.SessionID, authSelfOrDescendant, sendControl) {
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
				m, ok := decodePayload[protocol.SetStatusMsg](msg, sendControl, "invalid set_status message")
				if !ok {
					continue
				}

				if auth.authenticated {
					m.SessionID = auth.sessionID
				}

				if !auth.authorizeTarget(sm, m.SessionID, authSelfOnly, sendControl) {
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
				r, ok := decodePayload[protocol.ResumeMsg](msg, sendControl, "invalid resume message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, r.SessionID, authSelfOrDescendant, sendControl) {
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
				r, ok := decodePayload[protocol.RestartMsg](msg, sendControl, "invalid restart message")
				if !ok {
					continue
				}

				sm.log.Debug("control request",
					"op", "restart", "caller", auth.describe(),
					"target", r.SessionID, "children", r.Children)

				if !auth.authorizeTarget(sm, r.SessionID, authSelfOrDescendant, sendControl) {
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
				l, ok := decodePayload[protocol.LogsMsg](msg, sendControl, "invalid logs message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, l.SessionID, authSelfOrDescendant, sendControl) {
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
				m, ok := decodePayload[protocol.MsgPubMsg](msg, sendControl, "invalid msg_pub message")
				if !ok {
					continue
				}

				// Sender identity is forced by role so it can't be spoofed. A
				// session publishes as itself; the local human (CLI) may address
				// on behalf of a named session (trusted local use); a remote
				// human publishes as its device and CANNOT claim to be a session.
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
					sendControl("error", protocol.ErrorMsg{Message: "not authorized to publish"})
					continue
				}

				published, err := sm.messages.Publish(PublishOpts{Stream: m.Stream, SenderID: m.SenderID, SenderName: m.SenderName, Body: m.Body, ThreadID: m.ThreadID, ReplyTo: m.ReplyTo})
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
				m, ok := decodePayload[protocol.MsgAckMsg](msg, sendControl, "invalid msg_ack message")
				if !ok {
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
				m, ok := decodePayload[protocol.MsgTopicsMsg](msg, sendControl, "invalid msg_topics message")
				if !ok {
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
				m, ok := decodePayload[protocol.MsgConversationMsg](msg, sendControl, "invalid msg_conversation message")
				if !ok {
					continue
				}
				// Authorise the target session. The self-or-descendant rule lets
				// the human CLI (unauthenticated), the session itself, an ancestor
				// agent, or the orchestrator read the conversation. The by-sender
				// filter inside Conversation keeps the cross-inbox scan safe: for
				// the target session, outbound results are limited to messages it
				// authored, plus everything in its own inbox.
				if !auth.authorizeTarget(sm, m.SessionID, authSelfOrDescendant, sendControl) {
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
						System:     cm.System,
					}
				}

				sendControl("msg_conversation_list", protocol.MsgConversationListMsg{Messages: out})

			case "msg_jail_list":
				m, ok := decodePayload[protocol.MsgJailListMsg](msg, sendControl, "invalid msg_jail_list message")
				if !ok {
					continue
				}

				if sm.messages == nil {
					sendControl("msg_jail_list", protocol.MsgJailListResponse{})
					continue
				}

				jailed, err := sm.messages.ListJailed(m.IncludeReleased)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				// list is a metadata-only summary — the raw untrusted body is
				// never included here (even for the human), so an agent that
				// merely lists the jail can't be injected via a body dump. The
				// body is fetched deliberately via `show`, gated below.
				sendControl("msg_jail_list", protocol.MsgJailListResponse{Jailed: jailedToWire(jailed, false)})

			case "msg_jail_show":
				m, ok := decodePayload[protocol.MsgJailShowMsg](msg, sendControl, "invalid msg_jail_show message")
				if !ok {
					continue
				}

				if sm.messages == nil {
					sendControl("error", protocol.ErrorMsg{Message: "no jailed comments"})
					continue
				}

				j, found, err := sm.messages.GetJailed(m.ID)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				if !found {
					sendControl("error", protocol.ErrorMsg{Message: "no jailed comment with id " + m.ID})
					continue
				}

				// The raw body is quarantined untrusted content: reveal it only
				// to a release-authorized role (human/orchestrator). An ordinary
				// agent/guest gets the metadata with the body withheld, so `show`
				// can never be used to inject the payload the trust gate blocked.
				sendControl("msg_jail_show", protocol.MsgJailShowResponse{Jailed: jailedOneToWire(j, auth.mayReadJailBody(sm))})

			case "msg_jail_release":
				m, ok := decodePayload[protocol.MsgJailReleaseMsg](msg, sendControl, "invalid msg_jail_release message")
				if !ok {
					continue
				}

				// Release delivers quarantined untrusted content to a working
				// agent, so it is restricted to a human operator or the
				// orchestrator — a plain agent session is rejected (issue #1082).
				if !auth.authorizeJailRelease(sm, sendControl) {
					continue
				}

				var (
					released []JailedComment
					err      error
				)

				switch {
				case m.All:
					if m.Author == "" {
						sendControl("error", protocol.ErrorMsg{Message: "--all requires --author <login>"})
						continue
					}

					//nolint:contextcheck // ReleaseJailed(ByAuthor) delivers via notifyFromDaemon, whose auto-resume is detached and must outlive this request, so it deliberately does not inherit the connection ctx.
					released, err = sm.ReleaseJailedByAuthor(m.Author)
				case m.ID != "":
					var one JailedComment

					//nolint:contextcheck // ReleaseJailed delivers via notifyFromDaemon, whose auto-resume is detached and must outlive this request, so it deliberately does not inherit the connection ctx.
					one, err = sm.ReleaseJailed(m.ID)
					if err == nil {
						released = []JailedComment{one}
					}
				default:
					sendControl("error", protocol.ErrorMsg{Message: "specify a jail id, or --all --author <login>"})
					continue
				}

				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
					continue
				}

				// A release is authorized to see bodies (it's the human/
				// orchestrator) and echoes back what was delivered — include the
				// body. --all is best-effort (ReleaseJailedByAuthor continues past
				// a per-item failure), so Released reflects exactly what went out.
				sendControl("msg_jail_release", protocol.MsgJailReleaseResponse{Released: jailedToWire(released, true)})

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
				if auth.authenticated {
					sendControl("error", protocol.ErrorMsg{Message: "operation not permitted for agent sessions"})
					continue
				}

				resp, ok := decodePayload[protocol.ApprovalRespondMsg](msg, sendControl, "invalid approval_respond")
				if !ok {
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
				t, ok := decodePayload[protocol.TypeMsg](msg, sendControl, "invalid type message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, t.SessionID, authSelfOrDescendant, sendControl) {
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
				in, ok := decodePayload[protocol.InterruptMsg](msg, sendControl, "invalid interrupt message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, in.SessionID, authSelfOrDescendant, sendControl) {
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
				sp, ok := decodePayload[protocol.ScreenPreviewMsg](msg, sendControl, "invalid screen_preview message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, sp.SessionID, authSelfOrDescendant, sendControl) {
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
				ss, ok := decodePayload[protocol.ScreenSnapshotMsg](msg, sendControl, "invalid screen_snapshot message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, ss.SessionID, authSelfOrDescendant, sendControl) {
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
				sr, ok := decodePayload[protocol.StatusRequestMsg](msg, sendControl, "invalid status message")
				if !ok {
					continue
				}

				if !auth.authorizeTarget(sm, sr.SessionID, authSelfOrDescendant, sendControl) {
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
				sr, ok := decodePayload[protocol.StatusReportMsg](msg, sendControl, "invalid status_report")
				if !ok {
					continue
				}

				if auth.authenticated {
					sr.SessionID = auth.sessionID
				}

				if !auth.authorizeTarget(sm, sr.SessionID, authSelfOnly, sendControl) {
					continue
				}

				sm.HandleHookReport(sr)
				sendControl("status_reported", struct{}{})

			case "diagnostics":
				diag := sm.Diagnostics()
				diag.DaemonVersion = version.Version
				sendControl("diagnostics", diag)

			case "gc":
				var g protocol.GCMsg
				if err := protocol.DecodePayload(msg, &g); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: "invalid gc message"})
					continue
				}

				orphans := sm.RunGC(g.Force, time.Now())

				infos := make([]protocol.GCOrphanInfo, 0, len(orphans))
				for _, o := range orphans {
					infos = append(infos, protocol.GCOrphanInfo{
						Type:              o.Type,
						Path:              o.Path,
						ID:                o.ID,
						IsGitWorktree:     o.IsGitWorktree,
						HasDirtyFiles:     o.HasDirtyFiles,
						DirtyUndetermined: o.dirtyUndetermined,
						Removed:           o.Removed,
						Skipped:           o.Skipped,
						Reason:            o.Reason,
					})
				}

				sendControl("gc_result", protocol.GCResultMsg{DryRun: !g.Force, Orphans: infos})

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
				// read-only: no auth gate (like trigger_list/scenario_list)
				if sm.mcpManager == nil {
					sendControl("mcp_list", protocol.MCPListResponse{})
					continue
				}

				sendControl("mcp_list", protocol.MCPListResponse{Servers: sm.mcpManager.List()})

			case "mcp_restart":
				mr, ok := decodePayload[protocol.MCPRestartMsg](msg, sendControl, "invalid mcp_restart message")
				if !ok {
					continue
				}

				if !auth.authorizeTriggerOp(sm, sendControl) {
					continue
				}

				if sm.mcpManager == nil {
					sendControl("error", protocol.ErrorMsg{Message: "MCP manager not initialized"})
					continue
				}

				stopped, err := sm.mcpManager.Restart(mr.Name)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("mcp_restart", protocol.MCPRestartResponse{Name: mr.Name, Stopped: stopped})
				}

			case "mcp_logs":
				ml, ok := decodePayload[protocol.MCPLogsMsg](msg, sendControl, "invalid mcp_logs message")
				if !ok {
					continue
				}

				if sm.mcpManager == nil {
					sendControl("error", protocol.ErrorMsg{Message: "MCP manager not initialized"})
					continue
				}

				files, err := sm.mcpManager.LogFiles(ml.Name, ml.Lines)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("mcp_logs", protocol.MCPLogsResponse{Name: ml.Name, Files: files})
				}

			case "scenario_start":
				s, ok := decodePayload[protocol.ScenarioStartMsg](msg, sendControl, "invalid scenario_start message")
				if !ok {
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
				s, ok := decodePayload[protocol.ScenarioStopMsg](msg, sendControl, "invalid scenario_stop message")
				if !ok {
					continue
				}

				sm.log.Debug("control request",
					"op", "scenario_stop", "caller", auth.describe(), "scenario", s.Name)

				if !auth.authorizeScenarioOp(sm, s.Name, sendControl) {
					continue
				}

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
				s, ok := decodePayload[protocol.ScenarioDeleteMsg](msg, sendControl, "invalid scenario_delete message")
				if !ok {
					continue
				}

				sm.log.Debug("control request",
					"op", "scenario_delete", "caller", auth.describe(), "scenario", s.Name)

				if !auth.authorizeScenarioOp(sm, s.Name, sendControl) {
					continue
				}

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
				s, ok := decodePayload[protocol.ScenarioStatusMsg](msg, sendControl, "invalid scenario_status message")
				if !ok {
					continue
				}

				record, err := sm.ScenarioStatus(s.Name)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("scenario_status", protocol.ScenarioStatusResponse{Scenario: *record})
				}

			case "trigger_list":
				// read-only: no auth gate (like scenario_list/status)
				sendControl("trigger_list", protocol.TriggerListResponse{Triggers: sm.TriggerList()})

			case "trigger_status":
				s, ok := decodePayload[protocol.TriggerStatusMsg](msg, sendControl, "invalid trigger_status message")
				if !ok {
					continue
				}

				rec, err := sm.TriggerStatus(s.Name)
				if err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("trigger_status", protocol.TriggerStatusResponse{Trigger: rec})
				}

			case "trigger_run":
				s, ok := decodePayload[protocol.TriggerRunMsg](msg, sendControl, "invalid trigger_run message")
				if !ok {
					continue
				}

				if !auth.authorizeTriggerOp(sm, sendControl) {
					continue
				}

				if err := sm.TriggerRunNow(ctx, s.Name); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("trigger_run", struct {
						Name string `json:"name"`
					}{s.Name})
				}

			case "trigger_pause":
				s, ok := decodePayload[protocol.TriggerPauseMsg](msg, sendControl, "invalid trigger_pause message")
				if !ok {
					continue
				}

				if !auth.authorizeTriggerOp(sm, sendControl) {
					continue
				}

				if err := sm.TriggerPause(s.Name, s.Pause); err != nil {
					sendControl("error", protocol.ErrorMsg{Message: err.Error()})
				} else {
					sendControl("trigger_pause", struct {
						Name  string `json:"name"`
						Pause bool   `json:"pause"`
					}{s.Name, s.Pause})
				}

			case "notify":
				n, ok := decodePayload[protocol.NotifyMsg](msg, sendControl, "invalid notify message")
				if !ok {
					continue
				}

				if !auth.authorizeNotify(sm, sendControl) {
					continue
				}

				delivered, reason := sm.SendPushNotification(pushNotification{
					Title:    n.Title,
					Message:  n.Message,
					Priority: n.Priority,
				})
				sendControl("notify_response", protocol.NotifyResponse{Delivered: delivered, Reason: reason})

			case "scenario_resume":
				s, ok := decodePayload[protocol.ScenarioResumeMsg](msg, sendControl, "invalid scenario_resume message")
				if !ok {
					continue
				}

				if !auth.authorizeScenarioOp(sm, s.Name, sendControl) {
					continue
				}

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
				s, ok := decodePayload[protocol.ScenarioTaskDoneMsg](msg, sendControl, "invalid scenario_task_done message")
				if !ok {
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
				s, ok := decodePayload[protocol.ScenarioAddMsg](msg, sendControl, "invalid scenario_add message")
				if !ok {
					continue
				}

				if !auth.authorizeScenarioOp(sm, s.Name, sendControl) {
					continue
				}

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

// lifecycleRequest holds the fields shared by the stop and delete control
// messages, both of which target a session (optionally with descendants).
type lifecycleRequest struct {
	SessionID   string
	Children    bool
	ExcludeRoot bool
}

// handleDelete authorizes and dispatches a delete request. It chooses soft vs
// hard delete on the daemon side (soft when !Purge and retention > 0) and
// replies with a DeleteResultMsg carrying the soft/hard outcome and, for a soft
// delete, the computed expiry — enough for the CLI to render "Recoverable
// until …" vs "Deleted".
func handleDelete(sm *SessionManager, auth authContext, sendControl func(string, any), d protocol.DeleteMsg) {
	sm.log.Debug("control request",
		"op", "delete", "caller", auth.describe(),
		"target", d.SessionID, "children", d.Children, "purge", d.Purge)

	sm.mu.RLock()
	authErr := auth.checkTarget(sm, d.SessionID, authSelfOrDescendant)
	sm.mu.RUnlock()

	if authErr != nil {
		sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
		return
	}

	// Three-way routing, owned by the daemon (the CLI only forwards intent via
	// Purge):
	//   Purge              -> hard delete (gr purge)
	//   !Purge && ret > 0  -> soft delete (gr delete, recoverable)
	//   !Purge && ret == 0 -> reject: gr delete never destroys, so with soft
	//                         delete disabled there is nothing safe to do.
	if !d.Purge && sm.Config().Delete.RetentionDuration() <= 0 {
		sendControl("error", protocol.ErrorMsg{Message: "soft delete is disabled (retention=0); use gr purge"})
		return
	}

	soft := !d.Purge

	if d.Children {
		handleDeleteChildren(sm, sendControl, d, soft)
		return
	}

	if soft {
		snap, err := sm.SoftDelete(d.SessionID)
		if err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
			return
		}

		sendControl("deleted", softDeleteResult(snap))

		return
	}

	// Hard delete (gr purge): capture the name before the session is removed.
	name := sm.sessionName(d.SessionID)

	if err := sm.Delete(d.SessionID); err != nil {
		sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	sendControl("deleted", protocol.DeleteResultMsg{SessionID: d.SessionID, Name: name, Soft: false})
}

// handleDeleteChildren runs the with-children delete (soft or hard) and replies
// with a DeleteResultMsg whose Affected list carries the per-descendant outcome.
func handleDeleteChildren(sm *SessionManager, sendControl func(string, any), d protocol.DeleteMsg, soft bool) {
	var (
		affected []string
		err      error
	)

	if soft {
		affected, err = sm.SoftDeleteWithChildren(d.SessionID, d.ExcludeRoot)
	} else {
		affected, err = sm.DeleteWithChildren(d.SessionID, d.ExcludeRoot)
	}

	if err != nil {
		sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	result := protocol.DeleteResultMsg{SessionID: d.SessionID, Soft: soft}
	for _, id := range affected {
		if soft {
			result.Affected = append(result.Affected, softDeleteResult(sm.sessionSnapshot(id)))
		} else {
			result.Affected = append(result.Affected, protocol.DeleteResultMsg{SessionID: id, Soft: false})
		}
	}

	sendControl("deleted", result)
}

// softDeleteResult builds the delete response for a soft-deleted session,
// reading its frozen DeletedAt/ExpiresAt off the session state.
func softDeleteResult(s SessionState) protocol.DeleteResultMsg {
	r := protocol.DeleteResultMsg{SessionID: s.ID, Name: s.Name, Soft: true}
	if s.DeletedAt != nil {
		r.DeletedAt = s.DeletedAt.Format(time.RFC3339)
	}

	if s.ExpiresAt != nil {
		r.ExpiresAt = s.ExpiresAt.Format(time.RFC3339)
	}

	return r
}

// handleRestore authorizes and dispatches a restore request, restoring either a
// single soft-deleted session or (with Children) the whole soft-deleted subtree.
func handleRestore(sm *SessionManager, auth authContext, sendControl func(string, any), r protocol.RestoreMsg) {
	sm.mu.RLock()
	authErr := auth.checkTarget(sm, r.SessionID, authSelfOrDescendant)
	sm.mu.RUnlock()

	if authErr != nil {
		sendControl("error", protocol.ErrorMsg{Message: authErr.Error()})
		return
	}

	cfg := sm.Config()

	if r.Children {
		sessions, err := sm.RestoreWithChildren(r.SessionID)
		if err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})
			return
		}

		result := protocol.RestoreResultMsg{}
		for _, s := range sessions {
			result.Sessions = append(result.Sessions, toSessionInfo(s, cfg, sm.getHookReport(s.ID)))
		}

		sendControl("restored", result)

		return
	}

	sess, err := sm.Restore(r.SessionID)
	if err != nil {
		sendControl("error", protocol.ErrorMsg{Message: err.Error()})
		return
	}

	result := protocol.RestoreResultMsg{
		Sessions:           []protocol.SessionInfo{toSessionInfo(sess, cfg, sm.getHookReport(sess.ID))},
		DeletedDescendants: sm.softDeletedDescendantCount(sess.ID),
	}

	sendControl("restored", result)
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
