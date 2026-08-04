package cli

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/d0ugal/graith/internal/config"
)

const doctorTracingSection = "observability_tracing"

var doctorTracingURLPattern = regexp.MustCompile(`https?://[^\s"']+`)

func (dc *doctorContext) checkTracing() {
	dc.section("Observability / Tracing")

	if cfg == nil {
		dc.failf(doctorTracingSection, "Tracing config unavailable")
		dc.hintf("Run gr doctor after configuration has loaded, or pass --config explicitly")

		return
	}

	tracing := cfg.Telemetry.Tracing
	if tracing.Enabled {
		dc.passf(doctorTracingSection, "Tracing enabled")
	} else {
		dc.warnf(doctorTracingSection, "Tracing disabled; no direct trace export will run")
	}

	if err := tracing.Validate(); err != nil {
		dc.failf(doctorTracingSection, "Tracing config invalid: %s", sanitizeDoctorTracingText(err.Error(), tracing.Endpoint))

		return
	}

	dc.passf(doctorTracingSection, "Tracing config syntax valid")
	dc.checkTracingEndpoint(tracing)
	dc.checkTracingHeaderSources(tracing)
}

func (dc *doctorContext) checkTracingEndpoint(tracing config.TelemetryTracingConfig) {
	endpoint := strings.TrimSpace(tracing.Endpoint)
	if endpoint == "" {
		dc.warnf(doctorTracingSection, "Tracing endpoint is unset")

		return
	}

	protocol := tracing.ProtocolOrDefault()
	display := safeTracingEndpoint(endpoint)

	switch protocol {
	case config.TelemetryTracingProtocolGRPC:
		if tracing.Insecure {
			dc.passf(doctorTracingSection, "Tracing endpoint: grpc %s (plaintext)", display)

			if host, _, err := net.SplitHostPort(endpoint); err == nil && !doctorTracingHostIsLoopback(host) {
				dc.warnf(doctorTracingSection, "Tracing endpoint uses plaintext gRPC to a non-loopback host: %s", display)
			}
		} else {
			dc.passf(doctorTracingSection, "Tracing endpoint: grpc %s (TLS)", display)
		}
	case config.TelemetryTracingProtocolHTTPProtobuf:
		dc.passf(doctorTracingSection, "Tracing endpoint: http/protobuf %s", display)

		u, err := url.Parse(endpoint)
		if err != nil {
			return
		}

		if !strings.HasSuffix(u.Path, "/v1/traces") {
			if strings.HasSuffix(strings.ToLower(u.Hostname()), ".grafana.net") {
				dc.warnf(doctorTracingSection, "Grafana Cloud OTLP HTTP endpoints should use the stack-specific trace URL ending in /v1/traces: %s", display)
			} else {
				dc.warnf(doctorTracingSection, "HTTP/protobuf endpoint path is not /v1/traces; confirm the backend expects this exact trace URL: %s", display)
			}
		}
	}
}

func (dc *doctorContext) checkTracingHeaderSources(tracing config.TelemetryTracingConfig) {
	total := len(tracing.Headers) + len(tracing.HeadersEnv) + len(tracing.HeadersFile)
	if total == 0 {
		dc.passf(doctorTracingSection, "Tracing headers: none configured")

		return
	}

	if len(tracing.Headers) > 0 {
		dc.passf(doctorTracingSection, "Tracing inline headers configured: %s (values redacted)", doctorTracingHeaderNames(tracing.Headers))
	}

	if len(tracing.HeadersEnv) > 0 {
		dc.passf(doctorTracingSection, "Tracing env header sources configured: %s", doctorTracingHeaderSources(tracing.HeadersEnv))
	}

	if len(tracing.HeadersFile) > 0 {
		dc.passf(doctorTracingSection, "Tracing file header sources configured: %s", doctorTracingHeaderNames(tracing.HeadersFile))
	}

	sourceDir := doctorTracingConfigSourceDir()
	if _, err := tracing.ResolvedHeaders(sourceDir); err != nil {
		message := "Tracing header sources invalid: " + sanitizeDoctorTracingText(err.Error(), tracing.Endpoint)
		if tracing.Enabled {
			dc.failf(doctorTracingSection, "%s", message)
		} else {
			dc.warnf(doctorTracingSection, "%s", message)
		}

		return
	}

	dc.passf(doctorTracingSection, "Tracing header sources valid: %d configured (values redacted)", total)
}

func doctorTracingHeaderNames(headers map[string]string) string {
	if len(headers) == 0 {
		return "(none)"
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return strings.Join(keys, ", ")
}

func doctorTracingHeaderSources(headers map[string]string) string {
	if len(headers) == 0 {
		return "(none)"
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, fmt.Sprintf("%s from %s", key, headers[key]))
	}

	sort.Strings(keys)

	return strings.Join(keys, ", ")
}

func doctorTracingConfigSourceDir() string {
	if cfg != nil && cfg.SourceDir != "" {
		return cfg.SourceDir
	}

	target := cfgFile
	if target == "" {
		target = paths.ConfigFile
	}

	if target == "" {
		return ""
	}

	expanded := config.ExpandPath(target)
	if abs, err := filepath.Abs(expanded); err == nil {
		return filepath.Dir(abs)
	}

	return filepath.Dir(expanded)
}

func doctorTracingHostIsLoopback(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func safeTracingEndpoint(endpoint string) string {
	value := strings.TrimSpace(endpoint)
	if value == "" {
		return "(empty)"
	}

	if u, err := url.Parse(value); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""

		return safeDoctorText(u.String(), 200)
	}

	for _, scheme := range []string{"http://", "https://"} {
		if rest, ok := strings.CutPrefix(value, scheme); ok {
			rest, _, _ = strings.Cut(rest, "?")

			rest, _, _ = strings.Cut(rest, "#")
			if _, after, ok := strings.Cut(rest, "@"); ok {
				rest = after
			}

			return safeDoctorText(scheme+rest, 200)
		}
	}

	return safeDoctorText(value, 200)
}

func sanitizeDoctorTracingText(raw, endpoint string) string {
	text := strings.NewReplacer(
		"\r\n", "; ",
		"\n", "; ",
		"\r", "; ",
		"\t", " ",
	).Replace(raw)

	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		text = strings.ReplaceAll(text, endpoint, safeTracingEndpoint(endpoint))
	}

	text = doctorTracingURLPattern.ReplaceAllStringFunc(text, safeTracingEndpoint)

	return safeDoctorText(text, 500)
}
