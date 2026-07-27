//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"sync"
	"testing"
	"time"
)

func newIdleSession() *Session {
	s := &Session{}
	s.userInputCond = sync.NewCond(&sync.Mutex{})

	return s
}

func TestWaitForUserIdle_AlreadyIdle(t *testing.T) {
	s := newIdleSession()

	start := time.Now()
	ok := s.WaitForUserIdle(100*time.Millisecond, time.Second)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("expected idle=true when no user input has occurred")
	}

	if elapsed > 50*time.Millisecond {
		t.Fatalf("should have returned immediately, took %v", elapsed)
	}
}

func TestWaitForUserIdle_WaitsForIdle(t *testing.T) {
	s := newIdleSession()

	// Simulate recent user input.
	s.NotifyUserInput()

	start := time.Now()
	ok := s.WaitForUserIdle(200*time.Millisecond, 2*time.Second)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("expected idle=true")
	}

	if elapsed < 150*time.Millisecond {
		t.Fatalf("returned too quickly (%v), should have waited ~200ms", elapsed)
	}
}

func TestWaitForUserIdle_ResetByNewInput(t *testing.T) {
	const idleTimeout = 500 * time.Millisecond

	s := newIdleSession()
	s.NotifyUserInput()
	s.userInputCond.L.Lock()
	s.lastUserInputAt = time.Now().Add(time.Hour)
	s.userInputCond.L.Unlock()

	done := make(chan bool, 1)
	go func() {
		done <- s.WaitForUserIdle(idleTimeout, 30*time.Second)
	}()

	// Give the waiter a chance to block while the future input timestamp keeps
	// the idle condition unsatisfied. This avoids depending on scheduler timing
	// to beat the idle timeout on loaded CI runners.
	time.Sleep(20 * time.Millisecond)

	resetAt := notifyUserInputAt(s)

	ok := <-done
	elapsed := time.Since(resetAt)

	if !ok {
		t.Fatal("expected idle=true")
	}

	if elapsed < idleTimeout {
		t.Fatalf("returned too quickly after reset (%v)", elapsed)
	}
}

func notifyUserInputAt(s *Session) time.Time {
	at := time.Now()

	s.NotifyUserInput()

	return at
}

func TestWaitForUserIdle_MaxWaitExpires(t *testing.T) {
	s := newIdleSession()
	s.NotifyUserInput()

	// Keep typing faster than the idle timeout.
	stop := make(chan struct{})

	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				s.NotifyUserInput()
			}
		}
	}()

	defer close(stop)

	start := time.Now()
	ok := s.WaitForUserIdle(200*time.Millisecond, 500*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("expected idle=false when user keeps typing")
	}

	if elapsed < 400*time.Millisecond || elapsed > 700*time.Millisecond {
		t.Fatalf("expected maxWait ~500ms, got %v", elapsed)
	}
}
