package pty

import (
	"testing"
	"time"
)

func TestInputReadbackMarkerLifecycle(t *testing.T) {
	base := time.Unix(100, 0)

	tests := map[string]struct {
		run func(*testing.T, *Session)
	}{
		"committed marker is consumed once": {
			run: func(t *testing.T, session *Session) {
				startedAt := base
				committedAt := startedAt.Add(time.Millisecond)

				session.BeginInputReadback(startedAt)
				session.CommitInputReadback(startedAt, committedAt)

				if got := session.takeInputReadbackStartedAt(committedAt.Add(time.Millisecond)); !got.Equal(startedAt) {
					t.Fatalf("readback started at %v, want %v", got, startedAt)
				}

				if got := session.takeInputReadbackStartedAt(committedAt.Add(2 * time.Millisecond)); !got.IsZero() {
					t.Fatalf("second readback consume = %v, want zero", got)
				}
			},
		},
		"read ending before commit leaves marker pending": {
			run: func(t *testing.T, session *Session) {
				startedAt := base.Add(time.Second)
				committedAt := startedAt.Add(time.Millisecond)

				session.BeginInputReadback(startedAt)
				session.CommitInputReadback(startedAt, committedAt)

				if got := session.takeInputReadbackStartedAt(committedAt.Add(-time.Nanosecond)); !got.IsZero() {
					t.Fatalf("early readback consume = %v, want zero", got)
				}

				if got := session.takeInputReadbackStartedAt(committedAt.Add(time.Nanosecond)); !got.Equal(startedAt) {
					t.Fatalf("later readback started at %v, want %v", got, startedAt)
				}
			},
		},
		"cancel clears provisional marker": {
			run: func(t *testing.T, session *Session) {
				startedAt := base.Add(2 * time.Second)
				committedAt := startedAt.Add(time.Millisecond)

				session.BeginInputReadback(startedAt)
				session.CancelInputReadback(startedAt)
				session.CommitInputReadback(startedAt, committedAt)

				if got := session.takeInputReadbackStartedAt(committedAt.Add(time.Millisecond)); !got.IsZero() {
					t.Fatalf("cancelled readback consume = %v, want zero", got)
				}
			},
		},
		"stale marker is dropped": {
			run: func(t *testing.T, session *Session) {
				startedAt := base.Add(3 * time.Second)
				committedAt := startedAt.Add(time.Millisecond)

				session.BeginInputReadback(startedAt)
				session.CommitInputReadback(startedAt, committedAt)

				if got := session.takeInputReadbackStartedAt(startedAt.Add(maxInputReadbackAge + time.Nanosecond)); !got.IsZero() {
					t.Fatalf("stale readback consume = %v, want zero", got)
				}

				if got := session.takeInputReadbackStartedAt(startedAt.Add(maxInputReadbackAge + time.Second)); !got.IsZero() {
					t.Fatalf("dropped stale marker consume = %v, want zero", got)
				}
			},
		},
		"new begin replaces stale pending marker": {
			run: func(t *testing.T, session *Session) {
				staleAt := base.Add(4 * time.Second)
				startedAt := staleAt.Add(maxInputReadbackAge + time.Nanosecond)
				committedAt := startedAt.Add(time.Millisecond)

				session.BeginInputReadback(staleAt)
				session.BeginInputReadback(startedAt)
				session.CommitInputReadback(startedAt, committedAt)

				if got := session.takeInputReadbackStartedAt(committedAt.Add(time.Millisecond)); !got.Equal(startedAt) {
					t.Fatalf("replacement readback started at %v, want %v", got, startedAt)
				}
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			session := &Session{}
			session.SetTelemetryObservers(TelemetryObservers{
				InputReadback: func(PTYInputReadbackObservation) {},
			})

			test.run(t, session)
		})
	}
}

func TestBeginInputReadbackRequiresObserver(t *testing.T) {
	startedAt := time.Unix(200, 0)
	committedAt := startedAt.Add(time.Millisecond)

	tests := map[string]struct {
		observers TelemetryObservers
	}{
		"none":        {},
		"partial set": {observers: TelemetryObservers{OutputRead: func(PTYOutputReadObservation) {}}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			session := &Session{}
			session.SetTelemetryObservers(test.observers)
			session.BeginInputReadback(startedAt)
			session.CommitInputReadback(startedAt, committedAt)

			if got := session.takeInputReadbackStartedAt(committedAt.Add(time.Millisecond)); !got.IsZero() {
				t.Fatalf("readback marker = %v, want zero", got)
			}
		})
	}
}
