package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	TelemetryMetricsBindAddressDefault = "127.0.0.1:4824"
	TelemetryMetricsPathDefault        = "/metrics"

	TelemetryTracingProtocolGRPC         = "grpc"
	TelemetryTracingProtocolHTTPProtobuf = "http/protobuf"
	TelemetryTracingTimeoutDefault       = 10 * time.Second
)

// TelemetryConfig is the [telemetry] block. Metrics and tracing are independent
// opt-in runtimes; neither starts a listener, exporter, or network endpoint
// unless its own enabled flag is true.
type TelemetryConfig struct {
	Metrics TelemetryMetricsConfig `toml:"metrics"`
	Tracing TelemetryTracingConfig `toml:"tracing"`
}

func (t TelemetryConfig) Enabled() bool {
	return t.Metrics.Enabled || t.Tracing.Enabled
}

func (t TelemetryConfig) Validate() error {
	return errors.Join(t.Metrics.Validate(), t.Tracing.Validate())
}

// TelemetryMetricsConfig controls the daemon's local Prometheus scrape endpoint.
type TelemetryMetricsConfig struct {
	Enabled     bool   `toml:"enabled"`
	BindAddress string `toml:"bind_address"`
	Path        string `toml:"path"`
}

func (m TelemetryMetricsConfig) BindAddressOrDefault() string {
	if strings.TrimSpace(m.BindAddress) == "" {
		return TelemetryMetricsBindAddressDefault
	}

	return m.BindAddress
}

func (m TelemetryMetricsConfig) PathOrDefault() string {
	if strings.TrimSpace(m.Path) == "" {
		return TelemetryMetricsPathDefault
	}

	return m.Path
}

func (m TelemetryMetricsConfig) Validate() error {
	var errs []error

	bindAddress := TelemetryMetricsBindAddressDefault
	if m.BindAddress != "" {
		bindAddress = m.BindAddress
	}

	if err := validateTCPBindAddress("telemetry.metrics.bind_address", bindAddress, false); err != nil {
		errs = append(errs, err)
	}

	if err := validateMetricsPath("telemetry.metrics.path", m.Path); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// TelemetryTracingConfig controls optional trace export. The exporter is wired
// by daemon runtime code; instrumentation remains dormant unless enabled is true.
type TelemetryTracingConfig struct {
	Enabled  bool              `toml:"enabled"`
	Endpoint string            `toml:"endpoint"`
	Protocol string            `toml:"protocol"`
	Insecure bool              `toml:"insecure"`
	Timeout  string            `toml:"timeout"`
	Headers  map[string]string `toml:"headers"`
}

func (t TelemetryTracingConfig) ProtocolOrDefault() string {
	if strings.TrimSpace(t.Protocol) == "" {
		return TelemetryTracingProtocolGRPC
	}

	return t.Protocol
}

func (t TelemetryTracingConfig) TimeoutDuration() time.Duration {
	return positiveDurationOrDefault(t.Timeout, TelemetryTracingTimeoutDefault)
}

func (t TelemetryTracingConfig) Validate() error {
	var errs []error

	protocol := t.ProtocolOrDefault()
	if t.Protocol != "" && t.Protocol != strings.TrimSpace(t.Protocol) {
		errs = append(errs, fmt.Errorf("telemetry.tracing.protocol %q: must not have leading or trailing whitespace", t.Protocol))
	}

	switch protocol {
	case TelemetryTracingProtocolGRPC, TelemetryTracingProtocolHTTPProtobuf:
	default:
		errs = append(errs, fmt.Errorf("telemetry.tracing.protocol %q: must be one of %q, %q",
			t.Protocol, TelemetryTracingProtocolGRPC, TelemetryTracingProtocolHTTPProtobuf))
	}

	endpoint := strings.TrimSpace(t.Endpoint)
	if t.Enabled && endpoint == "" {
		errs = append(errs, errors.New("telemetry.tracing.endpoint is required when telemetry.tracing.enabled is true"))
	}

	if t.Endpoint != "" && t.Endpoint != endpoint {
		errs = append(errs, fmt.Errorf("telemetry.tracing.endpoint %q: must not have leading or trailing whitespace", t.Endpoint))
	}

	if endpoint != "" {
		switch protocol {
		case TelemetryTracingProtocolGRPC:
			if err := validateOTLPGRPCEndpoint(endpoint); err != nil {
				errs = append(errs, err)
			}
		case TelemetryTracingProtocolHTTPProtobuf:
			if err := validateOTLPHTTPProtobufEndpoint(endpoint); err != nil {
				errs = append(errs, err)
			}

			if t.Insecure {
				errs = append(errs, errors.New("telemetry.tracing.insecure is only supported with telemetry.tracing.protocol = \"grpc\"; use an http:// endpoint for http/protobuf without TLS"))
			}
		}
	}

	if s := strings.TrimSpace(t.Timeout); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("telemetry.tracing.timeout %q: %w", t.Timeout, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("telemetry.tracing.timeout %q: must be greater than zero", t.Timeout))
		}
	}

	for name, value := range t.Headers {
		if !validHTTPHeaderName(name) {
			errs = append(errs, fmt.Errorf("telemetry.tracing.headers[%q]: header name must be a valid HTTP token", name))
		}

		if containsControl(value) || strings.ContainsAny(value, "\r\n") {
			errs = append(errs, fmt.Errorf("telemetry.tracing.headers[%q]: header value must not contain control characters", name))
		}
	}

	return errors.Join(errs...)
}

func validateTCPBindAddress(field, raw string, allowPortZero bool) error {
	if raw == "" || strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%s: must be a TCP address like %q", field, TelemetryMetricsBindAddressDefault)
	}

	if raw != strings.TrimSpace(raw) || containsControl(raw) {
		return fmt.Errorf("%s %q: must not contain whitespace or control characters", field, raw)
	}

	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return fmt.Errorf("%s %q: must be in host:port form", field, raw)
	}

	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("%s %q: host must not contain whitespace", field, raw)
	}

	return validateTCPPort(field, raw, port, allowPortZero)
}

func validateTCPPort(field, raw, port string, allowZero bool) error {
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("%s %q: port must be numeric", field, raw)
	}

	if n == 0 && allowZero {
		return nil
	}

	if n < 1 || n > 65535 {
		return fmt.Errorf("%s %q: port must be in range 1-65535", field, raw)
	}

	return nil
}

func validateMetricsPath(field, path string) error {
	if path == "" {
		return nil
	}

	if path != strings.TrimSpace(path) || containsControl(path) {
		return fmt.Errorf("%s %q: must not contain whitespace or control characters", field, path)
	}

	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%s %q: must start with /", field, path)
	}

	if strings.ContainsAny(path, "?#") {
		return fmt.Errorf("%s %q: must not contain query strings or fragments", field, path)
	}

	return nil
}

func validateOTLPGRPCEndpoint(endpoint string) error {
	if endpoint != strings.TrimSpace(endpoint) || containsControl(endpoint) {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace or control characters", endpoint)
	}

	if strings.ContainsAny(endpoint, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace", endpoint)
	}

	if strings.Contains(endpoint, "://") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: grpc endpoints must be host:port without a URL scheme", endpoint)
	}

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("telemetry.tracing.endpoint %q: grpc endpoints must be in host:port form", endpoint)
	}

	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host is required", endpoint)
	}

	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host must not contain whitespace", endpoint)
	}

	return validateTCPPort("telemetry.tracing.endpoint", endpoint, port, false)
}

func validateOTLPHTTPProtobufEndpoint(endpoint string) error {
	if endpoint != strings.TrimSpace(endpoint) || containsControl(endpoint) {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace or control characters", endpoint)
	}

	if strings.ContainsAny(endpoint, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace", endpoint)
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port") {
			return fmt.Errorf("telemetry.tracing.endpoint %q: port must be numeric", endpoint)
		}

		return fmt.Errorf("telemetry.tracing.endpoint %q: invalid URL: %w", endpoint, err)
	}

	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("telemetry.tracing.endpoint %q: http/protobuf endpoints must use http or https", endpoint)
	}

	if u.Host == "" || strings.TrimSpace(u.Hostname()) == "" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host is required", endpoint)
	}

	if strings.ContainsAny(u.Host, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host must not contain whitespace", endpoint)
	}

	if u.User != nil {
		return fmt.Errorf("telemetry.tracing.endpoint %q: credentials belong in telemetry.tracing.headers, not the URL", endpoint)
	}

	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: query strings and fragments are not supported", endpoint)
	}

	if u.Path == "" || u.Path == "/" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: http/protobuf endpoints must include an OTLP traces path such as /v1/traces", endpoint)
	}

	if port := u.Port(); port != "" {
		if err := validateTCPPort("telemetry.tracing.endpoint", endpoint, port, false); err != nil {
			return err
		}
	} else if strings.Contains(u.Host, ":") && strings.LastIndex(u.Host, ":") > strings.LastIndex(u.Host, "]") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: port must be numeric", endpoint)
	}

	return nil
}

func validHTTPHeaderName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}

	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}

	return true
}

func containsControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}

	return false
}
