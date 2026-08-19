//nolint:wsl_v5 // Polling orchestration intentionally keeps lock snapshots and worker setup together.
package dependencyhealth

import (
	"context"
	"sort"
	"sync"
	"time"
)

//nolint:inamedparam // Context and duration names are self-documenting interface types.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type Timer interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }
func (t realTimer) Stop()               { _ = t.Timer.Stop() }

type Poller struct {
	Provider      Provider
	Clock         Clock
	Jitter        time.Duration
	OnObservation func(Observation)
	OnTransition  func(PollTransition)
	mu            sync.RWMutex
	services      []ServiceConfig
	observations  map[string]Observation
	generations   map[string]uint64
	lastPoll      map[string]time.Time
}

//nolint:inamedparam // Context is conventional for provider implementations.
type Provider interface {
	Poll(context.Context, ServiceConfig) (Observation, error)
}

func NewPoller(provider Provider, services []ServiceConfig) *Poller {
	return &Poller{Provider: provider, Clock: realClock{}, services: append([]ServiceConfig(nil), services...), observations: make(map[string]Observation), generations: make(map[string]uint64), lastPoll: make(map[string]time.Time)}
}

func (p *Poller) SetServices(services []ServiceConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.services = append([]ServiceConfig(nil), services...)
}

func (p *Poller) Snapshot() []Observation {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Observation, 0, len(p.observations))
	for _, observation := range p.observations {
		observation.IncidentIDs = append([]string(nil), observation.IncidentIDs...)
		out = append(out, observation)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// PollOnce polls a snapshot of configured services. At most four providers are
// active at once; one failing service cannot prevent the others from updating.
func (p *Poller) PollOnce(ctx context.Context) {
	p.poll(ctx, nil)
}

func (p *Poller) poll(ctx context.Context, due func(ServiceConfig, time.Time, time.Time) bool) {
	p.mu.RLock()
	services := append([]ServiceConfig(nil), p.services...)
	lastPoll := make(map[string]time.Time, len(p.lastPoll))
	for name, last := range p.lastPoll {
		lastPoll[name] = last
	}
	p.mu.RUnlock()
	now := p.now()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range services {
		service := services[i]
		if due != nil && !due(service, lastPoll[service.Name], now) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			observation, err := p.Provider.Poll(ctx, service)
			p.record(service, observation, err)
		}()
	}
	wg.Wait()
}

func (p *Poller) record(service ServiceConfig, observation Observation, pollErr error) {
	now := p.now()
	p.mu.Lock()
	p.lastPoll[service.Name] = now
	previous, existed := p.observations[service.Name]
	if !existed {
		previous.State = Unknown
	}
	if pollErr != nil {
		if !existed {
			previous = Observation{Service: service.Name, State: Unknown, SourceHealth: Failed, SourceURL: service.BaseURL}
		}
		previous.SourceHealth = Failed
		previous.LastFailureAt = now
		if !previous.LastSuccessAt.IsZero() && now.Sub(previous.LastSuccessAt) >= 2*service.PollInterval {
			previous.SourceHealth = Stale
		}
		p.observations[service.Name] = previous
		p.mu.Unlock()
		return
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = now
	}
	if observation.LastSuccessAt.IsZero() {
		observation.LastSuccessAt = observation.ObservedAt
	}
	observation.SourceHealth = Fresh
	observation.LastFailureAt = previous.LastFailureAt
	if (!existed || previous.SourceHealth == Fresh) && previous.State != observation.State && isNotifiable(previous.State, observation.State) {
		p.generations[service.Name]++
		transition := PollTransition{Service: service.Name, Generation: p.generations[service.Name], Previous: previous.State, Current: observation.State, ObservedAt: observation.ObservedAt}
		p.observations[service.Name] = observation
		callback, transitionCallback := p.OnObservation, p.OnTransition
		p.mu.Unlock()
		if callback != nil {
			callback(observation)
		}
		if transitionCallback != nil {
			transitionCallback(transition)
		}
		return
	}
	p.observations[service.Name] = observation
	callback := p.OnObservation
	p.mu.Unlock()
	if callback != nil {
		callback(observation)
	}
}

func isNotifiable(previous, current ObservedState) bool {
	return (previous == Unknown || previous == Operational || previous == Degraded || previous == Down) && (current == Degraded || current == Down || current == Operational) && previous != current
}

func (p *Poller) now() time.Time {
	if p.Clock != nil {
		return p.Clock.Now()
	}
	return time.Now()
}

func (p *Poller) Run(ctx context.Context) {
	p.PollOnce(ctx)
	for {
		delay := p.nextDelay()
		timer := p.clock().NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
			p.poll(ctx, func(service ServiceConfig, last, now time.Time) bool {
				return last.IsZero() || !now.Before(last.Add(p.intervalFor(service)))
			})
		}
	}
}

func (p *Poller) intervalFor(service ServiceConfig) time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if observation, ok := p.observations[service.Name]; ok && (observation.State == Degraded || observation.State == Down) && service.RecoveryPollInterval > 0 {
		return service.RecoveryPollInterval
	}
	if service.PollInterval > 0 {
		return service.PollInterval
	}
	return 5 * time.Minute
}

func (p *Poller) nextDelay() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	delay := 5 * time.Minute
	for _, service := range p.services {
		candidate := service.PollInterval
		if observation, ok := p.observations[service.Name]; ok && (observation.State == Degraded || observation.State == Down) {
			candidate = service.RecoveryPollInterval
		}
		if candidate > 0 && candidate < delay {
			delay = candidate
		}
	}
	if p.Jitter > 0 {
		delay += p.Jitter / 2
	}
	if delay <= 0 {
		return time.Minute
	}
	return delay
}

func (p *Poller) clock() Clock {
	if p.Clock != nil {
		return p.Clock
	}
	return realClock{}
}
