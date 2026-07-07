package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/approvals/localmost"
	"github.com/d0ugal/graith/internal/config"
)

// TestApprovalsEngineInline verifies the CLI compiles inline [approvals.builtin]
// rules when no --config flag is given, and reports the inline source (#737).
func TestApprovalsEngineInline(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	cfg = config.Default()
	cfg.Approvals = config.Approvals{
		Backend: "builtin",
		Builtin: config.ApprovalsBuiltin{Deny: []any{"echo @*"}},
	}

	eng, source, err := approvalsEngine("")
	if err != nil {
		t.Fatalf("approvalsEngine: %v", err)
	}

	if !strings.Contains(source, "inline") {
		t.Errorf("source = %q, want to mention inline", source)
	}

	// The inline deny rule should take effect.
	if pol, _ := eng.Evaluate("echo hi"); pol != localmost.PolicyDeny {
		t.Errorf("policy = %q, want deny", pol)
	}
}

// TestApprovalsEngineFlagWinsOverInline verifies an explicit --config flag wins
// over inline rules: the external file's rules apply, not the inline ones.
func TestApprovalsEngineFlagWinsOverInline(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	dir := t.TempDir()
	external := filepath.Join(dir, "approvals.json")
	if err := os.WriteFile(external, []byte(`{"allow":[{"rule":"echo @*"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg = config.Default()
	cfg.Approvals = config.Approvals{
		Backend: "builtin",
		// Inline would deny echo; the --config flag file allows it.
		Builtin: config.ApprovalsBuiltin{Deny: []any{"echo @*"}},
	}

	eng, source, err := approvalsEngine(external)
	if err != nil {
		t.Fatalf("approvalsEngine: %v", err)
	}

	if source != external {
		t.Errorf("source = %q, want %q", source, external)
	}

	if pol, _ := eng.Evaluate("echo hi"); pol != localmost.PolicyAllow {
		t.Errorf("policy = %q, want allow (flag file should win)", pol)
	}
}

// TestApprovalsEngineNoConfig verifies a clear error when neither inline rules,
// a configured path, nor a --config flag is set.
func TestApprovalsEngineNoConfig(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	cfg = config.Default()
	cfg.Approvals = config.Approvals{Backend: "builtin"}

	if _, _, err := approvalsEngine(""); err == nil {
		t.Fatal("approvalsEngine should error when no rule source is configured")
	}
}
