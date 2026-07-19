package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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

	writeCommandPolicyHookResponse(os.Getenv("GRAITH_AGENT_TYPE"), evaluateCommandPolicyHook())

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	os.Stdout = origStdout

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	got := string(data)
	if !strings.Contains(got, `"hookEventName":"PreToolUse"`) ||
		!strings.Contains(got, `"permissionDecision":"deny"`) ||
		!strings.Contains(got, "dreich configuration") {
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

	writeCommandPolicyHookResponse(os.Getenv("GRAITH_AGENT_TYPE"), evaluateCommandPolicyHook())

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

func TestCommandPolicySupervisorFailsClosedOnWorkerFailure(t *testing.T) {
	t.Setenv("GRAITH_AGENT_TYPE", "codex")

	tests := []struct {
		name string
		mode string
	}{
		{name: "nonzero", mode: "nonzero"},
		{name: "malformed", mode: "malformed"},
		{name: "timeout", mode: "timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GRAITH_POLICY_WORKER_TEST", tc.mode)

			origExec := commandPolicyExec
			origExecutable := commandPolicyExecutable
			origDeadline := commandPolicyDeadline
			commandPolicyExecutable = func() (string, error) { return os.Args[0], nil }
			commandPolicyDeadline = func() time.Duration { return 100 * time.Millisecond }
			commandPolicyExec = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				//nolint:gosec // The executable is this test process and every argument is a fixed test-runner flag.
				return exec.CommandContext(ctx, os.Args[0], "-test.run=TestCommandPolicyWorkerProcess")
			}

			t.Cleanup(func() {
				commandPolicyExec = origExec
				commandPolicyExecutable = origExecutable
				commandPolicyDeadline = origDeadline
			})

			output := captureCommandPolicyStdout(t, superviseCommandPolicyHook)
			if !strings.Contains(output, `"permissionDecision":"deny"`) ||
				!strings.Contains(output, "command policy worker") {
				t.Fatalf("supervisor output = %q, want native fail-closed deny", output)
			}
		})
	}
}

func TestCommandPolicyWorkerProcess(t *testing.T) {
	switch os.Getenv("GRAITH_POLICY_WORKER_TEST") {
	case "nonzero":
		os.Exit(17)
	case "malformed":
		_, _ = os.Stdout.WriteString("not-json\n")

		os.Exit(0)
	case "timeout":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
}

func captureCommandPolicyStdout(t *testing.T, run func() error) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	orig := os.Stdout
	os.Stdout = w

	t.Cleanup(func() { os.Stdout = orig })

	if err := run(); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	os.Stdout = orig

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}
