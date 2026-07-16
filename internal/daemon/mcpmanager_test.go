package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestMCPManagerConnectDisconnect(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "echo", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("echo", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Verify process is running.
	select {
	case <-proc.done:
		t.Fatal("process should still be running")
	default:
	}

	mgr.Disconnect("proxy-1", nil)

	// Verify process is gone.
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process should be done after disconnect")
	}
}

// TestMCPManagerSandboxRequiresBackend: an enabled sandbox with no backend
// selected must fail closed for MCP servers exactly as it does for agent
// sessions — never silently fall back to safehouse (see #787).
func TestMCPManagerSandboxRequiresBackend(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		Sandbox: config.SandboxConfig{Enabled: true}, // no Backend
		MCPServers: []config.MCPServerConfig{
			{Name: "thrawn", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	_, err := mgr.Connect("thrawn", "proxy-1", config.TemplateVars{})
	if err == nil {
		t.Fatal("expected a fail-closed error when sandbox is enabled with no backend")
	}

	if !strings.Contains(err.Error(), "no backend selected") {
		t.Errorf("error should explain the missing backend, got: %v", err)
	}

	if !strings.Contains(err.Error(), "safehouse") || !strings.Contains(err.Error(), "nono") {
		t.Errorf("error should name safehouse and nono, got: %v", err)
	}
}

// TestMCPManagerSandboxDisabledNoBackendNeeded: when the per-server sandbox is
// turned off, an unset global backend must not block the process from starting.
func TestMCPManagerSandboxDisabledNoBackendNeeded(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		Sandbox: config.SandboxConfig{Enabled: true}, // no Backend
		MCPServers: []config.MCPServerConfig{
			{Name: "canny", Command: "cat", Sandbox: boolPtr(false)},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	if _, err := mgr.Connect("canny", "proxy-1", config.TemplateVars{}); err != nil {
		t.Fatalf("Connect() with sandbox disabled should not require a backend, got: %v", err)
	}

	mgr.Disconnect("proxy-1", nil)
}

func TestMCPManagerConnectUnknownServer(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	_, err := mgr.Connect("nonexistent", "proxy-1", config.TemplateVars{})
	if err == nil {
		t.Fatal("expected error for unknown server")
	}
}

func TestMCPManagerDuplicateProxy(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "echo", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	_, err := mgr.Connect("echo", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("first Connect() error = %v", err)
	}

	_, err = mgr.Connect("echo", "proxy-1", config.TemplateVars{})
	if err == nil {
		t.Fatal("expected error for duplicate proxy ID")
	}
}

func TestMCPManagerReload(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "echo", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	if !mgr.HasServer("echo") {
		t.Error("should have 'echo' server")
	}

	newCfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "newserver", Command: "cat"},
		},
	}
	mgr.Reload(newCfg)

	if mgr.HasServer("echo") {
		t.Error("should not have 'echo' after reload")
	}

	if !mgr.HasServer("newserver") {
		t.Error("should have 'newserver' after reload")
	}
}

func TestMCPManagerReloadKillsProcesses(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "echo", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("echo", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Reload with a changed command — should kill the running process.
	newCfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "echo", Command: "cat", Args: []string{"-v"}},
		},
	}
	mgr.Reload(newCfg)

	select {
	case <-proc.done:
	case <-time.After(10 * time.Second):
		t.Fatal("process should be killed after reload with changed config")
	}
}

func TestMCPManagerStderrCapture(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "stderr-test", Command: "sh", Args: []string{"-c", "echo error >&2; sleep 0.1; cat"}},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("stderr-test", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	_ = proc
	logPath := logDir + "/mcp/stderr-test-proxy-1.log"

	var data []byte

	for range 50 {
		if d, err := os.ReadFile(logPath); err == nil && len(d) > 0 {
			data = d
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	mgr.Disconnect("proxy-1", nil)

	if len(data) == 0 {
		t.Fatal("stderr log should have content")
	}
}

func TestMCPManagerExpandsTemplateVars(t *testing.T) {
	logDir := t.TempDir()
	outDir := t.TempDir()
	// The command writes its expanded {session_id} to a file whose name also
	// references {session_id}, so we verify expansion in args reaches argv.
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "tmpl", Command: "sh", Args: []string{"-c", "echo {session_id} > " + outDir + "/{session_id}.txt; cat"}, Sandbox: boolPtr(false)},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("tmpl", "proxy-braw", config.TemplateVars{SessionID: "bairn-42"})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	_ = proc

	wantPath := outDir + "/bairn-42.txt"

	var data []byte

	for range 50 {
		if d, err := os.ReadFile(wantPath); err == nil && len(d) > 0 {
			data = d
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	mgr.Disconnect("proxy-braw", nil)

	if len(data) == 0 {
		t.Fatalf("expected file %q with expanded session id", wantPath)
	}

	if got := string(data); got != "bairn-42\n" {
		t.Errorf("expanded content = %q, want %q", got, "bairn-42\n")
	}
}

func TestMCPManagerEmptySessionFallsBackToProxyID(t *testing.T) {
	logDir := t.TempDir()
	outDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "tmpl", Command: "sh", Args: []string{"-c", "echo {session_id} > " + outDir + "/out.txt; cat"}, Sandbox: boolPtr(false)},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	// No SessionID: {session_id} must fall back to the proxyID so it never
	// collapses to a shared empty value across sessions.
	if _, err := mgr.Connect("tmpl", "proxy-haar", config.TemplateVars{}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	wantPath := outDir + "/out.txt"

	var data []byte

	for range 50 {
		if d, err := os.ReadFile(wantPath); err == nil && len(d) > 0 {
			data = d
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	mgr.Disconnect("proxy-haar", nil)

	if got := string(data); got != "proxy-haar\n" {
		t.Errorf("fallback content = %q, want %q", got, "proxy-haar\n")
	}
}

func TestMCPManagerExpandsEnvValues(t *testing.T) {
	logDir := t.TempDir()
	outDir := t.TempDir()
	// The process echoes an env var (expanded from {session_id}) to a file.
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{
				Name:    "tmpl",
				Command: "sh",
				Args:    []string{"-c", "echo \"$PROFILE\" > " + outDir + "/env.txt; cat"},
				Env:     map[string]string{"PROFILE": "chrome-{session_id}"},
				Sandbox: boolPtr(false),
			},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	if _, err := mgr.Connect("tmpl", "proxy-canny", config.TemplateVars{SessionID: "bonnie-7"}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	wantPath := outDir + "/env.txt"

	var data []byte

	for range 50 {
		if d, err := os.ReadFile(wantPath); err == nil && len(d) > 0 {
			data = d
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	mgr.Disconnect("proxy-canny", nil)

	if got := string(data); got != "chrome-bonnie-7\n" {
		t.Errorf("expanded env = %q, want %q", got, "chrome-bonnie-7\n")
	}
}

func TestMCPManagerExpandsCommand(t *testing.T) {
	logDir := t.TempDir()
	// {session_id} in the command name itself must expand. We symlink a known
	// binary to a session-templated name and invoke it through the template.
	binDir := t.TempDir()
	if err := os.Symlink("/bin/echo", binDir+"/braw-x"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "tmpl", Command: binDir + "/braw-{session_id}", Args: []string{"ok"}, Sandbox: boolPtr(false)},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	// braw-x exists; braw-{session_id} with SessionID "x" resolves to it.
	proc, err := mgr.Connect("tmpl", "proxy-1", config.TemplateVars{SessionID: "x"})
	if err != nil {
		t.Fatalf("Connect() with expanded command error = %v", err)
	}

	_ = proc

	mgr.Disconnect("proxy-1", nil)
}

func TestMCPManagerReloadDetectsEnvChange(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "echo", Command: "cat", Env: map[string]string{"K": "v1"}},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("echo", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Reload with only the env value changed — the running process must be
	// killed so the proxy reconnects with the new env.
	mgr.Reload(&config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "echo", Command: "cat", Env: map[string]string{"K": "v2"}},
		},
	})

	select {
	case <-proc.done:
		// killed as expected
	case <-time.After(5 * time.Second):
		t.Fatal("process should have been killed after env-only config change")
	}
}

// TestMCPManagerReloadTightensGlobalSandbox: a reload that changes only the
// global sandbox policy must restart already-running MCP servers so the
// tightened policy applies to them, not just to future connections (see #788).
func TestMCPManagerReloadTightensGlobalSandbox(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		Sandbox: config.SandboxConfig{Enabled: false},
		MCPServers: []config.MCPServerConfig{
			{Name: "canny", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("canny", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Only the global sandbox policy changes (off -> on with a backend); the
	// server command/args/env are untouched. The running process must be killed
	// so it re-launches under the tightened policy.
	mgr.Reload(&config.Config{
		Sandbox: config.SandboxConfig{Enabled: true, Backend: "nono"},
		MCPServers: []config.MCPServerConfig{
			{Name: "canny", Command: "cat"},
		},
	})

	select {
	case <-proc.done:
		// killed as expected
	case <-time.After(5 * time.Second):
		t.Fatal("process should have been killed after a global-sandbox-only change")
	}
}

// TestMCPManagerReloadTightensServerSandbox: a reload that changes only a
// per-server sandbox override must restart that server's running process.
func TestMCPManagerReloadTightensServerSandbox(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "canny", Command: "cat", Sandbox: boolPtr(false)},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("canny", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Flip the per-server sandbox override on and add a config block — only the
	// sandbox fields change. The process must be killed.
	mgr.Reload(&config.Config{
		MCPServers: []config.MCPServerConfig{
			{
				Name:          "canny",
				Command:       "cat",
				Sandbox:       boolPtr(true),
				SandboxConfig: &config.SandboxConfig{Backend: "nono", ReadDirs: []string{"/glen"}},
			},
		},
	})

	select {
	case <-proc.done:
		// killed as expected
	case <-time.After(5 * time.Second):
		t.Fatal("process should have been killed after a per-server sandbox change")
	}
}

// TestMCPManagerReloadDetectsServerSandboxConfigOnly: a reload that changes only
// a nested field of the per-server SandboxConfig (leaving the Sandbox enable flag
// untouched) must still restart the process. This isolates the SandboxConfig arm
// of mcpSandboxEqual — a comparison that only looked at the Sandbox *bool would
// pass the sibling test but fail this one.
func TestMCPManagerReloadDetectsServerSandboxConfigOnly(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{
				Name:          "canny",
				Command:       "cat",
				Sandbox:       boolPtr(false),
				SandboxConfig: &config.SandboxConfig{Backend: "nono", ReadDirs: []string{"/glen"}},
			},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("canny", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Only a nested SandboxConfig field changes (add a write dir); the Sandbox
	// enable flag is identical. The process must still be killed.
	mgr.Reload(&config.Config{
		MCPServers: []config.MCPServerConfig{
			{
				Name:          "canny",
				Command:       "cat",
				Sandbox:       boolPtr(false),
				SandboxConfig: &config.SandboxConfig{Backend: "nono", ReadDirs: []string{"/glen"}, WriteDirs: []string{"/brae"}},
			},
		},
	})

	select {
	case <-proc.done:
		// killed as expected
	case <-time.After(5 * time.Second):
		t.Fatal("process should have been killed after a per-server SandboxConfig-only change")
	}
}

// TestMCPManagerReloadUnchangedSandboxKeepsProcess: reloading with an identical
// config (sandbox included) must NOT restart a running process — the #788 fix
// must not over-kill on no-op reloads.
func TestMCPManagerReloadUnchangedSandboxKeepsProcess(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		Sandbox: config.SandboxConfig{Enabled: true, Backend: "nono", ReadDirs: []string{"/glen"}},
		MCPServers: []config.MCPServerConfig{
			{Name: "bide", Command: "cat", Sandbox: boolPtr(false)},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("bide", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Reload with a deep-equal copy of the same config — nothing changed.
	mgr.Reload(&config.Config{
		Sandbox: config.SandboxConfig{Enabled: true, Backend: "nono", ReadDirs: []string{"/glen"}},
		MCPServers: []config.MCPServerConfig{
			{Name: "bide", Command: "cat", Sandbox: boolPtr(false)},
		},
	})

	select {
	case <-proc.done:
		t.Fatal("process should NOT be killed when the config is unchanged")
	case <-time.After(500 * time.Millisecond):
		// still running as expected
	}
}

func TestMCPManagerUnknownTemplateVarFails(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "thrawn", Command: "cat", Args: []string{"--dir={nonsense}"}},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	if _, err := mgr.Connect("thrawn", "proxy-1", config.TemplateVars{SessionID: "x"}); err == nil {
		t.Fatal("expected error for unknown template variable")
	}
}

func TestMCPManagerHasServer(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "enabled", Command: "cat"},
			{Name: "disabled", Command: "cat", Disabled: true},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	if !mgr.HasServer("enabled") {
		t.Error("should have 'enabled'")
	}

	if mgr.HasServer("disabled") {
		t.Error("should not have 'disabled'")
	}
}

func TestMCPManagerExtraServers(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "configured", Command: "cat"},
		},
	}
	extra := []config.MCPServerConfig{
		{Name: "injected", Command: "cat"},
	}

	mgr := NewMCPManager(cfg, extra, logDir, slog.Default())
	defer mgr.Shutdown()

	if !mgr.HasServer("configured") {
		t.Error("should have 'configured' from config")
	}

	if !mgr.HasServer("injected") {
		t.Error("should have 'injected' from extra servers")
	}

	// Extra servers should survive reload.
	mgr.Reload(&config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "newserver", Command: "cat"},
		},
	})

	if !mgr.HasServer("injected") {
		t.Error("should still have 'injected' after reload")
	}

	if !mgr.HasServer("newserver") {
		t.Error("should have 'newserver' after reload")
	}

	if mgr.HasServer("configured") {
		t.Error("should not have 'configured' after reload")
	}

	// User config can disable an extra server.
	mgr.Reload(&config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "injected", Command: "cat", Disabled: true},
		},
	})

	if mgr.HasServer("injected") {
		t.Error("should not have 'injected' after user disables it")
	}
}

func TestMCPManagerDeletedCwd(t *testing.T) {
	// Simulate the daemon's cwd being a deleted worktree. The MCP server
	// process must still start because startProcess sets cmd.Dir explicitly.
	logDir := t.TempDir()
	doomedDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(doomedDir); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(doomedDir); err != nil {
		t.Fatal(err)
	}

	defer func() { _ = os.Chdir(origDir) }()

	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "cwd-bothy", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	proc, err := mgr.Connect("cwd-bothy", "proxy-haar", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	select {
	case <-proc.done:
		t.Fatal("process should still be running despite deleted cwd")
	case <-time.After(500 * time.Millisecond):
	}

	mgr.Disconnect("proxy-haar", nil)

	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process should be done after disconnect")
	}
}

func TestMCPManagerList(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "braw", Command: "cat"},
			{Name: "canny", Command: "cat", Sandbox: boolPtr(false)},
		},
	}
	extra := []config.MCPServerConfig{
		{Name: "graith", Command: "cat"},
	}

	mgr := NewMCPManager(cfg, extra, logDir, slog.Default())
	defer mgr.Shutdown()

	if _, err := mgr.Connect("braw", "sess1-braw", config.TemplateVars{}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	servers := mgr.List()
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}

	// Sorted by name: braw, canny, graith.
	byName := make(map[string]int)
	for i, s := range servers {
		byName[s.Name] = i
	}

	braw := servers[byName["braw"]]
	if len(braw.Connections) != 1 {
		t.Errorf("braw should have 1 connection, got %d", len(braw.Connections))
	}

	// Global sandbox is off, so even a per-server-default server is not
	// effectively sandboxed.
	if braw.Sandboxed {
		t.Error("braw should report sandboxed=false when the global sandbox is off")
	}

	if braw.Connections[0].PID == 0 || !braw.Connections[0].Running {
		t.Errorf("braw connection should be running with a PID, got %+v", braw.Connections[0])
	}

	canny := servers[byName["canny"]]
	if canny.Sandboxed {
		t.Error("canny has Sandbox=false, should report sandboxed=false")
	}

	if len(canny.Connections) != 0 {
		t.Errorf("canny should have no connections, got %d", len(canny.Connections))
	}

	graith := servers[byName["graith"]]
	if !graith.AutoInjected {
		t.Error("graith is an extra server, should be flagged auto_injected")
	}
}

// TestMCPManagerListEffectiveSandbox pins that List reports the *effective*
// sandbox state (global enabled AND per-server enabled), not the raw per-server
// config intent. List does not start processes, so an unavailable backend does
// not matter here.
func TestMCPManagerListEffectiveSandbox(t *testing.T) {
	base := []config.MCPServerConfig{
		{Name: "braw", Command: "cat"},                           // per-server default (true)
		{Name: "canny", Command: "cat", Sandbox: boolPtr(false)}, // per-server opt-out
	}

	// Global sandbox enabled: braw effective true, canny effective false.
	on := NewMCPManager(&config.Config{
		Sandbox:    config.SandboxConfig{Enabled: true, Backend: "nono"},
		MCPServers: base,
	}, nil, t.TempDir(), slog.Default())
	defer on.Shutdown()

	got := make(map[string]bool)
	for _, s := range on.List() {
		got[s.Name] = s.Sandboxed
	}

	if !got["braw"] {
		t.Error("braw should be sandboxed when global sandbox is on and per-server defaults to true")
	}

	if got["canny"] {
		t.Error("canny opts out per-server, should be sandboxed=false even with global on")
	}

	// Global sandbox off: nothing is effectively sandboxed.
	off := NewMCPManager(&config.Config{MCPServers: base}, nil, t.TempDir(), slog.Default())
	defer off.Shutdown()

	for _, s := range off.List() {
		if s.Sandboxed {
			t.Errorf("%s should be sandboxed=false when the global sandbox is off", s.Name)
		}
	}
}

// TestMCPManagerDisconnectIdentity pins that an identity-checked Disconnect does
// not kill a replacement process that reused the same proxy ID — the ABA case
// that arises when Restart kills a process and the proxy reconnects before the
// old connection's deferred cleanup runs.
func TestMCPManagerDisconnectIdentity(t *testing.T) {
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{{Name: "bide", Command: "cat"}},
	}

	mgr := NewMCPManager(cfg, nil, t.TempDir(), slog.Default())
	defer mgr.Shutdown()

	p1, err := mgr.Connect("bide", "sess1-bide", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect(p1) error = %v", err)
	}

	// Simulate a restart of p1, then a reconnect installing p2 under the same ID.
	if _, err := mgr.Restart("bide"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	p2, err := mgr.Connect("bide", "sess1-bide", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect(p2) error = %v", err)
	}

	// The old connection's deferred cleanup fires with p1's identity: it must
	// NOT touch p2, which now owns the proxy ID.
	mgr.Disconnect("sess1-bide", p1)

	select {
	case <-p2.done:
		t.Fatal("p2 must survive a stale Disconnect keyed on p1's identity")
	case <-time.After(300 * time.Millisecond):
	}

	// A correctly-identified Disconnect does stop p2.
	mgr.Disconnect("sess1-bide", p2)

	select {
	case <-p2.done:
	case <-time.After(5 * time.Second):
		t.Fatal("p2 should stop when Disconnect is keyed on its own identity")
	}

	_ = p1
}

// TestMCPManagerLogFilesRemovedCollidingServer pins that log attribution is
// config-independent: a shorter-named server does not pick up a longer-named
// sibling's historical logs even after that sibling is removed from config.
func TestMCPManagerLogFilesRemovedCollidingServer(t *testing.T) {
	logDir := t.TempDir()
	// Only "graith" is configured now; "graith-x" was removed.
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{{Name: "graith", Command: "cat"}},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	mcpDir := filepath.Join(logDir, "mcp")
	if err := os.MkdirAll(mcpDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Historical log from the removed "graith-x" server, named per the daemon's
	// "<server>-<sessionID>-<server>.log" convention.
	if err := os.WriteFile(filepath.Join(mcpDir, "graith-x-abc123-graith-x.log"), []byte("removed sibling\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(mcpDir, "graith-abc123-graith.log"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := mgr.LogFiles("graith", 0)
	if err != nil {
		t.Fatalf("LogFiles() error = %v", err)
	}

	if len(files) != 1 || !strings.Contains(files[0].Content, "mine") {
		t.Fatalf("graith should not pick up removed graith-x logs, got %+v", files)
	}
}

func TestTailFileLargeDropsPartialFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")

	// Build a file well over 1 MiB. Each line is uniquely numbered so we can
	// assert the first returned line is whole (not a mid-line fragment).
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	line := strings.Repeat("x", 200)
	for i := 0; i < 20000; i++ {
		if _, err := fmt.Fprintf(f, "%06d-%s\n", i, line); err != nil {
			t.Fatal(err)
		}
	}

	_ = f.Close()

	got, err := tailFile(path, 5, 0)
	if err != nil {
		t.Fatalf("tailFile() error = %v", err)
	}

	first := strings.SplitN(got, "\n", 2)[0]
	// A whole line is exactly "NNNNNN-" followed by 200 x's = 207 chars.
	if len(first) != 207 || !strings.HasSuffix(first, line) {
		t.Errorf("first returned line looks truncated: len=%d %q", len(first), first)
	}

	if strings.Count(strings.TrimRight(got, "\n"), "\n")+1 != 5 {
		t.Errorf("expected 5 lines, got %q", got)
	}
}

func TestMCPManagerRestart(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "thrawn", Command: "cat"},
			{Name: "bide", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	p1, err := mgr.Connect("thrawn", "sess1-thrawn", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect(sess1) error = %v", err)
	}

	p2, err := mgr.Connect("thrawn", "sess2-thrawn", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect(sess2) error = %v", err)
	}

	other, err := mgr.Connect("bide", "sess1-bide", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect(bide) error = %v", err)
	}

	stopped, err := mgr.Restart("thrawn")
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if stopped != 2 {
		t.Errorf("expected 2 thrawn processes stopped, got %d", stopped)
	}

	for _, p := range []*MCPProcess{p1, p2} {
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Fatal("restarted process should be stopped")
		}
	}

	// The other server's process must be untouched.
	select {
	case <-other.done:
		t.Fatal("bide process should not be affected by restarting thrawn")
	case <-time.After(200 * time.Millisecond):
	}

	// Restarting again with no live processes reports zero.
	stopped, err = mgr.Restart("thrawn")
	if err != nil {
		t.Fatalf("second Restart() error = %v", err)
	}

	if stopped != 0 {
		t.Errorf("expected 0 stopped on second restart, got %d", stopped)
	}
}

func TestMCPManagerRestartUnknown(t *testing.T) {
	mgr := NewMCPManager(&config.Config{}, nil, t.TempDir(), slog.Default())
	defer mgr.Shutdown()

	if _, err := mgr.Restart("dreich"); err == nil {
		t.Fatal("expected error restarting an unknown server")
	}
}

func TestMCPManagerLogFiles(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "ken", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	mcpDir := filepath.Join(logDir, "mcp")
	if err := os.MkdirAll(mcpDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(mcpDir, "ken-sess1-ken.log"), []byte("speir line 1\nspeir line 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := mgr.LogFiles("ken", 0)
	if err != nil {
		t.Fatalf("LogFiles() error = %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 log file, got %d", len(files))
	}

	if files[0].ProxyID != "sess1-ken" {
		t.Errorf("ProxyID = %q, want %q", files[0].ProxyID, "sess1-ken")
	}

	if !strings.Contains(files[0].Content, "speir line 2") {
		t.Errorf("content missing expected line: %q", files[0].Content)
	}
}

// TestMCPManagerLogFilesUsesConfiguredDefault guards issue #1252: when a caller
// asks for lines <= 0, LogFiles falls back to the configured [limits] log_lines
// default (was a hard-coded 300), so raising or lowering the config changes how
// much the MCP log reader returns.
func TestMCPManagerLogFilesUsesConfiguredDefault(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{{Name: "ken", Command: "cat"}},
	}
	cfg.Limits.LogLines = 2 // custom small default

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	mcpDir := filepath.Join(logDir, "mcp")
	if err := os.MkdirAll(mcpDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Five lines on disk; the configured default (2) should bound the read.
	body := "l1\nl2\nl3\nl4\nl5\n"
	if err := os.WriteFile(filepath.Join(mcpDir, "ken-sess1-ken.log"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := mgr.LogFiles("ken", 0)
	if err != nil {
		t.Fatalf("LogFiles() error = %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 log file, got %d", len(files))
	}

	got := strings.Count(strings.TrimRight(files[0].Content, "\n"), "\n") + 1
	if got != 2 {
		t.Errorf("LogFiles(lines=0) returned %d lines, want configured default 2 (%q)", got, files[0].Content)
	}
}

// TestMCPManagerLogFilesPrefixCollision: a server whose name is a prefix of
// another's ("graith" vs "graith-x") must not pick up the other's logs.
func TestMCPManagerLogFilesPrefixCollision(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{Name: "graith", Command: "cat"},
			{Name: "graith-x", Command: "cat"},
		},
	}

	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	mcpDir := filepath.Join(logDir, "mcp")
	if err := os.MkdirAll(mcpDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(mcpDir, "graith-sess1-graith.log"), []byte("plain graith\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(mcpDir, "graith-x-sess1-graith-x.log"), []byte("graith x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := mgr.LogFiles("graith", 0)
	if err != nil {
		t.Fatalf("LogFiles() error = %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected only graith's own log, got %d files: %+v", len(files), files)
	}

	if !strings.Contains(files[0].Content, "plain graith") {
		t.Errorf("wrong file matched: %q", files[0].Content)
	}

	xfiles, err := mgr.LogFiles("graith-x", 0)
	if err != nil {
		t.Fatalf("LogFiles(graith-x) error = %v", err)
	}

	if len(xfiles) != 1 || !strings.Contains(xfiles[0].Content, "graith x") {
		t.Fatalf("graith-x should match its own log, got %+v", xfiles)
	}
}

func TestMCPManagerLogFilesUnknown(t *testing.T) {
	mgr := NewMCPManager(&config.Config{}, nil, t.TempDir(), slog.Default())
	defer mgr.Shutdown()

	if _, err := mgr.LogFiles("fash", 0); err == nil {
		t.Fatal("expected error for unknown server")
	}
}

func TestMCPManagerLogFilesNoDir(t *testing.T) {
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{{Name: "haar", Command: "cat"}},
	}

	mgr := NewMCPManager(cfg, nil, t.TempDir(), slog.Default())
	defer mgr.Shutdown()

	// No process has ever run, so the mcp log dir does not exist yet.
	files, err := mgr.LogFiles("haar", 0)
	if err != nil {
		t.Fatalf("LogFiles() with no log dir should not error, got %v", err)
	}

	if len(files) != 0 {
		t.Errorf("expected no files, got %d", len(files))
	}
}

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loch.log")

	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := tailFile(path, 3, 0)
	if err != nil {
		t.Fatalf("tailFile() error = %v", err)
	}

	want := "line 8\nline 9\nline 10\n"
	if got != want {
		t.Errorf("tailFile(3) = %q, want %q", got, want)
	}

	// Asking for more lines than exist returns everything.
	all, err := tailFile(path, 100, 0)
	if err != nil {
		t.Fatalf("tailFile(100) error = %v", err)
	}

	if strings.Count(all, "\n") != 10 {
		t.Errorf("expected 10 lines, got %d", strings.Count(all, "\n"))
	}
}

// TestTailFileMaxReadBound guards issue #1252: a small maxRead caps how many
// trailing bytes are read, so lines that fall outside that window are dropped
// even when more are requested. The cap is now a parameter (was a fixed 1 MiB
// local const).
func TestTailFileMaxReadBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loch.log")

	// Ten 8-byte lines ("line NN\n"); 80 bytes total.
	var sb strings.Builder
	for i := 10; i <= 19; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// A 20-byte read window can hold at most the final ~2 whole lines even though
	// 100 are requested; the partial leading fragment is dropped.
	got, err := tailFile(path, 100, 20)
	if err != nil {
		t.Fatalf("tailFile() error = %v", err)
	}

	lines := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1
	if lines >= 10 {
		t.Errorf("maxRead=20 returned %d lines (%q), want the read window to drop earlier lines", lines, got)
	}

	if !strings.Contains(got, "line 19") {
		t.Errorf("final line missing from bounded read: %q", got)
	}

	// A non-positive maxRead falls back to the config default (reads everything
	// for this tiny file).
	all, err := tailFile(path, 100, 0)
	if err != nil {
		t.Fatalf("tailFile(maxRead=0) error = %v", err)
	}

	if strings.Count(all, "\n") != 10 {
		t.Errorf("maxRead=0 (default) returned %d lines, want all 10", strings.Count(all, "\n"))
	}
}

func TestTailFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.log")

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := tailFile(path, 10, 0)
	if err != nil {
		t.Fatalf("tailFile() error = %v", err)
	}

	if got != "" {
		t.Errorf("empty file should tail to empty string, got %q", got)
	}
}
