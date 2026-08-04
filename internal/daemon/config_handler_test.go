package daemon

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// TestConfigHandlerReturnsEffectiveTOML verifies the "config" control message
// returns the daemon's effective configuration rendered as TOML.
func TestConfigHandlerReturnsEffectiveTOML(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "config", struct{}{})

	env := h.expectType(t, "config_response")

	var resp protocol.ConfigResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.EffectiveTOML == "" {
		t.Fatal("expected non-empty effective TOML")
	}

	// The default config always renders these top-level tables; a smoke check
	// that we serialized the real config, not an empty struct.
	if !strings.Contains(resp.EffectiveTOML, "[sandbox]") {
		t.Errorf("effective TOML missing [sandbox] table:\n%s", resp.EffectiveTOML)
	}
}

// TestConfigHandlerDiffEmptyForDefaults verifies that a daemon running on the
// built-in defaults reports no diff (the effective config equals the defaults).
func TestConfigHandlerDiffEmptyForDefaults(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "config", struct{}{})

	env := h.expectType(t, "config_response")

	var resp protocol.ConfigResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.DiffFromDefaults != "" {
		t.Errorf("expected empty diff for a default config, got:\n%s", resp.DiffFromDefaults)
	}
}

// TestConfigHandlerDiffReflectsCustomisation verifies the diff surfaces a value
// the daemon's config differs from the defaults on.
func TestConfigHandlerDiffReflectsCustomisation(t *testing.T) {
	h := newTestHarness(t)

	// Mutate the live config so it diverges from the built-in defaults.
	h.sm.mu.Lock()
	h.sm.cfg.Sandbox.Enabled = !h.sm.cfg.Sandbox.Enabled
	want := h.sm.cfg.Sandbox.Enabled
	h.sm.mu.Unlock()

	h.sendControl(t, "config", struct{}{})

	env := h.expectType(t, "config_response")

	var resp protocol.ConfigResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.DiffFromDefaults == "" {
		t.Fatal("expected a non-empty diff after customising the config")
	}

	if !strings.Contains(resp.DiffFromDefaults, "enabled") {
		t.Errorf("diff should mention the changed sandbox.enabled key:\n%s", resp.DiffFromDefaults)
	}

	if !want && !strings.Contains(resp.EffectiveTOML, "enabled = false") {
		t.Errorf("effective TOML should reflect the mutated value:\n%s", resp.EffectiveTOML)
	}
}

// TestConfigHandlerRedactsEnvSecrets verifies the config response masks secret-bearing
// agent env-map values so they never cross the control socket —
// neither to a remote paired user nor to a local session reading via the daemon.
func TestConfigHandlerRedactsEnvSecrets(t *testing.T) {
	h := newTestHarness(t)

	const envVal = "braw-fixture-val-42"

	h.addAuthenticatedSession(t, "canny-id", "canny", "tok-canny")

	h.sm.mu.Lock()

	if h.sm.cfg.Agents == nil {
		h.sm.cfg.Agents = map[string]config.Agent{}
	}

	agent := h.sm.cfg.Agents["claude"]
	agent.Env = map[string]string{"ANTHROPIC_API_KEY": envVal}
	h.sm.cfg.Agents["claude"] = agent

	h.sm.mu.Unlock()

	h.sendControlWithToken(t, "config", struct{}{}, "tok-canny")
	env := h.expectType(t, "config_response")

	var resp protocol.ConfigResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(resp.EffectiveTOML, envVal) {
		t.Errorf("effective TOML leaked an env value:\n%s", resp.EffectiveTOML)
	}

	if strings.Contains(resp.DiffFromDefaults, envVal) {
		t.Errorf("diff leaked an env value:\n%s", resp.DiffFromDefaults)
	}

	// The key is still visible (only the value is masked) so the shape is useful.
	if !strings.Contains(resp.EffectiveTOML, "ANTHROPIC_API_KEY") || !strings.Contains(resp.EffectiveTOML, config.RedactedMask) {
		t.Errorf("expected redacted env key with %q mask:\n%s", config.RedactedMask, resp.EffectiveTOML)
	}

	if !strings.Contains(resp.DiffFromDefaults, "ANTHROPIC_API_KEY") || !strings.Contains(resp.DiffFromDefaults, config.RedactedMask) {
		t.Errorf("expected redacted agent env key with %q mask in diff:\n%s", config.RedactedMask, resp.DiffFromDefaults)
	}

	// The live config must not have been mutated by redaction.
	if got := h.sm.Config().Agents["claude"].Env["ANTHROPIC_API_KEY"]; got != envVal {
		t.Errorf("live config env was mutated by redaction: got %q, want %q", got, envVal)
	}
}

func TestConfigHandlerRedactsTelemetryTracingHeaderSources(t *testing.T) {
	h := newTestHarness(t)

	const (
		inlineSecret = "Bearer canny-inline-secret" // #nosec G101 -- fixture exercises config output redaction.
		envName      = "OTLP_BRAW_HEADER"
		filePath     = "/Users/braw/.config/graith/otlp-header"
	)

	h.addAuthenticatedSession(t, "canny-id", "canny", "tok-canny")

	h.sm.mu.Lock()
	h.sm.cfg.Telemetry.Tracing.Headers = map[string]string{
		"authorization": inlineSecret,
	}
	h.sm.cfg.Telemetry.Tracing.HeadersEnv = map[string]string{
		"x-env": envName,
	}
	h.sm.cfg.Telemetry.Tracing.HeadersFile = map[string]string{
		"x-file": filePath,
	}
	h.sm.mu.Unlock()

	h.sendControlWithToken(t, "config", struct{}{}, "tok-canny")
	env := h.expectType(t, "config_response")

	var resp protocol.ConfigResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	for _, forbidden := range []string{inlineSecret, envName, filePath} {
		if strings.Contains(resp.EffectiveTOML, forbidden) {
			t.Errorf("effective TOML leaked telemetry tracing source %q:\n%s", forbidden, resp.EffectiveTOML)
		}

		if strings.Contains(resp.DiffFromDefaults, forbidden) {
			t.Errorf("diff leaked telemetry tracing source %q:\n%s", forbidden, resp.DiffFromDefaults)
		}
	}

	for _, want := range []string{"authorization", "x-env", "x-file", config.RedactedMask} {
		if !strings.Contains(resp.EffectiveTOML, want) {
			t.Errorf("effective TOML missing redacted telemetry tracing shape %q:\n%s", want, resp.EffectiveTOML)
		}
	}

	if got := h.sm.Config().Telemetry.Tracing.Headers["authorization"]; got != inlineSecret {
		t.Errorf("live config tracing header mutated by redaction: got %q, want %q", got, inlineSecret)
	}
}
