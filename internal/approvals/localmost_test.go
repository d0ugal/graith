package approvals

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeLocalmost writes an executable stub that consumes stdin and prints the
// given hook-output JSON, standing in for the real localmost binary.
func fakeLocalmost(t *testing.T, decision, reason string) string {
	t.Helper()

	body := "#!/bin/sh\ncat >/dev/null\n" +
		`printf '{"hookSpecificOutput":{"permissionDecision":"` + decision + `","permissionDecisionReason":"` + reason + `"}}'` + "\n"

	p := filepath.Join(t.TempDir(), "localmost")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatalf("write fake localmost: %v", err)
	}

	return p
}

func TestLocalmostDecisionMapping(t *testing.T) {
	cases := []struct {
		permission string
		want       string
	}{
		{"allow", DecisionAllow},
		{"deny", DecisionBlock},
		{"ask", DecisionDefer},
		{"", DecisionDefer},
	}
	for _, c := range cases {
		t.Run(c.permission, func(t *testing.T) {
			script := fakeLocalmost(t, c.permission, "hoots")

			d, err := localmostBackend{}.Decide(context.Background(),
				Request{ToolName: "Bash", ToolInput: `{"command":"ls -a"}`},
				Config{Command: script})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if d.Decision != c.want {
				t.Errorf("permission %q -> %q, want %q", c.permission, d.Decision, c.want)
			}
		})
	}
}

func TestLocalmostNonBashDefers(t *testing.T) {
	// Even with a script that would allow, a non-Bash tool must defer without
	// invoking localmost.
	script := fakeLocalmost(t, "allow", "")

	d, err := localmostBackend{}.Decide(context.Background(),
		Request{ToolName: "Write", ToolInput: `{"file_path":"x"}`},
		Config{Command: script})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.Decision != DecisionDefer {
		t.Errorf("non-Bash tool -> %q, want defer", d.Decision)
	}
}

func TestLocalmostReconstructsEnvelopeWithoutPayload(t *testing.T) {
	// No HookPayload: the backend reconstructs a PreToolUse envelope. The fake
	// echoes allow regardless; we just confirm the round-trip succeeds.
	script := fakeLocalmost(t, "allow", "")

	d, err := localmostBackend{}.Decide(context.Background(),
		Request{ToolName: "Bash", ToolInput: `{"command":"echo hi"}`},
		Config{Command: script})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.Decision != DecisionAllow {
		t.Errorf("decision = %q, want allow", d.Decision)
	}
}

func TestLocalmostAvailability(t *testing.T) {
	t.Run("default command absent from PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())

		if av := (localmostBackend{}).Availability(Config{}); av.CanEnforce {
			t.Error("localmost backend should fail closed when the default binary is absent")
		}
	})

	t.Run("default command found on PATH", func(t *testing.T) {
		script := fakeLocalmost(t, "allow", "")
		t.Setenv("PATH", filepath.Dir(script))

		if av := (localmostBackend{}).Availability(Config{}); !av.CanEnforce {
			t.Error("localmost backend should discover the default binary on PATH")
		}
	})

	t.Run("configured command absent", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "localmost")
		if av := (localmostBackend{}).Availability(Config{Command: missing}); av.CanEnforce {
			t.Error("localmost backend should fail closed when the configured binary is absent")
		}
	})

	t.Run("configured command exists", func(t *testing.T) {
		script := fakeLocalmost(t, "allow", "")
		if av := (localmostBackend{}).Availability(Config{Command: script}); !av.CanEnforce {
			t.Error("localmost backend should be available when the configured binary exists")
		}
	})
}

func TestLocalmostFailsClosedOnError(t *testing.T) {
	body := "#!/bin/sh\nexit 7\n"

	p := filepath.Join(t.TempDir(), "localmost")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatalf("write: %v", err)
	}

	d, _ := localmostBackend{}.Decide(context.Background(),
		Request{ToolName: "Bash", ToolInput: `{"command":"ls"}`},
		Config{Command: p})
	if d.Decision != DecisionDefer {
		t.Errorf("non-zero exit -> %q, want defer", d.Decision)
	}
}
