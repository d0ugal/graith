package dependencyhealth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPollerTransitionsAndFailureStaleness(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	provider := &fakeProvider{results: []fakeResult{{observation: Observation{State: Degraded}}, {err: errors.New("dreich")}}}

	var transitions []PollTransition

	poller := NewPoller(provider, []ServiceConfig{{Name: "braw", BaseURL: "https://status.example", PollInterval: time.Minute, RecoveryPollInterval: time.Second}})
	poller.Clock = clock
	poller.OnTransition = func(transition PollTransition) { transitions = append(transitions, transition) }
	poller.PollOnce(context.Background())

	if len(transitions) != 1 || transitions[0].Current != Degraded {
		t.Fatalf("transitions = %#v", transitions)
	}

	clock.now = clock.now.Add(2 * time.Minute)

	poller.PollOnce(context.Background())

	observations := poller.Snapshot()
	if len(observations) != 1 || observations[0].SourceHealth != Stale {
		t.Fatalf("observations = %#v", observations)
	}

	if len(transitions) != 1 {
		t.Fatalf("failure emitted transition: %#v", transitions)
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
