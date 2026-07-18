package cli

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCommandPolicyHookConfigFailureEmitsNativeDeny(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "canny-session")
	t.Setenv("GRAITH_AGENT_TYPE", "codex")

	origStartupErr := commandPolicyStartupError
	commandPolicyStartupError = errors.New("dreich configuration")
	t.Cleanup(func() { commandPolicyStartupError = origStartupErr })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	if err := commandPolicyCheckCmd.RunE(commandPolicyCheckCmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = origStdout
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"behavior":"deny"`) || !strings.Contains(got, "dreich configuration") {
		t.Fatalf("hook output = %q, want Codex-native deny diagnostic", got)
	}
}

func TestCommandPolicyHookMissingSessionFailsClosed(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")
	t.Setenv("GRAITH_AGENT_TYPE", "claude")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	if err := commandPolicyCheckCmd.RunE(commandPolicyCheckCmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = origStdout
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"permissionDecision":"deny"`) || !strings.Contains(got, "cannot identify") {
		t.Fatalf("hook output = %q, want Claude-native missing-session deny", got)
	}
}
