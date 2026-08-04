package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	TelemetryMetricsBindAddressDefault = "127.0.0.1:4824"
	TelemetryMetricsPathDefault        = "/metrics"

	TelemetryTracingProtocolGRPC         = "grpc"
	TelemetryTracingProtocolHTTPProtobuf = "http/protobuf"
	TelemetryTracingTimeoutDefault       = 10 * time.Second

	telemetryTracingHeaderFileMaxBytes = 16 * 1024
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
	Enabled     bool              `toml:"enabled"`
	Endpoint    string            `toml:"endpoint"`
	Protocol    string            `toml:"protocol"`
	Insecure    bool              `toml:"insecure"`
	Timeout     string            `toml:"timeout"`
	Headers     map[string]string `toml:"headers"`
	HeadersEnv  map[string]string `toml:"headers_env"`
	HeadersFile map[string]string `toml:"headers_file"`
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

	errs = append(errs, validateTelemetryTracingHeaderMap("telemetry.tracing.headers", t.Headers)...)

	for _, name := range sortedHeaderKeys(t.HeadersEnv) {
		envName := t.HeadersEnv[name]
		if !validHTTPHeaderName(name) {
			errs = append(errs, fmt.Errorf("telemetry.tracing.headers_env[%q]: header name must be a valid HTTP token", name))
		}

		if !validEnvironmentName(envName) {
			errs = append(errs, fmt.Errorf("telemetry.tracing.headers_env[%q] %q: must be a valid environment variable name", name, envName))
		}
	}

	for _, name := range sortedHeaderKeys(t.HeadersFile) {
		path := t.HeadersFile[name]
		if !validHTTPHeaderName(name) {
			errs = append(errs, fmt.Errorf("telemetry.tracing.headers_file[%q]: header name must be a valid HTTP token", name))
		}

		if path == "" || path != strings.TrimSpace(path) || containsControl(path) {
			errs = append(errs, fmt.Errorf("telemetry.tracing.headers_file[%q]: file path must not be empty, have leading or trailing whitespace, or contain control characters", name))
		}
	}

	errs = append(errs, t.validateHeaderSourceConflicts()...)

	return errors.Join(errs...)
}

// ResolvedHeaders returns the OTLP headers with inline values plus any
// configured external credential sources. Source values are read only when the
// tracing runtime starts; config rendering and diffing should not call this.
func (t TelemetryTracingConfig) ResolvedHeaders(sourceDir string) (map[string]string, error) {
	return t.resolveHeaders(sourceDir, os.LookupEnv, openTelemetryTracingHeaderFile)
}

func (t TelemetryTracingConfig) resolveHeaders(
	sourceDir string,
	lookupEnv func(string) (string, bool),
	openFile func(string) (*os.File, error),
) (map[string]string, error) {
	total := len(t.Headers) + len(t.HeadersEnv) + len(t.HeadersFile)
	if total == 0 {
		return nil, nil
	}

	headers := make(map[string]string, total)
	for _, name := range sortedHeaderKeys(t.Headers) {
		headers[name] = t.Headers[name]
	}

	var errs []error

	for _, name := range sortedHeaderKeys(t.HeadersEnv) {
		envName := t.HeadersEnv[name]

		value, err := resolveTelemetryTracingEnvHeader(name, envName, lookupEnv)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		headers[name] = value
	}

	for _, name := range sortedHeaderKeys(t.HeadersFile) {
		path, err := resolveTelemetryTracingHeaderFilePath(t.HeadersFile[name], sourceDir)
		if err != nil {
			errs = append(errs, fmt.Errorf("telemetry.tracing.headers_file[%q]: %w", name, err))
			continue
		}

		value, err := resolveTelemetryTracingFileHeader(name, path, openFile)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		headers[name] = value
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return headers, nil
}

func resolveTelemetryTracingEnvHeader(
	name string,
	envName string,
	lookupEnv func(string) (string, bool),
) (string, error) {
	value, ok := lookupEnv(envName)
	if !ok {
		return "", fmt.Errorf("telemetry.tracing.headers_env[%q]: environment variable %q is not set; set it before starting tracing or remove this header source", name, envName)
	}

	if value == "" {
		return "", fmt.Errorf("telemetry.tracing.headers_env[%q]: environment variable %q is empty; set a header value before starting tracing or remove this header source", name, envName)
	}

	if err := validateTelemetryTracingHeaderValue("telemetry.tracing.headers_env", name, value); err != nil {
		return "", fmt.Errorf("%w (from environment variable %q)", err, envName)
	}

	return value, nil
}

func resolveTelemetryTracingFileHeader(
	name string,
	path string,
	openFile func(string) (*os.File, error),
) (string, error) {
	file, err := openFile(path)
	if err != nil {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: open token file %q: %w; create an owner-only regular file before starting tracing or remove this header source", name, path, err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: stat token file %q: %w; fix the file before starting tracing or remove this header source", name, path, err)
	}

	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: token file %q is not a regular file; use an owner-only regular file or remove this header source", name, path)
	}

	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: token file %q has insecure mode %04o; remove group/other permissions or remove this header source", name, path, perm)
	}

	if info.Size() > telemetryTracingHeaderFileMaxBytes {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: token file %q exceeds %d byte limit; use a single header value or remove this header source", name, path, telemetryTracingHeaderFileMaxBytes)
	}

	data, err := io.ReadAll(io.LimitReader(file, telemetryTracingHeaderFileMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: read token file %q: %w; fix the file before starting tracing or remove this header source", name, path, err)
	}

	if len(data) > telemetryTracingHeaderFileMaxBytes {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: token file %q exceeds %d byte limit; use a single header value or remove this header source", name, path, telemetryTracingHeaderFileMaxBytes)
	}

	value := strings.TrimSpace(string(data))
	value = strings.TrimPrefix(value, "\ufeff")
	value = strings.TrimSpace(value)

	if value == "" {
		return "", fmt.Errorf("telemetry.tracing.headers_file[%q]: token file %q is empty after trimming whitespace; write a header value before starting tracing or remove this header source", name, path)
	}

	if err := validateTelemetryTracingHeaderValue("telemetry.tracing.headers_file", name, value); err != nil {
		return "", fmt.Errorf("%w (from token file %q)", err, path)
	}

	return value, nil
}

func openTelemetryTracingHeaderFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func resolveTelemetryTracingHeaderFilePath(path, sourceDir string) (string, error) {
	if strings.HasPrefix(path, "~/") || filepath.IsAbs(path) {
		return ExpandPath(path), nil
	}

	if sourceDir == "" {
		return "", fmt.Errorf("relative token file path %q requires a config file directory; use an absolute path, a ~/ path, or load config from disk", path)
	}

	return ExpandPath(filepath.Join(sourceDir, path)), nil
}

func validateTelemetryTracingHeaderMap(field string, headers map[string]string) []error {
	var errs []error

	for _, name := range sortedHeaderKeys(headers) {
		if !validHTTPHeaderName(name) {
			errs = append(errs, fmt.Errorf("%s[%q]: header name must be a valid HTTP token", field, name))
		}

		if err := validateTelemetryTracingHeaderValue(field, name, headers[name]); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

func validateTelemetryTracingHeaderValue(field, name, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s[%q]: header value must be valid UTF-8", field, name)
	}

	if containsControl(value) || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s[%q]: header value must not contain control characters", field, name)
	}

	return nil
}

func (t TelemetryTracingConfig) validateHeaderSourceConflicts() []error {
	var errs []error

	seen := map[string]string{}
	add := func(field string, headers map[string]string) {
		for _, name := range sortedHeaderKeys(headers) {
			key := strings.ToLower(name)
			if previous, ok := seen[key]; ok {
				source := fmt.Sprintf("%s[%q]", field, name)
				errs = append(errs, fmt.Errorf("%s: header already configured by %s; configure each OTLP header in only one telemetry.tracing header source", source, previous))

				continue
			}

			seen[key] = fmt.Sprintf("%s[%q]", field, name)
		}
	}

	add("telemetry.tracing.headers", t.Headers)
	add("telemetry.tracing.headers_env", t.HeadersEnv)
	add("telemetry.tracing.headers_file", t.HeadersFile)

	return errs
}

func sortedHeaderKeys(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
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
	displayEndpoint := redactedTelemetryTracingEndpoint(endpoint)

	if endpoint != strings.TrimSpace(endpoint) || containsControl(endpoint) {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace or control characters", displayEndpoint)
	}

	if strings.ContainsAny(endpoint, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace", displayEndpoint)
	}

	if strings.Contains(endpoint, "://") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: grpc endpoints must be host:port without a URL scheme", displayEndpoint)
	}

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("telemetry.tracing.endpoint %q: grpc endpoints must be in host:port form", displayEndpoint)
	}

	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host is required", displayEndpoint)
	}

	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host must not contain whitespace", displayEndpoint)
	}

	return validateTCPPort("telemetry.tracing.endpoint", displayEndpoint, port, false)
}

func validateOTLPHTTPProtobufEndpoint(endpoint string) error {
	displayEndpoint := redactedTelemetryTracingEndpoint(endpoint)

	if endpoint != strings.TrimSpace(endpoint) || containsControl(endpoint) {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace or control characters", displayEndpoint)
	}

	if strings.ContainsAny(endpoint, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: must not contain whitespace", displayEndpoint)
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port") {
			return fmt.Errorf("telemetry.tracing.endpoint %q: port must be numeric", displayEndpoint)
		}

		return fmt.Errorf("telemetry.tracing.endpoint %q: invalid URL", displayEndpoint)
	}

	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("telemetry.tracing.endpoint %q: http/protobuf endpoints must use http or https", displayEndpoint)
	}

	if u.Host == "" || strings.TrimSpace(u.Hostname()) == "" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host is required", displayEndpoint)
	}

	if strings.ContainsAny(u.Host, " \t\r\n") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: host must not contain whitespace", displayEndpoint)
	}

	if u.User != nil {
		return fmt.Errorf("telemetry.tracing.endpoint %q: credentials belong in telemetry.tracing.headers, not the URL", displayEndpoint)
	}

	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: query strings and fragments are not supported", displayEndpoint)
	}

	if u.Path == "" || u.Path == "/" {
		return fmt.Errorf("telemetry.tracing.endpoint %q: http/protobuf endpoints must include an OTLP traces path such as /v1/traces", displayEndpoint)
	}

	if port := u.Port(); port != "" {
		if err := validateTCPPort("telemetry.tracing.endpoint", displayEndpoint, port, false); err != nil {
			return err
		}
	} else if strings.Contains(u.Host, ":") && strings.LastIndex(u.Host, ":") > strings.LastIndex(u.Host, "]") {
		return fmt.Errorf("telemetry.tracing.endpoint %q: port must be numeric", displayEndpoint)
	}

	return nil
}

func redactedTelemetryTracingEndpoint(endpoint string) string {
	value := strings.TrimSpace(endpoint)

	if u, err := url.Parse(value); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""

		return u.String()
	}

	for _, scheme := range []string{"http://", "https://"} {
		if rest, ok := strings.CutPrefix(value, scheme); ok {
			rest, _, _ = strings.Cut(rest, "?")

			rest, _, _ = strings.Cut(rest, "#")
			if _, after, ok := strings.Cut(rest, "@"); ok {
				rest = after
			}

			return scheme + rest
		}
	}

	return value
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
