package daemon

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func newSandboxTestManager(t *testing.T, cfg *config.Config) *SessionManager {
	t.Helper()

	tmpDir := t.TempDir()

	return NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(tmpDir, "state.json"),
		DataDir:    tmpDir,
		LogDir:     tmpDir,
		RuntimeDir: filepath.Join(tmpDir, "runtime"),
	}, slog.Default())
}

func TestResolveSandboxRequiresBackend(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true}
	cfg.Agents["claude"] = config.Agent{Command: "claude"}

	sm := newSandboxTestManager(t, cfg)

	ok, err := sm.resolveSandbox("claude")
	if ok {
		t.Fatal("resolveSandbox should not report sandboxed when no backend is set")
	}

	if err == nil {
		t.Fatal("expected a fail-closed error when backend is unset")
	}

	if !strings.Contains(err.Error(), "no backend selected") {
		t.Errorf("error should explain the missing backend, got: %v", err)
	}

	if !strings.Contains(err.Error(), "safehouse") || !strings.Contains(err.Error(), "nono") {
		t.Errorf("error should name safehouse and nono, got: %v", err)
	}
}

func TestResolveSandboxDisabledNoBackendNeeded(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: false}
	cfg.Agents["canny"] = config.Agent{Command: "claude"}

	sm := newSandboxTestManager(t, cfg)

	ok, err := sm.resolveSandbox("canny")
	if ok || err != nil {
		t.Fatalf("disabled sandbox = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestResolveSandboxUnknownBackend(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "thrawn"}
	cfg.Agents["dreich"] = config.Agent{Command: "claude"}

	sm := newSandboxTestManager(t, cfg)

	ok, err := sm.resolveSandbox("dreich")
	if ok {
		t.Fatal("unknown backend should not report sandboxed")
	}

	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestSandboxOptsCarryBackend(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "nono"}

	sm := newSandboxTestManager(t, cfg)

	merged := cfg.Sandbox
	opts := sm.sandboxOptsFromConfig(merged, "braw123", "/tmp/bothy", "claude", []string{"TERM"}, false)

	if opts.Backend != "nono" {
		t.Errorf("opts.Backend = %q, want nono", opts.Backend)
	}

	if !strings.Contains(opts.ProfilePath, "braw123") {
		t.Errorf("opts.ProfilePath should be per-session, got %q", opts.ProfilePath)
	}

	if !strings.Contains(opts.ProfilePath, sm.paths.RuntimeDir) {
		t.Errorf("opts.ProfilePath should live under RuntimeDir (readable in sandbox), got %q", opts.ProfilePath)
	}
}

func TestAgentBackendOverridesGlobal(t *testing.T) {
	global := config.SandboxConfig{Enabled: true, Backend: "safehouse"}
	agent := config.SandboxConfig{Backend: "nono"}

	merged := global.Merge(agent)
	if merged.Backend != "nono" {
		t.Errorf("merged.Backend = %q, want nono (agent overrides global)", merged.Backend)
	}
}

// TestSandboxOptsCarryNetworkAndSignalMode: Phase-2 fields must reach WrapOpts
// so the nono backend can emit the network / security.signal_mode sections.
func TestSandboxOptsCarryNetworkAndSignalMode(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{
		Enabled:    true,
		Backend:    "nono",
		SignalMode: "isolated",
		Network:    &config.SandboxNetworkConfig{Block: true, AllowDomains: []string{"kirk.example"}},
	}

	sm := newSandboxTestManager(t, cfg)

	opts := sm.sandboxOptsFromConfig(cfg.Sandbox, "braw123", "/tmp/bothy", "claude", []string{"TERM"}, false)

	if opts.SignalMode != "isolated" {
		t.Errorf("opts.SignalMode = %q, want isolated", opts.SignalMode)
	}

	if opts.Network == nil || !opts.Network.Block {
		t.Fatalf("opts.Network should carry block=true, got %+v", opts.Network)
	}

	if len(opts.Network.AllowDomains) != 1 || opts.Network.AllowDomains[0] != "kirk.example" {
		t.Errorf("opts.Network.AllowDomains = %v, want [kirk.example]", opts.Network.AllowDomains)
	}
}

// TestSandboxOptsEmptyNetworkIsNil: an unset network policy must yield a nil
// WrapOpts.Network so the backend leaves nono's allow-by-default untouched.
func TestSandboxOptsEmptyNetworkIsNil(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "nono"}

	sm := newSandboxTestManager(t, cfg)

	opts := sm.sandboxOptsFromConfig(cfg.Sandbox, "canny123", "/tmp/bothy", "claude", nil, false)
	if opts.Network != nil {
		t.Errorf("opts.Network should be nil when no policy is set, got %+v", opts.Network)
	}
}

// TestSandboxOptsInjectsPathAndHome: nono's env allowlist scrubs everything not
// listed, so PATH and HOME must be added to EnvKeys or the agent breaks.
func TestSandboxOptsInjectsPathAndHome(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "nono"}

	sm := newSandboxTestManager(t, cfg)

	opts := sm.sandboxOptsFromConfig(cfg.Sandbox, "braw123", "/tmp/bothy", "claude", []string{"TERM"}, false)

	hasPath, hasHome := false, false

	for _, k := range opts.EnvKeys {
		switch k {
		case "PATH":
			hasPath = true
		case "HOME":
			hasHome = true
		}
	}

	if !hasPath || !hasHome {
		t.Errorf("EnvKeys must include PATH and HOME for nono allowlist mode, got %v", opts.EnvKeys)
	}
}

// TestSandboxOptsGrantsAgentBinaryDir: nono does not auto-grant the launched
// command's directory, so the resolved agent binary dir must be a read grant.
func TestSandboxOptsGrantsAgentBinaryDir(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "nono"}

	sm := newSandboxTestManager(t, cfg)

	// Use an absolute agent path so resolution is deterministic on any host.
	opts := sm.sandboxOptsFromConfig(cfg.Sandbox, "braw123", "/tmp/bothy", "/opt/agents/claude", []string{"TERM"}, false)

	found := false

	for _, d := range opts.ReadDirs {
		if d == "/opt/agents" {
			found = true
		}
	}

	if !found {
		t.Errorf("agent binary dir /opt/agents should be a read grant, got ReadDirs=%v", opts.ReadDirs)
	}
}
