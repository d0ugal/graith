package approvals

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalise(t *testing.T) {
	cases := map[string]string{
		"deny":  "block",
		"allow": "allow",
		"block": "block",
		"defer": "defer",
		"":      "",
	}
	for in, want := range cases {
		if got := Normalise(in); got != want {
			t.Errorf("Normalise(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBackendByName(t *testing.T) {
	cases := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"", "prompt", false},
		{"prompt", "prompt", false},
		{"command", "command", false},
		{"external", "command", false}, // synonym dispatches to the same backend
		{"localmost", "localmost", false},
		{"builtin", "builtin", false},
		{"auto", "auto", false},
		{"thrawn", "", true},
	}
	for _, c := range cases {
		be, err := BackendByName(c.name)
		if c.wantErr {
			if err == nil {
				t.Errorf("BackendByName(%q): expected error", c.name)
			}

			continue
		}

		if err != nil {
			t.Errorf("BackendByName(%q): unexpected error %v", c.name, err)
			continue
		}

		if be.Name() != c.want {
			t.Errorf("BackendByName(%q).Name() = %q, want %q", c.name, be.Name(), c.want)
		}
	}
}

func TestPromptBackendDefers(t *testing.T) {
	d, err := promptBackend{}.Decide(context.Background(), Request{ToolName: "Bash"}, Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.Decision != DecisionDefer {
		t.Errorf("promptBackend decision = %q, want defer", d.Decision)
	}

	if av := (promptBackend{}).Availability(Config{}); !av.CanEnforce {
		t.Error("promptBackend should always be available")
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "approve.sh")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatalf("write script: %v", err)
	}

	return p
}

func TestCommandBackendDecisions(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		want     string
		wantDeci bool // whether Decide yields a definitive allow/block
	}{
		{"allow", `#!/bin/sh` + "\n" + `echo '{"decision":"allow","reason":"bonnie"}'`, "allow", true},
		{"block", `#!/bin/sh` + "\n" + `echo '{"decision":"block"}'`, "block", true},
		{"deny normalises to block", `#!/bin/sh` + "\n" + `echo '{"decision":"deny"}'`, "block", true},
		{"defer", `#!/bin/sh` + "\n" + `echo '{"decision":"defer"}'`, "defer", false},
		{"empty decision defers", `#!/bin/sh` + "\n" + `echo '{}'`, "defer", false},
		{"bad json defers", `#!/bin/sh` + "\n" + `echo 'nae json'`, "defer", false},
		{"nonzero exit defers", `#!/bin/sh` + "\n" + `exit 3`, "defer", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			script := writeScript(t, c.body)
			d, err := commandBackend{}.Decide(context.Background(),
				Request{ToolName: "Bash", ToolInput: `{"command":"ls"}`},
				Config{Command: script})

			if c.wantDeci {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				if d.Decision != c.want {
					t.Errorf("decision = %q, want %q", d.Decision, c.want)
				}
			} else {
				// A defer is signalled either by a defer decision or by a
				// non-nil error (both fall through to the human).
				if d.Decision != DecisionDefer {
					t.Errorf("decision = %q, want defer", d.Decision)
				}
			}
		})
	}
}

func TestCommandBackendAvailability(t *testing.T) {
	if av := (commandBackend{}).Availability(Config{}); av.CanEnforce {
		t.Error("command backend with no command should be unavailable")
	}

	if av := (commandBackend{}).Availability(Config{Command: "localmost"}); !av.CanEnforce {
		t.Error("command backend with a command should be available")
	}
}

// TestBuiltinBackendFailsClosedWhenUnconfigured checks that the builtin
// backend refuses to enforce without a rules file or inline rules. Localmost's
// empty config intentionally discovers its default executable on PATH and is
// covered with a controlled PATH in localmost_test.go.
func TestBuiltinBackendFailsClosedWhenUnconfigured(t *testing.T) {
	be, _ := BackendByName(BackendBuiltin)
	if av := be.Availability(Config{}); av.CanEnforce {
		t.Error("builtin backend must fail closed when unconfigured")
	}
}
