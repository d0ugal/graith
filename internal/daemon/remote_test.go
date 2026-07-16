package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

type fakeRemoteListener struct {
	ln net.Listener

	mu        sync.Mutex
	closed    bool
	whoIs     func(context.Context, string) (*TailnetIdentity, error)
	listenErr error
}

func newFakeRemoteListener(t *testing.T) *fakeRemoteListener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	return &fakeRemoteListener{
		ln: ln,
		whoIs: func(context.Context, string) (*TailnetIdentity, error) {
			return &TailnetIdentity{User: "speir@example.com", Node: "ben"}, nil
		},
	}
}

func (l *fakeRemoteListener) Listen() (net.Listener, error) {
	if l.listenErr != nil {
		return nil, l.listenErr
	}

	return l.ln, nil
}

func (l *fakeRemoteListener) WhoIs(ctx context.Context, addr string) (*TailnetIdentity, error) {
	return l.whoIs(ctx, addr)
}

func (l *fakeRemoteListener) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()

	return l.ln.Close()
}

func (l *fakeRemoteListener) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.closed
}

type observedConn struct {
	net.Conn

	mu     sync.Mutex
	closed bool
}

func (c *observedConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()

	return c.Conn.Close()
}

func (c *observedConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed
}

func enabledRemoteConfig(users ...string) config.RemoteConfig {
	return config.RemoteConfig{
		Enabled:           true,
		Mode:              "interface",
		Port:              4823,
		RequirePairing:    true,
		AllowTailnetUsers: users,
	}
}

func installRemoteRuntimeForTest(
	t *testing.T,
	sm *SessionManager,
	factory remoteListenerFactory,
) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	sm.remoteRuntime = &remoteRuntime{
		ctx:         ctx,
		dataDir:     t.TempDir(),
		newListener: factory,
		loadTLS: func(string, string, string, time.Time) (tls.Certificate, string, error) {
			return tls.Certificate{}, "canny-test-pin", nil
		},
		now: time.Now,
	}

	t.Cleanup(func() {
		cancel()
		sm.stopRemoteRuntime()
	})
}

func addObservedGenerationConn(t *testing.T, gen *remoteGeneration, id *TailnetIdentity) *observedConn {
	t.Helper()

	client, server := net.Pipe()

	t.Cleanup(func() { _ = client.Close() })

	conn := &observedConn{Conn: server}

	gen.mu.Lock()
	gen.conns[conn] = id
	gen.mu.Unlock()

	return conn
}

func TestIdentityAllowed(t *testing.T) {
	user := &TailnetIdentity{User: "speir@example.com", Node: "ben"}
	tagged := &TailnetIdentity{Node: "brae", Tags: []string{"tag:graith"}}

	tests := []struct {
		name  string
		allow []string
		id    *TailnetIdentity
		want  bool
	}{
		{"empty allowlist denies user", nil, user, false},
		{"empty allowlist denies tagged", nil, tagged, false},
		{"nil identity denied", []string{"speir@example.com"}, nil, false},
		{"matching user allowed", []string{"speir@example.com"}, user, true},
		{"non-matching user denied", []string{"fash@example.com"}, user, false},
		{"tag entry admits tagged node", []string{"tag:graith"}, tagged, true},
		{"wrong tag denied", []string{"tag:other"}, tagged, false},
		{"tagged node not admitted by a user entry", []string{"speir@example.com"}, tagged, false},
		{"user not admitted by a tag entry", []string{"tag:graith"}, user, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.RemoteConfig{AllowTailnetUsers: tt.allow}
			if got := identityAllowed(cfg, tt.id); got != tt.want {
				t.Errorf("identityAllowed(%v, %+v) = %v, want %v", tt.allow, tt.id, got, tt.want)
			}
		})
	}
}

func TestIdentityFromWhoIs(t *testing.T) {
	if identityFromWhoIs(nil) != nil {
		t.Error("nil WhoIs must map to nil identity")
	}

	w := &apitype.WhoIsResponse{
		UserProfile: &tailcfg.UserProfile{LoginName: "speir@example.com"},
		Node:        &tailcfg.Node{StableID: tailcfg.StableNodeID("nBEN"), Tags: []string{"tag:graith"}},
	}

	id := identityFromWhoIs(w)
	if id == nil {
		t.Fatal("expected an identity")
	}

	if id.User != "speir@example.com" {
		t.Errorf("User = %q, want speir@example.com", id.User)
	}

	if id.Node != "nBEN" {
		t.Errorf("Node = %q, want nBEN", id.Node)
	}

	if len(id.Tags) != 1 || id.Tags[0] != "tag:graith" {
		t.Errorf("Tags = %v, want [tag:graith]", id.Tags)
	}
}

func TestNewRemoteListenerUnknownMode(t *testing.T) {
	if _, err := newRemoteListener(t.Context(), config.RemoteConfig{Mode: "wheesht"}, t.TempDir()); err == nil {
		t.Error("expected error for unknown remote mode")
	}
}

func TestNewTSNetListenerAppliesTags(t *testing.T) {
	l, err := newTSNetListener(config.RemoteConfig{
		Hostname: "canny",
		Port:     4823,
		Tags:     []string{"tag:graith", "tag:bothy"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if len(l.srv.AdvertiseTags) != 2 || l.srv.AdvertiseTags[0] != "tag:graith" || l.srv.AdvertiseTags[1] != "tag:bothy" {
		t.Errorf("AdvertiseTags = %v, want configured tags", l.srv.AdvertiseTags)
	}
}

func TestRemoteRuntimeEnabledTransitionsCloseActiveGeneration(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = config.RemoteConfig{}
	sm := newSMWithConfig(t, cfg)

	var made []*fakeRemoteListener

	installRemoteRuntimeForTest(t, sm, func(context.Context, config.RemoteConfig, string) (RemoteListener, error) {
		l := newFakeRemoteListener(t)
		made = append(made, l)

		return l, nil
	})

	enabled := config.Default()
	enabled.Remote = enabledRemoteConfig("speir@example.com")

	if err := sm.applyConfig(enabled); err != nil {
		t.Fatalf("enable remote: %v", err)
	}

	if sm.remoteRuntime.generation == nil || len(made) != 1 {
		t.Fatalf("enable did not start one generation: generation=%v listeners=%d", sm.remoteRuntime.generation, len(made))
	}

	active := addObservedGenerationConn(t, sm.remoteRuntime.generation, &TailnetIdentity{User: "speir@example.com", Node: "ben"})

	disabled := config.Default()
	disabled.Remote = config.RemoteConfig{}

	if err := sm.applyConfig(disabled); err != nil {
		t.Fatalf("disable remote: %v", err)
	}

	if sm.remoteRuntime.generation != nil {
		t.Fatal("disable left a remote generation active")
	}

	if !made[0].isClosed() || !active.isClosed() {
		t.Errorf("disable did not close listener/active conn: listener=%v conn=%v", made[0].isClosed(), active.isClosed())
	}

	if sm.RemoteTLSPin() != "" {
		t.Errorf("disable left TLS pin %q published", sm.RemoteTLSPin())
	}
}

func TestRemoteTransportReplacementClosesOldBeforePreparingNew(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = enabledRemoteConfig("speir@example.com")
	sm := newSMWithConfig(t, cfg)

	var made []*fakeRemoteListener

	installRemoteRuntimeForTest(t, sm, func(context.Context, config.RemoteConfig, string) (RemoteListener, error) {
		if len(made) == 1 && !made[0].isClosed() {
			t.Error("replacement factory called before old listener closed")
		}

		l := newFakeRemoteListener(t)
		made = append(made, l)

		return l, nil
	})

	if err := sm.startRemoteRuntime(cfg.Remote); err != nil {
		t.Fatal(err)
	}

	active := addObservedGenerationConn(t, sm.remoteRuntime.generation, &TailnetIdentity{User: "speir@example.com", Node: "ben"})

	next := *cfg
	next.Remote = cfg.Remote
	next.Remote.Port = 4923

	if err := sm.applyConfig(&next); err != nil {
		t.Fatalf("replace remote: %v", err)
	}

	if len(made) != 2 || !made[0].isClosed() || !active.isClosed() {
		t.Fatalf("replacement lifecycle: listeners=%d old_closed=%v conn_closed=%v", len(made), made[0].isClosed(), active.isClosed())
	}

	if sm.Config().Remote.Port != 4923 || sm.remoteRuntime.generation == nil {
		t.Errorf("replacement not committed: port=%d generation=%v", sm.Config().Remote.Port, sm.remoteRuntime.generation)
	}
}

func TestRemoteTransportReplacementJoinsStalledWhoIs(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = enabledRemoteConfig("speir@example.com")
	sm := newSMWithConfig(t, cfg)

	whoStarted := make(chan struct{})
	whoExited := make(chan struct{})

	var made []*fakeRemoteListener

	installRemoteRuntimeForTest(t, sm, func(context.Context, config.RemoteConfig, string) (RemoteListener, error) {
		l := newFakeRemoteListener(t)
		if len(made) == 0 {
			l.whoIs = func(ctx context.Context, _ string) (*TailnetIdentity, error) {
				close(whoStarted)
				defer close(whoExited)

				<-ctx.Done()

				return nil, ctx.Err()
			}
		}

		made = append(made, l)

		return l, nil
	})

	if err := sm.startRemoteRuntime(cfg.Remote); err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", made[0].ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	<-whoStarted

	next := *cfg
	next.Remote = cfg.Remote
	next.Remote.Port++

	if err := sm.applyConfig(&next); err != nil {
		t.Fatal(err)
	}

	select {
	case <-whoExited:
		// The old authentication goroutine was joined before apply returned.
	default:
		t.Fatal("replacement returned before the old WhoIs goroutine exited")
	}

	if len(made) != 2 || !made[0].isClosed() {
		t.Errorf("replacement lifecycle: listeners=%d old_closed=%v", len(made), made[0].isClosed())
	}
}

func TestRemoteReplacementFailureRejectsConfigAndStaysClosed(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = enabledRemoteConfig("speir@example.com")
	sm := newSMWithConfig(t, cfg)

	first := newFakeRemoteListener(t)
	calls := 0

	installRemoteRuntimeForTest(t, sm, func(context.Context, config.RemoteConfig, string) (RemoteListener, error) {
		calls++
		if calls == 1 {
			return first, nil
		}

		if !first.isClosed() {
			t.Error("replacement attempted before old generation closed")
		}

		return nil, errors.New("dreich bind failure")
	})

	if err := sm.startRemoteRuntime(cfg.Remote); err != nil {
		t.Fatal(err)
	}

	active := addObservedGenerationConn(t, sm.remoteRuntime.generation, &TailnetIdentity{User: "speir@example.com", Node: "ben"})
	next := *cfg
	next.Remote = cfg.Remote
	next.Remote.Port = 4923

	err := sm.applyConfig(&next)
	if err == nil || !strings.Contains(err.Error(), "dreich bind failure") {
		t.Fatalf("replacement failure = %v, want explicit error", err)
	}

	if sm.Config().Remote.Port != cfg.Remote.Port {
		t.Errorf("failed replacement published port %d, want old %d", sm.Config().Remote.Port, cfg.Remote.Port)
	}

	if sm.remoteRuntime.generation != nil || !first.isClosed() || !active.isClosed() {
		t.Errorf("failed replacement did not stay closed: generation=%v listener=%v conn=%v", sm.remoteRuntime.generation, first.isClosed(), active.isClosed())
	}

	if sm.RemoteTLSPin() != "" {
		t.Errorf("failed replacement left stale TLS pin %q published", sm.RemoteTLSPin())
	}
}

func TestRemoteAllowlistTightenAndExpandAffectLiveAndFutureAccess(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = enabledRemoteConfig("speir@example.com")
	sm := newSMWithConfig(t, cfg)

	installRemoteRuntimeForTest(t, sm, func(context.Context, config.RemoteConfig, string) (RemoteListener, error) {
		return newFakeRemoteListener(t), nil
	})

	if err := sm.startRemoteRuntime(cfg.Remote); err != nil {
		t.Fatal(err)
	}

	speir := &TailnetIdentity{User: "speir@example.com", Node: "ben"}
	canny := &TailnetIdentity{User: "canny@example.com", Node: "brae"}
	active := addObservedGenerationConn(t, sm.remoteRuntime.generation, speir)

	expanded := *cfg
	expanded.Remote = cfg.Remote
	expanded.Remote.AllowTailnetUsers = []string{"speir@example.com", "canny@example.com"}

	if err := sm.applyConfig(&expanded); err != nil {
		t.Fatal(err)
	}

	if active.isClosed() {
		t.Error("allowlist expansion closed an identity that remained allowed")
	}

	if !sm.remotePolicyAllows(canny) {
		t.Error("allowlist expansion did not admit matching future identity")
	}

	tightened := expanded
	tightened.Remote = expanded.Remote
	tightened.Remote.AllowTailnetUsers = []string{"canny@example.com"}

	if err := sm.applyConfig(&tightened); err != nil {
		t.Fatal(err)
	}

	if !active.isClosed() {
		t.Error("allowlist tightening did not close the revoked active identity")
	}

	if sm.remotePolicyAllows(speir) || !sm.remotePolicyAllows(canny) {
		t.Errorf("tightened live policy mismatch: speir=%v canny=%v", sm.remotePolicyAllows(speir), sm.remotePolicyAllows(canny))
	}
}

func TestRemoteRequirePairingChangeClosesConnections(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = enabledRemoteConfig("speir@example.com")
	sm := newSMWithConfig(t, cfg)

	installRemoteRuntimeForTest(t, sm, func(context.Context, config.RemoteConfig, string) (RemoteListener, error) {
		return newFakeRemoteListener(t), nil
	})

	if err := sm.startRemoteRuntime(cfg.Remote); err != nil {
		t.Fatal(err)
	}

	active := addObservedGenerationConn(t, sm.remoteRuntime.generation, &TailnetIdentity{User: "speir@example.com", Node: "ben"})
	next := *cfg
	next.Remote = cfg.Remote
	next.Remote.RequirePairing = false

	if err := sm.applyConfig(&next); err != nil {
		t.Fatal(err)
	}

	if !active.isClosed() {
		t.Error("require_pairing transition did not close active connections")
	}
}

func TestRemoteAcceptUsesLivePolicyAfterWhoIsRace(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = enabledRemoteConfig("speir@example.com")
	sm := newSMWithConfig(t, cfg)

	release := make(chan struct{})
	whoStarted := make(chan struct{})

	rl := newFakeRemoteListener(t)
	defer func() { _ = rl.Close() }()

	rl.whoIs = func(ctx context.Context, _ string) (*TailnetIdentity, error) {
		close(whoStarted)

		select {
		case <-release:
			return &TailnetIdentity{User: "speir@example.com", Node: "ben"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	gen := &remoteGeneration{
		sm: sm, rl: rl, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), conns: make(map[net.Conn]*TailnetIdentity),
	}

	if !gen.track(server) {
		t.Fatal("failed to track test connection")
	}

	go gen.handle(server)

	<-whoStarted

	tightened := *cfg
	tightened.Remote = cfg.Remote
	tightened.Remote.AllowTailnetUsers = []string{"canny@example.com"}

	if err := sm.applyConfig(&tightened); err != nil {
		t.Fatal(err)
	}

	close(release)

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection accepted under stale allowlist after reload")
	}

	gen.wg.Wait()
}

func TestRemotePolicyReloadRace(t *testing.T) {
	cfg := config.Default()
	cfg.Remote = enabledRemoteConfig("speir@example.com")
	sm := newSMWithConfig(t, cfg)
	id := &TailnetIdentity{User: "speir@example.com", Node: "ben"}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for range 500 {
				_ = sm.remotePolicyAllows(id)
				sm.mu.RLock()
				_, _ = resolveAuth(sm, "", ConnOrigin{Remote: true, Identity: id}, "")
				sm.mu.RUnlock()
			}
		}()
	}

	for i := range 100 {
		next := *cfg
		next.Remote = cfg.Remote

		if i%2 == 0 {
			next.Remote.AllowTailnetUsers = []string{"canny@example.com"}
		}

		if err := sm.applyConfig(&next); err != nil {
			t.Fatal(err)
		}
	}

	wg.Wait()

	final := *cfg
	final.Remote = cfg.Remote
	final.Remote.AllowTailnetUsers = []string{"canny@example.com"}

	if err := sm.applyConfig(&final); err != nil {
		t.Fatal(err)
	}

	if sm.remotePolicyAllows(id) {
		t.Error("final tightened policy still allowed revoked identity")
	}
}
