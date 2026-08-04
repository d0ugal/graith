package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
)

func TestCheckTracingValidSourcesRedactsValues(t *testing.T) {
	oldCfg, oldCfgFile, oldPaths, oldOut := cfg, cfgFile, paths, out

	t.Cleanup(func() {
		cfg, cfgFile, paths, out = oldCfg, oldCfgFile, oldPaths, oldOut
	})

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)

	sourceDir := t.TempDir()

	headerPath := filepath.Join(sourceDir, "headers", "authorization")
	if err := os.MkdirAll(filepath.Dir(headerPath), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(headerPath, []byte("Bearer file-thrawn-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OTLP_BRAW_HEADER", "Bearer env-thrawn-secret")

	cfg = config.Default()
	cfg.SourceDir = sourceDir
	cfg.Telemetry.Tracing = config.TelemetryTracingConfig{
		Enabled:  true,
		Endpoint: "https://otlp-gateway-prod-us-east-0.grafana.net/otlp/v1/traces",
		Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
		Headers: map[string]string{
			"x-inline": "Bearer inline-thrawn-secret",
		},
		HeadersEnv: map[string]string{
			"x-env": "OTLP_BRAW_HEADER",
		},
		HeadersFile: map[string]string{
			"authorization": "headers/authorization",
		},
	}

	dc := newDoctorContext()
	dc.checkTracing()

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("tracing check should pass, got failures: %s", failed)
	}

	rendered := buf.String() + "\n" + strings.Join(checkResults(dc, "ok"), "\n")
	for _, leaked := range []string{
		"inline-thrawn-secret",
		"env-thrawn-secret",
		"file-thrawn-secret",
	} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("tracing doctor leaked %q:\n%s", leaked, rendered)
		}
	}

	if !strings.Contains(rendered, "Tracing header sources valid: 3 configured") {
		t.Fatalf("expected valid header-source diagnostic, got:\n%s", rendered)
	}
}

func TestExecuteDoctorTracingConfigLoadErrorRedactsEndpointSecrets(t *testing.T) {
	oldCfg, oldCfgFile, oldPaths, oldOut, oldJSONOutput, oldAgentMode, oldDoctorTracing := cfg, cfgFile, paths, out, jsonOutput, agentMode, doctorTracing

	t.Cleanup(func() {
		cfg, cfgFile, paths, out = oldCfg, oldCfgFile, oldPaths, oldOut
		jsonOutput, agentMode, doctorTracing = oldJSONOutput, oldAgentMode, oldDoctorTracing
	})

	unsetGraithSessionID(t)
	t.Setenv("GR_AGENT_MODE", "0")

	cfg = nil
	cfgFile = ""
	paths = config.Paths{}
	out = nil
	jsonOutput = false
	agentMode = false
	doctorTracing = false

	dir := t.TempDir()

	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[telemetry.tracing]
enabled = true
endpoint = "https://dreich-secret@example.grafana.net/otlp/v1/traces?token=thrawn-secret#frag"
protocol = "http/protobuf"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	rendered := captureStderrForTracingTest(t, func() {
		err := executeWithArgs([]string{"--config", configPath, "doctor", "--tracing"})
		if err == nil {
			t.Fatal("executeWithArgs() = nil, want config validation error")
		}
	})

	if !strings.Contains(rendered, "credentials belong") {
		t.Fatalf("expected credential placement error, got:\n%s", rendered)
	}

	for _, leaked := range []string{"dreich-secret", "thrawn-secret", "token=", "#frag"} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("doctor config-load error leaked %q:\n%s", leaked, rendered)
		}
	}
}

func TestCheckTracingMissingEnvFailsWhenTracingEnabled(t *testing.T) {
	oldCfg, oldCfgFile, oldPaths, oldOut := cfg, cfgFile, paths, out

	t.Cleanup(func() {
		cfg, cfgFile, paths, out = oldCfg, oldCfgFile, oldPaths, oldOut
	})

	out = output.NewWithWriter(false, &bytes.Buffer{})

	unsetEnvForDoctorTracingTest(t, "OTLP_DREICH_HEADER")

	cfg = config.Default()
	cfg.Telemetry.Tracing = config.TelemetryTracingConfig{
		Enabled:  true,
		Endpoint: "127.0.0.1:4317",
		Protocol: config.TelemetryTracingProtocolGRPC,
		Insecure: true,
		HeadersEnv: map[string]string{
			"authorization": "OTLP_DREICH_HEADER",
		},
	}

	dc := newDoctorContext()
	dc.checkTracing()

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "OTLP_DREICH_HEADER") || !strings.Contains(failed, "not set") {
		t.Fatalf("expected missing env failure, got: %q", failed)
	}
}

func TestCheckTracingEndpointErrorRedactsUserinfo(t *testing.T) {
	oldCfg, oldCfgFile, oldPaths, oldOut := cfg, cfgFile, paths, out

	t.Cleanup(func() {
		cfg, cfgFile, paths, out = oldCfg, oldCfgFile, oldPaths, oldOut
	})

	var buf bytes.Buffer

	out = output.NewWithWriter(false, &buf)

	cfg = config.Default()
	cfg.Telemetry.Tracing = config.TelemetryTracingConfig{
		Enabled:  true,
		Endpoint: "https://dreich-secret@example.grafana.net/otlp/v1/traces?token=thrawn-secret",
		Protocol: config.TelemetryTracingProtocolHTTPProtobuf,
	}

	dc := newDoctorContext()
	dc.checkTracing()

	rendered := buf.String() + "\n" + strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(rendered, "credentials belong") {
		t.Fatalf("expected credential placement failure, got:\n%s", rendered)
	}

	for _, leaked := range []string{"dreich-secret", "thrawn-secret", "token="} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("tracing endpoint diagnostic leaked %q:\n%s", leaked, rendered)
		}
	}
}

func TestCheckTracingDisabledIsWarningOnly(t *testing.T) {
	oldCfg, oldCfgFile, oldPaths, oldOut := cfg, cfgFile, paths, out

	t.Cleanup(func() {
		cfg, cfgFile, paths, out = oldCfg, oldCfgFile, oldPaths, oldOut
	})

	out = output.NewWithWriter(false, &bytes.Buffer{})
	cfg = config.Default()

	dc := newDoctorContext()
	dc.checkTracing()

	if failed := strings.Join(checkResults(dc, "fail"), "\n"); failed != "" {
		t.Fatalf("disabled tracing should not fail, got: %s", failed)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(warned, "Tracing disabled") || !strings.Contains(warned, "Tracing endpoint is unset") {
		t.Fatalf("expected disabled tracing warnings, got: %q", warned)
	}
}

func unsetEnvForDoctorTracingTest(t *testing.T, name string) {
	t.Helper()

	old, ok := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}

	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func captureStderrForTracingTest(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	orig := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)

	go func() {
		var buf bytes.Buffer

		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	func() {
		defer func() {
			_ = w.Close()
			os.Stderr = orig
		}()

		fn()
	}()

	return <-done
}
