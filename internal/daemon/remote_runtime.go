package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

type remoteListenerFactory func(context.Context, config.RemoteConfig, string) (RemoteListener, error)
type remoteTLSLoader func(string, string, string, time.Time) (tls.Certificate, string, error)

// remoteController owns at most one listener generation. Its methods are called
// under SessionManager.configReloadMu, so fallible close/build work can remain
// outside SessionManager.mu without allowing two config generations to
// interleave.
type remoteController struct {
	sm          *SessionManager
	ctx         context.Context
	stateDir    string
	newListener remoteListenerFactory
	loadTLS     remoteTLSLoader
	runtime     *remoteRuntime
	nextGen     uint64
}

func newRemoteController(ctx context.Context, sm *SessionManager, stateDir string) *remoteController {
	return &remoteController{
		sm:          sm,
		ctx:         ctx,
		stateDir:    stateDir,
		newListener: newRemoteListener,
		loadTLS:     loadOrCreateRemoteTLS,
	}
}

// prepare reconciles listener-derived state but does not start accepting on a
// replacement until the caller has atomically published its config and pin.
// On replacement it invalidates and closes the old generation before any
// fallible provisioning, deliberately leaving remote access closed on error.
func (c *remoteController) prepare(cfg config.RemoteConfig) (runtime *remoteRuntime, changed bool, err error) {
	if !cfg.Enabled {
		if c.runtime == nil {
			return nil, false, nil
		}

		c.sm.invalidateRemoteGeneration()
		c.runtime.stop()
		c.runtime = nil

		return nil, true, nil
	}

	if c.runtime != nil && c.runtime.matches(cfg, c.stateDir) {
		return c.runtime, false, nil
	}

	// Invalidate first so an accept/auth already in flight cannot squeeze a
	// message through while the old sockets are being closed.
	c.sm.invalidateRemoteGeneration()

	if c.runtime != nil {
		c.runtime.stop()
		c.runtime = nil
	}

	c.nextGen++

	runtime, err = c.build(cfg, c.nextGen)
	if err != nil {
		return nil, true, err
	}

	c.runtime = runtime

	return runtime, true, nil
}

func (c *remoteController) build(cfg config.RemoteConfig, generation uint64) (*remoteRuntime, error) {
	certPath := filepath.Join(c.stateDir, "remote-tls.crt")
	keyPath := filepath.Join(c.stateDir, "remote-tls.key")

	cert, pin, err := c.loadTLS(certPath, keyPath, cfg.Hostname, time.Now())
	if err != nil {
		return nil, fmt.Errorf("TLS setup: %w", err)
	}

	material, err := remoteMaterialState(cfg, c.stateDir)
	if err != nil {
		return nil, fmt.Errorf("fingerprint listener material: %w", err)
	}

	rl, err := c.newListener(c.ctx, cfg, c.stateDir)
	if err != nil {
		return nil, fmt.Errorf("listener setup: %w", err)
	}

	raw, err := rl.Listen()
	if err != nil {
		_ = rl.Close()

		return nil, fmt.Errorf("listen: %w", err)
	}

	appliedMaterial, err := remoteMaterialState(cfg, c.stateDir)
	if err != nil {
		_ = raw.Close()
		_ = rl.Close()

		return nil, fmt.Errorf("fingerprint listener material: %w", err)
	}

	if appliedMaterial != material {
		_ = raw.Close()
		_ = rl.Close()

		return nil, errors.New("listener credential or TLS material changed while replacement was starting; retry reload")
	}

	ctx, cancel := context.WithCancel(c.ctx)
	rt := &remoteRuntime{
		sm:          c.sm,
		ctx:         ctx,
		cancel:      cancel,
		generation:  generation,
		pin:         pin,
		cfg:         cloneRemoteConfig(cfg),
		material:    material,
		resolver:    rl,
		listener:    tls.NewListener(raw, remoteTLSConfig(cert)),
		connections: make(map[net.Conn]*TailnetIdentity),
	}

	return rt, nil
}

func (c *remoteController) stop() {
	if c.runtime == nil {
		return
	}

	c.sm.invalidateRemoteGeneration()
	c.runtime.stop()
	c.runtime = nil
}

// discardPrepared closes a replacement that was bound but could not be
// published because another runtime transition in the same config generation
// failed. The old listener was already closed before preparation, so rollback
// remains fail-closed rather than trying to resurrect stale transport state.
func (c *remoteController) discardPrepared() {
	if c.runtime != nil {
		c.runtime.stop()
		c.runtime = nil
	}

	c.sm.invalidateRemoteGeneration()
}

type remoteMaterial struct {
	authKey string
	tlsPair string
}

func remoteMaterialState(cfg config.RemoteConfig, stateDir string) (remoteMaterial, error) {
	var material remoteMaterial

	if cfg.AuthKeyFile != "" {
		fingerprint, err := remoteFileFingerprint(config.ExpandPath(cfg.AuthKeyFile))
		if err != nil {
			return remoteMaterial{}, fmt.Errorf("read auth_key_file: %w", err)
		}

		material.authKey = fingerprint
	}

	tlsPair, err := remoteTLSMaterialFingerprint(stateDir)
	if err != nil {
		return remoteMaterial{}, err
	}

	material.tlsPair = tlsPair

	return material, nil
}

func remoteTLSMaterialFingerprint(stateDir string) (string, error) {
	certPath := filepath.Join(stateDir, "remote-tls.crt")
	keyPath := filepath.Join(stateDir, "remote-tls.key")
	pairBytes := make([]byte, 0)

	for _, path := range []string{certPath, keyPath} {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}

		pairBytes = append(pairBytes, b...)
		pairBytes = append(pairBytes, 0)
	}

	return fmt.Sprintf("%x", sha256.Sum256(pairBytes)), nil
}

func remoteFileFingerprint(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

func cloneRemoteConfig(cfg config.RemoteConfig) config.RemoteConfig {
	cloned := cfg
	cloned.Tags = append([]string(nil), cfg.Tags...)
	cloned.AllowTailnetUsers = append([]string(nil), cfg.AllowTailnetUsers...)

	return cloned
}

func remoteTransportEqual(a, b config.RemoteConfig) bool {
	return a.Mode == b.Mode &&
		a.Hostname == b.Hostname &&
		a.Port == b.Port &&
		a.AuthKeyFile == b.AuthKeyFile &&
		slices.Equal(a.Tags, b.Tags)
}

type remoteRuntime struct {
	sm         *SessionManager
	ctx        context.Context
	cancel     context.CancelFunc
	generation uint64
	pin        string
	cfg        config.RemoteConfig
	material   remoteMaterial
	resolver   RemoteListener
	listener   net.Listener

	mu          sync.Mutex
	stopping    bool
	connections map[net.Conn]*TailnetIdentity
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

func (r *remoteRuntime) matches(cfg config.RemoteConfig, stateDir string) bool {
	if !remoteTransportEqual(r.cfg, cfg) {
		return false
	}

	// TLS material is continuously required by the active listener, so any
	// change or read failure must replace the generation. A tsnet auth key is
	// different: it is consumed while creating the node and is not needed by an
	// already-running generation. If its unchanged path later becomes
	// unreadable, keep the active listener so an unrelated or security-policy
	// reload can still apply. A transport change remains strict and build will
	// fail closed until the key is readable again.
	tlsPair, err := remoteTLSMaterialFingerprint(stateDir)
	if err != nil || tlsPair != r.material.tlsPair {
		return false
	}

	if cfg.AuthKeyFile == "" {
		return r.material.authKey == ""
	}

	authKey, err := remoteFileFingerprint(config.ExpandPath(cfg.AuthKeyFile))
	if err != nil {
		r.sm.log.Warn("remote auth key unavailable; retaining active listener generation",
			"generation", r.generation,
		)

		return true
	}

	return authKey == r.material.authKey
}

func (r *remoteRuntime) activate() {
	r.sm.log.Info("[remote] listening",
		"addr", r.listener.Addr().String(),
		"mode", r.cfg.Mode,
		"port", r.cfg.Port,
		"generation", r.generation,
		"tls_spki", r.pin,
	)

	// Register the accept loop before the cancellation watcher can call stop and
	// wait, so WaitGroup.Add never races a Wait.
	r.wg.Add(1)
	go r.serve()

	go func() {
		<-r.ctx.Done()
		r.stop()
	}()
}

func (r *remoteRuntime) serve() {
	defer r.wg.Done()

	for {
		conn, err := r.listener.Accept()
		if err != nil {
			r.mu.Lock()
			stopping := r.stopping
			r.mu.Unlock()

			if stopping || r.ctx.Err() != nil {
				return
			}

			r.sm.log.Warn("remote accept error", "err", err, "generation", r.generation)

			continue
		}

		if !r.track(conn) {
			_ = conn.Close()

			continue
		}

		go r.handle(conn)
	}
}

func (r *remoteRuntime) track(conn net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopping {
		return false
	}

	// Add while holding the same lock stop uses to prohibit future tracks. Once
	// stop sets stopping, no Add can race its Wait.
	r.wg.Add(1)
	r.connections[conn] = nil

	return true
}

func (r *remoteRuntime) handle(conn net.Conn) {
	defer r.wg.Done()
	defer r.untrack(conn)

	id, err := r.resolver.WhoIs(r.ctx, conn.RemoteAddr().String())
	if err != nil || id == nil {
		r.sm.log.Warn("remote connection rejected by Gate 1", "addr", conn.RemoteAddr().String(), "err", err)
		_ = conn.Close()

		return
	}

	if !r.setIdentity(conn, id) {
		_ = conn.Close()

		return
	}

	origin := ConnOrigin{
		Remote:           true,
		Identity:         id,
		RemoteGeneration: r.generation,
		RemoteTLSPin:     r.pin,
	}
	if !r.sm.remoteOriginAllowed(origin) {
		r.sm.log.Warn("remote connection rejected by live Gate 1", "addr", conn.RemoteAddr().String())
		_ = conn.Close()

		return
	}

	HandleConnection(r.ctx, conn, origin, r.sm, r.sm.log)
}

func (r *remoteRuntime) setIdentity(conn net.Conn, id *TailnetIdentity) bool {
	cloned := *id
	cloned.Tags = append([]string(nil), id.Tags...)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopping {
		return false
	}

	if _, ok := r.connections[conn]; !ok {
		return false
	}

	r.connections[conn] = &cloned

	return true
}

func (r *remoteRuntime) untrack(conn net.Conn) {
	r.mu.Lock()
	delete(r.connections, conn)
	r.mu.Unlock()

	_ = conn.Close()
}

// closeUnauthorized proactively closes identities removed from the allowlist.
// Unknown identities are also closed: their WhoIs was racing the reload, so
// retaining them would create an authorization gap between policy publication
// and the handler's next frame check.
func (r *remoteRuntime) closeUnauthorized(cfg config.RemoteConfig) {
	r.mu.Lock()
	connections := make([]net.Conn, 0)

	for conn, id := range r.connections {
		if !cfg.Enabled || !identityAllowed(cfg, id) {
			connections = append(connections, conn)
		}
	}

	r.mu.Unlock()

	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (r *remoteRuntime) stop() {
	r.stopOnce.Do(func() {
		r.cancel()

		r.mu.Lock()
		r.stopping = true

		connections := make([]net.Conn, 0, len(r.connections))
		for conn := range r.connections {
			connections = append(connections, conn)
		}
		r.mu.Unlock()

		_ = r.listener.Close()
		_ = r.resolver.Close()

		for _, conn := range connections {
			_ = conn.Close()
		}

		r.wg.Wait()
	})
}

func (sm *SessionManager) invalidateRemoteGeneration() {
	sm.mu.Lock()
	sm.remoteGeneration = 0
	sm.remoteTLSPin = ""
	sm.mu.Unlock()
}

// startRemoteRuntime attaches the runtime controller to a serving daemon and
// provisions the startup config. Startup errors leave the local daemon running
// with remote access closed, matching the existing fail-closed startup policy.
func (sm *SessionManager) startRemoteRuntime(ctx context.Context) error {
	sm.configReloadMu.Lock()
	defer sm.configReloadMu.Unlock()

	sm.remote = newRemoteController(ctx, sm, sm.paths.DataDir)
	cfg := sm.Config().Remote

	runtime, changed, err := sm.remote.prepare(cfg)
	if err != nil {
		return err
	}

	if runtime != nil {
		sm.mu.Lock()
		sm.remoteGeneration = runtime.generation
		sm.remoteTLSPin = runtime.pin
		sm.mu.Unlock()

		if changed {
			runtime.activate()
		}
	}

	return nil
}

func (sm *SessionManager) stopRemoteRuntime() {
	sm.configReloadMu.Lock()
	defer sm.configReloadMu.Unlock()

	if sm.remote != nil {
		sm.remote.stop()
	}
}
