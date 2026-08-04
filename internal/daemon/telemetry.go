package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/telemetry"
	"github.com/d0ugal/graith/internal/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var telemetryListen = net.Listen

type telemetryRuntime struct {
	metrics *telemetryMetricsRuntime
	tracing *telemetryTracingRuntime
}

func newTelemetryRuntime(
	ctx context.Context,
	cfg config.TelemetryConfig,
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

	if cfg.Tracing.Enabled {
		tracing, err := newTelemetryTracingRuntime(ctx, cfg.Tracing, resource, log)
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
				"timeout", cfg.Tracing.TimeoutDuration(),
				"sampling_ratio", cfg.Tracing.SamplingRatioOrDefault(),
				"queue_size", cfg.Tracing.QueueSizeOrDefault(),
				"max_export_batch_size", cfg.Tracing.MaxExportBatchSizeOrDefault(),
				"schedule_delay", cfg.Tracing.ScheduleDelayDuration(),
				"compression", cfg.Tracing.CompressionOrDefault())
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
	cfg config.TelemetryTracingConfig
	rt  *telemetry.TracingRuntime
	log *slog.Logger
}

func newTelemetryTracingRuntime(
	ctx context.Context,
	cfg config.TelemetryTracingConfig,
	resource telemetry.ResourceOptions,
	log *slog.Logger,
) (*telemetryTracingRuntime, error) {
	samplingRatio := cfg.SamplingRatioOrDefault()

	tracing, err := telemetry.StartTracing(ctx, telemetry.TracingOptions{
		Endpoint:           cfg.Endpoint,
		Protocol:           cfg.ProtocolOrDefault(),
		Insecure:           cfg.Insecure,
		Timeout:            cfg.TimeoutDuration(),
		Headers:            cfg.Headers,
		SamplingRatio:      &samplingRatio,
		QueueSize:          cfg.QueueSizeOrDefault(),
		MaxExportBatchSize: cfg.MaxExportBatchSizeOrDefault(),
		ScheduleDelay:      cfg.ScheduleDelayDuration(),
		Compression:        cfg.CompressionOrDefault(),
		Resource:           resource,
		Logger:             log,
	})
	if err != nil {
		return nil, err
	}

	return &telemetryTracingRuntime{
		cfg: config.TelemetryTracingConfig{
			Enabled:            cfg.Enabled,
			Endpoint:           cfg.Endpoint,
			Protocol:           cfg.Protocol,
			Insecure:           cfg.Insecure,
			Timeout:            cfg.Timeout,
			SamplingRatio:      cloneFloat64Ptr(cfg.SamplingRatio),
			QueueSize:          cloneIntPtr(cfg.QueueSize),
			MaxExportBatchSize: cloneIntPtr(cfg.MaxExportBatchSize),
			ScheduleDelay:      cfg.ScheduleDelay,
			Compression:        cfg.Compression,
			Headers:            cloneTelemetryHeaders(cfg.Headers),
		},
		rt:  tracing,
		log: log,
	}, nil
}

func (rt *telemetryTracingRuntime) stop(ctx context.Context) {
	if rt == nil || rt.rt == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, rt.cfg.TimeoutDuration())
	defer cancel()

	if err := rt.rt.Shutdown(ctx); err != nil && rt.log != nil {
		rt.log.Warn("telemetry tracing exporter shutdown failed", "err", err)
	}
}

type telemetryRuntimeConfigSnapshot struct {
	Metrics *telemetryMetricsRuntimeConfigSnapshot
	Tracing *telemetryTracingRuntimeConfigSnapshot
}

type telemetryMetricsRuntimeConfigSnapshot struct {
	BindAddress string
	Path        string
}

type telemetryTracingRuntimeConfigSnapshot struct {
	Endpoint           string
	Protocol           string
	Insecure           bool
	Timeout            time.Duration
	SamplingRatio      float64
	QueueSize          int
	MaxExportBatchSize int
	ScheduleDelay      time.Duration
	Compression        string
	Headers            map[string]string
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
			Endpoint:           cfg.Tracing.Endpoint,
			Protocol:           cfg.Tracing.ProtocolOrDefault(),
			Insecure:           cfg.Tracing.Insecure,
			Timeout:            cfg.Tracing.TimeoutDuration(),
			SamplingRatio:      cfg.Tracing.SamplingRatioOrDefault(),
			QueueSize:          cfg.Tracing.QueueSizeOrDefault(),
			MaxExportBatchSize: cfg.Tracing.MaxExportBatchSizeOrDefault(),
			ScheduleDelay:      cfg.Tracing.ScheduleDelayDuration(),
			Compression:        cfg.Tracing.CompressionOrDefault(),
			Headers:            cloneTelemetryHeaders(cfg.Tracing.Headers),
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
	if cfg.Telemetry.Tracing.Enabled {
		resource = sm.telemetryResource()
	}

	rt, err := newTelemetryRuntime(ctx, cfg.Telemetry, metricsGatherer, sm.log, resource)
	if err != nil {
		return err
	}

	if metrics != nil {
		sm.metrics.Store(metrics)
	}

	sm.tracingEnabled.Store(rt != nil && rt.tracing != nil)

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

func cloneFloat64Ptr(in *float64) *float64 {
	if in == nil {
		return nil
	}

	out := *in

	return &out
}

func cloneIntPtr(in *int) *int {
	if in == nil {
		return nil
	}

	out := *in

	return &out
}
