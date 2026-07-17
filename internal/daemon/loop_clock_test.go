package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

type manualLoopTicker struct {
	c       chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newManualLoopTicker() *manualLoopTicker {
	return &manualLoopTicker{c: make(chan time.Time), stopped: make(chan struct{})}
}

func (t *manualLoopTicker) C() <-chan time.Time { return t.c }
func (t *manualLoopTicker) Stop()               { t.once.Do(func() { close(t.stopped) }) }

type manualLoopTimer struct {
	*manualLoopTicker

	resets chan time.Duration
}

func newManualLoopTimer() *manualLoopTimer {
	return &manualLoopTimer{manualLoopTicker: newManualLoopTicker(), resets: make(chan time.Duration, 1)}
}

func (t *manualLoopTimer) Reset(d time.Duration) { t.resets <- d }

func TestRunDetectionLoopDispatchesTicksAndStops(t *testing.T) {
	detectionTicker := newManualLoopTicker()
	fetchTicker := newManualLoopTicker()
	ctx, cancel := context.WithCancel(context.Background())
	detected := make(chan struct{}, 1)
	fetched := make(chan context.Context, 1)
	done := make(chan struct{})

	go func() {
		runDetectionLoop(ctx, detectionTicker, fetchTicker,
			func() { detected <- struct{}{} },
			func(got context.Context) { fetched <- got },
		)
		close(done)
	}()

	detectionTicker.c <- time.Now()

	select {
	case <-detected:
	case <-time.After(time.Second):
		t.Fatal("detection tick was not dispatched")
	}

	fetchTicker.c <- time.Now()

	select {
	case got := <-fetched:
		if got != ctx {
			t.Fatal("fetch callback received a different context")
		}
	case <-time.After(time.Second):
		t.Fatal("fetch tick was not dispatched")
	}

	cancel()
	assertLoopStopped(t, done, detectionTicker.stopped, fetchTicker.stopped)
}

func TestRunPurgeLoopReconcilesThenResetsAfterSweep(t *testing.T) {
	timer := newManualLoopTimer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reconciled := make(chan struct{}, 1)
	purged := make(chan time.Time, 1)
	timerCreated := make(chan time.Duration, 1)
	swept := make(chan time.Time, 1)
	done := make(chan struct{})
	wantNow := time.Unix(1234, 567)

	// Non-default cadence, exercised through the injected providers to prove the
	// loop uses the configured values rather than baked-in constants.
	const (
		wantStartup  = 2 * time.Second
		wantInterval = 90 * time.Second
	)

	go func() {
		runPurgeLoop(ctx, func(d time.Duration) loopTimer {
			timerCreated <- d
			return timer
		},
			func() { reconciled <- struct{}{} },
			func(now time.Time) { purged <- now },
			func() time.Time { return wantNow },
			func() time.Duration { return wantStartup },
			func() time.Duration { return wantInterval },
			func(ranAt time.Time, _ time.Duration) { swept <- ranAt },
		)
		close(done)
	}()

	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("startup reconciliation was not run")
	}

	if got := <-timerCreated; got != wantStartup {
		t.Fatalf("startup delay = %v, want %v", got, wantStartup)
	}

	timer.c <- time.Unix(999, 0)

	select {
	case got := <-purged:
		if !got.Equal(wantNow) {
			t.Fatalf("purge time = %v, want %v", got, wantNow)
		}
	case <-time.After(time.Second):
		t.Fatal("purge tick was not dispatched")
	}

	select {
	case got := <-swept:
		if !got.Equal(wantNow) {
			t.Fatalf("recorded sweep time = %v, want %v", got, wantNow)
		}
	case <-time.After(time.Second):
		t.Fatal("purge sweep was not recorded")
	}

	select {
	case got := <-timer.resets:
		if got != wantInterval {
			t.Fatalf("timer reset = %v, want %v", got, wantInterval)
		}
	case <-time.After(time.Second):
		t.Fatal("purge timer was not reset")
	}

	cancel()
	assertLoopStopped(t, done, timer.stopped)
}

func TestRunMessageCleanupLoopRunsImmediatelyAndOnTick(t *testing.T) {
	ticker := newManualLoopTicker()
	ctx, cancel := context.WithCancel(context.Background())
	cleaned := make(chan struct{}, 2)
	tickerCreated := make(chan time.Duration, 1)
	done := make(chan struct{})

	go func() {
		runMessageCleanupLoop(ctx, func(d time.Duration) loopTicker {
			tickerCreated <- d
			return ticker
		}, func() { cleaned <- struct{}{} })
		close(done)
	}()

	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("initial cleanup did not occur")
	}

	if got := <-tickerCreated; got != time.Hour {
		t.Fatalf("cleanup interval = %v, want %v", got, time.Hour)
	}

	ticker.c <- time.Now()

	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("tick cleanup did not occur")
	}

	cancel()
	assertLoopStopped(t, done, ticker.stopped)
}

func TestSessionManagerLoopClockDefaults(t *testing.T) {
	sm := &SessionManager{}
	ticker := sm.loopTicker(time.Hour)
	timer := sm.loopTimer(time.Millisecond)

	ticker.Stop()

	select {
	case <-timer.C():
	case <-time.After(time.Second):
		t.Fatal("real loop timer did not fire")
	}

	timer.Reset(time.Millisecond)

	select {
	case <-timer.C():
	case <-time.After(time.Second):
		t.Fatal("reset real loop timer did not fire")
	}

	timer.Stop()
}

func TestPublicLoopsUseInjectedClocksAndStop(t *testing.T) {
	t.Run("detection", func(t *testing.T) {
		sm := newTestSessionManager(t)
		created := make(chan *manualLoopTicker, 2)
		sm.newLoopTicker = func(time.Duration) loopTicker {
			ticker := newManualLoopTicker()
			created <- ticker

			return ticker
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		go func() {
			sm.RunDetectionLoop(ctx)
			close(done)
		}()

		first, second := <-created, <-created

		cancel()
		assertLoopStopped(t, done, first.stopped, second.stopped)
	})

	t.Run("detection intervals come from config", func(t *testing.T) {
		// #1241: the scan and fetch tickers must be built from [detection]
		// scan_interval / fetch_interval, not hard-coded 500ms / 5m.
		cfg := config.Default()
		cfg.Detection.ScanInterval = "250ms"
		cfg.Detection.FetchInterval = "2m"
		sm := newSMWithConfig(t, cfg)

		durations := make(chan time.Duration, 2)
		sm.newLoopTicker = func(d time.Duration) loopTicker {
			durations <- d
			return newManualLoopTicker()
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})

		go func() {
			sm.RunDetectionLoop(ctx)
			close(done)
		}()

		scan, fetch := <-durations, <-durations

		cancel()
		<-done

		if scan != 250*time.Millisecond {
			t.Errorf("scan ticker interval = %v, want 250ms", scan)
		}

		if fetch != 2*time.Minute {
			t.Errorf("fetch ticker interval = %v, want 2m", fetch)
		}
	})

	t.Run("purge", func(t *testing.T) {
		sm := newTestSessionManager(t)
		timer := newManualLoopTimer()
		createdWith := make(chan time.Duration, 1)
		sm.newLoopTimer = func(d time.Duration) loopTimer {
			createdWith <- d
			return timer
		}
		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			defer close(done)

			sm.RunPurgeLoop(ctx)
		}()

		if got := <-createdWith; got != config.DefaultPurgeStartupDelay {
			t.Fatalf("startup delay = %v, want %v", got, config.DefaultPurgeStartupDelay)
		}

		cancel()
		assertLoopStopped(t, done, timer.stopped)
	})

	t.Run("message cleanup without store", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.RunMessageCleanupLoop(context.Background())
	})

	t.Run("message cleanup", func(t *testing.T) {
		sm := newTestSessionManager(t)

		store, err := NewMsgStore(filepath.Join(t.TempDir(), "messages.sqlite"))
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = store.Close() })

		sm.messages = store

		type createdTicker struct {
			ticker   *manualLoopTicker
			duration time.Duration
		}

		created := make(chan createdTicker, 1)
		sm.newLoopTicker = func(d time.Duration) loopTicker {
			ticker := newManualLoopTicker()
			created <- createdTicker{ticker: ticker, duration: d}

			return ticker
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		go func() {
			defer close(done)

			sm.RunMessageCleanupLoop(ctx)
		}()

		got := <-created
		if got.duration != time.Hour {
			t.Fatalf("cleanup interval = %v, want %v", got.duration, time.Hour)
		}

		cancel()
		assertLoopStopped(t, done, got.ticker.stopped)
	})
}

func assertLoopStopped(t *testing.T, done <-chan struct{}, stopped ...<-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop after cancellation")
	}

	for i, ch := range stopped {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("clock %d was not stopped", i)
		}
	}
}

// mutableInterval is a goroutine-safe duration source standing in for a config
// generation that a reload swaps out mid-loop.
type mutableInterval struct {
	mu sync.Mutex
	d  time.Duration
}

func (m *mutableInterval) get() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.d
}

func (m *mutableInterval) set(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.d = d
}

// TestRunTokenLoopRetimesCadenceOnReload is the #1244 regression for the token
// loop: the poll cadence must be re-read from config after every tick so an
// accepted reload that tightens (or relaxes) poll_interval retimes the loop
// without a daemon restart, rather than latching the cadence at loop start.
func TestRunTokenLoopRetimesCadenceOnReload(t *testing.T) {
	timer := newManualLoopTimer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ticked := make(chan struct{}, 1)
	createdWith := make(chan time.Duration, 1)
	poll := &mutableInterval{d: 30 * time.Second}
	done := make(chan struct{})

	go func() {
		runTokenLoop(ctx,
			func(d time.Duration) loopTimer {
				createdWith <- d

				return timer
			},
			func() time.Duration { return 2 * time.Second },
			poll.get,
			func() { ticked <- struct{}{} },
		)
		close(done)
	}()

	if got := <-createdWith; got != 2*time.Second {
		t.Fatalf("startup delay = %v, want 2s", got)
	}

	// First tick resets with the original cadence.
	timer.c <- time.Unix(1, 0)

	<-ticked

	if got := <-timer.resets; got != 30*time.Second {
		t.Fatalf("first reset = %v, want 30s", got)
	}

	// A reload tightens the cadence between ticks.
	poll.set(5 * time.Second)

	// The next tick must observe the new generation.
	timer.c <- time.Unix(2, 0)

	<-ticked

	if got := <-timer.resets; got != 5*time.Second {
		t.Fatalf("second reset = %v, want 5s (reload cadence not observed)", got)
	}

	// A reload can also relax the cadence.
	poll.set(90 * time.Second)

	timer.c <- time.Unix(3, 0)

	<-ticked

	if got := <-timer.resets; got != 90*time.Second {
		t.Fatalf("third reset = %v, want 90s", got)
	}

	cancel()
	assertLoopStopped(t, done, timer.stopped)
}

// TestRunResourceMonitorLoopRetimesCadenceOnReload is the #1244 regression for
// the resource-monitor loop: it samples immediately, then re-reads the sample
// cadence after each scheduled sample so a reload retimes it. A kick forces an
// immediate one-off sample without disturbing the scheduled cadence.
func TestRunResourceMonitorLoopRetimesCadenceOnReload(t *testing.T) {
	timer := newManualLoopTimer()
	kick := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	sampled := make(chan struct{}, 1)
	createdWith := make(chan time.Duration, 1)
	interval := &mutableInterval{d: 30 * time.Second}
	done := make(chan struct{})

	go func() {
		runResourceMonitorLoop(ctx,
			func(d time.Duration) loopTimer {
				createdWith <- d

				return timer
			},
			interval.get,
			func() { sampled <- struct{}{} },
			kick,
		)
		close(done)
	}()

	// Immediate startup sample, then the timer is created with the current cadence.

	<-sampled

	if got := <-createdWith; got != 30*time.Second {
		t.Fatalf("initial timer cadence = %v, want 30s", got)
	}

	// A kick samples immediately but must NOT reset the scheduled timer.
	kick <- struct{}{}

	<-sampled

	select {
	case got := <-timer.resets:
		t.Fatalf("kick unexpectedly reset the cadence timer to %v", got)
	default:
	}

	// A scheduled tick samples and resets with the current cadence.
	timer.c <- time.Unix(1, 0)

	<-sampled

	if got := <-timer.resets; got != 30*time.Second {
		t.Fatalf("first scheduled reset = %v, want 30s", got)
	}

	// A reload tightens the cadence; the next scheduled sample observes it.
	interval.set(5 * time.Second)

	timer.c <- time.Unix(2, 0)

	<-sampled

	if got := <-timer.resets; got != 5*time.Second {
		t.Fatalf("second scheduled reset = %v, want 5s (reload cadence not observed)", got)
	}

	cancel()
	assertLoopStopped(t, done, timer.stopped)
}
