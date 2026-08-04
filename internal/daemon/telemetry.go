package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/telemetry"
	"github.com/d0ugal/graith/internal/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
)

var telemetryListen = net.Listen

type telemetryRuntime struct {
	metrics *telemetryMetricsRuntime
	tracing *telemetryTracingRuntime
	logs    *telemetryLogsRuntime
}

func newTelemetryRuntime(
	ctx context.Context,
	cfg config.TelemetryConfig,
	sourceDir string,
	metricsGatherer prometheus.Gatherer,
	log *slog.Logger,
	resource telemetry.ResourceOptions,
) (*telemetryRuntime, error) {
	if !cfg.Enabled() {
		return nil, nil
	}

	rt := &telemetryRuntime{}

	if cfg.Metrics.Enabled {
		metrics, err := startTelemetryMetricsRuntime(ctx, cfg.Metrics, metricsGatherer, log)
		if err != nil {
			return nil, err
		}

		rt.metrics = metrics
	}

	if cfg.Logs.Enabled {
		logs, err := newTelemetryLogsRuntime(ctx, cfg.Logs, resource, log)
		if err != nil {
			rt.stop(ctx)

			return nil, err
		}

		rt.logs = logs

		if log != nil {
			log.Info("telemetry logs exporter started",
				"endpoint", cfg.Logs.Endpoint,
				"protocol", cfg.Logs.ProtocolOrDefault(),
				"insecure", cfg.Logs.Insecure,
				"timeout", cfg.Logs.TimeoutDuration(),
				"queue_size", cfg.Logs.QueueSizeOrDefault(),
				"batch_size", cfg.Logs.BatchSizeOrDefault())
		}

		if err := logs.emit(ctx, telemetry.LogEvent{
			Severity: telemetry.LogSeverityInfo,
			Domain:   "telemetry",
			Name:     "telemetry.logs_started",
			Result:   "success",
			Attributes: []attribute.KeyValue{
				attribute.String("telemetry.protocol", cfg.Logs.ProtocolOrDefault()),
				attribute.Int("queue_size", cfg.Logs.QueueSizeOrDefault()),
				attribute.Int("batch_size", cfg.Logs.BatchSizeOrDefault()),
			},
		}); err != nil && log != nil {
			log.Warn("telemetry log event rejected", "event", "telemetry.logs_started", "err", err)
		}
	}

	if cfg.Tracing.Enabled {
		tracing, err := newTelemetryTracingRuntime(ctx, cfg.Tracing, sourceDir, resource, log)
		if err != nil {
			rt.stop(ctx)

			return nil, err
		}

		rt.tracing = tracing

		if log != nil {
			log.Info("telemetry tracing exporter started",
				"endpoint", cfg.Tracing.Endpoint,
				"protocol", cfg.Tracing.ProtocolOrDefault(),
				"insecure", cfg.Tracing.Insecure,
				"timeout", cfg.Tracing.TimeoutDuration())
		}
	}

	return rt, nil
}

func (rt *telemetryRuntime) stop(ctx context.Context) {
	if rt == nil {
		return
	}

	if rt.metrics != nil {
		rt.metrics.stop()
	}

	if rt.tracing != nil {
		rt.tracing.stop(ctx)
	}

	if rt.logs != nil {
		rt.logs.stop(ctx)
	}
}

type telemetryMetricsRuntime struct {
	server   *http.Server
	listener net.Listener

	mu        sync.Mutex
	serveErr  error
	stopped   bool
	boundAddr string
	path      string
}

func startTelemetryMetricsRuntime(ctx context.Context, cfg config.TelemetryMetricsConfig, gatherer prometheus.Gatherer, log *slog.Logger) (*telemetryMetricsRuntime, error) {
	path := cfg.PathOrDefault()

	mux := http.NewServeMux()
	handler := newTelemetryMetricsHandler(gatherer)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}

		handler.ServeHTTP(w, r)
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := telemetryListen("tcp", cfg.BindAddressOrDefault())
	if err != nil {
		return nil, fmt.Errorf("metrics listener: %w", err)
	}

	rt := &telemetryMetricsRuntime{
		server:    server,
		listener:  listener,
		boundAddr: listener.Addr().String(),
		path:      path,
	}

	go func() {
		<-ctx.Done()
		rt.stop()
	}()

	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			rt.mu.Lock()
			rt.serveErr = err
			rt.mu.Unlock()

			if log != nil {
				log.Error("telemetry metrics listener stopped", "addr", rt.boundAddr, "path", rt.path, "err", err)
			}
		}
	}()

	if log != nil {
		log.Info("telemetry metrics listener started", "addr", rt.boundAddr, "path", rt.path)
	}

	return rt, nil
}

func newTelemetryMetricsHandler(gatherer prometheus.Gatherer) http.Handler {
	if gatherer == nil {
		gatherer = prometheus.NewRegistry()
	}

	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
}

func (rt *telemetryMetricsRuntime) stop() {
	rt.mu.Lock()
	if rt.stopped {
		rt.mu.Unlock()
		return
	}

	rt.stopped = true
	server := rt.server
	listener := rt.listener
	rt.mu.Unlock()

	if server != nil {
		_ = server.Close()
	}

	if listener != nil {
		_ = listener.Close()
	}
}

func (rt *telemetryMetricsRuntime) endpoint() (addr, path string) {
	if rt == nil {
		return "", ""
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	return rt.boundAddr, rt.path
}

type telemetryTracingRuntime struct {
	shutdownTimeout time.Duration
	rt              *telemetry.TracingRuntime
	log             *slog.Logger
}

func newTelemetryTracingRuntime(
	ctx context.Context,
	cfg config.TelemetryTracingConfig,
	sourceDir string,
	resource telemetry.ResourceOptions,
	log *slog.Logger,
) (*telemetryTracingRuntime, error) {
	headers, err := cfg.ResolvedHeaders(sourceDir)
	if err != nil {
		return nil, err
	}

	tracing, err := telemetry.StartTracing(ctx, telemetry.TracingOptions{
		Endpoint: cfg.Endpoint,
		Protocol: cfg.ProtocolOrDefault(),
		Insecure: cfg.Insecure,
		Timeout:  cfg.TimeoutDuration(),
		Headers:  headers,
		Resource: resource,
		Logger:   log,
	})
	if err != nil {
		return nil, err
	}

	return &telemetryTracingRuntime{
		shutdownTimeout: cfg.TimeoutDuration(),
		rt:              tracing,
		log:             log,
	}, nil
}

func (rt *telemetryTracingRuntime) stop(ctx context.Context) {
	if rt == nil || rt.rt == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, rt.shutdownTimeout)
	defer cancel()

	if err := rt.rt.Shutdown(ctx); err != nil && rt.log != nil {
		rt.log.Warn("telemetry tracing exporter shutdown failed", "err", err)
	}
}

type telemetryLogsRuntime struct {
	cfg    config.TelemetryLogsConfig
	rt     *telemetry.LoggingRuntime
	log    *slog.Logger
	secret []byte
}

func newTelemetryLogsRuntime(
	ctx context.Context,
	cfg config.TelemetryLogsConfig,
	resource telemetry.ResourceOptions,
	log *slog.Logger,
) (*telemetryLogsRuntime, error) {
	logs, err := telemetry.StartLogging(ctx, telemetry.LoggingOptions{
		Endpoint:       cfg.Endpoint,
		Protocol:       cfg.ProtocolOrDefault(),
		Insecure:       cfg.Insecure,
		Timeout:        cfg.TimeoutDuration(),
		Headers:        cfg.Headers,
		QueueSize:      cfg.QueueSizeOrDefault(),
		BatchSize:      cfg.BatchSizeOrDefault(),
		ExportInterval: cfg.ExportIntervalDuration(),
		Resource:       resource,
		Logger:         log,
	})
	if err != nil {
		return nil, err
	}

	return &telemetryLogsRuntime{
		cfg: config.TelemetryLogsConfig{
			Enabled:        cfg.Enabled,
			Endpoint:       cfg.Endpoint,
			Protocol:       cfg.Protocol,
			Insecure:       cfg.Insecure,
			Timeout:        cfg.Timeout,
			ExportInterval: cfg.ExportInterval,
			QueueSize:      cfg.QueueSize,
			BatchSize:      cfg.BatchSize,
			Headers:        cloneTelemetryHeaders(cfg.Headers),
		},
		rt:  logs,
		log: log,
	}, nil
}

func (rt *telemetryLogsRuntime) stop(ctx context.Context) {
	if rt == nil || rt.rt == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, rt.cfg.TimeoutDuration())
	defer cancel()

	if err := rt.rt.Shutdown(ctx); err != nil && rt.log != nil {
		rt.log.Warn("telemetry logs exporter shutdown failed", "err", err, "dropped", rt.rt.Dropped())
	}
}

func (rt *telemetryLogsRuntime) emit(ctx context.Context, event telemetry.LogEvent) error {
	if rt == nil || rt.rt == nil {
		return nil
	}

	return rt.rt.Emit(ctx, event)
}

func (rt *telemetryLogsRuntime) ref(value string) string {
	if rt == nil {
		return ""
	}

	return telemetry.PseudonymousRef(rt.secret, value)
}

type telemetryRuntimeConfigSnapshot struct {
	Metrics *telemetryMetricsRuntimeConfigSnapshot
	Tracing *telemetryTracingRuntimeConfigSnapshot
	Logs    *telemetryLogsRuntimeConfigSnapshot
}

type telemetryMetricsRuntimeConfigSnapshot struct {
	BindAddress string
	Path        string
}

type telemetryTracingRuntimeConfigSnapshot struct {
	Endpoint    string
	Protocol    string
	Insecure    bool
	Timeout     time.Duration
	Headers     map[string]string
	HeadersEnv  map[string]string
	HeadersFile map[string]string
}

type telemetryLogsRuntimeConfigSnapshot struct {
	Endpoint       string
	Protocol       string
	Insecure       bool
	Timeout        time.Duration
	ExportInterval time.Duration
	QueueSize      int
	BatchSize      int
	Headers        map[string]string
}

func sameTelemetryRuntimeConfig(old, next config.TelemetryConfig) bool {
	return reflect.DeepEqual(
		telemetryRuntimeConfigSnapshotFor(old),
		telemetryRuntimeConfigSnapshotFor(next),
	)
}

func telemetryRuntimeConfigSnapshotFor(cfg config.TelemetryConfig) telemetryRuntimeConfigSnapshot {
	var out telemetryRuntimeConfigSnapshot

	if cfg.Metrics.Enabled {
		out.Metrics = &telemetryMetricsRuntimeConfigSnapshot{
			BindAddress: cfg.Metrics.BindAddressOrDefault(),
			Path:        cfg.Metrics.PathOrDefault(),
		}
	}

	if cfg.Tracing.Enabled {
		out.Tracing = &telemetryTracingRuntimeConfigSnapshot{
			Endpoint:    cfg.Tracing.Endpoint,
			Protocol:    cfg.Tracing.ProtocolOrDefault(),
			Insecure:    cfg.Tracing.Insecure,
			Timeout:     cfg.Tracing.TimeoutDuration(),
			Headers:     cloneTelemetryHeaders(cfg.Tracing.Headers),
			HeadersEnv:  cloneTelemetryHeaders(cfg.Tracing.HeadersEnv),
			HeadersFile: cloneTelemetryHeaders(cfg.Tracing.HeadersFile),
		}
	}

	if cfg.Logs.Enabled {
		out.Logs = &telemetryLogsRuntimeConfigSnapshot{
			Endpoint:       cfg.Logs.Endpoint,
			Protocol:       cfg.Logs.ProtocolOrDefault(),
			Insecure:       cfg.Logs.Insecure,
			Timeout:        cfg.Logs.TimeoutDuration(),
			ExportInterval: cfg.Logs.ExportIntervalDuration(),
			QueueSize:      cfg.Logs.QueueSizeOrDefault(),
			BatchSize:      cfg.Logs.BatchSizeOrDefault(),
			Headers:        cloneTelemetryHeaders(cfg.Logs.Headers),
		}
	}

	return out
}

func (sm *SessionManager) startTelemetryRuntime(ctx context.Context) error {
	sm.configReloadMu.Lock()
	defer sm.configReloadMu.Unlock()

	if sm.telemetry != nil {
		return errors.New("telemetry runtime already started")
	}

	cfg := sm.Config()

	var (
		metrics         *daemonMetrics
		metricsGatherer prometheus.Gatherer
	)

	if cfg.Telemetry.Metrics.Enabled {
		registry := prometheus.NewRegistry()
		metrics = newDaemonMetrics(sm)

		if err := metrics.register(registry); err != nil {
			return fmt.Errorf("register metrics: %w", err)
		}

		metricsGatherer = registry
	}

	resource := telemetry.ResourceOptions{}
	if cfg.Telemetry.Tracing.Enabled || cfg.Telemetry.Logs.Enabled {
		resource = sm.telemetryResource()
	}

	var logsSecret []byte

	if cfg.Telemetry.Logs.Enabled {
		secret, err := loadOrCreateTelemetryLogsSecret(filepath.Join(sm.paths.DataDir, "telemetry-logs.key"))
		if err != nil {
			sm.log.Warn("telemetry logs secret unavailable; session refs omitted", "err", err)
		} else {
			logsSecret = secret
		}
	}

	rt, err := newTelemetryRuntime(ctx, cfg.Telemetry, cfg.SourceDir, metricsGatherer, sm.log, resource)
	if err != nil {
		return err
	}

	if rt != nil && rt.logs != nil {
		rt.logs.secret = logsSecret
	}

	if metrics != nil {
		sm.metrics.Store(metrics)
	}

	sm.tracingEnabled.Store(rt != nil && rt.tracing != nil)

	if rt != nil {
		sm.logExporter.Store(rt.logs)
	} else {
		sm.logExporter.Store(nil)
	}

	sm.telemetry = rt
	sm.configureExistingPTYTelemetry()

	return nil
}

func (sm *SessionManager) stopTelemetryRuntime() {
	sm.configReloadMu.Lock()
	defer sm.configReloadMu.Unlock()

	if sm.telemetry == nil {
		return
	}

	sm.logExporter.Store(nil)
	sm.telemetry.stop(context.Background())
	sm.telemetry = nil
	sm.metrics.Store(nil)
	sm.tracingEnabled.Store(false)
	sm.configureExistingPTYTelemetry()
}

func (sm *SessionManager) telemetryMetricsEndpoint() (addr, path string, ok bool) {
	sm.configReloadMu.Lock()
	defer sm.configReloadMu.Unlock()

	if sm.telemetry == nil || sm.telemetry.metrics == nil {
		return "", "", false
	}

	addr, path = sm.telemetry.metrics.endpoint()

	return addr, path, true
}

func (sm *SessionManager) telemetryResource() telemetry.ResourceOptions {
	executableName := ""
	if executable, err := os.Executable(); err == nil {
		executableName = filepath.Base(executable)
	} else if sm.log != nil {
		sm.log.Warn("failed to resolve process executable for tracing resource", "err", err)
	}

	return telemetry.ResourceOptions{
		ServiceName:           telemetry.ServiceNameDefault,
		ServiceVersion:        version.Version,
		Commit:                version.CommitSHA,
		DaemonInstanceID:      sm.InstanceID(),
		Profile:               sm.paths.Profile,
		ProcessKind:           "daemon",
		ProcessPID:            os.Getpid(),
		ProcessExecutableName: executableName,
	}
}

func cloneTelemetryHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}

	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = v
	}

	return out
}

func loadOrCreateTelemetryLogsSecret(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, statErr
		}

		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s must be owner-only", path)
		}

		decoded, decErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decErr != nil {
			return nil, fmt.Errorf("decode existing secret: %w", decErr)
		}

		if len(decoded) != 32 {
			return nil, fmt.Errorf("existing secret has %d bytes, want 32", len(decoded))
		}

		return decoded, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateTelemetryLogsSecret(path)
	}

	if err != nil {
		return nil, err
	}

	_, writeErr := fmt.Fprintf(file, "%s\n", hex.EncodeToString(secret))
	closeErr := file.Close()

	if writeErr != nil {
		return nil, writeErr
	}

	if closeErr != nil {
		return nil, closeErr
	}

	return secret, nil
}
