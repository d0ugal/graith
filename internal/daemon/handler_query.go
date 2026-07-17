package daemon

import (
	"errors"
	"log/slog"
	"net"
	"os"
	"sort"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
)

// handleList returns the live (or soft-deleted, when Deleted is set) sessions.
func handleList(sm *SessionManager, send func(string, any), msg protocol.Envelope) {
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

	sm.fillTodoCounts(infos)
	send("session_list", protocol.SessionListMsg{Sessions: infos})
}

// handleDiagnostics returns host/daemon diagnostics with the daemon version.
func handleDiagnostics(sm *SessionManager, send func(string, any)) {
	diag := sm.Diagnostics()
	diag.DaemonVersion = version.Version
	send("diagnostics", diag)
}

// configFileExists interprets an os.Stat error on the config path. Only an
// explicit "does not exist" means the config is absent; any other stat error
// (e.g. EACCES) is ambiguous — the daemon did load a config, so don't claim it's
// running on defaults.
func configFileExists(statErr error) bool {
	return statErr == nil || !errors.Is(statErr, os.ErrNotExist)
}

// handleConfig returns the daemon's effective config as TOML plus a diff from
// the built-in defaults. Env-map secrets are redacted before rendering so raw
// MCP/agent env values never leave the daemon.
func handleConfig(sm *SessionManager, send func(string, any)) {
	cfg := config.RedactSecrets(sm.Config())

	tomlBytes, err := config.EffectiveTOML(cfg)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: "render config: " + err.Error()})

		return
	}

	diff, err := config.DiffFromDefaults(cfg, "effective")
	if err != nil {
		send("error", protocol.ErrorMsg{Message: "diff config: " + err.Error()})

		return
	}

	configPath := sm.paths.ConfigFile

	_, statErr := os.Stat(configPath)

	send("config_response", protocol.ConfigResponseMsg{
		EffectiveTOML:    string(tomlBytes),
		DiffFromDefaults: diff,
		ConfigPath:       configPath,
		ConfigExists:     configFileExists(statErr),
	})
}

// handleAgentCatalog returns the daemon's configured agent catalog (agent names
// plus launch command, sorted by name) and the configured default_agent. GUI
// clients use this to populate their agent pickers instead of hardcoding the
// list, so custom agents appear and the right default is preselected (#1234).
func handleAgentCatalog(sm *SessionManager, send func(string, any)) {
	cfg := sm.Config()

	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}

	sort.Strings(names)

	entries := make([]protocol.AgentCatalogEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, protocol.AgentCatalogEntry{
			Name:    name,
			Command: cfg.Agents[name].Command,
		})
	}

	send("agent_catalog_response", protocol.AgentCatalogResponseMsg{
		Agents:       entries,
		DefaultAgent: cfg.DefaultAgent,
	})
}

// handleGC runs the orphan garbage collector (dry-run unless Force) and returns
// the per-orphan outcome.
func handleGC(sm *SessionManager, send func(string, any), msg protocol.Envelope) {
	var g protocol.GCMsg
	if err := protocol.DecodePayload(msg, &g); err != nil {
		send("error", protocol.ErrorMsg{Message: "invalid gc message"})

		return
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

	send("gc_result", protocol.GCResultMsg{DryRun: !g.Force, Orphans: infos})
}

// handleRepoList returns the repos the daemon knows about so a remote client can
// offer a picker (it has no local cwd — design §C.4).
func handleRepoList(sm *SessionManager, send func(string, any)) {
	send("repo_list", protocol.RepoListResponseMsg{Repos: sm.availableRepos()})
}

// handleStoreList returns a read-only document-store listing for the GUI browser
// (#902). Human-only: a session must not use the unsandboxed daemon to read
// across the per-repo store isolation the sandbox enforces.
func handleStoreList(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	if !auth.isHuman() {
		send("error", protocol.ErrorMsg{Message: "store browsing requires a human operator"})

		return
	}

	sl, ok := decodePayload[protocol.StoreListMsg](msg, send, "invalid store_list message")
	if !ok {
		return
	}

	entries, err := listStoreEntries(sm.paths.DataDir, sl.Repo, sl.Shared, sl.Prefix)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("store_list", protocol.StoreListResponseMsg{Entries: entries})
}

// handleStoreGet returns a read-only document for the GUI browser (#902).
// Human-only for the same reason as handleStoreList.
func handleStoreGet(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	if !auth.isHuman() {
		send("error", protocol.ErrorMsg{Message: "store browsing requires a human operator"})

		return
	}

	sg, ok := decodePayload[protocol.StoreGetMsg](msg, send, "invalid store_get message")
	if !ok {
		return
	}

	resp, err := getStoreDocument(sm.paths.DataDir, sg.Repo, sg.Shared, sg.Key)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("store_get", resp)
}

// handleApprovalList returns the current set of pending approvals. Read-only.
func handleApprovalList(sm *SessionManager, send func(string, any)) {
	send("approval_notification", protocol.ApprovalNotificationMsg{
		Pending: sm.PendingApprovals(),
	})
}

// handleApprovalSubscribe registers a connection to receive approval
// notifications without attaching to a session (design §C.6). Human operators
// only; cleaned up on disconnect. The current pending set is sent immediately so
// the subscriber starts with fleet state.
func handleApprovalSubscribe(sm *SessionManager, auth authContext, send func(string, any), conn net.Conn) {
	if !auth.isHuman() {
		send("error", protocol.ErrorMsg{Message: "approval_subscribe requires a human operator"})

		return
	}

	sm.AddApprovalSubscriber(conn, send)
	send("approval_notification", protocol.ApprovalNotificationMsg{Pending: sm.PendingApprovals()})
}

// handleApprovalRespond records a human's decision on a pending approval. Not
// permitted for agent sessions.
func handleApprovalRespond(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	if auth.authenticated {
		send("error", protocol.ErrorMsg{Message: "operation not permitted for agent sessions"})

		return
	}

	resp, ok := decodePayload[protocol.ApprovalRespondMsg](msg, send, "invalid approval_respond")
	if !ok {
		return
	}

	if err := sm.RespondToApproval(resp.RequestID, resp.Decision, resp.Reason); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("approval_responded", struct{}{})
	}
}

// handleReload reloads the daemon config from disk. Not permitted for agent
// sessions.
func handleReload(sm *SessionManager, auth authContext, send func(string, any)) {
	if auth.authenticated {
		send("error", protocol.ErrorMsg{Message: "operation not permitted for agent sessions"})

		return
	}

	if err := sm.ReloadConfig(); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("reloaded", struct{}{})
	}
}

// handleScreenPreview returns a rendered text preview of a session's screen.
func handleScreenPreview(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	sp, ok := decodePayload[protocol.ScreenPreviewMsg](msg, send, "invalid screen_preview message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, sp.SessionID, authSelfOrDescendant, send) {
		return
	}

	ptySess, ok := sm.GetPTY(sp.SessionID)
	if !ok {
		send("error", protocol.ErrorMsg{Message: "session not found"})

		return
	}

	send("screen_preview_response", protocol.ScreenPreviewResponseMsg{
		SessionID: sp.SessionID,
		Preview:   ptySess.ScreenPreview(),
	})
}

// handleScreenSnapshot returns a structured snapshot of a session's screen
// (frame + cursor + dimensions).
func handleScreenSnapshot(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	ss, ok := decodePayload[protocol.ScreenSnapshotMsg](msg, send, "invalid screen_snapshot message")
	if !ok {
		return
	}

	if !auth.authorizeTarget(sm, ss.SessionID, authSelfOrDescendant, send) {
		return
	}

	ptySess, ok := sm.GetPTY(ss.SessionID)
	if !ok {
		send("error", protocol.ErrorMsg{Message: "session not found"})

		return
	}

	snap := ptySess.ScreenSnapshot()
	send("screen_snapshot_response", protocol.ScreenSnapshotResponseMsg{
		SessionID:     ss.SessionID,
		Frame:         snap.Frame,
		CursorX:       snap.CursorX,
		CursorY:       snap.CursorY,
		CursorVisible: snap.CursorVisible,
		Cols:          snap.Cols,
		Rows:          snap.Rows,
	})
}

// handlePairApprove approves a pending device pairing (local human only). A
// device paired while require_pairing=false gets the read-only guest role.
func handlePairApprove(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, log *slog.Logger) {
	if auth.role != roleLocalHuman {
		send("error", protocol.ErrorMsg{Message: "pairing approval is local-only"})

		return
	}

	pa, ok := decodePayload[protocol.PairApproveMsg](msg, send, "invalid pair_approve")
	if !ok {
		return
	}

	readOnly := !sm.Config().Remote.RequirePairing

	// Stage a "pending" reply carrying the TLS pin as soon as the approval
	// transaction has passed its guards and handed the credential to the live
	// requester, but before it blocks awaiting the device's receipt ack (issue
	// #1299). This lets the operator compare the pin immediately: a device (e.g.
	// the GUI) that withholds its pair_ack until the human confirms the pin would
	// otherwise deadlock against an approval that only prints the pin after commit.
	// It is invoked from inside ApprovePairing so an unknown/expired/disconnected
	// request returns an error first, never a premature pin.
	staged := func(tlsPin string) {
		send("pair_approval_pending", protocol.PairApprovalPendingMsg{RequestID: pa.RequestID, TLSPinSPKI: tlsPin})
	}

	deviceID, token, err := sm.ApprovePairing(pa.RequestID, readOnly, time.Now(), staged)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	log.Info("device paired", "device", deviceID, "read_only", readOnly)
	send("pair_approved", protocol.PairResponseMsg{RequestID: pa.RequestID, DeviceID: deviceID, ClientToken: token, DaemonProfile: sm.paths.Profile, TLSPinSPKI: sm.RemoteTLSPin()})
}

// handlePairList lists pending and paired devices (local human only).
func handlePairList(sm *SessionManager, auth authContext, send func(string, any)) {
	if auth.role != roleLocalHuman {
		send("error", protocol.ErrorMsg{Message: "pair list is local-only"})

		return
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

	send("pair_list", resp)
}

// handlePairRevoke revokes a paired device and closes its live connections
// (local human only).
func handlePairRevoke(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, log *slog.Logger) {
	if auth.role != roleLocalHuman {
		send("error", protocol.ErrorMsg{Message: "pair revoke is local-only"})

		return
	}

	pv, ok := decodePayload[protocol.PairRevokeMsg](msg, send, "invalid pair_revoke")
	if !ok {
		return
	}

	n, err := sm.RevokeDevice(pv.DeviceID)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	log.Info("device revoked", "device", pv.DeviceID, "connections_closed", n)
	send("pair_revoked", protocol.PairRevokeMsg{DeviceID: pv.DeviceID})
}
