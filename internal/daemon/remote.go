package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/d0ugal/graith/internal/config"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tsnet"
)

// RemoteListener is a tailnet-facing listener plus a way to resolve the tailnet
// identity behind an accepted connection (Gate 1). Two implementations back the
// two config modes: tsnet (embedded node) and interface (host tailscaled).
type RemoteListener interface {
	// Listen begins listening and returns the raw net.Listener (TLS is layered
	// on by the caller).
	Listen() (net.Listener, error)
	// WhoIs resolves the tailnet identity behind remoteAddr.
	WhoIs(ctx context.Context, remoteAddr string) (*TailnetIdentity, error)
	// Close releases the listener's resources.
	Close() error
}

// identityFromWhoIs maps a Tailscale WhoIs response to our TailnetIdentity,
// using stable identifiers (login name, stable node id) rather than display
// names. A tagged node has no user, so User is empty and Tags carries the tags.
func identityFromWhoIs(w *apitype.WhoIsResponse) *TailnetIdentity {
	if w == nil {
		return nil
	}

	id := &TailnetIdentity{}
	if w.UserProfile != nil {
		id.User = w.UserProfile.LoginName
	}

	if w.Node != nil {
		id.Node = string(w.Node.StableID)
		id.Tags = append([]string(nil), w.Node.Tags...)
	}

	return id
}

// identityAllowed enforces Gate 1: the WhoIs identity must be explicitly
// permitted by [remote].allow_tailnet_users. An empty allowlist denies everyone
// (fail closed). A "tag:" entry admits tagged nodes carrying that tag.
func identityAllowed(cfg config.RemoteConfig, id *TailnetIdentity) bool {
	if id == nil || len(cfg.AllowTailnetUsers) == 0 {
		return false
	}

	for _, allow := range cfg.AllowTailnetUsers {
		if strings.HasPrefix(allow, "tag:") {
			for _, tag := range id.Tags {
				if tag == allow {
					return true
				}
			}

			continue
		}

		if id.User != "" && id.User == allow {
			return true
		}
	}

	return false
}

// --- tsnet mode: embedded Tailscale node ---

type tsnetListener struct {
	srv  *tsnet.Server
	port int
}

func newTSNetListener(cfg config.RemoteConfig, stateDir string) (*tsnetListener, error) {
	srv := &tsnet.Server{
		Hostname:      cfg.Hostname,
		Dir:           filepath.Join(stateDir, "tsnet"),
		AdvertiseTags: append([]string(nil), cfg.Tags...),
	}

	if cfg.AuthKeyFile != "" {
		b, err := os.ReadFile(config.ExpandPath(cfg.AuthKeyFile))
		if err != nil {
			return nil, fmt.Errorf("read tsnet auth key: %w", err)
		}

		srv.AuthKey = strings.TrimSpace(string(b))
	}

	return &tsnetListener{srv: srv, port: cfg.Port}, nil
}

func (l *tsnetListener) Listen() (net.Listener, error) {
	return l.srv.Listen("tcp", fmt.Sprintf(":%d", l.port))
}

func (l *tsnetListener) WhoIs(ctx context.Context, remoteAddr string) (*TailnetIdentity, error) {
	lc, err := l.srv.LocalClient()
	if err != nil {
		return nil, err
	}

	w, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}

	return identityFromWhoIs(w), nil
}

func (l *tsnetListener) Close() error { return l.srv.Close() }

// --- interface mode: bind to the host tailscaled's tailnet IP ---

type interfaceListener struct {
	lc   *local.Client
	addr string
}

func newInterfaceListener(ctx context.Context, cfg config.RemoteConfig) (*interfaceListener, error) {
	lc := &local.Client{}

	st, err := lc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailscale status (is tailscaled running?): %w", err)
	}

	if st.Self == nil || len(st.Self.TailscaleIPs) == 0 {
		return nil, errors.New("no tailnet IP available (interface mode requires a running tailscaled)")
	}

	// Prefer IPv4, and NEVER fall back to a wildcard bind.
	ip := st.Self.TailscaleIPs[0].String()

	for _, a := range st.Self.TailscaleIPs {
		if a.Is4() {
			ip = a.String()
			break
		}
	}

	return &interfaceListener{lc: lc, addr: net.JoinHostPort(ip, strconv.Itoa(cfg.Port))}, nil
}

func (l *interfaceListener) Listen() (net.Listener, error) {
	return net.Listen("tcp", l.addr)
}

func (l *interfaceListener) WhoIs(ctx context.Context, remoteAddr string) (*TailnetIdentity, error) {
	w, err := l.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}

	return identityFromWhoIs(w), nil
}

func (l *interfaceListener) Close() error { return nil }

// newRemoteListener builds the RemoteListener for the configured mode.
func newRemoteListener(ctx context.Context, cfg config.RemoteConfig, stateDir string) (RemoteListener, error) {
	switch cfg.Mode {
	case "tsnet":
		return newTSNetListener(cfg, stateDir)
	case "interface":
		return newInterfaceListener(ctx, cfg)
	default:
		return nil, fmt.Errorf("unknown remote mode %q", cfg.Mode)
	}
}

// remoteOriginAllowed applies the live Gate 1 policy to a remote connection.
// Production origins must also belong to the currently-active listener
// generation. The handler calls this before every frame, not just at accept,
// so disablement and allowlist removal revoke already-authenticated clients.
func (sm *SessionManager) remoteOriginAllowed(origin ConnOrigin) bool {
	if !origin.Remote {
		return true
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.remoteOriginAllowedLocked(origin)
}

// remoteOriginAllowedLocked is remoteOriginAllowed for callers already holding
// sm.mu at least for reading.
func (sm *SessionManager) remoteOriginAllowedLocked(origin ConnOrigin) bool {
	if sm.cfg == nil || !sm.cfg.Remote.Enabled || !identityAllowed(sm.cfg.Remote, origin.Identity) {
		return false
	}

	return origin.RemoteGeneration == 0 || origin.RemoteGeneration == sm.remoteGeneration
}

// remoteDataAllowed re-resolves the proven device against live config before
// accepting an attached input frame. Data frames carry no token, so the
// connection's PoP-verified device ID is the credential at this boundary.
func (sm *SessionManager) remoteDataAllowed(origin ConnOrigin, deviceID string) bool {
	if !origin.Remote {
		return true
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if !sm.remoteOriginAllowedLocked(origin) || deviceID == "" {
		return false
	}

	d := sm.state.PairedDevices[deviceID]
	if d == nil || !identityMatchesDevice(origin.Identity, d) {
		return false
	}

	return remoteDeviceRole(sm.cfg.Remote, d) == roleRemoteHuman
}

// remoteDeviceRole applies the live require_pairing authority ceiling while
// preserving the device's approval-time ReadOnly restriction. Disabling
// pairing downgrades every device immediately; re-enabling it never elevates a
// device that was originally approved read-only.
func remoteDeviceRole(cfg config.RemoteConfig, d *PairedDevice) authRole {
	if d.ReadOnly || !cfg.RequirePairing {
		return roleRemoteGuest
	}

	return roleRemoteHuman
}

// RemoteTLSPin returns the active remote generation's SPKI pin.
func (sm *SessionManager) RemoteTLSPin() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.remoteTLSPin
}
