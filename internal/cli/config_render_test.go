package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"go.yaml.in/yaml/v3"
)

var updateConfigRenderGolden = flag.Bool("update", false, "regenerate config render golden files")

func TestRenderOTelCollectorConfigGolden(t *testing.T) {
	baseCfg := config.Default()
	baseCfg.Telemetry.Metrics.Enabled = true
	baseCfg.Telemetry.Metrics.BindAddress = "127.0.0.1:4824"
	baseCfg.Telemetry.Metrics.Path = "/metrics"
	baseCfg.Telemetry.Tracing.Enabled = true
	baseCfg.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	baseCfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolGRPC
	baseCfg.Telemetry.Tracing.Insecure = true

	paths := otelCollectorGoldenPaths()

	tests := map[string]struct {
		cfg       config.Config
		opts      otelCollectorRenderOptions
		golden    string
		forbidden []string
		required  []string
	}{
		"default metrics and traces": {
			cfg:    *baseCfg,
			opts:   defaultOTelCollectorRenderOptions(),
			golden: "otelcol_default.golden.yaml",
			forbidden: []string{
				"file_log/graith_daemon",
				"filelog/graith_daemon",
				"logs/graith_daemon",
				"/home/braw/.local/share/graith/logs",
				"/home/braw/.local/share/graith/daemon.log",
			},
			required: []string{
				"prometheus/graith",
				"otlp/graith",
				"prometheus_remote_write/graith",
				"otlp_http/graith",
				"retry_on_failure",
				"remote_write_queue",
				"Session scrollback logs are intentionally excluded",
			},
		},
		"daemon logs requested": {
			cfg: *baseCfg,
			opts: otelCollectorRenderOptions{
				includeDaemonLogs: true,
			},
			golden: "otelcol_daemon_logs.golden.yaml",
			forbidden: []string{
				"/home/braw/.local/share/graith/logs",
			},
			required: []string{
				"file_log/graith_daemon",
				"/home/braw/.local/share/graith/daemon.log",
				"/home/braw/.local/share/graith/daemon.stderr.log",
				"logs/graith_daemon",
				"file_storage/graith",
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := renderOTelCollectorConfig(test.cfg, paths, test.opts)
			if err != nil {
				t.Fatalf("render config: %v", err)
			}

			assertValidYAML(t, got)

			for _, forbidden := range test.forbidden {
				if strings.Contains(got, forbidden) {
					t.Fatalf("rendered config contains forbidden %q:\n%s", forbidden, got)
				}
			}

			for _, required := range test.required {
				if !strings.Contains(got, required) {
					t.Fatalf("rendered config missing required %q:\n%s", required, got)
				}
			}

			goldenPath := filepath.Join("testdata", test.golden)
			if *updateConfigRenderGolden {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
					t.Fatalf("create golden dir: %v", err)
				}

				if err := os.WriteFile(goldenPath, []byte(got), 0o600); err != nil {
					t.Fatalf("write golden: %v", err)
				}

				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v", goldenPath, err)
			}

			if got != string(want) {
				t.Fatalf("rendered config differs from %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, string(want))
			}
		})
	}
}

func TestConfigRenderOTelCollectorUsesCommandOutput(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")
	dataDir := filepath.Join(dir, "data")

	body := strings.Join([]string{
		`data_dir = "` + dataDir + `"`,
		"",
		"[telemetry.metrics]",
		"enabled = true",
		`bind_address = "127.0.0.1:4824"`,
		`path = "/metrics"`,
		"",
		"[telemetry.tracing]",
		"enabled = true",
		`endpoint = "127.0.0.1:4317"`,
		`protocol = "grpc"`,
		"insecure = true",
		"",
	}, "\n")

	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	previousOpts := configRenderOTelCollectorOpts

	var buf bytes.Buffer

	configRenderOTelCollectorOpts = defaultOTelCollectorRenderOptions()

	configRenderOTelCollectorCmd.SetOut(&buf)

	t.Cleanup(func() {
		configRenderOTelCollectorOpts = previousOpts

		configRenderOTelCollectorCmd.SetOut(nil)
	})

	withConfigGlobals(t, target, config.Paths{ConfigFile: target}, func() {
		if err := configRenderOTelCollectorCmd.RunE(configRenderOTelCollectorCmd, nil); err != nil {
			t.Fatal(err)
		}
	})

	got := buf.String()
	if !strings.Contains(got, `targets: ["127.0.0.1:4824"]`) {
		t.Fatalf("generated OTel Collector config was not written to command output:\n%s", got)
	}

	if !strings.Contains(got, filepath.Join(dataDir, "otelcol", "prometheus-remote-write-wal")) {
		t.Fatalf("generated OTel Collector config did not use custom data_dir:\n%s", got)
	}
}

func TestRenderOTelCollectorConfigDerivesTraceReceiverEndpoint(t *testing.T) {
	tests := map[string]struct {
		mutate   func(*config.Config)
		required []string
	}{
		"grpc endpoint": {
			mutate: func(cfg *config.Config) {
				cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolGRPC
				cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:9437"
				cfg.Telemetry.Tracing.Insecure = true
			},
			required: []string{
				`endpoint: "127.0.0.1:9437"`,
				`traces_url_path: "/v1/traces"`,
			},
		},
		"http endpoint host and path": {
			mutate: func(cfg *config.Config) {
				cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolHTTPProtobuf
				cfg.Telemetry.Tracing.Endpoint = "http://127.0.0.1:9438/custom/v1/traces"
			},
			required: []string{
				`endpoint: "127.0.0.1:9438"`,
				`traces_url_path: "/custom/v1/traces"`,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			test.mutate(cfg)

			got, err := renderOTelCollectorConfig(*cfg, otelCollectorGoldenPaths(), otelCollectorRenderOptions{})
			if err != nil {
				t.Fatalf("render config: %v", err)
			}

			assertValidYAML(t, got)

			for _, required := range test.required {
				if !strings.Contains(got, required) {
					t.Fatalf("rendered config missing derived value %q:\n%s", required, got)
				}
			}
		})
	}
}

func TestRenderOTelCollectorConfigDerivesTraceReceiverTLS(t *testing.T) {
	tests := map[string]struct {
		mutate   func(*config.Config)
		required []string
	}{
		"grpc tls endpoint": {
			mutate: func(cfg *config.Config) {
				cfg.Telemetry.Tracing.Enabled = true
				cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolGRPC
				cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:9437"
				cfg.Telemetry.Tracing.Insecure = false
			},
			required: []string{
				`endpoint: "127.0.0.1:9437"`,
				"Graith's local gRPC trace endpoint uses TLS",
				`cert_file: "${env:GRAITH_OTLP_RECEIVER_TLS_CERT_FILE}"`,
				`key_file: "${env:GRAITH_OTLP_RECEIVER_TLS_KEY_FILE}"`,
			},
		},
		"http tls endpoint": {
			mutate: func(cfg *config.Config) {
				cfg.Telemetry.Tracing.Enabled = true
				cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolHTTPProtobuf
				cfg.Telemetry.Tracing.Endpoint = "https://127.0.0.1:9438/custom/v1/traces"
			},
			required: []string{
				`endpoint: "127.0.0.1:9438"`,
				`traces_url_path: "/custom/v1/traces"`,
				"Graith's local HTTP trace endpoint uses TLS",
				`cert_file: "${env:GRAITH_OTLP_RECEIVER_TLS_CERT_FILE}"`,
				`key_file: "${env:GRAITH_OTLP_RECEIVER_TLS_KEY_FILE}"`,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			test.mutate(cfg)

			got, err := renderOTelCollectorConfig(*cfg, otelCollectorGoldenPaths(), otelCollectorRenderOptions{})
			if err != nil {
				t.Fatalf("render config: %v", err)
			}

			assertValidYAML(t, got)

			for _, required := range test.required {
				if !strings.Contains(got, required) {
					t.Fatalf("rendered config missing TLS value %q:\n%s", required, got)
				}
			}
		})
	}
}

func TestRenderOTelCollectorConfigPreservesHTTPTracePathWhenListenOverridden(t *testing.T) {
	cfg := config.Default()
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolHTTPProtobuf
	cfg.Telemetry.Tracing.Endpoint = "http://127.0.0.1:9438/custom/v1/traces"

	got, err := renderOTelCollectorConfig(*cfg, otelCollectorGoldenPaths(), otelCollectorRenderOptions{
		otlpHTTPListen: "127.0.0.1:5555",
	})
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	assertValidYAML(t, got)

	for _, required := range []string{
		`endpoint: "127.0.0.1:5555"`,
		`traces_url_path: "/custom/v1/traces"`,
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("rendered config missing overridden HTTP receiver value %q:\n%s", required, got)
		}
	}
}

func TestRenderOTelCollectorConfigWarnsForNonLoopbackListen(t *testing.T) {
	cfg := config.Default()

	got, err := renderOTelCollectorConfig(*cfg, otelCollectorGoldenPaths(), otelCollectorRenderOptions{
		otlpGRPCListen: "0.0.0.0:4317",
	})
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	assertValidYAML(t, got)

	if !strings.Contains(got, "Warning: this receiver is not bound to loopback") {
		t.Fatalf("rendered config should warn for non-loopback receivers:\n%s", got)
	}
}

func TestRenderOTelCollectorConfigRequiresDaemonLogPaths(t *testing.T) {
	cfg := config.Default()

	_, err := renderOTelCollectorConfig(*cfg, config.Paths{
		DataDir: t.TempDir(),
	}, otelCollectorRenderOptions{
		includeDaemonLogs: true,
	})
	if err == nil {
		t.Fatal("render config error = nil, want daemon log path error")
	}

	if !strings.Contains(err.Error(), "daemon log path") {
		t.Fatalf("render config error = %v, want daemon log path guidance", err)
	}
}

func TestRenderOTelCollectorConfigRejectsRemoteTraceReceiverEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "tempo.example.com:443"
	cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolGRPC

	_, err := renderOTelCollectorConfig(*cfg, otelCollectorGoldenPaths(), otelCollectorRenderOptions{})
	if err == nil {
		t.Fatal("render config error = nil, want remote receiver endpoint error")
	}

	if !strings.Contains(err.Error(), "is not loopback") {
		t.Fatalf("render config error = %v, want loopback guidance", err)
	}
}

func TestRenderOTelCollectorConfigDoesNotInlineTracingHeaders(t *testing.T) {
	cfg := config.Default()
	cfg.Telemetry.Tracing.Enabled = true
	cfg.Telemetry.Tracing.Endpoint = "127.0.0.1:4317"
	cfg.Telemetry.Tracing.Protocol = config.TelemetryTracingProtocolGRPC
	cfg.Telemetry.Tracing.Insecure = true
	cfg.Telemetry.Tracing.Headers = map[string]string{
		"Authorization": "Bearer thrawn-secret-fixture", // #nosec G101 -- fixture verifies redaction boundary.
	}

	got, err := renderOTelCollectorConfig(*cfg, otelCollectorGoldenPaths(), otelCollectorRenderOptions{})
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	assertValidYAML(t, got)

	if strings.Contains(got, "thrawn-secret-fixture") {
		t.Fatalf("generated OTel Collector config leaked tracing header:\n%s", got)
	}

	if !strings.Contains(got, defaultCollectorOTLPHTTPAuthHeader) {
		t.Fatalf("generated OTel Collector config should use backend token environment references:\n%s", got)
	}
}

func TestRenderOTelCollectorConfigNotesDisabledTelemetry(t *testing.T) {
	cfg := config.Default()

	got, err := renderOTelCollectorConfig(*cfg, otelCollectorGoldenPaths(), otelCollectorRenderOptions{})
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	assertValidYAML(t, got)

	if !strings.Contains(got, "[telemetry.metrics] enabled is false") {
		t.Fatalf("generated OTel Collector config should note disabled metrics:\n%s", got)
	}

	if !strings.Contains(got, "[telemetry.tracing] enabled is false") {
		t.Fatalf("generated OTel Collector config should note disabled tracing:\n%s", got)
	}
}

func TestCollectorScrapeTarget(t *testing.T) {
	tests := map[string]struct {
		bind string
		want string
	}{
		"loopback": {
			bind: "127.0.0.1:4824",
			want: "127.0.0.1:4824",
		},
		"wildcard ipv4": {
			bind: "0.0.0.0:4824",
			want: "127.0.0.1:4824",
		},
		"wildcard empty host": {
			bind: ":4824",
			want: "127.0.0.1:4824",
		},
		"ipv6 loopback": {
			bind: "[::1]:4824",
			want: "[::1]:4824",
		},
		"wildcard ipv6": {
			bind: "[::]:4824",
			want: "[::1]:4824",
		},
		"expanded wildcard ipv6": {
			bind: "[0:0:0:0:0:0:0:0]:4824",
			want: "[::1]:4824",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := collectorScrapeTarget(test.bind)
			if err != nil {
				t.Fatalf("collectorScrapeTarget: %v", err)
			}

			if got != test.want {
				t.Fatalf("collectorScrapeTarget(%q) = %q, want %q", test.bind, got, test.want)
			}
		})
	}
}

func TestOTelCollectorRenderOptionsValidate(t *testing.T) {
	tests := map[string]struct {
		opts    otelCollectorRenderOptions
		wantErr string
	}{
		"valid": {
			opts: defaultOTelCollectorRenderOptions(),
		},
		"valid prometheus combined scrape interval": {
			opts: otelCollectorRenderOptions{
				scrapeInterval:     "1h30m",
				otlpGRPCListen:     defaultCollectorOTLPGRPCListen,
				otlpHTTPListen:     defaultCollectorOTLPHTTPListen,
				otlpHTTPTracesPath: defaultCollectorOTLPHTTPTracesPath,
			},
		},
		"bad scrape interval": {
			opts: otelCollectorRenderOptions{
				scrapeInterval: "dreich",
				otlpGRPCListen: defaultCollectorOTLPGRPCListen,
				otlpHTTPListen: defaultCollectorOTLPHTTPListen,
			},
			wantErr: "--scrape-interval",
		},
		"fractional scrape interval": {
			opts: otelCollectorRenderOptions{
				scrapeInterval: "1.5s",
				otlpGRPCListen: defaultCollectorOTLPGRPCListen,
				otlpHTTPListen: defaultCollectorOTLPHTTPListen,
			},
			wantErr: "--scrape-interval",
		},
		"unsupported scrape interval unit": {
			opts: otelCollectorRenderOptions{
				scrapeInterval: "100us",
				otlpGRPCListen: defaultCollectorOTLPGRPCListen,
				otlpHTTPListen: defaultCollectorOTLPHTTPListen,
			},
			wantErr: "--scrape-interval",
		},
		"zero scrape interval": {
			opts: otelCollectorRenderOptions{
				scrapeInterval: "0s",
				otlpGRPCListen: defaultCollectorOTLPGRPCListen,
				otlpHTTPListen: defaultCollectorOTLPHTTPListen,
			},
			wantErr: "greater than zero",
		},
		"bad receiver address": {
			opts: otelCollectorRenderOptions{
				scrapeInterval: defaultCollectorScrapeInterval,
				otlpGRPCListen: "127.0.0.1",
				otlpHTTPListen: defaultCollectorOTLPHTTPListen,
			},
			wantErr: "--otlp-grpc-listen",
		},
		"bad receiver port": {
			opts: otelCollectorRenderOptions{
				scrapeInterval: defaultCollectorScrapeInterval,
				otlpGRPCListen: "127.0.0.1:dreich",
				otlpHTTPListen: defaultCollectorOTLPHTTPListen,
			},
			wantErr: "port must be numeric",
		},
		"same grpc and http receiver address": {
			opts: otelCollectorRenderOptions{
				scrapeInterval: defaultCollectorScrapeInterval,
				otlpGRPCListen: "127.0.0.1:4317",
				otlpHTTPListen: "127.0.0.1:4317",
			},
			wantErr: "must be different",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.opts.validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validate error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func otelCollectorGoldenPaths() config.Paths {
	dataDir := "/home/braw/.local/share/graith"

	return config.Paths{
		DataDir:         dataDir,
		LogDir:          filepath.Join(dataDir, "logs"),
		DaemonLog:       filepath.Join(dataDir, "daemon.log"),
		DaemonStderrLog: filepath.Join(dataDir, "daemon.stderr.log"),
	}
}

func assertValidYAML(t *testing.T, text string) {
	t.Helper()

	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("rendered config is not valid YAML: %v\n%s", err, text)
	}

	if _, ok := decoded["service"]; !ok {
		t.Fatalf("rendered config has no service section:\n%s", text)
	}
}
