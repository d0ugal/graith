package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelemetryDefaultsDisabled(t *testing.T) {
	cfg := Default()

	if cfg.Telemetry.Enabled() {
		t.Fatal("default telemetry must be disabled")
	}

	if cfg.Telemetry.Metrics.Enabled {
		t.Error("default metrics must be disabled")
	}

	if cfg.Telemetry.Metrics.BindAddress != TelemetryMetricsBindAddressDefault {
		t.Errorf("metrics bind address = %q, want %q", cfg.Telemetry.Metrics.BindAddress, TelemetryMetricsBindAddressDefault)
	}

	if cfg.Telemetry.Metrics.Path != TelemetryMetricsPathDefault {
		t.Errorf("metrics path = %q, want %q", cfg.Telemetry.Metrics.Path, TelemetryMetricsPathDefault)
	}

	if cfg.Telemetry.Tracing.Enabled {
		t.Error("default tracing must be disabled")
	}

	if cfg.Telemetry.Tracing.Endpoint != "" {
		t.Errorf("tracing endpoint = %q, want empty", cfg.Telemetry.Tracing.Endpoint)
	}

	if cfg.Telemetry.Tracing.Protocol != TelemetryTracingProtocolGRPC {
		t.Errorf("tracing protocol = %q, want %q", cfg.Telemetry.Tracing.Protocol, TelemetryTracingProtocolGRPC)
	}

	if got := cfg.Telemetry.Tracing.TimeoutDuration(); got != TelemetryTracingTimeoutDefault {
		t.Errorf("tracing timeout = %v, want %v", got, TelemetryTracingTimeoutDefault)
	}

	if cfg.Telemetry.Logs.Enabled {
		t.Error("default logs must be disabled")
	}

	if cfg.Telemetry.Logs.Endpoint != "" {
		t.Errorf("logs endpoint = %q, want empty", cfg.Telemetry.Logs.Endpoint)
	}

	if cfg.Telemetry.Logs.Protocol != TelemetryLogsProtocolGRPC {
		t.Errorf("logs protocol = %q, want %q", cfg.Telemetry.Logs.Protocol, TelemetryLogsProtocolGRPC)
	}

	if got := cfg.Telemetry.Logs.TimeoutDuration(); got != TelemetryLogsTimeoutDefault {
		t.Errorf("logs timeout = %v, want %v", got, TelemetryLogsTimeoutDefault)
	}

	if got := cfg.Telemetry.Logs.ExportIntervalDuration(); got != TelemetryLogsExportIntervalDefault {
		t.Errorf("logs export interval = %v, want %v", got, TelemetryLogsExportIntervalDefault)
	}

	if got := cfg.Telemetry.Logs.QueueSizeOrDefault(); got != TelemetryLogsQueueSizeDefault {
		t.Errorf("logs queue size = %d, want %d", got, TelemetryLogsQueueSizeDefault)
	}

	if got := cfg.Telemetry.Logs.BatchSizeOrDefault(); got != TelemetryLogsBatchSizeDefault {
		t.Errorf("logs batch size = %d, want %d", got, TelemetryLogsBatchSizeDefault)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config validation failed: %v", err)
	}
}

func TestTelemetryConfigValidateAcceptsExplicitValidValues(t *testing.T) {
	tests := map[string]TelemetryConfig{
		"metrics": {
			Metrics: TelemetryMetricsConfig{
				Enabled:     true,
				BindAddress: "127.0.0.1:9924",
				Path:        "/braw/metrics",
			},
		},
		"grpc tracing": {
			Tracing: TelemetryTracingConfig{
				Enabled:  true,
				Endpoint: "127.0.0.1:4317",
				Protocol: TelemetryTracingProtocolGRPC,
				Timeout:  "5s",
				Headers: map[string]string{
					"authorization": "Bearer canny",
				},
				HeadersEnv: map[string]string{
					"x-braw-env": "OTLP_BRAW_HEADER",
				},
				HeadersFile: map[string]string{
					"x-braw-file": "~/Library/Application Support/Graith/braw-otlp-header",
				},
			},
		},
		"http tracing": {
			Tracing: TelemetryTracingConfig{
				Enabled:  true,
				Endpoint: "http://127.0.0.1:4318/v1/traces",
				Protocol: TelemetryTracingProtocolHTTPProtobuf,
				Timeout:  "250ms",
			},
		},
		"grpc logs": {
			Logs: TelemetryLogsConfig{
				Enabled:        true,
				Endpoint:       "127.0.0.1:4317",
				Protocol:       TelemetryLogsProtocolGRPC,
				Timeout:        "5s",
				ExportInterval: "250ms",
				QueueSize:      64,
				BatchSize:      16,
				Headers: map[string]string{
					"authorization": "Bearer canny",
				},
			},
		},
		"http logs": {
			Logs: TelemetryLogsConfig{
				Enabled:  true,
				Endpoint: "http://127.0.0.1:4318/v1/logs",
				Protocol: TelemetryLogsProtocolHTTPProtobuf,
				Timeout:  "250ms",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.Telemetry = tc

			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() rejected valid telemetry config: %v", err)
			}
		})
	}
}

func TestTelemetryConfigValidateRejectsInvalidValues(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*TelemetryConfig)
		wantErr string
	}{
		"metrics bind missing port": {
			mutate:  func(t *TelemetryConfig) { t.Metrics.BindAddress = "127.0.0.1" },
			wantErr: "telemetry.metrics.bind_address",
		},
		"metrics bind whitespace": {
			mutate:  func(t *TelemetryConfig) { t.Metrics.BindAddress = "  " },
			wantErr: "telemetry.metrics.bind_address",
		},
		"metrics bind port zero": {
			mutate:  func(t *TelemetryConfig) { t.Metrics.BindAddress = "127.0.0.1:0" },
			wantErr: "port must be in range",
		},
		"metrics path relative": {
			mutate:  func(t *TelemetryConfig) { t.Metrics.Path = "metrics" },
			wantErr: "telemetry.metrics.path",
		},
		"metrics path query": {
			mutate:  func(t *TelemetryConfig) { t.Metrics.Path = "/metrics?format=openmetrics" },
			wantErr: "query",
		},
		"tracing enabled requires endpoint": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Enabled = true },
			wantErr: "telemetry.tracing.endpoint is required",
		},
		"tracing unknown protocol": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Protocol = "jaeger" },
			wantErr: "telemetry.tracing.protocol",
		},
		"tracing protocol whitespace": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Protocol = " grpc" },
			wantErr: "telemetry.tracing.protocol",
		},
		"grpc endpoint has scheme": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Endpoint = "http://127.0.0.1:4317" },
			wantErr: "grpc endpoints must be host:port",
		},
		"grpc endpoint has control character": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Endpoint = "dreich\x7f:4317" },
			wantErr: "control",
		},
		"http endpoint has userinfo": {
			mutate: func(t *TelemetryConfig) {
				t.Tracing.Protocol = TelemetryTracingProtocolHTTPProtobuf
				t.Tracing.Endpoint = "https://token@example.com/v1/traces"
			},
			wantErr: "credentials belong",
		},
		"http endpoint has whitespace": {
			mutate: func(t *TelemetryConfig) {
				t.Tracing.Protocol = TelemetryTracingProtocolHTTPProtobuf
				t.Tracing.Endpoint = "https://dreich host.example/v1/traces"
			},
			wantErr: "whitespace",
		},
		"http endpoint has non-numeric port": {
			mutate: func(t *TelemetryConfig) {
				t.Tracing.Protocol = TelemetryTracingProtocolHTTPProtobuf
				t.Tracing.Endpoint = "https://example.com:dreich/v1/traces"
			},
			wantErr: "port must be numeric",
		},
		"http endpoint missing traces path": {
			mutate: func(t *TelemetryConfig) {
				t.Tracing.Protocol = TelemetryTracingProtocolHTTPProtobuf
				t.Tracing.Endpoint = "http://127.0.0.1:4318"
			},
			wantErr: "must include an OTLP traces path",
		},
		"http endpoint with insecure flag": {
			mutate: func(t *TelemetryConfig) {
				t.Tracing.Protocol = TelemetryTracingProtocolHTTPProtobuf
				t.Tracing.Endpoint = "http://127.0.0.1:4318/v1/traces"
				t.Tracing.Insecure = true
			},
			wantErr: "only supported with telemetry.tracing.protocol",
		},
		"tracing timeout zero": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Timeout = "0" },
			wantErr: "telemetry.tracing.timeout",
		},
		"tracing header bad name": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Headers = map[string]string{"bad header": "value"} },
			wantErr: "telemetry.tracing.headers",
		},
		"tracing header control value": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.Headers = map[string]string{"authorization": "Bearer\nbraw"} },
			wantErr: "telemetry.tracing.headers",
		},
		"tracing env header bad name": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.HeadersEnv = map[string]string{"authorization": "1BAD"} },
			wantErr: "telemetry.tracing.headers_env",
		},
		"tracing file header empty path": {
			mutate:  func(t *TelemetryConfig) { t.Tracing.HeadersFile = map[string]string{"authorization": ""} },
			wantErr: "telemetry.tracing.headers_file",
		},
		"tracing duplicate header source": {
			mutate: func(t *TelemetryConfig) {
				t.Tracing.Headers = map[string]string{"Authorization": "Bearer braw"}
				t.Tracing.HeadersEnv = map[string]string{"authorization": "OTLP_BRAW_HEADER"}
			},
			wantErr: "header already configured",
		},
		"logs enabled requires endpoint": {
			mutate:  func(t *TelemetryConfig) { t.Logs.Enabled = true },
			wantErr: "telemetry.logs.endpoint is required",
		},
		"logs unknown protocol": {
			mutate:  func(t *TelemetryConfig) { t.Logs.Protocol = "loki" },
			wantErr: "telemetry.logs.protocol",
		},
		"logs grpc endpoint has scheme": {
			mutate:  func(t *TelemetryConfig) { t.Logs.Endpoint = "http://127.0.0.1:4317" },
			wantErr: "grpc endpoints must be host:port",
		},
		"logs http endpoint missing path": {
			mutate: func(t *TelemetryConfig) {
				t.Logs.Protocol = TelemetryLogsProtocolHTTPProtobuf
				t.Logs.Endpoint = "http://127.0.0.1:4318"
			},
			wantErr: "must include an OTLP logs path",
		},
		"logs http endpoint uses traces path": {
			mutate: func(t *TelemetryConfig) {
				t.Logs.Protocol = TelemetryLogsProtocolHTTPProtobuf
				t.Logs.Endpoint = "http://127.0.0.1:4318/v1/traces"
			},
			wantErr: "must use an OTLP logs path",
		},
		"logs http endpoint with insecure flag": {
			mutate: func(t *TelemetryConfig) {
				t.Logs.Protocol = TelemetryLogsProtocolHTTPProtobuf
				t.Logs.Endpoint = "http://127.0.0.1:4318/v1/logs"
				t.Logs.Insecure = true
			},
			wantErr: "only supported with telemetry.logs.protocol",
		},
		"logs timeout zero": {
			mutate:  func(t *TelemetryConfig) { t.Logs.Timeout = "0" },
			wantErr: "telemetry.logs.timeout",
		},
		"logs export interval zero": {
			mutate:  func(t *TelemetryConfig) { t.Logs.ExportInterval = "0" },
			wantErr: "telemetry.logs.export_interval",
		},
		"logs queue negative": {
			mutate:  func(t *TelemetryConfig) { t.Logs.QueueSize = -1 },
			wantErr: "telemetry.logs.queue_size",
		},
		"logs batch larger than queue": {
			mutate:  func(t *TelemetryConfig) { t.Logs.QueueSize = 2; t.Logs.BatchSize = 3 },
			wantErr: "telemetry.logs.batch_size",
		},
		"logs header bad name": {
			mutate:  func(t *TelemetryConfig) { t.Logs.Headers = map[string]string{"bad header": "value"} },
			wantErr: "telemetry.logs.headers",
		},
		"logs header control value": {
			mutate:  func(t *TelemetryConfig) { t.Logs.Headers = map[string]string{"authorization": "Bearer\nbraw"} },
			wantErr: "telemetry.logs.headers",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg.Telemetry)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want telemetry validation error")
			}

			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestTelemetryTracingResolvedHeaders(t *testing.T) {
	sourceDir := t.TempDir()

	headerDir := filepath.Join(sourceDir, "headers")
	if err := os.Mkdir(headerDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(headerDir, "authorization"), []byte("\ufeffBearer thrawn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OTLP_BRAW_HEADER", "braw-env-value")

	cfg := TelemetryTracingConfig{
		Headers: map[string]string{
			"x-inline": "inline-canny",
		},
		HeadersEnv: map[string]string{
			"x-env": "OTLP_BRAW_HEADER",
		},
		HeadersFile: map[string]string{
			"authorization": "headers/authorization",
		},
	}

	got, err := cfg.ResolvedHeaders(sourceDir)
	if err != nil {
		t.Fatalf("ResolvedHeaders() error = %v", err)
	}

	want := map[string]string{
		"x-inline":      "inline-canny",
		"x-env":         "braw-env-value",
		"authorization": "Bearer thrawn",
	}

	if len(got) != len(want) {
		t.Fatalf("ResolvedHeaders() returned %d headers, want %d: %#v", len(got), len(want), got)
	}

	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("ResolvedHeaders()[%q] = %q, want %q", name, got[name], wantValue)
		}
	}
}

func TestTelemetryTracingResolvedHeadersAcceptsAbsoluteAndHomeTokenFiles(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	homeHeaderPath := filepath.Join(homeDir, "headers", "authorization")
	if err := os.MkdirAll(filepath.Dir(homeHeaderPath), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(homeHeaderPath, []byte("Bearer braw\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	absoluteHeaderPath := filepath.Join(t.TempDir(), "otlp-header")
	if err := os.WriteFile(absoluteHeaderPath, []byte("Bearer canny\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := TelemetryTracingConfig{
		HeadersFile: map[string]string{
			"x-home":     "~/headers/authorization",
			"x-absolute": absoluteHeaderPath,
		},
	}

	got, err := cfg.ResolvedHeaders(t.TempDir())
	if err != nil {
		t.Fatalf("ResolvedHeaders() error = %v", err)
	}

	want := map[string]string{
		"x-home":     "Bearer braw",
		"x-absolute": "Bearer canny",
	}

	if len(got) != len(want) {
		t.Fatalf("ResolvedHeaders() returned %d headers, want %d: %#v", len(got), len(want), got)
	}

	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("ResolvedHeaders()[%q] = %q, want %q", name, got[name], wantValue)
		}
	}
}

func TestTelemetryTracingResolvedHeadersRejectsMissingSources(t *testing.T) {
	const envName = "OTLP_MISSING_BRAW_HEADER"

	unsetEnvForTest(t, envName)

	sourceDir := t.TempDir()
	cfg := TelemetryTracingConfig{
		HeadersEnv: map[string]string{
			"authorization": envName,
		},
		HeadersFile: map[string]string{
			"x-file": "missing-header",
		},
	}

	_, err := cfg.ResolvedHeaders(sourceDir)
	if err == nil {
		t.Fatal("ResolvedHeaders() error = nil, want missing source errors")
	}

	for _, want := range []string{
		`telemetry.tracing.headers_env["authorization"]`,
		envName,
		`telemetry.tracing.headers_file["x-file"]`,
		filepath.Join(sourceDir, "missing-header"),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ResolvedHeaders() error = %v, want substring %q", err, want)
		}
	}
}

func TestTelemetryTracingResolvedHeadersRejectsInvalidSourceValues(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "empty-header"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OTLP_DREICH_HEADER", "Bearer\ndreich")

	cfg := TelemetryTracingConfig{
		HeadersEnv: map[string]string{
			"authorization": "OTLP_DREICH_HEADER",
		},
		HeadersFile: map[string]string{
			"x-file": "empty-header",
		},
	}

	_, err := cfg.ResolvedHeaders(sourceDir)
	if err == nil {
		t.Fatal("ResolvedHeaders() error = nil, want invalid source value errors")
	}

	for _, want := range []string{
		"header value must not contain control characters",
		"empty after trimming whitespace",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ResolvedHeaders() error = %v, want substring %q", err, want)
		}
	}
}

func TestTelemetryTracingResolvedHeadersRejectsUnsafeTokenFiles(t *testing.T) {
	tests := map[string]struct {
		sourceDir string
		path      string
		setup     func(*testing.T, string)
		wantErr   string
	}{
		"relative path without source directory": {
			path:    "headers/authorization",
			wantErr: "relative token file path",
		},
		"group readable file": {
			sourceDir: t.TempDir(),
			path:      "authorization",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := os.WriteFile(path, []byte("Bearer braw"), 0o600); err != nil {
					t.Fatal(err)
				}

				if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // deliberately over-permissive fixture.
					t.Fatal(err)
				}
			},
			wantErr: "insecure mode",
		},
		"directory": {
			sourceDir: t.TempDir(),
			path:      "authorization",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "not a regular file",
		},
		"oversized file": {
			sourceDir: t.TempDir(),
			path:      "authorization",
			setup: func(t *testing.T, path string) {
				t.Helper()

				data := strings.Repeat("a", telemetryTracingHeaderFileMaxBytes+1)
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "exceeds",
		},
		"symlink": {
			sourceDir: t.TempDir(),
			path:      "authorization",
			setup: func(t *testing.T, path string) {
				t.Helper()

				realPath := filepath.Join(filepath.Dir(path), "real-authorization")
				if err := os.WriteFile(realPath, []byte("Bearer canny"), 0o600); err != nil {
					t.Fatal(err)
				}

				if err := os.Symlink(realPath, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			wantErr: "open token file",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := test.path
			if test.sourceDir != "" {
				path = filepath.Join(test.sourceDir, test.path)
			}

			if test.setup != nil {
				test.setup(t, path)
			}

			cfg := TelemetryTracingConfig{
				HeadersFile: map[string]string{
					"authorization": test.path,
				},
			}

			_, err := cfg.ResolvedHeaders(test.sourceDir)
			if err == nil {
				t.Fatal("ResolvedHeaders() error = nil, want unsafe token file error")
			}

			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ResolvedHeaders() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestTelemetryTracingTimeoutDuration(t *testing.T) {
	tests := map[string]struct {
		in   string
		want time.Duration
	}{
		"empty":       {"", TelemetryTracingTimeoutDefault},
		"unparseable": {"dreich", TelemetryTracingTimeoutDefault},
		"zero":        {"0", TelemetryTracingTimeoutDefault},
		"explicit":    {"2s", 2 * time.Second},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := (TelemetryTracingConfig{Timeout: test.in}).TimeoutDuration()
			if got != test.want {
				t.Errorf("TimeoutDuration() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestTelemetryLogsRuntimeDefaults(t *testing.T) {
	tests := map[string]struct {
		cfg          TelemetryLogsConfig
		wantQueue    int
		wantBatch    int
		wantInterval time.Duration
		wantTimeout  time.Duration
	}{
		"empty": {
			wantQueue:    TelemetryLogsQueueSizeDefault,
			wantBatch:    TelemetryLogsBatchSizeDefault,
			wantInterval: TelemetryLogsExportIntervalDefault,
			wantTimeout:  TelemetryLogsTimeoutDefault,
		},
		"explicit": {
			cfg: TelemetryLogsConfig{
				QueueSize:      12,
				BatchSize:      4,
				ExportInterval: "250ms",
				Timeout:        "2s",
			},
			wantQueue:    12,
			wantBatch:    4,
			wantInterval: 250 * time.Millisecond,
			wantTimeout:  2 * time.Second,
		},
		"batch clamps to queue": {
			cfg: TelemetryLogsConfig{
				QueueSize: 2,
				BatchSize: 4,
			},
			wantQueue:    2,
			wantBatch:    2,
			wantInterval: TelemetryLogsExportIntervalDefault,
			wantTimeout:  TelemetryLogsTimeoutDefault,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := test.cfg.QueueSizeOrDefault(); got != test.wantQueue {
				t.Errorf("QueueSizeOrDefault() = %d, want %d", got, test.wantQueue)
			}

			if got := test.cfg.BatchSizeOrDefault(); got != test.wantBatch {
				t.Errorf("BatchSizeOrDefault() = %d, want %d", got, test.wantBatch)
			}

			if got := test.cfg.ExportIntervalDuration(); got != test.wantInterval {
				t.Errorf("ExportIntervalDuration() = %v, want %v", got, test.wantInterval)
			}

			if got := test.cfg.TimeoutDuration(); got != test.wantTimeout {
				t.Errorf("TimeoutDuration() = %v, want %v", got, test.wantTimeout)
			}
		})
	}
}

func TestRedactSecretsMasksTelemetryTracingHeaders(t *testing.T) {
	cfg := Default()
	cfg.Telemetry.Tracing.Headers = map[string]string{
		"Authorization": "Bearer thrawn",
	}
	cfg.Telemetry.Tracing.HeadersEnv = map[string]string{
		"x-env": "OTLP_BRAW_HEADER",
	}
	cfg.Telemetry.Tracing.HeadersFile = map[string]string{
		"x-file": "/Users/braw/.config/graith/otlp-header",
	}
	cfg.Telemetry.Logs.Headers = map[string]string{
		"x-api-key": "braw",
	}

	redacted := RedactSecrets(cfg)
	if got := redacted.Telemetry.Tracing.Headers["Authorization"]; got != RedactedMask {
		t.Fatalf("redacted tracing header = %q, want %q", got, RedactedMask)
	}

	if got := redacted.Telemetry.Tracing.HeadersEnv["x-env"]; got != RedactedMask {
		t.Fatalf("redacted tracing env header source = %q, want %q", got, RedactedMask)
	}

	if got := redacted.Telemetry.Tracing.HeadersFile["x-file"]; got != RedactedMask {
		t.Fatalf("redacted tracing file header source = %q, want %q", got, RedactedMask)
	}

	data, err := EffectiveTOML(redacted)
	if err != nil {
		t.Fatal(err)
	}

	for _, forbidden := range []string{
		"Bearer thrawn",
		"OTLP_BRAW_HEADER",
		"/Users/braw/.config/graith/otlp-header",
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("redacted TOML leaked %q:\n%s", forbidden, data)
		}
	}

	if got := redacted.Telemetry.Logs.Headers["x-api-key"]; got != RedactedMask {
		t.Fatalf("redacted logs header = %q, want %q", got, RedactedMask)
	}

	if got := cfg.Telemetry.Tracing.Headers["Authorization"]; got != "Bearer thrawn" {
		t.Fatalf("live config header mutated to %q", got)
	}

	if got := cfg.Telemetry.Tracing.HeadersEnv["x-env"]; got != "OTLP_BRAW_HEADER" {
		t.Fatalf("live config env header source mutated to %q", got)
	}

	if got := cfg.Telemetry.Tracing.HeadersFile["x-file"]; got != "/Users/braw/.config/graith/otlp-header" {
		t.Fatalf("live config file header source mutated to %q", got)
	}

	if got := cfg.Telemetry.Logs.Headers["x-api-key"]; got != "braw" {
		t.Fatalf("live config log header mutated to %q", got)
	}
}

func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()

	// t.Setenv cannot unset a variable, and these tests must verify the missing
	// source path without depending on the developer's shell environment.
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
