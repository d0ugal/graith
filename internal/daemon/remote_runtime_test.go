package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

type fakeRemoteListener struct {
	mu        sync.Mutex
	listener  net.Listener
	listenErr error
	whoIs     func(context.Context, string) (*TailnetIdentity, error)
	closed    int
}

func (l *fakeRemoteListener) Listen() (net.Listener, error) {
	if l.listenErr != nil {
		return nil, l.listenErr
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.listener = listener
	l.mu.Unlock()

	return listener, nil
}

func (l *fakeRemoteListener) WhoIs(ctx context.Context, remoteAddr string) (*TailnetIdentity, error) {
	if l.whoIs != nil {
		return l.whoIs(ctx, remoteAddr)
	}

	return &TailnetIdentity{User: "speir@example.com", Node: "ben"}, nil
}

func (l *fakeRemoteListener) Close() error {
	l.mu.Lock()
	l.closed++
	listener := l.listener
	l.mu.Unlock()

	if listener != nil {
		return listener.Close()
	}

	return nil
}

func (l *fakeRemoteListener) closeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.closed
}

type fakeRemoteFactory struct {
	mu        sync.Mutex
	configs   []config.RemoteConfig
	listeners []*fakeRemoteListener
	failPort  int
	whoIs     func(context.Context, string) (*TailnetIdentity, error)
}

func (f *fakeRemoteFactory) new(_ context.Context, cfg config.RemoteConfig, _ string) (RemoteListener, error) {
	l := &fakeRemoteListener{whoIs: f.whoIs}
	if cfg.Port == f.failPort {
		l.listenErr = errors.New("braw bind failed")
	}

	f.mu.Lock()
	f.configs = append(f.configs, cloneRemoteConfig(cfg))
	f.listeners = append(f.listeners, l)
	f.mu.Unlock()

	return l, nil
}

func (f *fakeRemoteFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.listeners)
}

func (f *fakeRemoteFactory) listener(i int) *fakeRemoteListener {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.listeners[i]
}

func remoteRuntimeTestConfig(enabled bool) *config.Config {
	cfg := config.Default()
	cfg.Remote.Enabled = enabled
	cfg.Remote.Mode = "tsnet"
	cfg.Remote.Hostname = "ben"
	cfg.Remote.Port = 4823
	cfg.Remote.RequirePairing = true
	cfg.Remote.AllowTailnetUsers = []string{"speir@example.com"}

	return cfg
}

func newRemoteRuntimeTestSM(t *testing.T, cfg *config.Config, factory *fakeRemoteFactory) *SessionManager {
	t.Helper()

	dataDir := t.TempDir()
	paths := config.Paths{
		DataDir:   dataDir,
		StateFile: filepath.Join(dataDir, "state.json"),
	}
	sm := NewSessionManager(cfg, paths, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())

	sm.configReloadMu.Lock()
	sm.remote = newRemoteController(ctx, sm, dataDir)
	sm.remote.newListener = factory.new
	sm.configReloadMu.Unlock()

	t.Cleanup(func() {
		cancel()
		sm.stopRemoteRuntime()
	})

	return sm
}

func applyRemoteTestConfig(t *testing.T, sm *SessionManager, cfg *config.Config) {
	t.Helper()

	if err := sm.applyConfig(cfg); err != nil {
		t.Fatalf("applyConfig() error = %v", err)
	}
}

func TestRemoteRuntimeReloadEnableDisable(t *testing.T) {
	factory := &fakeRemoteFactory{}
	sm := newRemoteRuntimeTestSM(t, remoteRuntimeTestConfig(false), factory)

	enabled := remoteRuntimeTestConfig(true)
	applyRemoteTestConfig(t, sm, enabled)

	if factory.count() != 1 || sm.remote.runtime == nil {
		t.Fatalf("enable did not start one runtime: factory=%d runtime=%v", factory.count(), sm.remote.runtime)
	}

	sm.mu.RLock()
	activeGeneration := sm.remoteGeneration
	sm.mu.RUnlock()

	if activeGeneration == 0 {
		t.Fatal("enable left remote generation inactive")
	}

	disabled := *enabled
	disabled.Remote.Enabled = false
	applyRemoteTestConfig(t, sm, &disabled)

	if sm.remote.runtime != nil {
		t.Fatal("disable retained a remote runtime")
	}

	if factory.listener(0).closeCount() == 0 {
		t.Fatal("disable did not close the old remote listener")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.remoteGeneration != 0 || sm.remoteTLSPin != "" || sm.cfg.Remote.Enabled {
		t.Fatalf("disabled state = generation %d pin %q enabled %v", sm.remoteGeneration, sm.remoteTLSPin, sm.cfg.Remote.Enabled)
	}
}

func TestRemoteRuntimeReloadReplacesEveryTransportSetting(t *testing.T) {
	authKey := filepath.Join(t.TempDir(), "tsnet.key")
	if err := os.WriteFile(authKey, []byte("braw-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*config.RemoteConfig)
	}{
		{name: "mode", mutate: func(r *config.RemoteConfig) { r.Mode = "interface" }},
		{name: "hostname", mutate: func(r *config.RemoteConfig) { r.Hostname = "canny" }},
		{name: "port", mutate: func(r *config.RemoteConfig) { r.Port++ }},
		{name: "auth key file", mutate: func(r *config.RemoteConfig) { r.AuthKeyFile = authKey }},
		{name: "tags", mutate: func(r *config.RemoteConfig) { r.Tags = []string{"tag:croft"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := &fakeRemoteFactory{}
			initial := remoteRuntimeTestConfig(true)
			sm := newRemoteRuntimeTestSM(t, initial, factory)
			applyRemoteTestConfig(t, sm, initial)

			changed := *initial
			changed.Remote = cloneRemoteConfig(initial.Remote)
			tt.mutate(&changed.Remote)
			applyRemoteTestConfig(t, sm, &changed)

			if factory.count() != 2 {
				t.Fatalf("listener creations = %d, want 2", factory.count())
			}

			if factory.listener(0).closeCount() == 0 {
				t.Fatal("transport reload did not close the old listener")
			}
		})
	}
}

func TestRemoteRuntimeReloadTracksAuthKeyAndTLSMaterial(t *testing.T) {
	t.Run("auth key contents", func(t *testing.T) {
		authKey := filepath.Join(t.TempDir(), "tsnet.key")
		if err := os.WriteFile(authKey, []byte("braw-key\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		factory := &fakeRemoteFactory{}
		cfg := remoteRuntimeTestConfig(true)
		cfg.Remote.AuthKeyFile = authKey
		sm := newRemoteRuntimeTestSM(t, cfg, factory)
		applyRemoteTestConfig(t, sm, cfg)

		if err := os.WriteFile(authKey, []byte("canny-key\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		applyRemoteTestConfig(t, sm, cfg)

		if factory.count() != 2 {
			t.Fatalf("auth-key rotation listener creations = %d, want 2", factory.count())
		}
	})

	t.Run("TLS generation", func(t *testing.T) {
		factory := &fakeRemoteFactory{}
		cfg := remoteRuntimeTestConfig(true)
		sm := newRemoteRuntimeTestSM(t, cfg, factory)
		applyRemoteTestConfig(t, sm, cfg)

		if err := os.WriteFile(filepath.Join(sm.paths.DataDir, "remote-tls.crt"), []byte("dreich"), 0o600); err != nil {
			t.Fatal(err)
		}

		applyRemoteTestConfig(t, sm, cfg)

		if factory.count() != 2 {
			t.Fatalf("TLS rotation listener creations = %d, want 2", factory.count())
		}
	})
}

func TestRemoteRuntimeReloadRetainsConsumedAuthKeyUntilReplacement(t *testing.T) {
	authKey := filepath.Join(t.TempDir(), "tsnet.key")
	if err := os.WriteFile(authKey, []byte("braw-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	factory := &fakeRemoteFactory{}
	initial := remoteRuntimeTestConfig(true)
	initial.Remote.AuthKeyFile = authKey
	sm := newRemoteRuntimeTestSM(t, initial, factory)

	var logs bytes.Buffer

	sm.log = slog.New(slog.NewTextHandler(&logs, nil))
	applyRemoteTestConfig(t, sm, initial)
	logs.Reset()

	sm.mu.RLock()
	initialGeneration := sm.remoteGeneration
	sm.mu.RUnlock()

	if err := os.Remove(authKey); err != nil {
		t.Fatal(err)
	}

	// The active tsnet node has consumed its one-shot key. Its absence must not
	// block an unrelated change or, critically, a live allowlist tightening.
	changed := *initial
	changed.Remote = cloneRemoteConfig(initial.Remote)
	changed.BranchPrefix = "canny/"
	changed.Remote.AllowTailnetUsers = []string{"canny@example.com"}
	applyRemoteTestConfig(t, sm, &changed)

	if logText := logs.String(); !strings.Contains(logText, "remote auth key unavailable") || strings.Contains(logText, authKey) {
		t.Fatalf("auth-key warning leaked its path or was absent: %q", logText)
	}

	sm.mu.RLock()
	appliedGeneration := sm.remoteGeneration
	appliedConfig := sm.cfg
	sm.mu.RUnlock()

	if factory.count() != 1 || factory.listener(0).closeCount() != 0 {
		t.Fatalf("unchanged transport recreated or closed listener: creations=%d closes=%d", factory.count(), factory.listener(0).closeCount())
	}

	if appliedGeneration != initialGeneration {
		t.Fatalf("generation changed from %d to %d", initialGeneration, appliedGeneration)
	}

	if appliedConfig.BranchPrefix != "canny/" || !slices.Equal(appliedConfig.Remote.AllowTailnetUsers, []string{"canny@example.com"}) {
		t.Fatalf("reload was not published: branch_prefix=%q allowlist=%v", appliedConfig.BranchPrefix, appliedConfig.Remote.AllowTailnetUsers)
	}

	// A real transport replacement still needs the key. It closes the old
	// generation first and leaves the successfully-applied config visible.
	replacement := changed
	replacement.Remote = cloneRemoteConfig(changed.Remote)
	replacement.Remote.Hostname = "croft"

	err := sm.applyConfig(&replacement)
	if err == nil || !strings.Contains(err.Error(), "read auth_key_file") {
		t.Fatalf("replacement error = %v, want unreadable auth_key_file", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.remoteGeneration != 0 || sm.remoteTLSPin != "" {
		t.Fatalf("failed replacement left generation=%d pin=%q", sm.remoteGeneration, sm.remoteTLSPin)
	}

	if sm.cfg != appliedConfig || factory.listener(0).closeCount() == 0 {
		t.Fatal("failed replacement did not preserve config and close the old listener")
	}
}

func TestRemoteRuntimePolicyReloadKeepsTransport(t *testing.T) {
	factory := &fakeRemoteFactory{}
	initial := remoteRuntimeTestConfig(true)
	sm := newRemoteRuntimeTestSM(t, initial, factory)
	applyRemoteTestConfig(t, sm, initial)

	changed := *initial
	changed.Remote = cloneRemoteConfig(initial.Remote)
	changed.Remote.AllowTailnetUsers = []string{"tag:croft"}
	changed.Remote.RequirePairing = false
	changed.Remote.PairRequestRate = "7/min"
	changed.Remote.MaxPendingPairings = 23
	changed.Remote.PendingPairingTTL = "12m"
	applyRemoteTestConfig(t, sm, &changed)

	if factory.count() != 1 {
		t.Fatalf("policy-only reload created %d listeners, want 1", factory.count())
	}

	if factory.listener(0).closeCount() != 0 {
		t.Fatal("policy-only reload closed the listener")
	}
}

func TestRemoteRuntimeAllowlistTightenAndExpand(t *testing.T) {
	factory := &fakeRemoteFactory{}
	initial := remoteRuntimeTestConfig(true)
	sm := newRemoteRuntimeTestSM(t, initial, factory)
	applyRemoteTestConfig(t, sm, initial)
	addr := sm.remote.runtime.listener.Addr().String()

	client, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // Test-only self-signed listener.
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	tightened := *initial
	tightened.Remote = cloneRemoteConfig(initial.Remote)
	tightened.Remote.AllowTailnetUsers = []string{"canny@example.com"}
	applyRemoteTestConfig(t, sm, &tightened)

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, readErr := client.Read(make([]byte, 1)); readErr == nil {
		t.Fatal("allowlist tightening retained an existing connection")
	}

	if factory.count() != 1 || factory.listener(0).closeCount() != 0 {
		t.Fatal("allowlist tightening replaced the listener instead of only revoking connections")
	}

	expanded := tightened
	expanded.Remote = cloneRemoteConfig(tightened.Remote)
	expanded.Remote.AllowTailnetUsers = []string{"speir@example.com"}
	applyRemoteTestConfig(t, sm, &expanded)

	future, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // Test-only self-signed listener.
	})
	if err != nil {
		t.Fatalf("future connection after allowlist expansion: %v", err)
	}

	_ = future.Close()

	if factory.count() != 1 {
		t.Fatalf("allowlist expansion created %d listeners, want 1", factory.count())
	}
}

func TestRemoteRuntimeReplacementFailureClosesOldAndRollsBackConfig(t *testing.T) {
	factory := &fakeRemoteFactory{failPort: 4924}
	initial := remoteRuntimeTestConfig(true)
	sm := newRemoteRuntimeTestSM(t, initial, factory)
	applyRemoteTestConfig(t, sm, initial)
	oldGeneration := sm.remote.runtime.generation

	client, err := tls.Dial("tcp", sm.remote.runtime.listener.Addr().String(), &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // Test-only self-signed listener.
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = client.Close() }()

	broken := *initial
	broken.Remote = cloneRemoteConfig(initial.Remote)

	broken.Remote.Port = factory.failPort

	err = sm.applyConfig(&broken)
	if err == nil || !strings.Contains(err.Error(), "previous config remains active") {
		t.Fatalf("replacement error = %v, want explicit rollback error", err)
	}

	if factory.listener(0).closeCount() == 0 {
		t.Fatal("failed replacement retained the old listener")
	}

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, readErr := client.Read(make([]byte, 1)); readErr == nil {
		t.Fatal("failed replacement retained an old-generation connection")
	}

	if sm.remote.runtime != nil {
		t.Fatal("failed replacement retained a runtime")
	}

	sm.mu.RLock()

	if sm.cfg.Remote.Port != initial.Remote.Port || sm.remoteGeneration != 0 || sm.remoteTLSPin != "" {
		t.Errorf("after failure config port=%d generation=%d pin=%q", sm.cfg.Remote.Port, sm.remoteGeneration, sm.remoteTLSPin)
	}

	sm.mu.RUnlock()

	// The published config still equals initial, but the actual runtime is gone.
	// Re-applying it must recover rather than treating it as a no-op.
	applyRemoteTestConfig(t, sm, initial)

	if sm.remote.runtime == nil || sm.remote.runtime.generation <= oldGeneration {
		t.Fatalf("retry runtime = %+v, want a newer active generation", sm.remote.runtime)
	}
}

func TestRemoteRuntimePreparedGenerationClosesWhenAnotherReloadTransitionRejects(t *testing.T) {
	factory := &fakeRemoteFactory{}
	initial := remoteRuntimeTestConfig(true)
	initial.Orchestrator.Enabled = true
	sm := newRemoteRuntimeTestSM(t, initial, factory)
	applyRemoteTestConfig(t, sm, initial)

	putSession(sm, &SessionState{
		ID:         "orch-thrawn",
		Name:       OrchestratorSessionName,
		Status:     StatusCreating,
		SystemKind: SystemKindOrchestrator,
	})

	candidate := *initial
	candidate.Remote = cloneRemoteConfig(initial.Remote)
	candidate.Remote.Port++
	candidate.Orchestrator.Enabled = false

	err := sm.applyConfig(&candidate)
	if err == nil || !strings.Contains(err.Error(), "orchestrator.enabled") {
		t.Fatalf("applyConfig() error = %v, want orchestrator transition rejection", err)
	}

	if factory.count() != 2 {
		t.Fatalf("listener creations = %d, want old and prepared generations", factory.count())
	}

	if factory.listener(0).closeCount() == 0 || factory.listener(1).closeCount() == 0 {
		t.Fatalf("rejected generation leaked listener: old closes=%d prepared closes=%d", factory.listener(0).closeCount(), factory.listener(1).closeCount())
	}

	sm.configReloadMu.Lock()
	activeRuntime := sm.remote.runtime
	sm.configReloadMu.Unlock()

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.cfg != initial || sm.remoteGeneration != 0 || sm.remoteTLSPin != "" || activeRuntime != nil {
		t.Fatalf("rejected generation remained active: config=%p want=%p generation=%d pin=%q runtime=%v", sm.cfg, initial, sm.remoteGeneration, sm.remoteTLSPin, activeRuntime)
	}
}

func TestRemoteRuntimeDisableRacingWhoIsFailsClosed(t *testing.T) {
	whoIsStarted := make(chan struct{})
	factory := &fakeRemoteFactory{
		whoIs: func(ctx context.Context, _ string) (*TailnetIdentity, error) {
			close(whoIsStarted)
			<-ctx.Done()

			return nil, ctx.Err()
		},
	}
	initial := remoteRuntimeTestConfig(true)
	sm := newRemoteRuntimeTestSM(t, initial, factory)
	applyRemoteTestConfig(t, sm, initial)

	remoteAddr := sm.remote.runtime.listener.Addr().String()
	dialDone := make(chan error, 1)

	go func() {
		conn, err := tls.Dial("tcp", remoteAddr, &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // Test-only self-signed listener.
		})
		if err == nil {
			_ = conn.Close()
		}

		dialDone <- err
	}()

	select {
	case <-whoIsStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("accept did not reach WhoIs")
	}

	disabled := *initial
	disabled.Remote.Enabled = false
	applyRemoteTestConfig(t, sm, &disabled)

	select {
	case err := <-dialDone:
		if err == nil {
			t.Fatal("TLS dial racing disable unexpectedly authenticated")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("racing TLS dial was not closed by disable")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.remoteGeneration != 0 || len(sm.connsByDevice) != 0 {
		t.Fatalf("disable race left generation=%d device connections=%d", sm.remoteGeneration, len(sm.connsByDevice))
	}
}

func TestNewTSNetListenerAppliesTags(t *testing.T) {
	l, err := newTSNetListener(config.RemoteConfig{
		Hostname: "ben",
		Port:     4823,
		Tags:     []string{"tag:croft", "tag:bothy"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(l.srv.AdvertiseTags, ","); got != "tag:croft,tag:bothy" {
		t.Fatalf("AdvertiseTags = %q", got)
	}
}
