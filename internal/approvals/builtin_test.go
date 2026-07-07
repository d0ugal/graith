package approvals

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestBuiltinEngineCache verifies the compiled engine is reused while the
// config file is unchanged, and reloaded (picking up new rules) once its mtime
// changes.
func TestBuiltinEngineCache(t *testing.T) {
	p := writeConfig(t, `{"allow":[{"rule":"echo @*"}]}`)

	first, err := loadEngineCached(p)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	second, err := loadEngineCached(p)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}

	if first != second {
		t.Error("expected the cached engine to be reused for an unchanged file")
	}

	// Rewrite the file with different rules and bump the mtime so the cache key
	// changes; the reload should return a fresh engine.
	if err := os.WriteFile(p, []byte(`{"allow":[{"rule":"ls @*"}]}`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	third, err := loadEngineCached(p)
	if err != nil {
		t.Fatalf("third load: %v", err)
	}

	if third == first {
		t.Error("expected the engine to be reloaded after the file mtime changed")
	}
}

// TestBuiltinEngineCacheReloadFailure verifies a reload failure surfaces the
// error rather than serving a stale engine.
func TestBuiltinEngineCacheReloadFailure(t *testing.T) {
	p := writeConfig(t, builtinTestConfig)

	if _, err := loadEngineCached(p); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	// Corrupt the file and bump its mtime so the cache is invalidated.
	if err := os.WriteFile(p, []byte(`{"allow":[{"rule":"foo @("}]}`), 0o600); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}

	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := loadEngineCached(p); err == nil {
		t.Error("expected an error when reloading an invalid config, got nil")
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
