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
