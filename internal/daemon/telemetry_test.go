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
	"github.com/d0ugal/graith/internal/telemetry"
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

	if metrics := sm.metrics.Load(); metrics != nil {
		t.Fatalf("disabled telemetry created metrics registry: %+v", metrics)
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

	body := scrapeTelemetryMetrics(t, sm)

	for _, want := range []string{
		"# TYPE graith_daemon_info gauge",
		"# TYPE graith_daemon_uptime_seconds gauge",
		"# TYPE graith_daemon_attached_clients gauge",
		"# TYPE graith_sessions gauge",
		"# TYPE graith_session_launch_duration_seconds histogram",
		"# TYPE graith_session_lifecycle_transitions_total counter",
		"# TYPE graith_session_input_events_total counter",
		"# TYPE graith_session_input_bytes_total counter",
		"# TYPE graith_session_input_duration_seconds histogram",
		"# TYPE graith_session_input_readback_latency_seconds histogram",
		"# TYPE graith_pty_output_read_duration_seconds histogram",
		"# TYPE graith_pty_screen_update_duration_seconds histogram",
		"# TYPE graith_pty_attach_fanout_duration_seconds histogram",
		"# TYPE graith_attach_output_queue_delay_seconds histogram",
		"# TYPE graith_attach_output_write_duration_seconds histogram",
		"# TYPE graith_screen_snapshot_requests_total counter",
		"# TYPE graith_screen_snapshot_duration_seconds histogram",
		"# TYPE graith_messages_published_total counter",
	} {
		assertMetricsContain(t, body, want)
	}
}

func TestTelemetryRuntimeMetricsLabelsStayLowCardinality(t *testing.T) {
	cfg := config.Default()
	cfg.Telemetry.Metrics.Enabled = true
	cfg.Telemetry.Metrics.BindAddress = "127.0.0.1:0"

	deletedAt := time.Now()
	sm := newSMWithConfig(t, cfg)
	sm.state.Sessions["braw-id"] = &SessionState{
		ID:           "braw-id",
		Name:         "canny-name",
		Status:       StatusRunning,
		DriverKind:   DriverPTY,
		RepoPath:     "/repo/croft",
		WorktreePath: "/work/bothy",
		Branch:       "feature/thrawn",
	}
	sm.state.Sessions["headless-id"] = &SessionState{
		ID:           "headless-id",
		Name:         "dreich-headless",
		Status:       StatusStopped,
		DriverKind:   DriverHeadless,
		RepoPath:     "/repo/strath",
		WorktreePath: "/work/strath",
		Branch:       "feature/haar",
	}
	sm.state.Sessions["deleted-id"] = &SessionState{
		ID:         "deleted-id",
		Name:       "deleted-name",
		Status:     StatusRunning,
		DriverKind: DriverPTY,
		DeletedAt:  &deletedAt,
	}

	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	t.Cleanup(sm.stopTelemetryRuntime)

	sm.observeSessionLaunch("fork-braw-id", "custom-driver-braw-id", time.Millisecond, errors.New("dreich launch"))
	sm.observeSessionLaunch(metricOperationFork, DriverPTY, time.Millisecond, nil)
	sm.observeSessionLaunch(metricOperationOrchestratorCreate, DriverPTY, time.Millisecond, nil)
	sm.observeSessionInput("paste-canny-name", 42, time.Millisecond, nil)
	sm.observeSessionInputReadback("paste-canny-name", time.Millisecond)
	sm.observePTYOutputRead(time.Millisecond, errors.New("dreich read"))
	sm.observePTYScreenUpdate(time.Millisecond, errors.New("dreich screen"))
	sm.observePTYAttachFanout(time.Millisecond, nil)
	sm.observeAttachOutputQueueDelay(attachOutputMode("private-mode"), time.Millisecond)
	sm.observeAttachOutputWrite(attachOutputMode("private-mode"), time.Millisecond, errors.New("dreich write"))
	sm.observeScreenSnapshot("history-bothy", time.Millisecond)
	sm.observeSessionLifecycleTransition("fash-from", "thrawn-to")
	sm.observeSessionLifecycleTransition(string(StatusRunning), string(StatusStopped))
	sm.observeMessagePublished(Message{Stream: "inbox:braw-id", SenderID: "device:canny-device"})
	sm.observeMessagePublished(Message{Stream: "blether-topic", SenderID: "headless-id"})

	body := scrapeTelemetryMetrics(t, sm)

	for _, want := range []string{
		`graith_sessions{driver_kind="pty",status="running"} 1`,
		`graith_sessions{driver_kind="headless",status="stopped"} 1`,
		`graith_session_launch_duration_seconds_count{driver_kind="pty",operation="fork",result="success"} 1`,
		`graith_session_launch_duration_seconds_count{driver_kind="pty",operation="orchestrator_create",result="success"} 1`,
		`graith_session_launch_duration_seconds_count{driver_kind="unknown",operation="unknown",result="error"} 1`,
		`graith_session_input_events_total{operation="unknown",result="success"} 1`,
		`graith_session_input_readback_latency_seconds_count{operation="unknown"} 1`,
		`graith_pty_output_read_duration_seconds_count{result="error"} 1`,
		`graith_pty_screen_update_duration_seconds_count{result="error"} 1`,
		`graith_pty_attach_fanout_duration_seconds_count{result="success"} 1`,
		`graith_attach_output_queue_delay_seconds_count{mode="unknown"} 1`,
		`graith_attach_output_write_duration_seconds_count{mode="unknown",result="error"} 1`,
		`graith_screen_snapshot_requests_total{kind="unknown"} 1`,
		`graith_session_lifecycle_transitions_total{from="running",to="stopped"} 1`,
		`graith_messages_published_total{sender_kind="device",stream_kind="inbox"} 1`,
		`graith_messages_published_total{sender_kind="session",stream_kind="topic"} 1`,
	} {
		assertMetricsContain(t, body, want)
	}

	for _, secret := range []string{
		"braw-id",
		"canny-name",
		"custom-driver-braw-id",
		"/repo/croft",
		"/work/bothy",
		"feature/thrawn",
		"headless-id",
		"deleted-name",
		"paste-canny-name",
		"private-mode",
		"fash-from",
		"thrawn-to",
		`graith_session_lifecycle_transitions_total{from="unknown",to="unknown"}`,
	} {
		assertMetricsNotContain(t, body, secret)
	}
}

func TestAttachLatencyTelemetryDisabledObserversAreEmpty(t *testing.T) {
	sm := newSMWithConfig(t, config.Default())

	if sm.latencyTelemetryEnabled() {
		t.Fatal("disabled telemetry reported latency telemetry enabled")
	}

	if observers := sm.ptyTelemetryObservers(); !observers.Empty() {
		t.Fatalf("disabled telemetry installed PTY observers: %#v", observers)
	}

	attachTelemetry := sm.attachOutputTelemetry()
	if attachTelemetry.observesQueueDelay() || attachTelemetry.observesWrite() {
		t.Fatalf("disabled telemetry installed attach output callbacks: %#v", attachTelemetry)
	}
}

func scrapeTelemetryMetrics(t *testing.T, sm *SessionManager) string {
	t.Helper()

	addr, path, ok := sm.telemetryMetricsEndpoint()
	if !ok {
		t.Fatal("metrics endpoint not active")
	}

	client := &http.Client{Timeout: 2 * time.Second}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatalf("create metrics request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET metrics endpoint: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d body prefix=%q, want 200", resp.StatusCode, string(body[:min(len(body), 512)]))
	}

	return string(body)
}

func assertMetricsContain(t *testing.T, body, want string) {
	t.Helper()

	if !strings.Contains(body, want) {
		t.Fatalf("metrics body missing %q", want)
	}
}

func assertMetricsNotContain(t *testing.T, body, forbidden string) {
	t.Helper()

	if strings.Contains(body, forbidden) {
		t.Fatalf("metrics body contains high-cardinality value %q", forbidden)
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

	if metrics := sm.metrics.Load(); metrics != nil {
		t.Fatalf("tracing-only telemetry created metrics registry: %+v", metrics)
	}
}

func TestTelemetryRuntimeTracingResource(t *testing.T) {
	sm := newSMWithConfig(t, config.Default())
	sm.paths.Profile = "canny"

	got := sm.telemetryResource()
	if got.ServiceName != telemetry.ServiceNameDefault {
		t.Errorf("ServiceName = %q, want %q", got.ServiceName, telemetry.ServiceNameDefault)
	}

	if got.DaemonInstanceID != sm.InstanceID() {
		t.Errorf("DaemonInstanceID = %q, want %q", got.DaemonInstanceID, sm.InstanceID())
	}

	if got.Profile != "canny" {
		t.Errorf("Profile = %q, want canny", got.Profile)
	}

	if got.ProcessPID <= 0 {
		t.Errorf("ProcessPID = %d, want positive", got.ProcessPID)
	}

	if got.ProcessKind != "daemon" {
		t.Errorf("ProcessKind = %q, want daemon", got.ProcessKind)
	}

	if got.ProcessExecutableName == "" {
		t.Error("ProcessExecutableName is empty")
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

	if metrics := sm.metrics.Load(); metrics != nil {
		t.Fatalf("failed start retained metrics registry: %+v", metrics)
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

func TestApplyConfigRejectsEnabledTracingKnobReload(t *testing.T) {
	tests := map[string]struct {
		oldMutate     func(*config.TelemetryTracingConfig)
		changedMutate func(*config.TelemetryTracingConfig)
	}{
		"sampling ratio": {
			changedMutate: func(c *config.TelemetryTracingConfig) {
				ratio := 0.5
				c.SamplingRatio = &ratio
			},
		},
		"queue size": {
			changedMutate: func(c *config.TelemetryTracingConfig) {
				queueSize := 1024
				c.QueueSize = &queueSize
			},
		},
		"max export batch size": {
			changedMutate: func(c *config.TelemetryTracingConfig) {
				batchSize := 256
				c.MaxExportBatchSize = &batchSize
			},
		},
		"schedule delay": {
			changedMutate: func(c *config.TelemetryTracingConfig) {
				c.ScheduleDelay = "1s"
			},
		},
		"compression": {
			oldMutate: func(c *config.TelemetryTracingConfig) {
				c.Endpoint = "http://127.0.0.1:4318/v1/traces"
				c.Protocol = config.TelemetryTracingProtocolHTTPProtobuf
			},
			changedMutate: func(c *config.TelemetryTracingConfig) {
				c.Endpoint = "http://127.0.0.1:4318/v1/traces"
				c.Protocol = config.TelemetryTracingProtocolHTTPProtobuf
				c.Compression = config.TelemetryTracingCompressionGzip
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			old := config.Default()
			old.Telemetry.Tracing.Enabled = true
			old.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
			if test.oldMutate != nil {
				test.oldMutate(&old.Telemetry.Tracing)
			}

			changed := config.Default()
			changed.Telemetry.Tracing.Enabled = true
			changed.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
			if test.changedMutate != nil {
				test.changedMutate(&changed.Telemetry.Tracing)
			}

			sm := newSMWithConfig(t, old)

			err := sm.applyConfig(changed)
			if err == nil || !strings.Contains(err.Error(), "gr daemon restart") {
				t.Fatalf("applyConfig() error = %v, want restart-only telemetry rejection", err)
			}
		})
	}
}
