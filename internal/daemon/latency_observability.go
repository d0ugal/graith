package daemon

import (
	"context"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type ptyTelemetrySetter interface {
	SetTelemetryObservers(observers grpty.TelemetryObservers)
}

type inputReadbackTracker interface {
	BeginInputReadback(startedAt time.Time)
	CommitInputReadback(startedAt, committedAt time.Time)
	CancelInputReadback(startedAt time.Time)
}

type attachOutputTelemetry struct {
	observeQueueDelay func(attachOutputMode, time.Duration)
	observeWrite      func(attachOutputMode, time.Duration, error)
	traceQueueDelay   func(attachOutputMode, time.Time, time.Time)
	traceWrite        func(attachOutputMode, time.Time, time.Time, int, error)
}

func (t attachOutputTelemetry) observesQueueDelay() bool {
	return t.observeQueueDelay != nil || t.traceQueueDelay != nil
}

func (t attachOutputTelemetry) observesWrite() bool {
	return t.observeWrite != nil || t.traceWrite != nil
}

func (sm *SessionManager) latencyTelemetryEnabled() bool {
	return sm.metrics.Load() != nil || sm.tracingEnabled.Load()
}

func (sm *SessionManager) startLatencySpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	if !sm.tracingEnabled.Load() {
		return ctx, func(error) {}
	}

	spanCtx, span := startDaemonSpan(ctx, name, attrs...)

	return spanCtx, func(err error) {
		endDaemonSpan(span, err)
	}
}

func (sm *SessionManager) recordLatencySpan(name string, startedAt, endedAt time.Time, err error, attrs ...attribute.KeyValue) {
	if !sm.tracingEnabled.Load() || startedAt.IsZero() || endedAt.IsZero() {
		return
	}

	if endedAt.Before(startedAt) {
		endedAt = startedAt
	}

	opts := []trace.SpanStartOption{trace.WithTimestamp(startedAt)}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}

	_, span := telemetry.Tracer(daemonTracerName).Start(context.Background(), name, opts...)
	recordDaemonSpanError(span, err)
	span.End(trace.WithTimestamp(endedAt))
}

func (sm *SessionManager) ptyTelemetryObservers() grpty.TelemetryObservers {
	if !sm.latencyTelemetryEnabled() {
		return grpty.TelemetryObservers{}
	}

	return grpty.TelemetryObservers{
		OutputRead: func(obs grpty.PTYOutputReadObservation) {
			duration := observedDuration(obs.StartedAt, obs.EndedAt)
			sm.observePTYOutputRead(duration, obs.Err)
			sm.recordLatencySpan("graith.pty.output.read", obs.StartedAt, obs.EndedAt, obs.Err,
				attribute.Int("graith.output.bytes", obs.Bytes),
			)
		},
		ScreenUpdate: func(obs grpty.PTYScreenUpdateObservation) {
			duration := observedDuration(obs.StartedAt, obs.EndedAt)
			sm.observePTYScreenUpdate(duration, obs.Err)
			sm.recordLatencySpan("graith.pty.screen.update", obs.StartedAt, obs.EndedAt, obs.Err,
				attribute.Int("graith.output.bytes", obs.Bytes),
			)
		},
		AttachFanout: func(obs grpty.PTYAttachFanoutObservation) {
			duration := observedDuration(obs.StartedAt, obs.EndedAt)
			sm.observePTYAttachFanout(duration, obs.Err)
			sm.recordLatencySpan("graith.pty.attach.fanout", obs.StartedAt, obs.EndedAt, obs.Err,
				attribute.Int("graith.output.bytes", obs.Bytes),
				attribute.Int("graith.attach.writers", obs.Writers),
			)
		},
		InputReadback: func(obs grpty.PTYInputReadbackObservation) {
			duration := observedDuration(obs.StartedAt, obs.EndedAt)
			sm.observeSessionInputReadback(metricOperationAttach, duration)
			sm.recordLatencySpan("graith.session.input.readback", obs.StartedAt, obs.EndedAt, nil,
				attribute.String("graith.input.operation", metricOperationAttach),
				attribute.Int("graith.output.bytes", obs.Bytes),
			)
		},
	}
}

func (sm *SessionManager) configurePTYTelemetry(driver any) {
	setter, ok := driver.(ptyTelemetrySetter)
	if !ok {
		return
	}

	setter.SetTelemetryObservers(sm.ptyTelemetryObservers())
}

func (sm *SessionManager) configureExistingPTYTelemetry() {
	sm.mu.RLock()
	drivers := make([]sessionDriver, 0, len(sm.sessions))

	for _, driver := range sm.sessions {
		drivers = append(drivers, driver)
	}

	sm.mu.RUnlock()

	for _, driver := range drivers {
		sm.configurePTYTelemetry(driver)
	}
}

func (sm *SessionManager) beginAttachInputReadback(driver any, startedAt time.Time) func(error, time.Time) {
	if !sm.latencyTelemetryEnabled() {
		return func(error, time.Time) {}
	}

	tracker, ok := driver.(inputReadbackTracker)
	if !ok {
		return func(error, time.Time) {}
	}

	tracker.BeginInputReadback(startedAt)

	return func(err error, committedAt time.Time) {
		if err != nil {
			tracker.CancelInputReadback(startedAt)

			return
		}

		tracker.CommitInputReadback(startedAt, committedAt)
	}
}

func (sm *SessionManager) attachOutputTelemetry() attachOutputTelemetry {
	if !sm.latencyTelemetryEnabled() {
		return attachOutputTelemetry{}
	}

	var outputTelemetry attachOutputTelemetry

	if sm.metrics.Load() != nil {
		outputTelemetry.observeQueueDelay = sm.observeAttachOutputQueueDelay
		outputTelemetry.observeWrite = sm.observeAttachOutputWrite
	}

	if sm.tracingEnabled.Load() {
		outputTelemetry.traceQueueDelay = func(mode attachOutputMode, startedAt, endedAt time.Time) {
			sm.recordLatencySpan("graith.attach.output.queue_delay", startedAt, endedAt, nil,
				attribute.String("graith.attach.output.mode", metricAttachOutputMode(mode)),
			)
		}
		outputTelemetry.traceWrite = func(mode attachOutputMode, startedAt, endedAt time.Time, bytes int, err error) {
			sm.recordLatencySpan("graith.attach.output.write", startedAt, endedAt, err,
				attribute.String("graith.attach.output.mode", metricAttachOutputMode(mode)),
				attribute.Int("graith.output.bytes", bytes),
			)
		}
	}

	return outputTelemetry
}

func observedDuration(startedAt, endedAt time.Time) time.Duration {
	if startedAt.IsZero() || endedAt.IsZero() {
		return 0
	}

	if endedAt.Before(startedAt) {
		return 0
	}

	return endedAt.Sub(startedAt)
}
