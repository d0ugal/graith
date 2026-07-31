package daemon

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestTelemetryRuntimeDisabledStartsNothing(t *testing.T) {
	var listens atomic.Int32

	oldListen := telemetryListen
	telemetryListen = func(network, address string) (net.Listener, error) {
		listens.Add(1)

		return nil, errors.New("unexpected listen")
	}

	t.Cleanup(func() { telemetryListen = oldListen })

	sm := newSMWithConfig(t, config.Default())

	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	if got := listens.Load(); got != 0 {
		t.Fatalf("disabled telemetry called listen %d time(s)", got)
	}

	if sm.telemetry != nil {
		t.Fatalf("disabled telemetry created runtime: %+v", sm.telemetry)
	}
}

func TestTelemetryRuntimeMetricsEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Telemetry.Metrics.Enabled = true
	cfg.Telemetry.Metrics.BindAddress = "127.0.0.1:0"
	cfg.Telemetry.Metrics.Path = "/braw/metrics"

	sm := newSMWithConfig(t, cfg)
	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	t.Cleanup(sm.stopTelemetryRuntime)

	addr, path, ok := sm.telemetryMetricsEndpoint()
	if !ok {
		t.Fatal("metrics endpoint not active")
	}

	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Get("http://" + addr + path) //nolint:noctx // bounded by client timeout and t.Context cleanup.
	if err != nil {
		t.Fatalf("GET metrics endpoint: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d body=%q, want 200", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), "graith metrics endpoint enabled") {
		t.Fatalf("metrics body = %q", body)
	}
}

func TestTelemetryRuntimeMetricsPathIsLiteral(t *testing.T) {
	cfg := config.Default()
	cfg.Telemetry.Metrics.Enabled = true
	cfg.Telemetry.Metrics.BindAddress = "127.0.0.1:0"
	cfg.Telemetry.Metrics.Path = "/met{rics"

	sm := newSMWithConfig(t, cfg)
	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	t.Cleanup(sm.stopTelemetryRuntime)

	addr, path, ok := sm.telemetryMetricsEndpoint()
	if !ok {
		t.Fatal("metrics endpoint not active")
	}

	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Get("http://" + addr + path) //nolint:noctx // bounded by client timeout and t.Context cleanup.
	if err != nil {
		t.Fatalf("GET literal metrics endpoint: %v", err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("literal metrics status = %d, want 200", resp.StatusCode)
	}

	resp, err = client.Get("http://" + addr + "/metrics") //nolint:noctx // bounded by client timeout and t.Context cleanup.
	if err != nil {
		t.Fatalf("GET unmatched metrics endpoint: %v", err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unmatched metrics status = %d, want 404", resp.StatusCode)
	}
}

func TestTelemetryRuntimeTracingOnlyDoesNotListen(t *testing.T) {
	var listens atomic.Int32

	oldListen := telemetryListen
	telemetryListen = func(network, address string) (net.Listener, error) {
		listens.Add(1)

		return nil, errors.New("unexpected listen")
	}

	t.Cleanup(func() { telemetryListen = oldListen })

	cfg := config.Default()
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"

	sm := newSMWithConfig(t, cfg)
	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	t.Cleanup(sm.stopTelemetryRuntime)

	if got := listens.Load(); got != 0 {
		t.Fatalf("tracing-only telemetry called listen %d time(s)", got)
	}

	if sm.telemetry == nil || sm.telemetry.tracing == nil {
		t.Fatalf("tracing runtime not recorded: %+v", sm.telemetry)
	}
}

func TestTelemetryRuntimeMetricsListenFailure(t *testing.T) {
	oldListen := telemetryListen
	telemetryListen = func(network, address string) (net.Listener, error) {
		return nil, errors.New("dreich bind failed")
	}

	t.Cleanup(func() { telemetryListen = oldListen })

	cfg := config.Default()
	cfg.Telemetry.Metrics.Enabled = true

	sm := newSMWithConfig(t, cfg)

	err := sm.startTelemetryRuntime(t.Context())
	if err == nil || !strings.Contains(err.Error(), "dreich bind failed") {
		t.Fatalf("startTelemetryRuntime() error = %v, want bind failure", err)
	}

	if sm.telemetry != nil {
		t.Fatalf("failed start retained runtime: %+v", sm.telemetry)
	}
}

func TestApplyConfigRejectsTelemetryReload(t *testing.T) {
	sm := newSMWithConfig(t, config.Default())

	changed := config.Default()
	changed.Telemetry.Metrics.Enabled = true

	err := sm.applyConfig(changed)
	if err == nil || !strings.Contains(err.Error(), "gr daemon restart") {
		t.Fatalf("applyConfig() error = %v, want restart-only telemetry rejection", err)
	}

	if sm.Config().Telemetry.Metrics.Enabled {
		t.Fatal("rejected telemetry reload was published")
	}
}

func TestApplyConfigAllowsInactiveTelemetryReload(t *testing.T) {
	sm := newSMWithConfig(t, config.Default())

	changed := config.Default()
	changed.Telemetry.Metrics.BindAddress = "127.0.0.1:9924"
	changed.Telemetry.Metrics.Path = "/canny/metrics"
	changed.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"

	if err := sm.applyConfig(changed); err != nil {
		t.Fatalf("applyConfig() error = %v, want inactive telemetry changes to publish", err)
	}

	if got := sm.Config().Telemetry.Metrics.Path; got != "/canny/metrics" {
		t.Fatalf("inactive telemetry reload was not published; metrics path = %q", got)
	}
}

func TestApplyConfigRejectsEnabledTelemetryRuntimeReload(t *testing.T) {
	old := config.Default()
	old.Telemetry.Metrics.Enabled = true
	old.Telemetry.Metrics.BindAddress = "127.0.0.1:9924"

	sm := newSMWithConfig(t, old)

	changed := config.Default()
	changed.Telemetry.Metrics.Enabled = true
	changed.Telemetry.Metrics.BindAddress = "127.0.0.1:9925"

	err := sm.applyConfig(changed)
	if err == nil || !strings.Contains(err.Error(), "gr daemon restart") {
		t.Fatalf("applyConfig() error = %v, want restart-only telemetry rejection", err)
	}

	if got := sm.Config().Telemetry.Metrics.BindAddress; got != "127.0.0.1:9924" {
		t.Fatalf("rejected telemetry reload was published; bind address = %q", got)
	}
}
