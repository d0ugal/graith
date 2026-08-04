package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	collectorlogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

const cannyLogRef = "0123456789abcdef0123456789abcdef"

func TestNewLogRecordAllowlistAndTruncation(t *testing.T) {
	longCommit := strings.Repeat("b", 300)

	record, err := NewLogRecord(LogEvent{
		Time:     time.Unix(123, 0),
		Severity: LogSeverityInfo,
		Domain:   "session",
		Name:     "session.exited",
		Result:   "success",
		Attributes: []attribute.KeyValue{
			attribute.String("session.driver_kind", "pty"),
			attribute.String("session.status", "stopped"),
			attribute.String("session.ref", cannyLogRef),
			attribute.String("graith.commit", longCommit),
			attribute.Int("process.exit_code", 0),
			attribute.Bool("session.sandboxed", true),
		},
	})
	if err != nil {
		t.Fatalf("NewLogRecord() error = %v", err)
	}

	attrs := logRecordAttributes(record)
	for _, key := range []string{"schema", "service.name", "event.domain", "event.name", "result"} {
		if _, ok := attrs[key]; !ok {
			t.Fatalf("record missing %s: %#v", key, attrs)
		}
	}

	if got := attrs["schema"].AsString(); got != DaemonLogSchema {
		t.Fatalf("schema = %q, want %q", got, DaemonLogSchema)
	}

	if got := attrs["session.ref"].AsString(); got != cannyLogRef {
		t.Fatalf("session.ref = %q, want %q", got, cannyLogRef)
	}

	if got := attrs["graith.commit"].AsString(); len(got) != 256 {
		t.Fatalf("truncated commit length = %d, want 256", len(got))
	}

	if got := attrs["graith.commit.truncated"].AsBool(); !got {
		t.Fatalf("graith.commit.truncated = %v, want true", got)
	}

	if got := attrs["graith.commit.original_bytes"].AsInt64(); got != 300 {
		t.Fatalf("graith.commit.original_bytes = %d, want 300", got)
	}
}

func TestNewLogRecordRejectsUnlistedEventAndAttributes(t *testing.T) {
	tests := map[string]LogEvent{
		"unknown event": {
			Name:   "session.named",
			Result: "success",
		},
		"domain mismatch": {
			Domain: "daemon",
			Name:   "session.exited",
			Result: "success",
		},
		"raw path attribute": {
			Domain: "session",
			Name:   "session.exited",
			Result: "success",
			Attributes: []attribute.KeyValue{
				attribute.String("path", "/Users/canny/secret"),
			},
		},
		"raw event id attribute": {
			Domain: "session",
			Name:   "session.exited",
			Result: "success",
			Attributes: []attribute.KeyValue{
				attribute.String("event_id", "evt-canny"),
			},
		},
		"unlisted enum": {
			Domain: "session",
			Name:   "session.exited",
			Result: "success",
			Attributes: []attribute.KeyValue{
				attribute.String("session.driver_kind", "tmux"),
			},
		},
		"raw session ref": {
			Domain: "session",
			Name:   "session.exited",
			Result: "success",
			Attributes: []attribute.KeyValue{
				attribute.String("session.ref", "raw-session-braw"),
			},
		},
		"uppercase session ref": {
			Domain: "session",
			Name:   "session.exited",
			Result: "success",
			Attributes: []attribute.KeyValue{
				attribute.String("session.ref", "0123456789ABCDEF0123456789ABCDEF"),
			},
		},
		"wrong type": {
			Domain: "session",
			Name:   "session.exited",
			Result: "success",
			Attributes: []attribute.KeyValue{
				attribute.String("process.exit_code", "0"),
			},
		},
	}

	for name, event := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewLogRecord(event); err == nil {
				t.Fatal("NewLogRecord() error = nil, want allowlist rejection")
			}
		})
	}
}

func TestNewLogRecordSanitizesInvalidUTF8(t *testing.T) {
	record, err := NewLogRecord(LogEvent{
		Domain: "session",
		Name:   "session.exited",
		Result: "success",
		Attributes: []attribute.KeyValue{
			attribute.String("graith.commit", "braw"+string([]byte{0xff})+"canny"),
		},
	})
	if err != nil {
		t.Fatalf("NewLogRecord() error = %v", err)
	}

	attrs := logRecordAttributes(record)
	got := attrs["graith.commit"].AsString()

	if !utf8.ValidString(got) {
		t.Fatalf("graith.commit = %q, want valid UTF-8", got)
	}

	if got != "brawcanny" {
		t.Fatalf("graith.commit = %q, want invalid bytes removed", got)
	}

	if _, ok := attrs["graith.commit.truncated"]; ok {
		t.Fatalf("graith.commit.truncated set for UTF-8 sanitization without length truncation: %#v", attrs)
	}
}

func TestPseudonymousRefStableAndKeyed(t *testing.T) {
	first := PseudonymousRef([]byte("canny-secret"), "session-braw")
	second := PseudonymousRef([]byte("canny-secret"), "session-braw")
	otherKey := PseudonymousRef([]byte("dreich-secret"), "session-braw")

	if first == "" {
		t.Fatal("PseudonymousRef() returned empty ref")
	}

	if len(first) != 32 {
		t.Fatalf("ref length = %d, want 32 hex chars", len(first))
	}

	if first != second {
		t.Fatalf("same key/value ref changed: %q != %q", first, second)
	}

	if first == otherKey {
		t.Fatal("different keys produced same ref")
	}

	if first == "session-braw" {
		t.Fatal("ref exposed raw session id")
	}
}

func TestLoggingRuntimeShutdownFlushesQueuedRecords(t *testing.T) {
	exporter := &recordingLogExporter{}
	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	t.Cleanup(func() { newLogExporter = oldExporter })

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      8,
		BatchSize:      8,
		ExportInterval: time.Hour,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	for range 2 {
		if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
			t.Fatalf("Emit() error = %v", err)
		}
	}

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	records := exporter.records()
	if len(records) != 2 {
		t.Fatalf("exported records = %d, want 2", len(records))
	}

	if !exporter.shutdownCalled() {
		t.Fatal("exporter Shutdown was not called")
	}
}

func TestLoggingRuntimeForceFlushExportsQueuedRecords(t *testing.T) {
	exporter := &recordingLogExporter{}
	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	t.Cleanup(func() { newLogExporter = oldExporter })

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      8,
		BatchSize:      8,
		ExportInterval: time.Hour,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
		t.Fatalf("Emit() error = %v", err)
	}

	if err := rt.ForceFlush(t.Context()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}

	records := exporter.records()
	if len(records) != 1 {
		t.Fatalf("exported records = %d, want 1", len(records))
	}

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestLoggingRuntimeShutdownWithCanceledContextCanRetry(t *testing.T) {
	exporter := &recordingLogExporter{}
	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	t.Cleanup(func() { newLogExporter = oldExporter })

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      8,
		BatchSize:      8,
		ExportInterval: time.Hour,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := rt.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown(canceled) error = %v, want context.Canceled", err)
	}

	if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
		t.Fatalf("Emit() after failed shutdown error = %v", err)
	}

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(retry) error = %v", err)
	}

	if !exporter.shutdownCalled() {
		t.Fatal("exporter Shutdown was not called after retry")
	}
}

func TestLoggingRuntimeShutdownTimeoutBeforeWorkerAcceptsCanRetry(t *testing.T) {
	exportStarted := make(chan struct{}, 1)
	releaseExport := make(chan struct{})
	exporter := &recordingLogExporter{
		onExport: func(ctx context.Context, _ []LogRecord) error {
			select {
			case exportStarted <- struct{}{}:
			default:
			}

			select {
			case <-releaseExport:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	var releaseOnce sync.Once

	release := func() {
		releaseOnce.Do(func() { close(releaseExport) })
	}

	t.Cleanup(func() {
		release()

		newLogExporter = oldExporter
	})

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      1,
		BatchSize:      1,
		ExportInterval: time.Hour,
		Timeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
		t.Fatalf("Emit() error = %v", err)
	}

	select {
	case <-exportStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for export to start")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	if err := rt.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown(timeout) error = %v, want context.DeadlineExceeded", err)
	}

	release()

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(retry) error = %v", err)
	}

	if !exporter.shutdownCalled() {
		t.Fatal("exporter Shutdown was not called after retry")
	}
}

func TestLoggingRuntimeShutdownReturnsExporterError(t *testing.T) {
	shutdownErr := errors.New("dreich shutdown failed")
	exporter := &recordingLogExporter{
		onShutdown: func(context.Context) error {
			return shutdownErr
		},
	}

	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	t.Cleanup(func() { newLogExporter = oldExporter })

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      8,
		BatchSize:      8,
		ExportInterval: time.Hour,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	if err := rt.Shutdown(t.Context()); !errors.Is(err, shutdownErr) {
		t.Fatalf("Shutdown() error = %v, want %v", err, shutdownErr)
	}

	if err := rt.Shutdown(t.Context()); !errors.Is(err, shutdownErr) {
		t.Fatalf("Shutdown(repeat) error = %v, want stored %v", err, shutdownErr)
	}
}

func TestBoundedLogProcessorFlushBatchExportsFinalPartialAfterFailedFullBatch(t *testing.T) {
	exportErr := errors.New("dreich export failed")

	var calls atomic.Int32

	exporter := &recordingLogExporter{
		onExport: func(context.Context, []LogRecord) error {
			if calls.Add(1) == 1 {
				return exportErr
			}

			return nil
		},
	}

	processor := &boundedLogProcessor{
		exporter:  exporter,
		queue:     make(chan LogRecord, 3),
		batchSize: 2,
	}

	for range 3 {
		processor.queue <- mustNewLogRecord(t, cannyLogEvent())
	}

	var batch []LogRecord
	if err := processor.flushBatch(t.Context(), &batch); !errors.Is(err, exportErr) {
		t.Fatalf("flushBatch() error = %v, want %v", err, exportErr)
	}

	if got := processor.Dropped(); got != 2 {
		t.Fatalf("Dropped() = %d, want failed full batch to count two records", got)
	}

	records := exporter.records()
	if len(records) != 1 {
		t.Fatalf("successful exported records = %d, want final partial record exported", len(records))
	}
}

func TestLoggingRuntimeEmitAfterShutdownCountsDrop(t *testing.T) {
	exporter := &recordingLogExporter{}
	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	t.Cleanup(func() { newLogExporter = oldExporter })

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      8,
		BatchSize:      8,
		ExportInterval: time.Hour,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
		t.Fatalf("Emit(after shutdown) error = %v", err)
	}

	if got := rt.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want post-shutdown emit counted as dropped", got)
	}
}

func TestLoggingRuntimeCountsFailedExportAsDropped(t *testing.T) {
	exportErr := errors.New("dreich export failed")
	exporter := &recordingLogExporter{
		onExport: func(context.Context, []LogRecord) error {
			return exportErr
		},
	}

	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	t.Cleanup(func() { newLogExporter = oldExporter })

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      8,
		BatchSize:      8,
		ExportInterval: time.Hour,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
		t.Fatalf("Emit() error = %v", err)
	}

	if err := rt.Shutdown(t.Context()); !errors.Is(err, exportErr) {
		t.Fatalf("Shutdown() error = %v, want %v", err, exportErr)
	}

	if got := rt.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want failed export to count as dropped", got)
	}
}

func TestLoggingRuntimeCountsDropsWithoutBlockingEmit(t *testing.T) {
	exportStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	exporter := &recordingLogExporter{
		onExport: func(ctx context.Context, _ []LogRecord) error {
			select {
			case exportStarted <- struct{}{}:
			default:
			}

			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	oldExporter := newLogExporter
	newLogExporter = func(context.Context, LoggingOptions) (logExporter, error) {
		return exporter, nil
	}

	var releaseOnce sync.Once

	closeRelease := func() {
		releaseOnce.Do(func() { close(release) })
	}

	t.Cleanup(func() {
		closeRelease()

		newLogExporter = oldExporter
	})

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       "127.0.0.1:4317",
		Protocol:       LogProtocolGRPC,
		QueueSize:      1,
		BatchSize:      1,
		ExportInterval: time.Hour,
		Timeout:        250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
		t.Fatalf("Emit() error = %v", err)
	}

	select {
	case <-exportStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first export")
	}

	for range 10 {
		if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
			t.Fatalf("Emit() error = %v", err)
		}
	}

	if got := rt.Dropped(); got == 0 {
		t.Fatal("Dropped() = 0, want drop accounting after queue overflow")
	}

	closeRelease()

	if err := rt.Shutdown(t.Context()); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestHTTPLogExporterIgnoresOTLPEnvironment(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1/wrong")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://127.0.0.1:1/wrong-logs")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-global=bad")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "x-env=bad,x-graith=env")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_COMPRESSION", "gzip")

	type logRequest struct {
		path            string
		configHeader    string
		envHeader       string
		globalHeader    string
		contentEncoding string
		contentType     string
		userAgent       string
	}

	requests := make(chan logRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests <- logRequest{
			path:            r.URL.Path,
			configHeader:    r.Header.Get("x-graith"),
			envHeader:       r.Header.Get("x-env"),
			globalHeader:    r.Header.Get("x-global"),
			contentEncoding: r.Header.Get("content-encoding"),
			contentType:     r.Header.Get("content-type"),
			userAgent:       r.UserAgent(),
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       server.URL + "/canny/logs",
		Protocol:       LogProtocolHTTPProtobuf,
		Timeout:        500 * time.Millisecond,
		QueueSize:      4,
		BatchSize:      4,
		ExportInterval: time.Hour,
		Headers: map[string]string{
			"content-type": "application/json",
			"user-agent":   "bad-client",
			"x-graith":     "config",
		},
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
		t.Fatalf("Emit() error = %v", err)
	}

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	var got logRequest
	select {
	case got = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OTLP HTTP log request")
	}

	want := logRequest{
		path:         "/canny/logs",
		configHeader: "config",
		contentType:  "application/x-protobuf",
		userAgent:    "graith-otlp-logs",
	}
	if got != want {
		t.Fatalf("OTLP HTTP log request = %#v, want %#v", got, want)
	}
}

func TestHTTPLogExporterCountsPartialSuccessAsDropped(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		requests <- struct{}{}

		body, err := proto.Marshal(&collectorlogpb.ExportLogsServiceResponse{
			PartialSuccess: &collectorlogpb.ExportLogsPartialSuccess{
				RejectedLogRecords: 1,
				ErrorMessage:       "canny record rejected",
			},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	rt, err := StartLogging(t.Context(), LoggingOptions{
		Endpoint:       server.URL + "/v1/logs",
		Protocol:       LogProtocolHTTPProtobuf,
		Timeout:        500 * time.Millisecond,
		QueueSize:      4,
		BatchSize:      4,
		ExportInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("StartLogging() error = %v", err)
	}

	for range 2 {
		if err := rt.Emit(t.Context(), cannyLogEvent()); err != nil {
			t.Fatalf("Emit() error = %v", err)
		}
	}

	var partial partialLogExportError
	if err := rt.Shutdown(t.Context()); !errors.As(err, &partial) {
		t.Fatalf("Shutdown() error = %v, want partialLogExportError", err)
	}

	if partial.rejected != 1 {
		t.Fatalf("partial rejected = %d, want 1", partial.rejected)
	}

	if got := rt.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want rejected records counted", got)
	}

	select {
	case <-requests:
	default:
		t.Fatal("server did not receive export request")
	}
}

func cannyLogEvent() LogEvent {
	return LogEvent{
		Time:     time.Unix(123, 0),
		Severity: LogSeverityInfo,
		Domain:   "session",
		Name:     "session.exited",
		Result:   "success",
		Attributes: []attribute.KeyValue{
			attribute.String("session.driver_kind", "pty"),
			attribute.String("session.status", "stopped"),
			attribute.String("session.ref", cannyLogRef),
			attribute.Int("process.exit_code", 0),
		},
	}
}

func mustNewLogRecord(t *testing.T, event LogEvent) LogRecord {
	t.Helper()

	record, err := NewLogRecord(event)
	if err != nil {
		t.Fatalf("NewLogRecord() error = %v", err)
	}

	return record
}

func logRecordAttributes(record LogRecord) map[string]attribute.Value {
	out := make(map[string]attribute.Value, len(record.Attributes))
	for _, attr := range record.Attributes {
		out[string(attr.Key)] = attr.Value
	}

	return out
}

type recordingLogExporter struct {
	mu         sync.Mutex
	batches    [][]LogRecord
	shutdown   bool
	onExport   func(context.Context, []LogRecord) error
	onShutdown func(context.Context) error
}

func (e *recordingLogExporter) Export(ctx context.Context, records []LogRecord) error {
	if e.onExport != nil {
		if err := e.onExport(ctx, records); err != nil {
			return err
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.batches = append(e.batches, append([]LogRecord(nil), records...))

	return nil
}

func (e *recordingLogExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.shutdown = true

	if e.onShutdown != nil {
		return e.onShutdown(ctx)
	}

	return nil
}

func (e *recordingLogExporter) records() []LogRecord {
	e.mu.Lock()
	defer e.mu.Unlock()

	var out []LogRecord
	for _, batch := range e.batches {
		out = append(out, batch...)
	}

	return out
}

func (e *recordingLogExporter) shutdownCalled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.shutdown
}
