package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

const (
	doctorAlloySection = "observability_alloy"

	doctorAlloyCommandTimeout = 5 * time.Second
	doctorAlloyHTTPTimeout    = 2 * time.Second
	doctorCommandOutputLimit  = 16 * 1024
)

var doctorAlloyHTTPClient = func() *http.Client {
	return &http.Client{
		Timeout: doctorAlloyHTTPTimeout,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
}

type doctorCommandOutput struct {
	stdout string
	stderr string
	err    error
}

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)

	if b.limit <= 0 || b.buf.Len() >= b.limit {
		return written, nil
	}

	remaining := b.limit - b.buf.Len()
	if len(p) > remaining {
		p = p[:remaining]
	}

	_, _ = b.buf.Write(p)

	return written, nil
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}

func (dc *doctorContext) checkAlloy() {
	dc.section("Observability / Alloy")

	signals, err := parseAlloySignals(doctorAlloySignals)
	if err != nil {
		dc.failf(doctorAlloySection, "Alloy signals invalid: %v", err)
		dc.hintf("Use --alloy-signals daemon-logs,metrics,traces or --alloy-signals all")

		return
	}

	selected := alloySignalSet(signals)

	binary, source, err := resolveDoctorAlloyBinary(doctorAlloyBinary)
	if err != nil {
		dc.failf(doctorAlloySection, "Alloy binary unavailable: %v", err)
		dc.hintf("Install Grafana Alloy or pass --alloy-binary /path/to/alloy")
	} else {
		dc.passf(doctorAlloySection, "Alloy binary: %s (%s)", binary, source)
		dc.checkAlloyVersion(binary)
		dc.checkAlloyConfigValidation(binary, signals)
	}

	if selected[alloySignalDaemonLogs] {
		dc.checkAlloyLogFiles()
	}

	if selected[alloySignalMetrics] {
		dc.checkAlloyMetricsEndpoint()
	}

	dc.checkAlloyBackendURLs(selected)
	dc.checkAlloyServiceStatus()
}

func resolveDoctorAlloyBinary(raw string) (string, string, error) {
	if strings.TrimSpace(raw) == "" {
		path, err := exec.LookPath("alloy")
		if err != nil {
			return "", "", errors.New("alloy not found on PATH")
		}

		return path, "PATH", nil
	}

	value := strings.TrimSpace(raw)
	if doctorAlloyBinaryLooksLikePath(value) {
		value = config.ExpandPath(value)
		if !filepath.IsAbs(value) {
			abs, err := filepath.Abs(value)
			if err != nil {
				return "", "", fmt.Errorf("resolve configured path %q: %w", raw, err)
			}

			value = abs
		}

		if err := checkDoctorExecutable(value); err != nil {
			return "", "", fmt.Errorf("configured path %s: %w", value, err)
		}

		return value, "configured path", nil
	}

	path, err := exec.LookPath(value)
	if err != nil {
		return "", "", fmt.Errorf("configured command %q not found on PATH", value)
	}

	return path, "configured command", nil
}

func doctorAlloyBinaryLooksLikePath(value string) bool {
	if value == "." || value == ".." || filepath.IsAbs(value) {
		return true
	}

	return strings.ContainsAny(value, `/\`)
}

func checkDoctorExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return errors.New("is a directory")
	}

	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return errors.New("is not executable")
	}

	return nil
}

func (dc *doctorContext) checkAlloyVersion(binary string) {
	result := runDoctorCommand(doctorAlloyCommandTimeout, binary, []string{"--version"}, doctorMinimalEnv(nil))
	if result.err != nil {
		dc.failf(doctorAlloySection, "Alloy version check failed: %s", doctorCommandErr(result.err))
		return
	}

	versionLine := firstSafeLine(result.stdout, result.stderr)
	if versionLine == "" {
		dc.warnf(doctorAlloySection, "Alloy version: command succeeded but printed no version")
		return
	}

	dc.passf(doctorAlloySection, "Alloy version: %s", versionLine)
}

func (dc *doctorContext) checkAlloyConfigValidation(binary string, signals []alloySignal) {
	supported, err := alloyValidateSupported(binary)
	if err != nil {
		dc.warnf(doctorAlloySection, "Cannot determine whether Alloy supports local config validation: %s", doctorCommandErr(err))
		dc.hintf("Upgrade Alloy or run `alloy validate <config.alloy>` manually")

		return
	}

	if !supported {
		dc.warnf(doctorAlloySection, "Alloy config validation unavailable: installed Alloy does not support `alloy validate`")
		dc.hintf("Upgrade Alloy to validate configuration locally")

		return
	}

	validation, err := doctorAlloyValidationConfig(signals)
	if err != nil {
		message := sanitizeDoctorAlloyConfigError(err)
		if doctorAlloyTraceReceiverConfigError(err) {
			dc.warnf(doctorAlloySection, "Generated Alloy config validation skipped: %s", message)
			dc.hintf("Re-run with --alloy-signals metrics if Graith exports traces directly rather than through Alloy")

			return
		}

		dc.failf(doctorAlloySection, "Alloy config unavailable for validation: %s", message)

		return
	}

	if validation.cleanup != nil {
		defer validation.cleanup()
	}

	result := runDoctorCommand(doctorAlloyCommandTimeout, binary, []string{"validate", validation.path}, doctorAlloyValidationEnv())
	if result.err != nil {
		if validation.generated {
			dc.failf(doctorAlloySection, "Generated Alloy config validation failed: %s", doctorCommandErr(result.err))
			dc.hintf("Run: gr config alloy --signals %s > config.alloy && alloy validate config.alloy", alloySignalList(signals))
		} else {
			dc.failf(doctorAlloySection, "Alloy config validation failed for %s: %s", validation.path, doctorCommandErr(result.err))
			dc.hintf("Run: alloy validate %s", validation.path)
		}

		return
	}

	if validation.generated {
		dc.passf(doctorAlloySection, "Generated Alloy config validates for signal(s): %s", alloySignalList(signals))
	} else {
		dc.passf(doctorAlloySection, "Alloy config validates: %s", validation.path)
	}
}

func alloyValidateSupported(binary string) (bool, error) {
	for _, args := range [][]string{
		{"validate", "--help"},
		{"help", "validate"},
	} {
		result := runDoctorCommand(doctorAlloyCommandTimeout, binary, args, doctorMinimalEnv(nil))

		text := strings.ToLower(result.stdout + "\n" + result.stderr)
		if strings.Contains(text, "unknown command") ||
			strings.Contains(text, "unknown help topic") ||
			strings.Contains(text, "no help topic") ||
			strings.Contains(text, "unknown shorthand flag") {
			continue
		}

		if result.err == nil {
			return true, nil
		}

		return false, result.err
	}

	return false, nil
}

func sanitizeDoctorAlloyConfigError(err error) string {
	if err == nil {
		return ""
	}

	text := strings.ReplaceAll(err.Error(), "--signals", "--alloy-signals")

	endpoint := ""
	if cfg != nil {
		endpoint = cfg.Telemetry.Tracing.Endpoint
	}

	return sanitizeDoctorTracingText(text, endpoint)
}

func doctorAlloyTraceReceiverConfigError(err error) bool {
	if err == nil {
		return false
	}

	text := err.Error()

	return strings.Contains(text, "tracing endpoint") &&
		(strings.Contains(text, "loopback host:port") || strings.Contains(text, "is not loopback"))
}

type doctorAlloyValidation struct {
	path      string
	generated bool
	cleanup   func()
}

func doctorAlloyValidationConfig(signals []alloySignal) (doctorAlloyValidation, error) {
	if strings.TrimSpace(doctorAlloyConfig) != "" {
		path, err := filepath.Abs(config.ExpandPath(strings.TrimSpace(doctorAlloyConfig)))
		if err != nil {
			return doctorAlloyValidation{}, fmt.Errorf("resolve --alloy-config: %w", err)
		}

		if _, err := os.Stat(path); err != nil {
			return doctorAlloyValidation{}, fmt.Errorf("%s: %w", path, err)
		}

		return doctorAlloyValidation{path: path}, nil
	}

	effectiveCfg := cfg
	if effectiveCfg == nil {
		effectiveCfg = config.Default()
	}

	resolvedPaths, err := resolvedAlloyPaths(paths, effectiveCfg)
	if err != nil {
		return doctorAlloyValidation{}, err
	}

	text, err := renderAlloyConfig(alloyRenderInput{
		Config:  effectiveCfg,
		Paths:   resolvedPaths,
		Signals: signals,
	})
	if err != nil {
		return doctorAlloyValidation{}, err
	}

	file, err := os.CreateTemp("", "graith-alloy-doctor-*.alloy")
	if err != nil {
		return doctorAlloyValidation{}, err
	}

	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }

	if _, err := file.WriteString(text); err != nil {
		_ = file.Close()

		cleanup()

		return doctorAlloyValidation{}, err
	}

	if err := file.Close(); err != nil {
		cleanup()

		return doctorAlloyValidation{}, err
	}

	return doctorAlloyValidation{path: path, generated: true, cleanup: cleanup}, nil
}

func alloySignalList(signals []alloySignal) string {
	names := make([]string, 0, len(signals))
	for _, signal := range signals {
		names = append(names, string(signal))
	}

	return strings.Join(names, ",")
}

func (dc *doctorContext) checkAlloyLogFiles() {
	effectiveCfg := cfg
	if effectiveCfg == nil {
		effectiveCfg = config.Default()
	}

	resolvedPaths, err := resolvedAlloyPaths(paths, effectiveCfg)
	if err != nil {
		dc.failf(doctorAlloySection, "Alloy log paths cannot be resolved: %v", err)
		return
	}

	dc.checkAlloyReadableLogFile("daemon log", resolvedPaths.DaemonLog)
	dc.checkAlloyReadableLogFile("daemon stderr log", resolvedPaths.DaemonStderrLogPath())
}

func (dc *doctorContext) checkAlloyReadableLogFile(label, path string) {
	if strings.TrimSpace(path) == "" {
		dc.warnf(doctorAlloySection, "Alloy %s path is not resolved", label)
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			dc.warnf(doctorAlloySection, "Alloy %s missing: %s", label, path)
			dc.hintf("Start or restart the daemon so Graith creates the log file, or omit daemon-logs")
		} else {
			dc.failf(doctorAlloySection, "Alloy %s cannot be inspected: %s (%v)", label, path, err)
		}

		return
	}

	if info.IsDir() {
		dc.failf(doctorAlloySection, "Alloy %s is a directory, not a file: %s", label, path)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		dc.failf(doctorAlloySection, "Alloy %s is not readable by current user: %s (%v)", label, path, err)
		return
	}

	_ = f.Close()

	dc.passf(doctorAlloySection, "Alloy %s readable: %s (%s)", label, path, formatBytes(info.Size()))
}

func (dc *doctorContext) checkAlloyMetricsEndpoint() {
	if cfg == nil {
		dc.warnf(doctorAlloySection, "Cannot check Alloy metrics scrape target without loaded Graith config")
		return
	}

	metrics := cfg.Telemetry.Metrics
	if !metrics.Enabled {
		dc.warnf(doctorAlloySection, "Metrics signal selected but [telemetry.metrics] enabled is false")
		dc.hintf("Set [telemetry.metrics] enabled = true and restart the daemon, or omit metrics with --alloy-signals")

		return
	}

	target, err := alloyMetricsScrapeAddress(metrics.BindAddressOrDefault())
	if err != nil {
		dc.failf(doctorAlloySection, "Metrics scrape target invalid: %v", err)
		return
	}

	host, _, err := net.SplitHostPort(target)
	if err != nil {
		dc.failf(doctorAlloySection, "Metrics scrape target invalid: %s (%v)", target, err)
		return
	}

	if !alloyLoopbackHost(host) {
		dc.warnf(doctorAlloySection, "Metrics scrape reachability skipped for non-loopback target %s", target)
		dc.hintf("Curl the target from the Alloy host; doctor only performs local loopback scrape checks")

		return
	}

	u := url.URL{Scheme: "http", Host: target, Path: metrics.PathOrDefault()}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
	if err != nil {
		dc.failf(doctorAlloySection, "Metrics scrape request invalid: %v", err)
		return
	}

	resp, err := doctorAlloyHTTPClient().Do(req)
	if err != nil {
		dc.failf(doctorAlloySection, "Metrics scrape endpoint not reachable: %s (%v)", u.String(), err)
		dc.hintf("Confirm the daemon was restarted after enabling [telemetry.metrics]")

		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		dc.failf(doctorAlloySection, "Metrics scrape endpoint returned HTTP %d: %s", resp.StatusCode, u.String())
		return
	}

	dc.passf(doctorAlloySection, "Metrics scrape endpoint reachable: %s", u.String())
}

func (dc *doctorContext) checkAlloyBackendURLs(selected map[alloySignal]bool) {
	specs := []doctorBackendURLSpec{
		{signal: alloySignalDaemonLogs, env: "GRAITH_LOKI_URL", kind: doctorBackendLoki},
		{signal: alloySignalMetrics, env: "GRAITH_MIMIR_URL", kind: doctorBackendMimir},
		{signal: alloySignalTraces, env: "GRAITH_TEMPO_OTLP_ENDPOINT", kind: doctorBackendTempo},
	}

	for _, spec := range specs {
		if !selected[spec.signal] {
			continue
		}

		value, ok := os.LookupEnv(spec.env)
		if !ok || strings.TrimSpace(value) == "" {
			dc.warnf(doctorAlloySection, "%s is not set in the current shell", spec.env)
			dc.hintf("Set it in Alloy's service environment before running generated config")

			continue
		}

		diagnostic := validateDoctorBackendURL(spec.kind, value)
		switch diagnostic.level {
		case "ok":
			dc.passf(doctorAlloySection, "%s shape: %s", spec.env, diagnostic.message)
		case "warn":
			dc.warnf(doctorAlloySection, "%s shape: %s", spec.env, diagnostic.message)
		default:
			dc.failf(doctorAlloySection, "%s shape: %s", spec.env, diagnostic.message)
		}
	}
}

type doctorBackendKind string

const (
	doctorBackendLoki  doctorBackendKind = "loki"
	doctorBackendMimir doctorBackendKind = "mimir"
	doctorBackendTempo doctorBackendKind = "tempo"
)

type doctorBackendURLSpec struct {
	signal alloySignal
	env    string
	kind   doctorBackendKind
}

type doctorBackendURLDiagnostic struct {
	level   string
	message string
}

func validateDoctorBackendURL(kind doctorBackendKind, raw string) doctorBackendURLDiagnostic {
	if raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, " \t\r\n") || containsDoctorControl(raw) {
		return doctorBackendURLDiagnostic{"fail", "must not contain whitespace or control characters"}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return doctorBackendURLDiagnostic{"fail", "is not a valid URL"}
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return doctorBackendURLDiagnostic{"fail", "must use http or https"}
	}

	if u.Host == "" || strings.TrimSpace(u.Hostname()) == "" {
		return doctorBackendURLDiagnostic{"fail", "must include a host"}
	}

	if u.User != nil {
		return doctorBackendURLDiagnostic{"fail", "must not embed credentials in the URL; use the generated username/token environment variables"}
	}

	if u.RawQuery != "" || u.Fragment != "" {
		return doctorBackendURLDiagnostic{"fail", "must not include query strings or fragments"}
	}

	switch kind {
	case doctorBackendLoki:
		if !strings.HasSuffix(u.Path, "/loki/api/v1/push") {
			return doctorBackendURLDiagnostic{"warn", "path does not look like Loki push endpoint /loki/api/v1/push"}
		}

		return doctorBackendURLDiagnostic{"ok", "http(s) URL with Loki push path"}
	case doctorBackendMimir:
		if !strings.HasSuffix(u.Path, "/api/prom/push") &&
			!strings.HasSuffix(u.Path, "/api/v1/push") &&
			!strings.HasSuffix(u.Path, "/prometheus/api/v1/write") {
			return doctorBackendURLDiagnostic{"warn", "path does not look like a Prometheus remote-write endpoint"}
		}

		return doctorBackendURLDiagnostic{"ok", "http(s) URL with Prometheus remote-write path"}
	case doctorBackendTempo:
		if strings.HasSuffix(u.Path, "/v1/traces") {
			return doctorBackendURLDiagnostic{"warn", "looks like a trace-specific URL; generated Alloy config expects an OTLP HTTP base endpoint"}
		}

		if u.Path != "" && u.Path != "/" && !strings.HasSuffix(u.Path, "/otlp") {
			return doctorBackendURLDiagnostic{"warn", "path is unusual for an OTLP HTTP base endpoint"}
		}

		return doctorBackendURLDiagnostic{"ok", "http(s) URL with OTLP HTTP base shape"}
	default:
		return doctorBackendURLDiagnostic{"ok", "http(s) URL"}
	}
}

func containsDoctorControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}

	return false
}

func (dc *doctorContext) checkAlloyServiceStatus() {
	switch runtime.GOOS {
	case "darwin":
		dc.checkAlloyDarwinServiceStatus()
	case "linux":
		dc.checkAlloyLinuxServiceStatus()
	default:
		dc.warnf(doctorAlloySection, "Alloy service status not checked on %s", runtime.GOOS)
	}
}

func (dc *doctorContext) checkAlloyLinuxServiceStatus() {
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		dc.warnf(doctorAlloySection, "Alloy service status not checked: systemctl not found")
		return
	}

	userState, userKnown := systemdServiceState(systemctl, "--user", "is-active", "alloy.service")
	if userState == "active" {
		dc.passf(doctorAlloySection, "Alloy user service active (systemd --user alloy.service)")
		return
	}

	systemState, systemKnown := systemdServiceState(systemctl, "is-active", "alloy.service")
	if systemState == "active" {
		dc.passf(doctorAlloySection, "Alloy system service active (alloy.service)")
		return
	}

	switch {
	case userKnown && systemKnown:
		dc.warnf(doctorAlloySection, "Alloy service not active (systemd --user: %s; system: %s)", userState, systemState)
	case userKnown:
		dc.warnf(doctorAlloySection, "Alloy user service not active: %s", userState)
	case systemKnown:
		dc.warnf(doctorAlloySection, "Alloy system service not active: %s", systemState)
	default:
		dc.warnf(doctorAlloySection, "Alloy service status not detectable with systemctl")
	}
}

func systemdServiceState(systemctl string, args ...string) (string, bool) {
	result := runDoctorCommand(doctorAlloyCommandTimeout, systemctl, args, doctorMinimalEnv(doctorSystemdEnv()))

	state := strings.ToLower(firstSafeLine(result.stdout, result.stderr))
	if state == "" {
		return "", false
	}

	switch state {
	case "active", "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "unknown":
		return state, true
	default:
		if result.err != nil {
			return "", false
		}

		return state, true
	}
}

func (dc *doctorContext) checkAlloyDarwinServiceStatus() {
	if checked := dc.checkAlloyBrewServiceStatus(); checked {
		return
	}

	launchctl, err := exec.LookPath("launchctl")
	if err != nil {
		dc.warnf(doctorAlloySection, "Alloy service status not checked: brew services and launchctl unavailable")
		return
	}

	label := "homebrew.mxcl.alloy"
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)

	result := runDoctorCommand(doctorAlloyCommandTimeout, launchctl, []string{"print", target}, doctorMinimalEnv(nil))
	if result.err != nil {
		dc.warnf(doctorAlloySection, "Alloy launchd service not detected (%s)", label)
		return
	}

	text := strings.ToLower(result.stdout + "\n" + result.stderr)
	if strings.Contains(text, "state = running") || strings.Contains(text, "pid =") {
		dc.passf(doctorAlloySection, "Alloy launchd service appears loaded and running (%s)", label)
		return
	}

	dc.warnf(doctorAlloySection, "Alloy launchd service appears loaded but not running (%s)", label)
}

func (dc *doctorContext) checkAlloyBrewServiceStatus() bool {
	brew, err := exec.LookPath("brew")
	if err != nil {
		return false
	}

	result := runDoctorCommand(doctorAlloyCommandTimeout, brew, []string{"services", "list", "--json"}, doctorMinimalEnv(nil))
	if result.err != nil {
		return false
	}

	services, err := parseBrewServices(result.stdout)
	if err != nil {
		return false
	}

	for _, svc := range services {
		if svc.Name != "alloy" && svc.Name != "grafana/grafana/alloy" {
			continue
		}

		status := strings.ToLower(strings.TrimSpace(svc.Status))
		if strings.Contains(status, "started") || strings.Contains(status, "running") {
			dc.passf(doctorAlloySection, "Alloy Homebrew service appears started")
		} else {
			dc.warnf(doctorAlloySection, "Alloy Homebrew service status: %s", emptyOr(status, "unknown"))
		}

		return true
	}

	return false
}

type brewService struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func parseBrewServices(raw string) ([]brewService, error) {
	var services []brewService
	if err := json.Unmarshal([]byte(raw), &services); err != nil {
		return nil, err
	}

	return services, nil
}

func runDoctorCommand(timeout time.Duration, command string, args []string, env []string) doctorCommandOutput {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	configureDoctorCommand(cmd)

	var stdout, stderr cappedBuffer

	stdout.limit = doctorCommandOutputLimit
	stderr.limit = doctorCommandOutputLimit
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	return doctorCommandOutput{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func doctorMinimalEnv(extra []string) []string {
	keep := map[string]bool{
		"HOME":       true,
		"LANG":       true,
		"LC_ALL":     true,
		"LC_CTYPE":   true,
		"PATH":       true,
		"SHELL":      true,
		"TMPDIR":     true,
		"TEMP":       true,
		"TMP":        true,
		"SYSTEMROOT": true,
	}

	env := make([]string, 0, len(keep)+len(extra))

	for _, item := range os.Environ() {
		name, _, ok := strings.Cut(item, "=")
		if ok && keep[name] {
			env = append(env, item)
		}
	}

	env = append(env, extra...)

	return env
}

func doctorSystemdEnv() []string {
	var env []string

	for _, name := range []string{"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}

	return env
}

func doctorAlloyValidationEnv() []string {
	return doctorMinimalEnv([]string{
		"GRAITH_LOKI_URL=http://127.0.0.1:3100/loki/api/v1/push",
		"GRAITH_LOKI_USERNAME=doctor-placeholder",
		"GRAITH_LOKI_TOKEN=doctor-placeholder",
		"GRAITH_MIMIR_URL=http://127.0.0.1:9009/api/prom/push",
		"GRAITH_MIMIR_USERNAME=doctor-placeholder",
		"GRAITH_MIMIR_TOKEN=doctor-placeholder",
		"GRAITH_TEMPO_OTLP_ENDPOINT=http://127.0.0.1:4318",
		"GRAITH_TEMPO_USERNAME=doctor-placeholder",
		"GRAITH_TEMPO_TOKEN=doctor-placeholder",
		"GRAITH_OTLP_RECEIVER_TLS_CERT_FILE=/dev/null",
		"GRAITH_OTLP_RECEIVER_TLS_KEY_FILE=/dev/null",
	})
}

func firstSafeLine(outputs ...string) string {
	for _, output := range outputs {
		for _, line := range strings.Split(output, "\n") {
			line = safeDoctorText(strings.TrimSpace(line), 200)
			if line != "" {
				return line
			}
		}
	}

	return ""
}

func safeDoctorText(s string, limit int) string {
	var b strings.Builder

	for _, r := range s {
		if r == '\t' {
			r = ' '
		}

		if r < 0x20 || r == 0x7f {
			continue
		}

		b.WriteRune(r)

		if limit > 0 && b.Len() >= limit {
			break
		}
	}

	return strings.TrimSpace(b.String())
}

func doctorCommandErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.String()
	}

	return err.Error()
}

func emptyOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}

	return value
}
