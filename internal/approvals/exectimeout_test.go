package approvals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConfigExecTimeout covers the resolved Config's execTimeout fallback: a
// zero ExecTimeout uses the built-in default (the historical fixed 5s), and an
// explicit value is honoured.
func TestConfigExecTimeout(t *testing.T) {
	if got := (Config{}).execTimeout(); got != defaultExecTimeout {
		t.Errorf("zero ExecTimeout -> %v, want default %v", got, defaultExecTimeout)
	}

	if got := (Config{ExecTimeout: 250 * time.Millisecond}).execTimeout(); got != 250*time.Millisecond {
		t.Errorf("explicit ExecTimeout -> %v, want 250ms", got)
	}

	// A negative value is treated as unset (defensive; Validate rejects it
	// upstream, but the backend must never pass a non-positive deadline that
	// would make context.WithTimeout fire immediately).
	if got := (Config{ExecTimeout: -1}).execTimeout(); got != defaultExecTimeout {
		t.Errorf("negative ExecTimeout -> %v, want default %v", got, defaultExecTimeout)
	}
}

// TestCommandBackendHonoursExecTimeout is the regression for #1251: a backend
// invocation that outlives its configured execution timeout is cut off and
// defers, and the same slow command completes under a generous timeout. On the
// old fixed-5s path a caller could not tighten this bound at all.
func TestCommandBackendHonoursExecTimeout(t *testing.T) {
	// Sleeps well past a tight timeout, then would allow.
	script := writeScript(t, "#!/bin/sh\nsleep 1\necho '{\"decision\":\"allow\"}'\n")

	req := Request{ToolName: "Bash", ToolInput: `{"command":"ls"}`}

	d, err := commandBackend{}.Decide(context.Background(), req,
		Config{Command: script, ExecTimeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatalf("expected a timeout error, got decision %q", d.Decision)
	}

	if d.Decision != DecisionDefer {
		t.Errorf("timed-out command -> %q, want defer", d.Decision)
	}

	if !strings.Contains(err.Error(), "command backend execution deadline") {
		t.Errorf("timeout error = %q, want named command execution deadline", err)
	}
}

// TestCommandBackendCompletesUnderGenerousTimeout confirms the timeout only
// bounds slow commands: a fast command still decides even with a small budget.
func TestCommandBackendCompletesUnderGenerousTimeout(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\necho '{\"decision\":\"allow\",\"reason\":\"canny\"}'\n")

	d, err := commandBackend{}.Decide(context.Background(),
		Request{ToolName: "Bash", ToolInput: `{"command":"ls"}`},
		Config{Command: script, ExecTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.Decision != DecisionAllow {
		t.Errorf("decision = %q, want allow", d.Decision)
	}
}

// TestLocalmostBackendHonoursExecTimeout mirrors the command-backend regression
// for the localmost path, which shared the same fixed 5s constant.
func TestLocalmostBackendHonoursExecTimeout(t *testing.T) {
	body := "#!/bin/sh\ncat >/dev/null\nsleep 1\n" +
		`printf '{"hookSpecificOutput":{"permissionDecision":"allow"}}'` + "\n"

	p := filepath.Join(t.TempDir(), "localmost")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatalf("write fake localmost: %v", err)
	}

	d, err := localmostBackend{}.Decide(context.Background(),
		Request{ToolName: "Bash", ToolInput: `{"command":"ls"}`},
		Config{Command: p, ExecTimeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatalf("expected a timeout error, got decision %q", d.Decision)
	}

	if d.Decision != DecisionDefer {
		t.Errorf("timed-out localmost -> %q, want defer", d.Decision)
	}

	if !strings.Contains(err.Error(), "localmost backend execution deadline") {
		t.Errorf("timeout error = %q, want named localmost execution deadline", err)
	}
}
