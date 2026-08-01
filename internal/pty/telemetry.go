package pty

import "time"

const maxInputReadbackAge = 5 * time.Second

// TelemetryObservers are daemon-owned callbacks for narrow PTY latency
// instrumentation. A zero value is intentionally inert so callers that do not
// enable telemetry pay only an atomic nil check on the hot path.
type TelemetryObservers struct {
	OutputRead    func(PTYOutputReadObservation)
	ScreenUpdate  func(PTYScreenUpdateObservation)
	AttachFanout  func(PTYAttachFanoutObservation)
	InputReadback func(PTYInputReadbackObservation)
}

func (o TelemetryObservers) Empty() bool {
	return o.OutputRead == nil &&
		o.ScreenUpdate == nil &&
		o.AttachFanout == nil &&
		o.InputReadback == nil
}

type PTYOutputReadObservation struct {
	StartedAt time.Time
	EndedAt   time.Time
	Bytes     int
	Err       error
}

type PTYScreenUpdateObservation struct {
	StartedAt time.Time
	EndedAt   time.Time
	Bytes     int
	Err       error
}

type PTYAttachFanoutObservation struct {
	StartedAt time.Time
	EndedAt   time.Time
	Bytes     int
	Writers   int
	Err       error
}

type PTYInputReadbackObservation struct {
	StartedAt time.Time
	EndedAt   time.Time
	Bytes     int
}

type inputReadbackMarker struct {
	startedAt   time.Time
	committedAt time.Time
}

func (s *Session) SetTelemetryObservers(observers TelemetryObservers) {
	if observers.Empty() {
		s.telemetryObservers.Store(nil)

		return
	}

	obs := observers
	s.telemetryObservers.Store(&obs)
}

func (s *Session) BeginInputReadback(startedAt time.Time) {
	observers := s.telemetryObservers.Load()
	if observers == nil || observers.InputReadback == nil {
		return
	}

	if startedAt.IsZero() {
		startedAt = time.Now()
	}

	s.inputReadbackMu.Lock()
	if s.inputReadback.startedAt.IsZero() || inputReadbackExpired(s.inputReadback.startedAt, startedAt) {
		s.inputReadback = inputReadbackMarker{startedAt: startedAt}
	}
	s.inputReadbackMu.Unlock()
}

func (s *Session) CommitInputReadback(startedAt, committedAt time.Time) {
	if startedAt.IsZero() {
		return
	}

	if committedAt.IsZero() {
		committedAt = time.Now()
	}

	s.inputReadbackMu.Lock()
	if s.inputReadback.startedAt.Equal(startedAt) {
		s.inputReadback.committedAt = committedAt
	}
	s.inputReadbackMu.Unlock()
}

func (s *Session) CancelInputReadback(startedAt time.Time) {
	if startedAt.IsZero() {
		return
	}

	s.inputReadbackMu.Lock()
	if s.inputReadback.startedAt.Equal(startedAt) {
		s.inputReadback = inputReadbackMarker{}
	}
	s.inputReadbackMu.Unlock()
}

func (s *Session) takeInputReadbackStartedAt(readEnded time.Time) time.Time {
	if readEnded.IsZero() {
		readEnded = time.Now()
	}

	s.inputReadbackMu.Lock()
	marker := s.inputReadback
	startedAt := marker.startedAt

	switch {
	case startedAt.IsZero():
		startedAt = time.Time{}
	case marker.committedAt.IsZero():
		startedAt = time.Time{}
	case readEnded.Before(marker.committedAt):
		startedAt = time.Time{}
	case inputReadbackExpired(startedAt, readEnded):
		s.inputReadback = inputReadbackMarker{}
		startedAt = time.Time{}
	default:
		s.inputReadback = inputReadbackMarker{}
	}

	s.inputReadbackMu.Unlock()

	return startedAt
}

func inputReadbackExpired(startedAt, now time.Time) bool {
	return !startedAt.IsZero() && !now.IsZero() && now.Sub(startedAt) > maxInputReadbackAge
}
