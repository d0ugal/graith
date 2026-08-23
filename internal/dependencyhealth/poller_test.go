package dependencyhealth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

//nolint:wsl_v5
func TestPollerTransitionsAndFailureStaleness(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	provider := &fakeProvider{results: []fakeResult{{observation: Observation{State: Degraded}}, {err: errors.New("dreich")}}}
	var transitions []ObservationTransition
	var observations []Observation
	poller := NewPoller(provider, []ServiceConfig{{Name: "braw", BaseURL: "https://status.example", PollInterval: time.Minute, RecoveryPollInterval: time.Second}})
	poller.Clock = clock
	poller.OnTransition = func(transition ObservationTransition) { transitions = append(transitions, transition) }
	poller.OnObservation = func(observation Observation) { observations = append(observations, observation) }
	poller.PollOnce(context.Background())
	if len(transitions) != 1 || transitions[0].Current != Degraded {
		t.Fatalf("transitions = %#v", transitions)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	poller.PollOnce(context.Background())
	snapshot := poller.Snapshot()
	if len(snapshot) != 1 || snapshot[0].SourceHealth != Stale {
		t.Fatalf("observations = %#v", snapshot)
	}
	if len(transitions) != 1 {
		t.Fatalf("failure emitted transition: %#v", transitions)
	}
	if len(observations) != 2 || observations[1].SourceHealth != Stale || observations[1].LastFailureAt.IsZero() {
		t.Fatalf("failure observation callback = %#v", observations)
	}
}

//nolint:wsl_v5 // The fixed test service count is intentionally small and bounded.
func TestPollerBoundsConcurrencyAndReportsOutcomes(t *testing.T) {
	provider := &concurrencyProvider{}
	services := make([]ServiceConfig, 8)
	for i := range services {
		services[i] = ServiceConfig{Name: fmt.Sprintf("svc-%d", i), BaseURL: "https://status.example"}
	}
	poller := NewPoller(provider, services)
	var outcomes atomic.Int32
	var invalid atomic.Bool
	poller.OnPollOutcome = func(outcome PollOutcome) {
		if outcome.Result != "success" || outcome.Service == "" || outcome.Duration < 0 {
			invalid.Store(true)
		}
		outcomes.Add(1)
	}
	poller.PollOnce(context.Background())
	if got := provider.max.Load(); got > MaxPollConcurrency {
		t.Fatalf("maximum concurrent polls = %d, want <= %d", got, MaxPollConcurrency)
	}
	if got := provider.max.Load(); got != MaxPollConcurrency {
		t.Fatalf("maximum concurrent polls = %d, want %d", got, MaxPollConcurrency)
	}
	if got := int(outcomes.Load()); got != len(services) {
		t.Fatalf("outcomes = %d, want %d", got, len(services))
	}
	if invalid.Load() {
		t.Fatal("poller reported an invalid outcome")
	}
}

//nolint:wsl_v5 // This test deliberately interleaves reload and cancellation steps.
func TestPollerReloadAndShutdownAreSafeDuringPoll(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{})}
	poller := NewPoller(provider, []ServiceConfig{{Name: "braw", BaseURL: "https://status.example"}})
	poller.OnPollOutcome = func(PollOutcome) {}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.PollOnce(ctx)
		close(done)
	}()
	<-provider.started
	poller.SetServices([]ServiceConfig{{Name: "canny", BaseURL: "https://status.example"}})
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll did not stop after context cancellation")
	}
}

//nolint:wsl_v5 // The ordered calls document the service snapshot transition.
func TestPollerReloadUsesNewServiceSnapshot(t *testing.T) {
	provider := &recordingProvider{}
	poller := NewPoller(provider, []ServiceConfig{{Name: "braw", BaseURL: "https://status.example"}})
	poller.PollOnce(context.Background())
	poller.SetServices([]ServiceConfig{{Name: "canny", BaseURL: "https://status.example"}})
	poller.PollOnce(context.Background())

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.names) != 2 || provider.names[0] != "braw" || provider.names[1] != "canny" {
		t.Fatalf("polled services = %#v, want [braw canny]", provider.names)
	}
}

type recordingProvider struct {
	mu    sync.Mutex
	names []string
}

//nolint:wsl_v5 // Recording is intentionally a short lock-and-return fake.
func (p *recordingProvider) Poll(_ context.Context, service ServiceConfig) (Observation, error) {
	p.mu.Lock()
	p.names = append(p.names, service.Name)
	p.mu.Unlock()
	return Observation{Service: service.Name, State: Operational}, nil
}

type blockingProvider struct{ started chan struct{} }

//nolint:wsl_v5 // Cancellation behavior is the purpose of this fake provider.
func (p *blockingProvider) Poll(ctx context.Context, service ServiceConfig) (Observation, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return Observation{}, ctx.Err()
}

type concurrencyProvider struct {
	active atomic.Int32
	max    atomic.Int32
}

//nolint:wsl_v5 // The provider keeps the synchronization steps explicit for the race test.
func (p *concurrencyProvider) Poll(ctx context.Context, service ServiceConfig) (Observation, error) {
	active := p.active.Add(1)
	for {
		previous := p.max.Load()
		if active <= previous || p.max.CompareAndSwap(previous, active) {
			break
		}
	}
	defer p.active.Add(-1)
	select {
	case <-ctx.Done():
		return Observation{}, ctx.Err()
	case <-time.After(time.Millisecond):
		return Observation{Service: service.Name, State: Operational}, nil
	}
}

type fakeProvider struct {
	mu      sync.Mutex
	results []fakeResult
}
type fakeResult struct {
	observation Observation
	err         error
}

//nolint:wsl_v5
func (p *fakeProvider) Poll(context.Context, ServiceConfig) (Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.results) == 0 {
		return Observation{State: Operational}, nil
	}
	result := p.results[0]
	p.results = p.results[1:]
	return result.observation, result.err
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time             { return c.now }
func (*fakeClock) NewTimer(time.Duration) Timer { return &fakeTimer{c: make(chan time.Time)} }

type fakeTimer struct{ c chan time.Time }

func (t *fakeTimer) C() <-chan time.Time { return t.c }
func (*fakeTimer) Stop()                 {}
