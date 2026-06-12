package daemon

import (
	"log/slog"
	"os"
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

	proc, err := mgr.Connect("echo", "proxy-1")
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

func TestMCPManagerConnectUnknownServer(t *testing.T) {
	logDir := t.TempDir()
	cfg := &config.Config{}
	mgr := NewMCPManager(cfg, nil, logDir, slog.Default())
	defer mgr.Shutdown()

	_, err := mgr.Connect("nonexistent", "proxy-1")
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

	_, err := mgr.Connect("echo", "proxy-1")
	if err != nil {
		t.Fatalf("first Connect() error = %v", err)
	}

	_, err = mgr.Connect("echo", "proxy-1")
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

	proc, err := mgr.Connect("echo", "proxy-1")
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

	proc, err := mgr.Connect("stderr-test", "proxy-1")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Wait for the echo to stderr to be written and flushed.
	_ = proc
	// Give the sh process time to write stderr output before disconnect.
	for range 20 {
		logPath := logDir + "/mcp/stderr-test-proxy-1.log"
		if data, err := os.ReadFile(logPath); err == nil && len(data) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mgr.Disconnect("proxy-1")

	logPath := logDir + "/mcp/stderr-test-proxy-1.log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read stderr log: %v", err)
	}
	if len(data) == 0 {
		t.Error("stderr log should have content")
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
}
