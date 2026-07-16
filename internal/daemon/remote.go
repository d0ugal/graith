package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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

type remoteListenerFactory func(context.Context, config.RemoteConfig, string) (RemoteListener, error)
type remoteTLSLoader func(string, string, string, time.Time) (tls.Certificate, string, error)

// remoteRuntime owns the currently-active tailnet listener generation. All
// mutations happen under SessionManager.configApplyMu, which serializes manual
// and watched config reloads without putting network or filesystem work under
// SessionManager.mu.
type remoteRuntime struct {
	ctx         context.Context
	dataDir     string
	newListener remoteListenerFactory
	loadTLS     remoteTLSLoader
	now         func() time.Time
	generation  *remoteGeneration
}

// remoteGeneration owns everything derived from one [remote] transport
// snapshot: the Tailscale adapter, bound TLS listener, accepted connections,
// and their handler goroutines. Connections are tracked before WhoIs so a
// generation replacement can also interrupt stalled authentication.
type remoteGeneration struct {
	sm     *SessionManager
	cfg    config.RemoteConfig
	rl     RemoteListener
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	conns   map[net.Conn]*TailnetIdentity
	wg      sync.WaitGroup
	started bool
	closed  bool
}

type remoteConfigChange struct {
	topology  bool
	pin       string
	next      *remoteGeneration
	allowlist bool
	pairing   bool
}

func (c remoteConfigChange) publishLocked(sm *SessionManager) {
	if c.topology {
		sm.remoteTLSPin = c.pin
	}
}

func (c remoteConfigChange) commit(sm *SessionManager, cfg config.RemoteConfig) {
	rt := sm.remoteRuntime
	if rt == nil {
		return
	}

	if c.topology {
		rt.generation = c.next
		if c.next != nil {
			c.next.activate()
		}

		return
	}

	gen := rt.generation
	if gen == nil {
		return
	}

	if c.pairing {
		// Pairing mode changes the role of existing device connections. Closing
		// them avoids leaving an attached stream alive with its previous role;
		// reconnect re-evaluates the device under the newly-published policy.
		gen.closeAllConnections()
		return
	}

	if c.allowlist {
		gen.closeUnauthorized(cfg)
	}
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

// configureRemoteRuntime installs the process-lifetime dependencies used to
// create listener generations. Run calls it before config watching begins;
// unit SessionManagers that never serve a network listener leave it nil.
func (sm *SessionManager) configureRemoteRuntime(ctx context.Context, dataDir string) {
	sm.configApplyMu.Lock()
	sm.remoteRuntime = &remoteRuntime{
		ctx:         ctx,
		dataDir:     dataDir,
		newListener: newRemoteListener,
		loadTLS:     loadOrCreateRemoteTLS,
		now:         time.Now,
	}
	sm.configApplyMu.Unlock()
}

// startRemoteRuntime applies the initial [remote] transport after the runtime
// dependencies are installed. Startup retains the historical behaviour of
// leaving the local daemon available when optional remote provisioning fails;
// subsequent reloads can retry the failed generation.
func (sm *SessionManager) startRemoteRuntime(cfg config.RemoteConfig) error {
	sm.configApplyMu.Lock()
	defer sm.configApplyMu.Unlock()

	change, err := sm.prepareRemoteConfig(config.RemoteConfig{}, cfg)
	if err != nil {
		return err
	}

	sm.mu.Lock()
	change.publishLocked(sm)
	sm.mu.Unlock()
	change.commit(sm, cfg)

	return nil
}

func (sm *SessionManager) stopRemoteRuntime() {
	sm.configApplyMu.Lock()
	defer sm.configApplyMu.Unlock()

	if sm.remoteRuntime == nil || sm.remoteRuntime.generation == nil {
		return
	}

	sm.remoteRuntime.generation.stop()
	sm.remoteRuntime.generation = nil

	sm.mu.Lock()
	sm.remoteTLSPin = ""
	sm.mu.Unlock()
}

// prepareRemoteConfig performs the slow half of a remote config transition.
// For a topology change it closes the old generation before constructing and
// binding the replacement. The returned change is committed only after the
// caller publishes the new config under sm.mu.
func (sm *SessionManager) prepareRemoteConfig(oldCfg, newCfg config.RemoteConfig) (remoteConfigChange, error) {
	change := remoteConfigChange{
		allowlist: !slices.Equal(oldCfg.AllowTailnetUsers, newCfg.AllowTailnetUsers),
		pairing:   oldCfg.RequirePairing != newCfg.RequirePairing,
	}

	rt := sm.remoteRuntime
	if rt == nil {
		return change, nil
	}

	topology := remoteTransportChanged(oldCfg, newCfg) || (newCfg.Enabled && rt.generation == nil)
	if !topology {
		return change, nil
	}

	change.topology = true
	if rt.generation != nil {
		rt.generation.stop()
		rt.generation = nil

		// The old TLS channel binding no longer names a reachable generation.
		// Clear it immediately so a failed replacement cannot hand a newly-paired
		// device a stale pin while the remote surface is fail-closed.
		sm.mu.Lock()
		sm.remoteTLSPin = ""
		sm.mu.Unlock()
	}

	if !newCfg.Enabled {
		return change, nil
	}

	gen, pin, err := sm.prepareRemoteGeneration(rt, newCfg)
	if err != nil {
		return remoteConfigChange{}, fmt.Errorf("apply [remote] config: %w; old remote generation was closed and remote access remains disabled", err)
	}

	change.pin = pin
	change.next = gen

	return change, nil
}

func remoteTransportChanged(a, b config.RemoteConfig) bool {
	if a.Enabled != b.Enabled {
		return true
	}

	if !a.Enabled && !b.Enabled {
		return false
	}

	return a.Mode != b.Mode ||
		a.Hostname != b.Hostname ||
		a.Port != b.Port ||
		a.AuthKeyFile != b.AuthKeyFile ||
		!slices.Equal(a.Tags, b.Tags)
}

func (sm *SessionManager) prepareRemoteGeneration(rt *remoteRuntime, cfg config.RemoteConfig) (*remoteGeneration, string, error) {
	if len(cfg.AllowTailnetUsers) == 0 {
		sm.log.Warn("[remote] enabled with empty allow_tailnet_users — all remote connections denied (Gate 1 fail-closed)")
	}

	certPath := filepath.Join(rt.dataDir, "remote-tls.crt")
	keyPath := filepath.Join(rt.dataDir, "remote-tls.key")

	cert, pin, err := rt.loadTLS(certPath, keyPath, cfg.Hostname, rt.now())
	if err != nil {
		return nil, "", fmt.Errorf("TLS setup: %w", err)
	}

	rl, err := rt.newListener(rt.ctx, cfg, rt.dataDir)
	if err != nil {
		return nil, "", fmt.Errorf("listener setup: %w", err)
	}

	raw, err := rl.Listen()
	if err != nil {
		_ = rl.Close()
		return nil, "", fmt.Errorf("remote listen: %w", err)
	}

	ctx, cancel := context.WithCancel(rt.ctx)
	gen := &remoteGeneration{
		sm:     sm,
		cfg:    cfg,
		rl:     rl,
		ln:     tls.NewListener(raw, remoteTLSConfig(cert)),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		conns:  make(map[net.Conn]*TailnetIdentity),
	}

	sm.log.Info("[remote] prepared control surface", "mode", cfg.Mode, "port", cfg.Port, "tls_spki", pin)

	return gen, pin, nil
}

func (g *remoteGeneration) activate() {
	g.mu.Lock()
	if g.closed || g.started {
		g.mu.Unlock()
		return
	}

	g.started = true
	g.mu.Unlock()

	g.sm.log.Info("[remote] listening", "addr", g.ln.Addr().String(), "mode", g.cfg.Mode)

	go func() {
		defer close(g.done)
		g.serve()
	}()
}

func (g *remoteGeneration) serve() {
	for {
		conn, err := g.ln.Accept()
		if err != nil {
			if g.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}

			g.sm.log.Warn("remote accept error", "err", err)
			continue
		}

		if !g.track(conn) {
			_ = conn.Close()
			return
		}

		go g.handle(conn)
	}
}

func (g *remoteGeneration) track(conn net.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closed {
		return false
	}

	g.conns[conn] = nil
	g.wg.Add(1)

	return true
}

func (g *remoteGeneration) setIdentity(conn net.Conn, id *TailnetIdentity) {
	g.mu.Lock()
	if _, ok := g.conns[conn]; ok {
		g.conns[conn] = id
	}
	g.mu.Unlock()
}

func (g *remoteGeneration) untrack(conn net.Conn) {
	g.mu.Lock()
	delete(g.conns, conn)
	g.mu.Unlock()
	g.wg.Done()
}

func (g *remoteGeneration) handle(conn net.Conn) {
	defer g.untrack(conn)
	defer func() { _ = conn.Close() }()

	id, err := g.rl.WhoIs(g.ctx, conn.RemoteAddr().String())
	if err != nil {
		g.sm.log.Warn("remote connection rejected by Gate 1", "addr", conn.RemoteAddr().String(), "err", err)
		return
	}

	g.setIdentity(conn, id)
	if !g.sm.remotePolicyAllows(id) {
		g.sm.log.Warn("remote connection rejected by live Gate 1", "addr", conn.RemoteAddr().String())
		return
	}

	HandleConnection(g.ctx, conn, ConnOrigin{Remote: true, Identity: id}, g.sm, g.sm.log)
}

func (g *remoteGeneration) closeUnauthorized(cfg config.RemoteConfig) {
	g.mu.Lock()
	var closeConns []net.Conn
	for conn, id := range g.conns {
		if !cfg.Enabled || !identityAllowed(cfg, id) {
			closeConns = append(closeConns, conn)
		}
	}
	g.mu.Unlock()

	for _, conn := range closeConns {
		_ = conn.Close()
	}
}

func (g *remoteGeneration) closeAllConnections() {
	g.mu.Lock()
	conns := make([]net.Conn, 0, len(g.conns))
	for conn := range g.conns {
		conns = append(conns, conn)
	}
	g.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (g *remoteGeneration) stop() {
	g.mu.Lock()
	g.closed = true
	started := g.started
	conns := make([]net.Conn, 0, len(g.conns))
	for conn := range g.conns {
		conns = append(conns, conn)
	}
	g.mu.Unlock()

	// Marking the generation closed before interrupting Accept makes track fail
	// deterministically for a connection returned concurrently with shutdown.
	g.cancel()
	_ = g.ln.Close()
	_ = g.rl.Close()

	for _, conn := range conns {
		_ = conn.Close()
	}

	if started {
		<-g.done
	}

	g.wg.Wait()
}

// remotePolicyAllows is the live Gate 1 backstop used both after WhoIs and for
// every inbound remote frame. The config snapshot is read under sm.mu, so a
// completed config publication immediately governs already-open connections.
func (sm *SessionManager) remotePolicyAllows(id *TailnetIdentity) bool {
	sm.mu.RLock()
	cfg := sm.cfg.Remote
	sm.mu.RUnlock()

	return cfg.Enabled && identityAllowed(cfg, id)
}

func (sm *SessionManager) remoteInputAllowed(auth authContext) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	switch auth.role {
	case roleSession, roleOrchestrator:
		_, ok := sm.state.Sessions[auth.sessionID]
		return ok
	case roleRemoteHuman:
		dev := sm.state.PairedDevices[auth.deviceID]
		return sm.cfg.Remote.RequirePairing && dev != nil && !dev.ReadOnly
	default:
		return false
	}
}

func (sm *SessionManager) RemoteTLSPin() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.remoteTLSPin
}
