package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/telemetry"
	collectorlogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
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

	if logs := sm.logExporter.Load(); logs != nil {
		t.Fatalf("disabled telemetry created logs exporter: %+v", logs)
	}

	if _, err := os.Stat(filepath.Join(sm.paths.DataDir, "telemetry-logs.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled telemetry log secret stat err = %v, want not exist", err)
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

func TestTelemetryRuntimeLogsOptInCreatesExporterAndFlushesStartupEvent(t *testing.T) {
	receiver := newOTLPLogReceiver(t)

	cfg := config.Default()
	cfg.Telemetry.Logs.Enabled = true
	cfg.Telemetry.Logs.Endpoint = receiver.url + "/v1/logs"
	cfg.Telemetry.Logs.Protocol = config.TelemetryLogsProtocolHTTPProtobuf
	cfg.Telemetry.Logs.Timeout = "1s"
	cfg.Telemetry.Logs.ExportInterval = "1h"
	cfg.Telemetry.Logs.QueueSize = 4
	cfg.Telemetry.Logs.BatchSize = 4
	cfg.Telemetry.Logs.Headers = map[string]string{"x-graith": "canny"}

	sm := newSMWithConfig(t, cfg)
	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	if logs := sm.logExporter.Load(); logs == nil {
		t.Fatal("enabled telemetry logs did not store runtime pointer")
	}

	info, err := os.Stat(filepath.Join(sm.paths.DataDir, "telemetry-logs.key"))
	if err != nil {
		t.Fatalf("telemetry logs secret stat: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("telemetry logs secret mode = %v, want 0600", info.Mode().Perm())
	}

	sm.stopTelemetryRuntime()

	reqs := receiver.requests(t, 1)

	record := findOTLPLogRecord(reqs, "telemetry.logs_started")
	if record == nil {
		t.Fatalf("telemetry.logs_started record missing: %s", reqs)
	}

	attrs := otlpAttrMap(record)
	if got := otlpString(attrs["telemetry.protocol"]); got != config.TelemetryLogsProtocolHTTPProtobuf {
		t.Fatalf("telemetry.protocol = %q, want %q", got, config.TelemetryLogsProtocolHTTPProtobuf)
	}

	if _, ok := attrs["endpoint"]; ok {
		t.Fatalf("startup event exported endpoint attribute: %#v", attrs["endpoint"])
	}

	if _, ok := attrs["headers"]; ok {
		t.Fatalf("startup event exported headers attribute: %#v", attrs["headers"])
	}

	if got := receiver.header("x-graith"); got != "canny" {
		t.Fatalf("export HTTP header x-graith = %q, want canny", got)
	}
}

func TestSessionExitedLogEventRedactsRawSessionFields(t *testing.T) {
	receiver := newOTLPLogReceiver(t)

	cfg := config.Default()
	cfg.Telemetry.Logs.Enabled = true
	cfg.Telemetry.Logs.Endpoint = receiver.url + "/v1/logs"
	cfg.Telemetry.Logs.Protocol = config.TelemetryLogsProtocolHTTPProtobuf
	cfg.Telemetry.Logs.Timeout = "1s"
	cfg.Telemetry.Logs.ExportInterval = "1h"
	cfg.Telemetry.Logs.QueueSize = 8
	cfg.Telemetry.Logs.BatchSize = 8

	sm := newSMWithConfig(t, cfg)
	sm.paths.Profile = "canny-profile"

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}

	instanceID := sm.InstanceID()

	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	id := "braw-sensitive-id"
	sm.state.Sessions[id] = &SessionState{
		ID:           id,
		Name:         "canny-private-name",
		Status:       StatusRunning,
		Agent:        "private-agent-name",
		DriverKind:   DriverPTY,
		StopReason:   StopReasonUser,
		RepoPath:     "/repo/private",
		WorktreePath: "/work/private",
		Branch:       "feature/private",
		Sandboxed:    true,
	}

	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)

	sm.sessions[id] = sess
	sm.watchSession(id, sess)
	sm.stopTelemetryRuntime()

	reqs := receiver.requests(t, 1)

	record := findOTLPLogRecord(reqs, "session.exited")
	if record == nil {
		t.Fatalf("session.exited record missing: %s", reqs)
	}

	attrs := otlpAttrMap(record)

	if got := otlpString(attrs["schema"]); got != telemetry.DaemonLogSchema {
		t.Fatalf("schema = %q, want %q", got, telemetry.DaemonLogSchema)
	}

	if got := record.GetBody().GetStringValue(); got != "session.exited" {
		t.Fatalf("record body = %q, want session.exited", got)
	}

	if got := record.GetSeverityText(); got != telemetry.LogSeverityInfo {
		t.Fatalf("severity text = %q, want %q", got, telemetry.LogSeverityInfo)
	}

	if got := record.GetSeverityNumber(); got != logspb.SeverityNumber_SEVERITY_NUMBER_INFO {
		t.Fatalf("severity number = %s, want %s", got, logspb.SeverityNumber_SEVERITY_NUMBER_INFO)
	}

	if record.GetTimeUnixNano() == 0 {
		t.Fatal("record time is zero")
	}

	if got := otlpString(attrs["session.driver_kind"]); got != DriverPTY {
		t.Fatalf("session.driver_kind = %q, want %q", got, DriverPTY)
	}

	if got := otlpString(attrs["session.stop_reason"]); got != StopReasonUser {
		t.Fatalf("session.stop_reason = %q, want %q", got, StopReasonUser)
	}

	if got := otlpString(attrs["agent_kind"]); got != "custom" {
		t.Fatalf("agent_kind = %q, want custom", got)
	}

	if got := otlpBool(attrs["session.sandboxed"]); !got {
		t.Fatal("session.sandboxed = false, want true")
	}

	ref := otlpString(attrs["session.ref"])
	if ref == "" {
		t.Fatal("session.ref missing")
	}

	if ref == id || strings.Contains(ref, id) {
		t.Fatalf("session.ref exposed raw id: %q", ref)
	}

	resourceAttrs := findOTLPLogResourceAttributes(reqs, "session.exited")
	if got := otlpString(resourceAttrs["profile_kind"]); got != "custom" {
		t.Fatalf("resource profile_kind = %q, want custom", got)
	}

	for _, forbidden := range []string{
		"graith.daemon.instance_id",
		"graith.profile",
		"process.executable.name",
		"service.instance.id",
	} {
		if _, ok := resourceAttrs[forbidden]; ok {
			t.Fatalf("exported forbidden resource attribute %q: %#v", forbidden, resourceAttrs[forbidden])
		}
	}

	for _, forbidden := range []string{
		"id",
		"name",
		"session_id",
		"repo",
		"worktree",
		"branch",
		"path",
	} {
		if _, ok := attrs[forbidden]; ok {
			t.Fatalf("exported forbidden attribute %q: %#v", forbidden, attrs[forbidden])
		}
	}

	wire := fmt.Sprint(reqs)
	for _, forbidden := range []string{
		id,
		"canny-private-name",
		"private-agent-name",
		"/repo/private",
		"/work/private",
		"feature/private",
		"canny-profile",
		filepath.Base(executable),
		instanceID,
	} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("OTLP request contained forbidden raw value %q: %s", forbidden, wire)
		}
	}
}

func TestTelemetryRuntimeLogsSecretFailureOmitsSessionRef(t *testing.T) {
	receiver := newOTLPLogReceiver(t)

	cfg := config.Default()
	cfg.Telemetry.Logs.Enabled = true
	cfg.Telemetry.Logs.Endpoint = receiver.url + "/v1/logs"
	cfg.Telemetry.Logs.Protocol = config.TelemetryLogsProtocolHTTPProtobuf
	cfg.Telemetry.Logs.Timeout = "1s"
	cfg.Telemetry.Logs.ExportInterval = "1h"

	sm := newSMWithConfig(t, cfg)
	if err := os.WriteFile(filepath.Join(sm.paths.DataDir, "telemetry-logs.key"), []byte("dreich\n"), 0o600); err != nil {
		t.Fatalf("write corrupt telemetry logs secret: %v", err)
	}

	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v, want degraded logs without session refs", err)
	}

	sm.emitSessionExitedLogEvent(sessionExitedLogObservation{
		SessionID:    "braw-sensitive-id",
		DriverKind:   DriverPTY,
		Status:       StatusStopped,
		StopReason:   StopReasonUser,
		ExitCode:     0,
		ExitCategory: "exit-clean",
		SignalSource: "none",
		AgentKind:    "codex",
		PID:          0,
		PGID:         0,
		Signal:       0,
	})
	sm.stopTelemetryRuntime()

	reqs := receiver.requests(t, 1)

	record := findOTLPLogRecord(reqs, "session.exited")
	if record == nil {
		t.Fatalf("session.exited record missing: %s", reqs)
	}

	attrs := otlpAttrMap(record)
	if _, ok := attrs["session.ref"]; ok {
		t.Fatalf("session.ref exported despite unavailable secret: %#v", attrs["session.ref"])
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

type otlpLogReceiver struct {
	url      string
	request  chan *collectorlogpb.ExportLogsServiceRequest
	headerCh chan http.Header
}

func newOTLPLogReceiver(t *testing.T) *otlpLogReceiver {
	t.Helper()

	receiver := &otlpLogReceiver{
		request:  make(chan *collectorlogpb.ExportLogsServiceRequest, 8),
		headerCh: make(chan http.Header, 8),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req collectorlogpb.ExportLogsServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		receiver.headerCh <- r.Header.Clone()

		receiver.request <- &req

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	receiver.url = server.URL

	return receiver
}

func (r *otlpLogReceiver) requests(t *testing.T, minCount int) []*collectorlogpb.ExportLogsServiceRequest {
	t.Helper()

	var out []*collectorlogpb.ExportLogsServiceRequest

	timeout := time.After(2 * time.Second)

	for len(out) < minCount {
		select {
		case req := <-r.request:
			out = append(out, req)
		case <-timeout:
			t.Fatalf("timed out waiting for %d OTLP log request(s), got %d", minCount, len(out))
		}
	}

	for {
		select {
		case req := <-r.request:
			out = append(out, req)
		default:
			return out
		}
	}
}

func (r *otlpLogReceiver) header(name string) string {
	select {
	case header := <-r.headerCh:
		return header.Get(name)
	default:
		return ""
	}
}

func findOTLPLogRecord(reqs []*collectorlogpb.ExportLogsServiceRequest, eventName string) *logspb.LogRecord {
	for _, req := range reqs {
		for _, resourceLogs := range req.ResourceLogs {
			for _, scopeLogs := range resourceLogs.ScopeLogs {
				for _, record := range scopeLogs.LogRecords {
					if record.EventName == eventName {
						return record
					}
				}
			}
		}
	}

	return nil
}

func findOTLPLogResourceAttributes(reqs []*collectorlogpb.ExportLogsServiceRequest, eventName string) map[string]*commonpb.AnyValue {
	for _, req := range reqs {
		for _, resourceLogs := range req.ResourceLogs {
			for _, scopeLogs := range resourceLogs.ScopeLogs {
				for _, record := range scopeLogs.LogRecords {
					if record.EventName == eventName && resourceLogs.Resource != nil {
						return otlpKeyValues(resourceLogs.Resource.Attributes)
					}
				}
			}
		}
	}

	return nil
}

func otlpAttrMap(record *logspb.LogRecord) map[string]*commonpb.AnyValue {
	return otlpKeyValues(record.Attributes)
}

func otlpKeyValues(attrs []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	out := make(map[string]*commonpb.AnyValue, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr.Value
	}

	return out
}

func otlpString(value *commonpb.AnyValue) string {
	if value == nil {
		return ""
	}

	return value.GetStringValue()
}

func otlpBool(value *commonpb.AnyValue) bool {
	if value == nil {
		return false
	}

	return value.GetBoolValue()
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

func TestTelemetryRuntimeTracingMissingHeaderSourcesFail(t *testing.T) {
	const envName = "OTLP_MISSING_BRAW_HEADER"

	unsetEnvForDaemonTest(t, envName)

	sourceDir := t.TempDir()
	cfg := config.Default()
	cfg.SourceDir = sourceDir
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	cfg.Telemetry.Tracing.HeadersEnv = map[string]string{
		"authorization": envName,
	}
	cfg.Telemetry.Tracing.HeadersFile = map[string]string{
		"x-file": "missing-header",
	}

	sm := newSMWithConfig(t, cfg)

	err := sm.startTelemetryRuntime(t.Context())
	if err == nil {
		t.Fatal("startTelemetryRuntime() error = nil, want missing header source errors")
	}

	for _, want := range []string{
		`telemetry.tracing.headers_env["authorization"]`,
		envName,
		`telemetry.tracing.headers_file["x-file"]`,
		filepath.Join(sourceDir, "missing-header"),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("startTelemetryRuntime() error = %v, want substring %q", err, want)
		}
	}

	if sm.telemetry != nil {
		t.Fatalf("failed start retained runtime: %+v", sm.telemetry)
	}

	if sm.tracingEnabled.Load() {
		t.Fatal("failed tracing start left tracing enabled")
	}
}

func TestTelemetryRuntimeTracingHeaderSecretsAreNotLogged(t *testing.T) {
	const (
		inlineSecret = "Bearer canny-inline-secret" // #nosec G101 -- fixture exercises log redaction.
		envSecret    = "Bearer braw-env-secret"     // #nosec G101 -- fixture exercises log redaction.
		fileSecret   = "Bearer dreich-file-secret"  // #nosec G101 -- fixture exercises log redaction.
	)

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "otlp-header"), []byte(fileSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OTLP_BRAW_HEADER", envSecret)

	cfg := config.Default()
	cfg.SourceDir = sourceDir
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	cfg.Telemetry.Tracing.Headers = map[string]string{
		"x-inline": inlineSecret,
	}
	cfg.Telemetry.Tracing.HeadersEnv = map[string]string{
		"x-env": "OTLP_BRAW_HEADER",
	}
	cfg.Telemetry.Tracing.HeadersFile = map[string]string{
		"x-file": "otlp-header",
	}

	sm := newSMWithConfig(t, cfg)

	var logs bytes.Buffer

	sm.log = slog.New(slog.NewTextHandler(&logs, nil))

	if err := sm.startTelemetryRuntime(t.Context()); err != nil {
		t.Fatalf("startTelemetryRuntime() error = %v", err)
	}

	t.Cleanup(sm.stopTelemetryRuntime)

	got := logs.String()
	if !strings.Contains(got, "telemetry tracing exporter started") {
		t.Fatalf("startup log missing tracing start message:\n%s", got)
	}

	for _, forbidden := range []string{inlineSecret, envSecret, fileSecret, "OTLP_BRAW_HEADER", "otlp-header"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("startup log leaked %q:\n%s", forbidden, got)
		}
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
	changed.Telemetry.Logs.Endpoint = "127.0.0.1:4317"
	changed.Telemetry.Logs.QueueSize = 64

	if err := sm.applyConfig(changed); err != nil {
		t.Fatalf("applyConfig() error = %v, want inactive telemetry changes to publish", err)
	}

	if got := sm.Config().Telemetry.Metrics.Path; got != "/canny/metrics" {
		t.Fatalf("inactive telemetry reload was not published; metrics path = %q", got)
	}

	if got := sm.Config().Telemetry.Logs.QueueSize; got != 64 {
		t.Fatalf("inactive telemetry reload was not published; logs queue_size = %d", got)
	}
}

func TestApplyConfigRejectsEnabledTelemetryRuntimeReload(t *testing.T) {
	t.Run("metrics", func(t *testing.T) {
		old := config.Default()
		old.Telemetry.Metrics.Enabled = true
		old.Telemetry.Metrics.BindAddress = "127.0.0.1:9924"

		changed := config.Default()
		changed.Telemetry.Metrics.Enabled = true
		changed.Telemetry.Metrics.BindAddress = "127.0.0.1:9925"

		sm := assertApplyConfigRejectsRuntimeTelemetryReload(t, old, changed, "gr daemon restart")

		if got := sm.Config().Telemetry.Metrics.BindAddress; got != "127.0.0.1:9924" {
			t.Fatalf("rejected telemetry reload was published; bind address = %q", got)
		}
	})

	t.Run("logs", func(t *testing.T) {
		old := config.Default()
		old.Telemetry.Logs.Enabled = true
		old.Telemetry.Logs.Endpoint = "127.0.0.1:4317"

		changed := config.Default()
		changed.Telemetry.Logs.Enabled = true
		changed.Telemetry.Logs.Endpoint = "127.0.0.1:4318"

		sm := assertApplyConfigRejectsRuntimeTelemetryReload(t, old, changed, "log export")

		if got := sm.Config().Telemetry.Logs.Endpoint; got != "127.0.0.1:4317" {
			t.Fatalf("rejected telemetry reload was published; logs endpoint = %q", got)
		}
	})
}

func assertApplyConfigRejectsRuntimeTelemetryReload(
	t *testing.T,
	oldCfg *config.Config,
	changedCfg *config.Config,
	wantErr string,
) *SessionManager {
	t.Helper()

	sm := newSMWithConfig(t, oldCfg)

	err := sm.applyConfig(changedCfg)
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("applyConfig() error = %v, want substring %q", err, wantErr)
	}

	return sm
}

func TestApplyConfigRejectsEnabledTracingHeaderSourceReload(t *testing.T) {
	old := config.Default()
	old.Telemetry.Tracing.Enabled = true
	old.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	old.Telemetry.Tracing.HeadersEnv = map[string]string{
		"authorization": "OTLP_BRAW_HEADER",
	}

	sm := newSMWithConfig(t, old)

	changed := config.Default()
	changed.Telemetry.Tracing.Enabled = true
	changed.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	changed.Telemetry.Tracing.HeadersEnv = map[string]string{
		"authorization": "OTLP_CANNY_HEADER",
	}

	err := sm.applyConfig(changed)
	if err == nil || !strings.Contains(err.Error(), "gr daemon restart") {
		t.Fatalf("applyConfig() error = %v, want restart-only telemetry rejection", err)
	}

	if got := sm.Config().Telemetry.Tracing.HeadersEnv["authorization"]; got != "OTLP_BRAW_HEADER" {
		t.Fatalf("rejected telemetry reload was published; header env = %q", got)
	}
}

func unsetEnvForDaemonTest(t *testing.T, name string) {
	t.Helper()

	value, ok := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}

	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(name, value)
			return
		}

		_ = os.Unsetenv(name)
	})
}
