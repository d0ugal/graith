package daemon

import (
	"context"
	"crypto/tls"
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
		Hostname: cfg.Hostname,
		Dir:      filepath.Join(stateDir, "tsnet"),
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

// serveRemote runs the tailnet-facing listener alongside the Unix socket. Each
// accepted connection is TLS-wrapped, its tailnet identity resolved (Gate 1),
// and — if permitted — dispatched to HandleConnection with a remote ConnOrigin.
// It returns when ctx is cancelled.
func (sm *SessionManager) serveRemote(ctx context.Context, rl RemoteListener, cfg config.RemoteConfig, cert tls.Certificate) error {
	raw, err := rl.Listen()
	if err != nil {
		return fmt.Errorf("remote listen: %w", err)
	}

	sm.log.Info("[remote] listening", "addr", raw.Addr().String())

	ln := tls.NewListener(raw, remoteTLSConfig(cert))

	go func() {
		<-ctx.Done()

		_ = ln.Close()
		_ = rl.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			sm.log.Warn("remote accept error", "err", err)

			continue
		}

		go sm.handleRemoteConn(ctx, conn, rl, cfg)
	}
}

func (sm *SessionManager) handleRemoteConn(ctx context.Context, conn net.Conn, rl RemoteListener, cfg config.RemoteConfig) {
	id, err := rl.WhoIs(ctx, conn.RemoteAddr().String())
	if err != nil || !identityAllowed(cfg, id) {
		sm.log.Warn("remote connection rejected by Gate 1", "addr", conn.RemoteAddr().String(), "err", err)
		_ = conn.Close()

		return
	}

	HandleConnection(ctx, conn, ConnOrigin{Remote: true, Identity: id}, sm, sm.log)
}
