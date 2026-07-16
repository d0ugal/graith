package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
	done := make(chan struct{})
	wantNow := time.Unix(1234, 567)

	go func() {
		runPurgeLoop(ctx, func(d time.Duration) loopTimer {
			timerCreated <- d
			return timer
		},
			func() { reconciled <- struct{}{} },
			func(now time.Time) { purged <- now },
			func() time.Time { return wantNow },
		)
		close(done)
	}()

	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("startup reconciliation was not run")
	}

	if got := <-timerCreated; got != purgeStartupDelay {
		t.Fatalf("startup delay = %v, want %v", got, purgeStartupDelay)
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
	case got := <-timer.resets:
		if got != purgeInterval {
			t.Fatalf("timer reset = %v, want %v", got, purgeInterval)
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

		if got := <-createdWith; got != purgeStartupDelay {
			t.Fatalf("startup delay = %v, want %v", got, purgeStartupDelay)
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
