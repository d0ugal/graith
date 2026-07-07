package approvals

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "approvals.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return p
}

const builtinTestConfig = `{"allow":[{"rule":"echo @*"},{"rule":"ls @*"}],"deny":[{"rule":"rm @arg*"}]}`

func TestBuiltinBackendDecisions(t *testing.T) {
	cfgPath := writeConfig(t, builtinTestConfig)

	cases := []struct {
		name     string
		toolName string
		input    string
		want     string
	}{
		{"allow", "Bash", `{"command":"echo hi"}`, DecisionAllow},
		{"deny -> block", "Bash", `{"command":"rm -rf /"}`, DecisionBlock},
		{"ask -> defer", "Bash", `{"command":"kubectl get pods"}`, DecisionDefer},
		{"non-bash defers", "Write", `{"file_path":"x"}`, DecisionDefer},
		{"empty command defers", "Bash", `{}`, DecisionDefer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := builtinBackend{}.Decide(context.Background(),
				Request{ToolName: c.toolName, ToolInput: c.input},
				Config{BuiltinConfig: cfgPath})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if d.Decision != c.want {
				t.Errorf("decision = %q, want %q", d.Decision, c.want)
			}
		})
	}
}

func TestBuiltinBackendAvailability(t *testing.T) {
	if av := (builtinBackend{}).Availability(Config{}); av.CanEnforce {
		t.Error("builtin with no config path should fail closed")
	}

	bad := writeConfig(t, `{"allow":[{"rule":"foo @("}]}`) // unbalanced group
	if av := (builtinBackend{}).Availability(Config{BuiltinConfig: bad}); av.CanEnforce {
		t.Error("builtin with an invalid config should fail closed")
	}

	good := writeConfig(t, builtinTestConfig)
	if av := (builtinBackend{}).Availability(Config{BuiltinConfig: good}); !av.CanEnforce {
		t.Error("builtin with a valid config should be available")
	}
}

// TestBuiltinBackendInline exercises the inline ruleset path (BuiltinInline)
// that config.toml's [approvals.builtin] allow/deny keys compile to.
func TestBuiltinBackendInline(t *testing.T) {
	inline := []byte(builtinTestConfig)

	if av := (builtinBackend{}).Availability(Config{BuiltinInline: inline}); !av.CanEnforce {
		t.Error("builtin with valid inline rules should be available")
	}

	badInline := []byte(`{"allow":[{"rule":"foo @("}]}`)
	if av := (builtinBackend{}).Availability(Config{BuiltinInline: badInline}); av.CanEnforce {
		t.Error("builtin with invalid inline rules should fail closed")
	}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"allow", `{"command":"echo hi"}`, DecisionAllow},
		{"deny -> block", `{"command":"rm -rf /"}`, DecisionBlock},
		{"ask -> defer", `{"command":"kubectl get pods"}`, DecisionDefer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := builtinBackend{}.Decide(context.Background(),
				Request{ToolName: "Bash", ToolInput: c.input},
				Config{BuiltinInline: inline})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if d.Decision != c.want {
				t.Errorf("decision = %q, want %q", d.Decision, c.want)
			}
		})
	}
}

// TestBuiltinBackendInlinePrecedence checks inline rules take precedence over a
// (possibly stale) config path when both happen to reach the backend.
func TestBuiltinBackendInlinePrecedence(t *testing.T) {
	// File denies echo; inline allows it. Inline should win.
	filePath := writeConfig(t, `{"deny":[{"rule":"echo @*"}]}`)
	inline := []byte(`{"allow":[{"rule":"echo @*"}]}`)

	d, err := builtinBackend{}.Decide(context.Background(),
		Request{ToolName: "Bash", ToolInput: `{"command":"echo hi"}`},
		Config{BuiltinConfig: filePath, BuiltinInline: inline})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.Decision != DecisionAllow {
		t.Errorf("decision = %q, want %q (inline should take precedence)", d.Decision, DecisionAllow)
	}
}
