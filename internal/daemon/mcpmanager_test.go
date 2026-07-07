package daemon

import (
	"log/slog"
	"os"
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

	mgr.Disconnect("proxy-1")

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

	proc, err := mgr.Connect("canny", "proxy-1", config.TemplateVars{})
	if err != nil {
		t.Fatalf("Connect() with sandbox disabled should not require a backend, got: %v", err)
	}

	mgr.Disconnect("proxy-1")
	_ = proc
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

	mgr.Disconnect("proxy-1")

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

	mgr.Disconnect("proxy-braw")

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

	mgr.Disconnect("proxy-haar")

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

	mgr.Disconnect("proxy-canny")

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

	mgr.Disconnect("proxy-1")
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

	mgr.Disconnect("proxy-haar")

	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process should be done after disconnect")
	}
}
