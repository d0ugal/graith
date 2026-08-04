package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
)

func TestCheckAlloyGeneratedConfigValidationUsesPlaceholderEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	oldCfg, oldPaths, oldOut, oldAlloyConfig := cfg, paths, out, doctorAlloyConfig

	t.Cleanup(func() {
		cfg, paths, out, doctorAlloyConfig = oldCfg, oldPaths, oldOut, oldAlloyConfig
	})

	out = output.NewWithWriter(false, &bytes.Buffer{})
	doctorAlloyConfig = ""

	dir := t.TempDir()
	envCapture := filepath.Join(dir, "env.txt")
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
if [ "$1" = "--version" ]; then
  echo "alloy, version 1.18.0"
  exit 0
fi
if [ "$1" = "validate" ] && [ "$2" = "--help" ]; then
  echo "validate help"
  exit 0
fi
if [ "$1" = "validate" ]; then
  env > '`+envCapture+`'
  test -f "$2"
  exit $?
fi
echo "unknown command" >&2
exit 1
`)

	t.Setenv("GRAITH_LOKI_TOKEN", "real-loki-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "real-cloud-secret")

	cfg = config.Default()
	paths = alloyTestPaths(filepath.Join(dir, "data"))

	dc := newDoctorContext()
	dc.checkAlloyConfigValidation(binary, []alloySignal{
		alloySignalDaemonLogs,
		alloySignalMetrics,
		alloySignalTraces,
	})

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("generated Alloy validation failed: %s", failed)
	}

	envData, err := os.ReadFile(envCapture)
	if err != nil {
		t.Fatal(err)
	}

	envText := string(envData)
	for _, leaked := range []string{"real-loki-secret", "real-cloud-secret", "AWS_SECRET_ACCESS_KEY"} {
		if strings.Contains(envText, leaked) {
			t.Fatalf("Alloy validation environment leaked %q:\n%s", leaked, envText)
		}
	}

	if !strings.Contains(envText, "GRAITH_LOKI_TOKEN=doctor-placeholder") {
		t.Fatalf("Alloy validation environment did not include generated placeholder values:\n%s", envText)
	}
}

func TestCheckAlloySuppliedConfigValidationFailureDoesNotLeakAlloyOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	oldOut, oldAlloyConfig := out, doctorAlloyConfig

	t.Cleanup(func() {
		out, doctorAlloyConfig = oldOut, oldAlloyConfig
	})

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)

	dir := t.TempDir()

	configPath := filepath.Join(dir, "config.alloy")
	if err := os.WriteFile(configPath, []byte("logging {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	doctorAlloyConfig = configPath
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
if [ "$1" = "validate" ] && [ "$2" = "--help" ]; then
  echo "validate help"
  exit 0
fi
if [ "$1" = "validate" ]; then
  echo "diagnostic with thrawn-secret" >&2
  exit 2
fi
exit 1
`)

	dc := newDoctorContext()
	dc.checkAlloyConfigValidation(binary, []alloySignal{alloySignalDaemonLogs})

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "validation failed") {
		t.Fatalf("expected validation failure, got: %q", failed)
	}

	if strings.Contains(failed, "thrawn-secret") || strings.Contains(buf.String(), "thrawn-secret") {
		t.Fatalf("doctor leaked Alloy validation output:\nchecks=%s\nrendered=%s", failed, buf.String())
	}
}

func TestAlloyValidateSupportedHandlesCobraUnknownHelpTopic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	dir := t.TempDir()
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
if [ "$1" = "validate" ] && [ "$2" = "--help" ]; then
  echo "unknown command validate" >&2
  exit 1
fi
if [ "$1" = "help" ] && [ "$2" = "validate" ]; then
  echo "Unknown help topic validate" >&2
  exit 0
fi
exit 1
`)

	supported, err := alloyValidateSupported(binary)
	if err != nil {
		t.Fatalf("alloyValidateSupported() error = %v", err)
	}

	if supported {
		t.Fatal("alloyValidateSupported() = true, want false for Cobra unknown help topic")
	}
}

func TestCheckAlloyUnsupportedValidationWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, &bytes.Buffer{})

	dir := t.TempDir()
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
if [ "$1" = "validate" ] && [ "$2" = "--help" ]; then
  echo "unknown command validate" >&2
  exit 1
fi
if [ "$1" = "help" ] && [ "$2" = "validate" ]; then
  echo "Unknown help topic validate" >&2
  exit 0
fi
exit 1
`)

	dc := newDoctorContext()
	dc.checkAlloyConfigValidation(binary, []alloySignal{alloySignalDaemonLogs})

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("unsupported validation should warn, got failures: %s", failed)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "does not support `alloy validate`") {
		t.Fatalf("expected unsupported validation warning, got: %q", warned)
	}
}

func TestResolveDoctorAlloyBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	dir := t.TempDir()
	binary := writeDoctorAlloyStub(t, dir, "alloy", `exit 0`)

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tests := map[string]struct {
		raw        string
		wantPath   string
		wantSource string
	}{
		"path search": {
			wantPath:   binary,
			wantSource: "PATH",
		},
		"configured path": {
			raw:        binary,
			wantPath:   binary,
			wantSource: "configured path",
		},
		"configured command": {
			raw:        "alloy",
			wantPath:   binary,
			wantSource: "configured command",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gotPath, gotSource, err := resolveDoctorAlloyBinary(test.raw)
			if err != nil {
				t.Fatalf("resolveDoctorAlloyBinary(%q) error = %v", test.raw, err)
			}

			if gotPath != test.wantPath || gotSource != test.wantSource {
				t.Fatalf("resolveDoctorAlloyBinary(%q) = %q, %q; want %q, %q", test.raw, gotPath, gotSource, test.wantPath, test.wantSource)
			}
		})
	}
}

func TestCheckAlloyVersionReportsFirstLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, &bytes.Buffer{})

	dir := t.TempDir()
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
if [ "$1" = "--version" ]; then
  printf 'alloy, version 1.18.0\nbuild details\n'
  exit 0
fi
exit 1
`)

	dc := newDoctorContext()
	dc.checkAlloyVersion(binary)

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("version check should pass, got failures: %s", failed)
	}

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "Alloy version: alloy, version 1.18.0") || strings.Contains(passed, "build details") {
		t.Fatalf("version check did not report only first line: %q", passed)
	}
}

func TestRunDoctorCommandTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group cancellation fixture is Unix-only")
	}

	originalWaitDelay := doctorCommandWaitDelay
	doctorCommandWaitDelay = 100 * time.Millisecond

	t.Cleanup(func() { doctorCommandWaitDelay = originalWaitDelay })

	dir := t.TempDir()
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
trap '' TERM
sleep 30 &
wait
`)

	start := time.Now()
	result := runDoctorCommand(100*time.Millisecond, binary, []string{"--version"}, doctorMinimalEnv(nil))
	elapsed := time.Since(start)

	if !strings.Contains(doctorCommandErr(result.err), "timed out") {
		t.Fatalf("runDoctorCommand() err = %v, want timeout", result.err)
	}

	if elapsed > 2*time.Second {
		t.Fatalf("runDoctorCommand() elapsed = %v, want bounded timeout", elapsed)
	}
}

func TestRunDoctorCommandTruncatesLargeSuccessfulOutputWithoutShortWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	dir := t.TempDir()
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
i=0
while [ "$i" -lt 20000 ]; do
  printf x
  i=$((i + 1))
done
exit 0
`)

	result := runDoctorCommand(doctorAlloyCommandTimeout, binary, []string{"--version"}, doctorMinimalEnv(nil))
	if result.err != nil {
		t.Fatalf("runDoctorCommand() error = %v, want nil", result.err)
	}

	if len(result.stdout) != doctorCommandOutputLimit {
		t.Fatalf("stdout length = %d, want capped length %d", len(result.stdout), doctorCommandOutputLimit)
	}
}

func TestCheckAlloyMetricsEndpointReachable(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() { cfg, out = oldCfg, oldOut })

	out = output.NewWithWriter(false, &bytes.Buffer{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/braw/metrics" {
			t.Fatalf("metrics request path = %q, want /braw/metrics", r.URL.Path)
		}

		_, _ = w.Write([]byte("# HELP graith_daemon_info\n"))
	}))
	defer server.Close()

	cfg = config.Default()
	cfg.Telemetry.Metrics.Enabled = true
	cfg.Telemetry.Metrics.BindAddress = strings.TrimPrefix(server.URL, "http://")
	cfg.Telemetry.Metrics.Path = "/braw/metrics"

	dc := newDoctorContext()
	dc.checkAlloyMetricsEndpoint()

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("metrics endpoint should be reachable, got failures: %s", failed)
	}

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "Metrics scrape endpoint reachable") {
		t.Fatalf("expected metrics reachability pass, got: %q", passed)
	}
}

func TestCheckAlloyMetricsEndpointDisabledWarns(t *testing.T) {
	oldCfg, oldOut := cfg, out

	t.Cleanup(func() { cfg, out = oldCfg, oldOut })

	out = output.NewWithWriter(false, &bytes.Buffer{})
	cfg = config.Default()

	dc := newDoctorContext()
	dc.checkAlloyMetricsEndpoint()

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "[telemetry.metrics] enabled is false") {
		t.Fatalf("expected disabled metrics warning, got: %q", warned)
	}
}

func TestCheckAlloyReadableLogFiles(t *testing.T) {
	oldCfg, oldPaths, oldOut := cfg, paths, out

	t.Cleanup(func() { cfg, paths, out = oldCfg, oldPaths, oldOut })

	out = output.NewWithWriter(false, &bytes.Buffer{})

	dir := t.TempDir()
	paths = alloyTestPaths(dir)
	cfg = config.Default()

	for _, path := range []string{paths.DaemonLog, paths.DaemonStderrLog} {
		if err := os.WriteFile(path, []byte("braw\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dc := newDoctorContext()
	dc.checkAlloyLogFiles()

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("log files should be readable, got failures: %s", failed)
	}

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	for _, want := range []string{"daemon log readable", "daemon stderr log readable"} {
		if !strings.Contains(passed, want) {
			t.Fatalf("expected %q in log checks, got: %q", want, passed)
		}
	}
}

func TestValidateDoctorBackendURL(t *testing.T) {
	tests := map[string]struct {
		kind      doctorBackendKind
		raw       string
		wantLevel string
		wantText  string
		notText   string
	}{
		"loki push URL": {
			kind:      doctorBackendLoki,
			raw:       "https://logs-prod.example.net/loki/api/v1/push",
			wantLevel: "ok",
			wantText:  "Loki push",
		},
		"mimir remote write URL": {
			kind:      doctorBackendMimir,
			raw:       "https://prometheus-prod.example.net/api/prom/push",
			wantLevel: "ok",
			wantText:  "remote-write",
		},
		"tempo base URL": {
			kind:      doctorBackendTempo,
			raw:       "https://otlp-gateway.example.net/otlp",
			wantLevel: "ok",
			wantText:  "OTLP HTTP base",
		},
		"credentials rejected without echoing them": {
			kind:      doctorBackendLoki,
			raw:       "https://token-secret@logs.example.net/loki/api/v1/push",
			wantLevel: "fail",
			wantText:  "must not embed credentials",
			notText:   "token-secret",
		},
		"query rejected without echoing it": {
			kind:      doctorBackendMimir,
			raw:       "https://prometheus.example.net/api/prom/push?token=secret",
			wantLevel: "fail",
			wantText:  "query strings",
			notText:   "secret",
		},
		"internal whitespace rejected": {
			kind:      doctorBackendLoki,
			raw:       "https://logs.example.net/foo /loki/api/v1/push",
			wantLevel: "fail",
			wantText:  "whitespace",
		},
		"tempo trace URL warns": {
			kind:      doctorBackendTempo,
			raw:       "https://otlp.example.net/otlp/v1/traces",
			wantLevel: "warn",
			wantText:  "trace-specific",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := validateDoctorBackendURL(test.kind, test.raw)
			if got.level != test.wantLevel {
				t.Fatalf("level = %q, want %q (%s)", got.level, test.wantLevel, got.message)
			}

			if !strings.Contains(got.message, test.wantText) {
				t.Fatalf("message = %q, want substring %q", got.message, test.wantText)
			}

			if test.notText != "" && strings.Contains(got.message, test.notText) {
				t.Fatalf("message leaked %q: %q", test.notText, got.message)
			}
		})
	}
}

func TestCheckAlloyBackendURLsDoesNotPrintSecretValues(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)

	t.Setenv("GRAITH_LOKI_URL", "https://token-secret@logs.example.net/loki/api/v1/push")

	dc := newDoctorContext()
	dc.checkAlloyBackendURLs(map[alloySignal]bool{alloySignalDaemonLogs: true})

	rendered := buf.String()
	if !strings.Contains(rendered, "must not embed credentials") {
		t.Fatalf("expected credential-shape warning, got:\n%s", rendered)
	}

	if strings.Contains(rendered, "token-secret") {
		t.Fatalf("backend URL diagnostic leaked URL userinfo:\n%s", rendered)
	}
}

func TestCheckAlloyConfigValidationRemoteTraceEndpointWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	oldCfg, oldPaths, oldOut, oldAlloyConfig := cfg, paths, out, doctorAlloyConfig

	t.Cleanup(func() {
		cfg, paths, out, doctorAlloyConfig = oldCfg, oldPaths, oldOut, oldAlloyConfig
	})

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)
	doctorAlloyConfig = ""

	dir := t.TempDir()
	binary := writeDoctorAlloyStub(t, dir, "alloy", `
if [ "$1" = "validate" ] && [ "$2" = "--help" ]; then
  echo "validate help"
  exit 0
fi
if [ "$1" = "validate" ]; then
  echo "validate should not run for remote trace endpoint" >&2
  exit 9
fi
exit 1
`)

	cfg = config.Default()
	cfg.Telemetry.Tracing = config.TelemetryTracingConfig{
		Enabled:  true,
		Endpoint: "https://otlp-gateway-prod-us-east-0.grafana.net/otlp/v1/traces",
		Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
	}
	paths = alloyTestPaths(filepath.Join(dir, "data"))

	dc := newDoctorContext()
	dc.checkAlloyConfigValidation(binary, []alloySignal{alloySignalMetrics, alloySignalTraces})

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("remote direct trace endpoint should warn, not fail: %s", failed)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Generated Alloy config validation skipped") {
		t.Fatalf("expected generated config validation warning, got: %q", warned)
	}

	rendered := buf.String()
	if !strings.Contains(rendered, "--alloy-signals metrics") {
		t.Fatalf("expected doctor-specific --alloy-signals hint, got:\n%s", rendered)
	}

	if strings.Contains(rendered, "--signals daemon-logs,metrics") {
		t.Fatalf("doctor output used config command flag name instead of --alloy-signals:\n%s", rendered)
	}
}

func TestCheckAlloyDefaultSignalsSkipLokiChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	oldCfg, oldPaths, oldOut, oldBinary, oldSignals := cfg, paths, out, doctorAlloyBinary, doctorAlloySignals

	t.Cleanup(func() {
		cfg, paths, out, doctorAlloyBinary, doctorAlloySignals = oldCfg, oldPaths, oldOut, oldBinary, oldSignals
	})

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)

	dir := t.TempDir()
	doctorAlloyBinary = writeDoctorAlloyStub(t, dir, "alloy", `
if [ "$1" = "--version" ]; then
  echo "alloy, version 1.18.0"
  exit 0
fi
if [ "$1" = "validate" ] && [ "$2" = "--help" ]; then
  echo "validate help"
  exit 0
fi
if [ "$1" = "validate" ]; then
  test -f "$2"
  exit $?
fi
exit 1
`)
	doctorAlloySignals = configAlloyDefaultSignals

	t.Setenv("GRAITH_LOKI_URL", "https://token-secret@logs.example.net/loki/api/v1/push")
	t.Setenv("GRAITH_MIMIR_URL", "https://prometheus.example.net/api/prom/push")
	t.Setenv("GRAITH_TEMPO_OTLP_ENDPOINT", "https://otlp.example.net/otlp")

	cfg = config.Default()
	paths = alloyTestPaths(filepath.Join(dir, "data"))

	dc := newDoctorContext()
	dc.checkAlloy()

	rendered := buf.String()
	if strings.Contains(rendered, "GRAITH_LOKI_URL") || strings.Contains(rendered, "token-secret") {
		t.Fatalf("default Alloy doctor signals should not inspect Loki settings:\n%s", rendered)
	}
}

func TestParseBrewServices(t *testing.T) {
	got, err := parseBrewServices(`[{"name":"alloy","status":"started"},{"name":"caddy","status":"none"}]`)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0].Name != "alloy" || got[0].Status != "started" {
		t.Fatalf("parseBrewServices() = %+v", got)
	}
}

func TestSystemdServiceStateParsesKnownInactiveOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	dir := t.TempDir()
	systemctl := writeDoctorAlloyStub(t, dir, "systemctl", `
echo inactive
exit 3
`)

	state, known := systemdServiceState(systemctl, "is-active", "alloy.service")
	if !known || state != "inactive" {
		t.Fatalf("systemdServiceState() = %q, %v; want inactive, true", state, known)
	}
}

func TestSystemdServiceStatePreservesUserBusEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}

	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, "runtime")
	bus := "unix:path=" + filepath.Join(runtimeDir, "bus")

	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", bus)

	systemctl := writeDoctorAlloyStub(t, dir, "systemctl", `
if [ "$1" = "--user" ]; then
  test "$XDG_RUNTIME_DIR" = "`+runtimeDir+`" || exit 42
  test "$DBUS_SESSION_BUS_ADDRESS" = "`+bus+`" || exit 43
fi
echo active
exit 0
`)

	state, known := systemdServiceState(systemctl, "--user", "is-active", "alloy.service")
	if !known || state != "active" {
		t.Fatalf("systemdServiceState() = %q, %v; want active, true", state, known)
	}
}

func writeDoctorAlloyStub(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // executable test fixture
		t.Fatalf("write stub %s: %v", name, err)
	}

	return path
}
