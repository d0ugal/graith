package config

import (
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

func TestRedactSecretsMasksTelemetryTracingHeaders(t *testing.T) {
	cfg := Default()
	cfg.Telemetry.Tracing.Headers = map[string]string{
		"Authorization": "Bearer thrawn",
	}

	redacted := RedactSecrets(cfg)
	if got := redacted.Telemetry.Tracing.Headers["Authorization"]; got != RedactedMask {
		t.Fatalf("redacted tracing header = %q, want %q", got, RedactedMask)
	}

	if got := cfg.Telemetry.Tracing.Headers["Authorization"]; got != "Bearer thrawn" {
		t.Fatalf("live config header mutated to %q", got)
	}
}
