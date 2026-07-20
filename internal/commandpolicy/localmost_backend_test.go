package commandpolicy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePolicyScript(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "localmost")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // executable test fixture
		t.Fatalf("write command-policy fixture: %v", err)
	}

	return path
}

func TestLocalmostDecisionSemantics(t *testing.T) {
	tests := []struct {
		name, output, want, reason string
	}{
		{name: "allow", output: `{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"braw"}}`, want: DecisionAllow, reason: "braw"},
		{name: "deny", output: `{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"dreich"}}`, want: DecisionDeny, reason: "dreich"},
		{name: "ask fails closed", output: `{"hookSpecificOutput":{"permissionDecision":"ask"}}`, want: DecisionDeny, reason: "no human decision path"},
		{name: "defer fails closed", output: `{"decision":"defer"}`, want: DecisionDeny, reason: "no human decision path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := writePolicyScript(t, "printf '%s' '"+tt.output+"'\n")

			got, err := (localmostBackend{}).Evaluate(context.Background(), Request{
				ToolName: "Bash", ToolInput: `{"command":"echo canny"}`,
			}, Config{Command: script})
			if err != nil {
				t.Fatal(err)
			}

			if got.Decision != tt.want || !strings.Contains(got.Reason, tt.reason) {
				t.Fatalf("Evaluate = %+v, want decision %q and reason containing %q", got, tt.want, tt.reason)
			}
		})
	}
}

func TestLocalmostEvaluationFailuresAreExplicit(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{name: "malformed output", body: "printf 'nae json'\n", want: "malformed output"},
		{name: "unknown decision", body: `printf '%s' '{"decision":"perhaps"}'` + "\n", want: "unknown decision"},
		{name: "backend error", body: "exit 7\n", want: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := writePolicyScript(t, tt.body)

			_, err := (localmostBackend{}).Evaluate(context.Background(), Request{
				ToolName: "exec_command", ToolInput: `{"cmd":"echo bothy"}`,
			}, Config{Command: script})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Evaluate error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLocalmostEvaluationOutputIsBounded(t *testing.T) {
	script := writePolicyScript(t, "printf '%s' '"+strings.Repeat("x", maxLocalmostOutput+1)+"'\n")

	_, err := (localmostBackend{}).Evaluate(context.Background(), Request{
		ToolName: "Bash", ToolInput: `{"command":"echo bairn"}`,
	}, Config{Command: script})
	if !errors.Is(err, errLocalmostOutputLimit) {
		t.Fatalf("Evaluate error = %v, want bounded-output failure", err)
	}
}

func TestLocalmostEvaluationTimeout(t *testing.T) {
	assertLocalmostEvaluationTimesOut(t, "exec sleep 2\n", "run_shell_command", "bounded evaluation")
}

func TestLocalmostEvaluationTimeoutClosesDescendantPipes(t *testing.T) {
	assertLocalmostEvaluationTimesOut(t, "sleep 10 &\nwait\n", "Bash", "descendant-held pipes")
}

func assertLocalmostEvaluationTimesOut(t *testing.T, body, toolName, label string) {
	t.Helper()

	script := writePolicyScript(t, body)
	start := time.Now()

	_, err := (localmostBackend{}).Evaluate(context.Background(), Request{
		ToolName: toolName, ToolInput: `{"command":"echo strath"}`,
	}, Config{Command: script, ExecTimeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Evaluate error = %v, want timeout", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("%s exceeded the backend bound: %v", label, elapsed)
	}
}

func TestLocalmostAvailabilityAndScope(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "localmost")
	if got := (localmostBackend{}).Availability(Config{Command: missing}); got.CanEnforce || got.Detail == "" {
		t.Fatalf("missing backend availability = %+v, want explicit failure", got)
	}

	// A non-shell tool never invokes the configured executable; it proceeds to
	// the authoritative sandbox even if that executable path is now absent.
	got, err := (localmostBackend{}).Evaluate(context.Background(), Request{
		ToolName: "Read", ToolInput: `{"file_path":"croft"}`,
	}, Config{Command: missing})
	if err != nil || got.Decision != DecisionAllow {
		t.Fatalf("out-of-scope Evaluate = %+v, %v, want allow", got, err)
	}
}
