package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestSandboxPolicyEngineUsesInlineRules(t *testing.T) {
	oldCfg, oldPaths := cfg, paths

	t.Cleanup(func() { cfg, paths = oldCfg, oldPaths })

	cfg = config.Default()
	cfg.CommandPolicy = config.CommandPolicy{
		Backend: "builtin",
		Builtin: config.CommandPolicyBuiltin{Allow: []any{"echo @*"}},
	}
	paths = config.Paths{ConfigFile: filepath.Join(t.TempDir(), "config.toml")}

	engine, source, err := sandboxPolicyEngine("")
	if err != nil {
		t.Fatal(err)
	}

	if source != "inline [command_policy.builtin]" {
		t.Fatalf("source = %q", source)
	}

	policy, err := engine.Evaluate("echo braw")
	if err != nil || policy != "allow" {
		t.Fatalf("Evaluate = %q, %v", policy, err)
	}
}

func TestSandboxPolicyEngineExplicitFile(t *testing.T) {
	oldCfg, oldPaths := cfg, paths

	t.Cleanup(func() { cfg, paths = oldCfg, oldPaths })

	cfg = config.Default()
	paths = config.Paths{ConfigFile: filepath.Join(t.TempDir(), "config.toml")}

	policyPath := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"deny":["rm @*"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	engine, source, err := sandboxPolicyEngine(policyPath)
	if err != nil {
		t.Fatal(err)
	}

	if source != policyPath {
		t.Fatalf("source = %q", source)
	}

	policy, err := engine.Evaluate("rm dreich")
	if err != nil || policy != "deny" {
		t.Fatalf("Evaluate = %q, %v", policy, err)
	}
}
