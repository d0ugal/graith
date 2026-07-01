package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestExecuteJSONErrorFormat(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	os.Stderr = w

	execErr := executeWithArgs([]string{"--json", "nonexistent-command"})

	_ = w.Close()

	os.Stderr = oldStderr

	if execErr == nil {
		t.Fatal("expected error for unknown command")
	}

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, r)

	var jsonErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &jsonErr); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	if jsonErr.Error == "" {
		t.Error("JSON error message is empty")
	}
}

func TestExecutePlainTextErrorFormat(t *testing.T) {
	origOut := out
	origJSON := jsonOutput
	origAgentMode := agentMode

	defer func() {
		out = origOut
		jsonOutput = origJSON
		agentMode = origAgentMode
	}()

	t.Setenv("GR_AGENT_MODE", "0")

	agentMode = false

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	os.Stderr = w

	execErr := executeWithArgs([]string{"nonexistent-command"})

	_ = w.Close()

	os.Stderr = oldStderr

	if execErr == nil {
		t.Fatal("expected error for unknown command")
	}

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, r)

	got := buf.String()
	if !strings.HasPrefix(got, "error: ") {
		t.Errorf("expected plain text error starting with 'error: ', got %q", got)
	}

	var jsonErr struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(buf.Bytes(), &jsonErr) == nil {
		t.Error("plain text error should not be valid JSON")
	}
}

func TestConfigFlagBlockedInsideSession(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	t.Setenv("GRAITH_SESSION_ID", "test-session-123")
	t.Setenv("GR_AGENT_MODE", "0")

	err := executeWithArgs([]string{"--config", "/tmp/evil.toml", "list"})
	if err == nil {
		t.Fatal("expected error when --config is used inside a session")
	}

	if !strings.Contains(err.Error(), "not allowed inside a graith session") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestConfigFlagAllowedOutsideSession(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	if v, ok := os.LookupEnv("GRAITH_SESSION_ID"); ok {
		t.Cleanup(func() { _ = os.Setenv("GRAITH_SESSION_ID", v) })
	}

	_ = os.Unsetenv("GRAITH_SESSION_ID")

	t.Setenv("GR_AGENT_MODE", "0")

	// This will fail to connect to the daemon (expected), but should NOT
	// fail with the "not allowed inside a session" error.
	err := executeWithArgs([]string{"--config", "/tmp/nonexistent.toml", "list"})
	if err != nil && strings.Contains(err.Error(), "not allowed inside a graith session") {
		t.Error("--config should be allowed outside a graith session")
	}
}

func TestConfigFlagBlockedForConfigSubcommand(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	t.Setenv("GRAITH_SESSION_ID", "test-session-123")
	t.Setenv("GR_AGENT_MODE", "0")

	err := executeWithArgs([]string{"--config", "/tmp/evil.toml", "config", "show"})
	if err == nil {
		t.Fatal("expected error when --config is used with config subcommand inside a session")
	}

	if !strings.Contains(err.Error(), "not allowed inside a graith session") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestConfigFlagBlockedWhenSetEmpty(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	t.Setenv("GRAITH_SESSION_ID", "")
	t.Setenv("GR_AGENT_MODE", "0")

	// GRAITH_SESSION_ID="" (set but empty) should still be treated as inside
	// a session — prevents bypass via GRAITH_SESSION_ID= gr --config ...
	err := executeWithArgs([]string{"--config", "/tmp/evil.toml", "list"})
	if err == nil {
		t.Fatal("expected error when --config is used with GRAITH_SESSION_ID set to empty")
	}

	if !strings.Contains(err.Error(), "not allowed inside a graith session") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestExecuteCobraSilencesOwnErrors(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	os.Stdout = w

	_ = executeWithArgs([]string{"nonexistent-command"})

	_ = w.Close()

	os.Stdout = oldStdout

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, r)

	if strings.Contains(buf.String(), "Error:") {
		t.Errorf("Cobra's default error message appeared on stdout: %s", buf.String())
	}
}

// TestRegisterCommandsIdempotent verifies that command registration (moved out
// of per-file init() functions into registerCommands) wires subcommands onto
// rootCmd and can be invoked repeatedly without duplicating them — the
// sync.Once guard makes executeWithArgs safe to call more than once.
func TestRegisterCommandsIdempotent(t *testing.T) {
	registerCommands()
	registerCommands()

	want := []string{"new", "list", "msg", "scenario", "store", "daemon", "config"}
	for _, name := range want {
		found := false

		for _, c := range rootCmd.Commands() {
			if c.Name() == name {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("subcommand %q not registered on rootCmd", name)
		}
	}

	// Persistent flags added during registration must be present exactly once.
	if rootCmd.PersistentFlags().Lookup("json") == nil {
		t.Error("--json persistent flag not registered")
	}

	// No duplicate command names (a second registration must not re-add them).
	seen := map[string]int{}
	for _, c := range rootCmd.Commands() {
		seen[c.Name()]++
	}

	for name, n := range seen {
		if n > 1 {
			t.Errorf("subcommand %q registered %d times, want 1", name, n)
		}
	}
}
