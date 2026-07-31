package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

var telemetryListen = net.Listen

type telemetryRuntime struct {
	metrics *telemetryMetricsRuntime
	tracing *telemetryTracingRuntime
}

func newTelemetryRuntime(ctx context.Context, cfg config.TelemetryConfig, log *slog.Logger) (*telemetryRuntime, error) {
	if !cfg.Enabled() {
		return nil, nil
	}

	rt := &telemetryRuntime{}

	if cfg.Metrics.Enabled {
		metrics, err := startTelemetryMetricsRuntime(ctx, cfg.Metrics, log)
		if err != nil {
			return nil, err
		}

		rt.metrics = metrics
	}

	if cfg.Tracing.Enabled {
		rt.tracing = newTelemetryTracingRuntime(cfg.Tracing)
		if log != nil {
			log.Info("telemetry tracing configured",
				"endpoint", cfg.Tracing.Endpoint,
				"protocol", cfg.Tracing.ProtocolOrDefault(),
				"insecure", cfg.Tracing.Insecure,
				"timeout", cfg.Tracing.TimeoutDuration())
		}
	}

	return rt, nil
}

func (rt *telemetryRuntime) stop() {
	if rt == nil {
		return
	}

	if rt.metrics != nil {
		rt.metrics.stop()
	}

	if rt.tracing != nil {
		rt.tracing.stop()
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

func startTelemetryMetricsRuntime(ctx context.Context, cfg config.TelemetryMetricsConfig, log *slog.Logger) (*telemetryMetricsRuntime, error) {
	path := cfg.PathOrDefault()

	mux := http.NewServeMux()
	handler := newTelemetryMetricsHandler()

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

func newTelemetryMetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, "# graith metrics endpoint enabled\n")
	})
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
}

func newTelemetryTracingRuntime(cfg config.TelemetryTracingConfig) *telemetryTracingRuntime {
	return &telemetryTracingRuntime{
		cfg: config.TelemetryTracingConfig{
			Enabled:  cfg.Enabled,
			Endpoint: cfg.Endpoint,
			Protocol: cfg.Protocol,
			Insecure: cfg.Insecure,
			Timeout:  cfg.Timeout,
			Headers:  cloneTelemetryHeaders(cfg.Headers),
		},
	}
}

func (rt *telemetryTracingRuntime) stop() {}

type telemetryRuntimeConfigSnapshot struct {
	Metrics *telemetryMetricsRuntimeConfigSnapshot
	Tracing *telemetryTracingRuntimeConfigSnapshot
}

type telemetryMetricsRuntimeConfigSnapshot struct {
	BindAddress string
	Path        string
}

type telemetryTracingRuntimeConfigSnapshot struct {
	Endpoint string
	Protocol string
	Insecure bool
	Timeout  time.Duration
	Headers  map[string]string
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
			Endpoint: cfg.Tracing.Endpoint,
			Protocol: cfg.Tracing.ProtocolOrDefault(),
			Insecure: cfg.Tracing.Insecure,
			Timeout:  cfg.Tracing.TimeoutDuration(),
			Headers:  cloneTelemetryHeaders(cfg.Tracing.Headers),
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

	rt, err := newTelemetryRuntime(ctx, cfg.Telemetry, sm.log)
	if err != nil {
		return err
	}

	sm.telemetry = rt

	return nil
}

func (sm *SessionManager) stopTelemetryRuntime() {
	sm.configReloadMu.Lock()
	defer sm.configReloadMu.Unlock()

	if sm.telemetry == nil {
		return
	}

	sm.telemetry.stop()
	sm.telemetry = nil
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
