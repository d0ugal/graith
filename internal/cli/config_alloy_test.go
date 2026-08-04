package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestParseAlloySignals(t *testing.T) {
	tests := map[string]struct {
		raw     string
		want    []alloySignal
		wantErr string
	}{
		"all": {
			raw:  "all",
			want: []alloySignal{alloySignalDaemonLogs, alloySignalMetrics, alloySignalTraces},
		},
		"aliases and stable order": {
			raw:  "trace,logs,metric",
			want: []alloySignal{alloySignalDaemonLogs, alloySignalMetrics, alloySignalTraces},
		},
		"duplicates collapse": {
			raw:  "metrics,metrics",
			want: []alloySignal{alloySignalMetrics},
		},
		"empty token rejected": {
			raw:     "metrics,",
			wantErr: "empty signal",
		},
		"unknown rejected": {
			raw:     "session-logs",
			wantErr: "unknown Alloy signal",
		},
		"all with another token rejected": {
			raw:     "all,metrics",
			wantErr: "all",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseAlloySignals(test.raw)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseAlloySignals(%q) error = %v, want %q", test.raw, err, test.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseAlloySignals(%q) error = %v", test.raw, err)
			}

			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseAlloySignals(%q) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}

func TestRenderAlloyConfigGolden(t *testing.T) {
	tests := map[string]struct {
		cfg     *config.Config
		paths   config.Paths
		signals string
		golden  string
	}{
		"linux grpc": {
			cfg:     alloyTestConfigGRPC(),
			paths:   alloyTestPaths("/home/bairn/.local/share/graith"),
			signals: "all",
			golden:  "alloy_linux_grpc.golden",
		},
		"macos http": {
			cfg:     alloyTestConfigHTTP(),
			paths:   alloyTestPaths("/Users/bairn/Library/Application Support/graith"),
			signals: "all",
			golden:  "alloy_macos_http.golden",
		},
		"profile logs metrics": {
			cfg:     alloyTestConfigProfile(),
			paths:   alloyTestPaths("/home/bairn/.local/share/graith-canny"),
			signals: "daemon-logs,metrics",
			golden:  "alloy_profile_logs_metrics.golden",
		},
		"traces grpc tls": {
			cfg:     alloyTestConfigGRPCTLS(),
			paths:   alloyTestPaths("/home/bairn/.local/share/graith"),
			signals: "traces",
			golden:  "alloy_traces_grpc_tls.golden",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			signals, err := parseAlloySignals(test.signals)
			if err != nil {
				t.Fatal(err)
			}

			got, err := renderAlloyConfig(alloyRenderInput{
				Config:  test.cfg,
				Paths:   test.paths,
				Signals: signals,
			})
			if err != nil {
				t.Fatal(err)
			}

			want := readAlloyGolden(t, test.golden)
			if got != want {
				t.Fatalf("renderAlloyConfig() mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
			}
		})
	}
}

func TestConfigAlloyCommandUsesCustomDataDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")
	customDataDir := filepath.Join(dir, "dreich-data")
	body := fmt.Sprintf("data_dir = %q\n", customDataDir)

	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	baseDataDir := filepath.Join(dir, "xdg", "graith-canny")
	basePaths := alloyTestPaths(baseDataDir)
	basePaths.Profile = "canny"
	basePaths.AppName = "graith-canny"
	basePaths.ConfigFile = target

	previousSignals := configAlloySignals

	var buf bytes.Buffer

	configAlloySignals = "daemon-logs"

	configAlloyCmd.SetOut(&buf)

	t.Cleanup(func() {
		configAlloySignals = previousSignals

		configAlloyCmd.SetOut(nil)
	})

	withConfigGlobals(t, target, basePaths, func() {
		if err := configAlloyCmd.RunE(configAlloyCmd, nil); err != nil {
			t.Fatal(err)
		}
	})

	got := buf.String()
	if !strings.Contains(got, filepath.Join(customDataDir, "daemon.log")) {
		t.Fatalf("generated Alloy config did not use custom data_dir:\n%s", got)
	}

	if strings.Contains(got, baseDataDir) {
		t.Fatalf("generated Alloy config used profile default data dir despite custom data_dir:\n%s", got)
	}

	if strings.Contains(got, filepath.Join("logs", "*.log")) {
		t.Fatalf("generated Alloy config included session log glob:\n%s", got)
	}
}

func TestConfigAlloyCommandResolvesProfilePathAndSignalsFlag(t *testing.T) {
	dir := t.TempDir()
	dataHome := filepath.Join(dir, "data")

	t.Setenv("GRAITH_PROFILE", "canny")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))
	unsetGraithSessionID(t)

	got := runConfigAlloyCLI(t, "config", "alloy", "--signals", "daemon-logs")
	wantDaemonLog := filepath.Join(dataHome, "graith-canny", "daemon.log")

	if !strings.Contains(got, wantDaemonLog) {
		t.Fatalf("generated Alloy config did not use resolved profile data path %q:\n%s", wantDaemonLog, got)
	}

	if strings.Contains(got, "prometheus.scrape") || strings.Contains(got, "otelcol.receiver.otlp") {
		t.Fatalf("--signals daemon-logs rendered unrequested telemetry signals:\n%s", got)
	}
}

func TestConfigAlloyCommandConfigFlagDataDirOverridesProfile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")
	customDataDir := filepath.Join(dir, "dreich-data")
	profileDataDir := filepath.Join(dir, "data", "graith-canny")
	body := fmt.Sprintf("data_dir = %q\n", customDataDir)

	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GRAITH_PROFILE", "canny")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))
	unsetGraithSessionID(t)

	got := runConfigAlloyCLI(t, "--config", target, "config", "alloy", "--signals", "daemon-logs")

	if !strings.Contains(got, filepath.Join(customDataDir, "daemon.log")) {
		t.Fatalf("generated Alloy config did not use custom data_dir:\n%s", got)
	}

	if strings.Contains(got, profileDataDir) {
		t.Fatalf("generated Alloy config used profile default data dir despite custom data_dir:\n%s", got)
	}

	if strings.Contains(got, filepath.Join("logs", "*.log")) {
		t.Fatalf("generated Alloy config included session log glob:\n%s", got)
	}
}

func TestRenderAlloyConfigDoesNotInlineTracingHeaders(t *testing.T) {
	cfg := alloyTestConfigGRPC()
	cfg.Telemetry.Tracing.Headers = map[string]string{
		"Authorization": "Bearer thrawn-secret-fixture", // #nosec G101 -- fixture verifies redaction boundary.
	}

	got, err := renderAlloyConfig(alloyRenderInput{
		Config:  cfg,
		Paths:   alloyTestPaths("/home/bairn/.local/share/graith"),
		Signals: []alloySignal{alloySignalTraces},
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(got, "thrawn-secret-fixture") {
		t.Fatalf("generated Alloy config leaked tracing header:\n%s", got)
	}

	if !strings.Contains(got, `sys.env("GRAITH_TEMPO_TOKEN")`) {
		t.Fatalf("generated Alloy config should use backend token environment references:\n%s", got)
	}
}

func TestRenderAlloyConfigNotesDisabledTelemetry(t *testing.T) {
	cfg := config.Default()

	got, err := renderAlloyConfig(alloyRenderInput{
		Config:  cfg,
		Paths:   alloyTestPaths("/home/bairn/.local/share/graith"),
		Signals: []alloySignal{alloySignalMetrics, alloySignalTraces},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "[telemetry.metrics] enabled is false") {
		t.Fatalf("generated Alloy config should note disabled metrics:\n%s", got)
	}

	if !strings.Contains(got, "[telemetry.tracing] enabled is false") {
		t.Fatalf("generated Alloy config should note disabled tracing:\n%s", got)
	}
}

func TestParseAlloySignalsTrimsUnknownToken(t *testing.T) {
	_, err := parseAlloySignals("metrics, sessions")
	if err == nil {
		t.Fatal("parseAlloySignals() error = nil, want unknown signal")
	}

	if !strings.Contains(err.Error(), `unknown Alloy signal "sessions"`) {
		t.Fatalf("parseAlloySignals() error = %v, want trimmed token", err)
	}
}

func TestResolveAlloyTraceReceiverRejectsInvalidLocalListeners(t *testing.T) {
	tests := map[string]struct {
		tracing config.TelemetryTracingConfig
		wantErr string
	}{
		"remote grpc exporter target": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "collector.example.net:4317",
				Protocol: config.TelemetryTracingProtocolGRPC,
				Insecure: true,
			},
			wantErr: "not loopback",
		},
		"remote http exporter target": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "https://collector.example.net:4318/v1/traces",
				Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
			},
			wantErr: "not loopback",
		},
		"portless http endpoint": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "http://localhost/v1/traces",
				Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
			},
			wantErr: "host:port",
		},
		"portless https endpoint": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "https://localhost/v1/traces",
				Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
			},
			wantErr: "host:port",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAlloyTraceReceiver(test.tracing)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("resolveAlloyTraceReceiver() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestResolveAlloyTraceReceiverAllowsLoopbackListeners(t *testing.T) {
	tests := map[string]struct {
		tracing      config.TelemetryTracingConfig
		wantEndpoint string
	}{
		"grpc localhost": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "localhost:4317",
				Protocol: config.TelemetryTracingProtocolGRPC,
				Insecure: true,
			},
			wantEndpoint: "localhost:4317",
		},
		"grpc ipv6 loopback": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "[::1]:4317",
				Protocol: config.TelemetryTracingProtocolGRPC,
				Insecure: true,
			},
			wantEndpoint: "[::1]:4317",
		},
		"http localhost": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "http://localhost:4318/v1/traces",
				Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
			},
			wantEndpoint: "localhost:4318",
		},
		"https ipv6 loopback": {
			tracing: config.TelemetryTracingConfig{
				Endpoint: "https://[::1]:4318/v1/traces",
				Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
			},
			wantEndpoint: "[::1]:4318",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := resolveAlloyTraceReceiver(test.tracing)
			if err != nil {
				t.Fatal(err)
			}

			if got.Endpoint != test.wantEndpoint {
				t.Fatalf("Endpoint = %q, want %q", got.Endpoint, test.wantEndpoint)
			}
		})
	}
}

func TestAlloyMetricsScrapeAddress(t *testing.T) {
	tests := map[string]struct {
		bindAddress string
		want        string
	}{
		"empty host": {
			bindAddress: ":4824",
			want:        "127.0.0.1:4824",
		},
		"ipv4 wildcard": {
			bindAddress: "0.0.0.0:4824",
			want:        "127.0.0.1:4824",
		},
		"ipv6 wildcard": {
			bindAddress: "[::]:4824",
			want:        "[::1]:4824",
		},
		"ipv4 loopback": {
			bindAddress: "127.0.0.1:4824",
			want:        "127.0.0.1:4824",
		},
		"ipv6 loopback": {
			bindAddress: "[::1]:4824",
			want:        "[::1]:4824",
		},
		"hostname": {
			bindAddress: "metrics.local:4824",
			want:        "metrics.local:4824",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := alloyMetricsScrapeAddress(test.bindAddress)
			if err != nil {
				t.Fatal(err)
			}

			if got != test.want {
				t.Fatalf("alloyMetricsScrapeAddress(%q) = %q, want %q", test.bindAddress, got, test.want)
			}
		})
	}
}

func alloyTestConfigGRPC() *config.Config {
	cfg := config.Default()
	cfg.Telemetry.Metrics.Enabled = true
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolGRPC
	cfg.Telemetry.Tracing.Insecure = true

	return cfg
}

func alloyTestConfigGRPCTLS() *config.Config {
	cfg := config.Default()
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolGRPC

	return cfg
}

func alloyTestConfigHTTP() *config.Config {
	cfg := config.Default()
	cfg.Telemetry.Metrics.Enabled = true
	cfg.Telemetry.Metrics.BindAddress = "127.0.0.1:9824"
	cfg.Telemetry.Metrics.Path = "/graith/metrics"
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "http://127.0.0.1:4318/otlp/v1/traces"
	cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolHTTPProtobuf

	return cfg
}

func alloyTestConfigProfile() *config.Config {
	cfg := config.Default()
	cfg.Telemetry.Metrics.Enabled = true
	cfg.Telemetry.Metrics.BindAddress = "[::1]:4824"

	return cfg
}

func alloyTestPaths(dataDir string) config.Paths {
	return config.Paths{
		DataDir:         dataDir,
		LogDir:          filepath.Join(dataDir, "logs"),
		DaemonLog:       filepath.Join(dataDir, "daemon.log"),
		DaemonStderrLog: filepath.Join(dataDir, "daemon.stderr.log"),
	}
}

func readAlloyGolden(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func runConfigAlloyCLI(t *testing.T, args ...string) string {
	t.Helper()
	registerCommands()
	restoreConfigFlag(t)

	signalsFlag := configAlloyCmd.Flags().Lookup("signals")
	originalSignalsFlagValue := signalsFlag.Value.String()
	originalSignalsFlagChanged := signalsFlag.Changed
	originalCfgFile := cfgFile
	originalPaths := paths
	originalSignals := configAlloySignals
	originalJSONOutput := jsonOutput
	originalAgentMode := agentMode
	originalOut := out

	cfgFile = ""
	paths = config.Paths{}
	configAlloySignals = configAlloyDefaultSignals
	jsonOutput = false
	agentMode = false
	out = nil

	t.Cleanup(func() {
		_ = signalsFlag.Value.Set(originalSignalsFlagValue)
		signalsFlag.Changed = originalSignalsFlagChanged
		cfgFile = originalCfgFile
		paths = originalPaths
		configAlloySignals = originalSignals
		jsonOutput = originalJSONOutput
		agentMode = originalAgentMode
		out = originalOut
	})

	return captureStdout(t, func() {
		if err := executeWithArgs(args); err != nil {
			t.Fatalf("gr %s: %v", strings.Join(args, " "), err)
		}
	})
}

func unsetGraithSessionID(t *testing.T) {
	t.Helper()

	value, ok := os.LookupEnv("GRAITH_SESSION_ID")

	if err := os.Unsetenv("GRAITH_SESSION_ID"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if ok {
			_ = os.Setenv("GRAITH_SESSION_ID", value)
		} else {
			_ = os.Unsetenv("GRAITH_SESSION_ID")
		}
	})
}
