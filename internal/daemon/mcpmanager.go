package daemon

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/sandbox"
)

// MCPProcess represents a running MCP server process for a single proxy connection.
type MCPProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *os.File
	done   chan struct{}
}

// MCPManager manages MCP server processes. Each proxy connection gets its own
// dedicated MCP server process, started lazily on connect.
type MCPManager struct {
	mu        sync.Mutex
	servers   map[string]config.MCPServerConfig // server name -> config
	processes map[string]*MCPProcess            // proxyID -> process
	extraSvrs []config.MCPServerConfig          // auto-injected servers (e.g. graith)
	logDir    string
	globalSbx config.SandboxConfig
	log       *slog.Logger
}

// NewMCPManager creates an MCPManager. extraServers are auto-injected servers
// (like the graith MCP server) that aren't in the config file but must be
// available for proxy connections.
func NewMCPManager(cfg *config.Config, extraServers []config.MCPServerConfig, logDir string, log *slog.Logger) *MCPManager {
	servers := make(map[string]config.MCPServerConfig, len(cfg.MCPServers)+len(extraServers))
	for _, s := range extraServers {
		if !s.Disabled {
			servers[s.Name] = s
		}
	}
	for _, s := range cfg.MCPServers {
		if !s.Disabled {
			servers[s.Name] = s
		}
	}
	return &MCPManager{
		servers:   servers,
		processes: make(map[string]*MCPProcess),
		extraSvrs: extraServers,
		logDir:    logDir,
		globalSbx: cfg.Sandbox,
		log:       log,
	}
}

// Connect starts a new MCP server process for the given proxy and returns
// the process's stdin (for writing JSON-RPC) and stdout reader (for reading
// JSON-RPC). The caller owns the I/O loop.
func (m *MCPManager) Connect(serverName, proxyID string) (*MCPProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	serverCfg, ok := m.servers[serverName]
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", serverName)
	}

	if _, exists := m.processes[proxyID]; exists {
		return nil, fmt.Errorf("proxy %q already connected", proxyID)
	}

	proc, err := m.startProcess(serverCfg, proxyID)
	if err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", serverName, err)
	}

	m.processes[proxyID] = proc
	m.log.Info("MCP server started", "server", serverName, "proxy_id", proxyID, "pid", proc.cmd.Process.Pid)
	return proc, nil
}

// Disconnect kills the MCP server process for the given proxy.
func (m *MCPManager) Disconnect(proxyID string) {
	m.mu.Lock()
	proc, ok := m.processes[proxyID]
	if ok {
		delete(m.processes, proxyID)
	}
	m.mu.Unlock()

	if ok {
		m.killProcess(proc)
		m.log.Info("MCP server stopped", "proxy_id", proxyID)
	}
}

// Reload updates the server config. Running processes for removed or changed
// servers are killed so proxies reconnect with the new config.
func (m *MCPManager) Reload(cfg *config.Config) {
	m.mu.Lock()

	newServers := make(map[string]config.MCPServerConfig, len(cfg.MCPServers)+len(m.extraSvrs))
	for _, s := range m.extraSvrs {
		if !s.Disabled {
			newServers[s.Name] = s
		}
	}
	for _, s := range cfg.MCPServers {
		if !s.Disabled {
			newServers[s.Name] = s
		}
	}

	var toKill []string
	configChanged := len(newServers) != len(m.servers)
	if !configChanged {
		for name, newCfg := range newServers {
			oldCfg, ok := m.servers[name]
			if !ok || oldCfg.Command != newCfg.Command || !slicesEqual(oldCfg.Args, newCfg.Args) {
				configChanged = true
				break
			}
		}
	}

	if configChanged {
		for proxyID := range m.processes {
			toKill = append(toKill, proxyID)
		}
	}

	killed := make(map[string]*MCPProcess, len(toKill))
	for _, proxyID := range toKill {
		killed[proxyID] = m.processes[proxyID]
		delete(m.processes, proxyID)
	}

	m.servers = newServers
	m.globalSbx = cfg.Sandbox
	m.mu.Unlock()

	for proxyID, proc := range killed {
		m.killProcess(proc)
		m.log.Info("MCP server stopped (config reload)", "proxy_id", proxyID)
	}
}

// Shutdown kills all running MCP server processes.
func (m *MCPManager) Shutdown() {
	m.mu.Lock()
	procs := make(map[string]*MCPProcess, len(m.processes))
	for k, v := range m.processes {
		procs[k] = v
	}
	m.processes = make(map[string]*MCPProcess)
	m.mu.Unlock()

	for proxyID, proc := range procs {
		m.killProcess(proc)
		m.log.Info("MCP server stopped (shutdown)", "proxy_id", proxyID)
	}
}

// HasServer returns true if the named MCP server is configured.
func (m *MCPManager) HasServer(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.servers[name]
	return ok
}

func (m *MCPManager) startProcess(serverCfg config.MCPServerConfig, proxyID string) (*MCPProcess, error) {
	mcpLogDir := filepath.Join(m.logDir, "mcp")
	if err := os.MkdirAll(mcpLogDir, 0o700); err != nil {
		return nil, fmt.Errorf("create MCP log dir: %w", err)
	}

	stderrPath := filepath.Join(mcpLogDir, fmt.Sprintf("%s-%s.log", serverCfg.Name, proxyID))
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open stderr log: %w", err)
	}

	command := serverCfg.Command
	args := serverCfg.Args

	sbxEnabled := true
	if serverCfg.Sandbox != nil {
		sbxEnabled = *serverCfg.Sandbox
	}
	if sbxEnabled && sandbox.Available() {
		merged := m.globalSbx
		if serverCfg.SandboxConfig != nil {
			merged = merged.Merge(*serverCfg.SandboxConfig)
		}
		merged.ReadDirs = expandPaths(merged.ReadDirs)
		merged.WriteDirs = expandPaths(merged.WriteDirs)

		envKeys := make([]string, 0, len(serverCfg.Env)+1)
		envKeys = append(envKeys, "PATH", "HOME", "TERM")
		for k := range serverCfg.Env {
			envKeys = append(envKeys, k)
		}

		opts := sandbox.WrapOpts{
			WorktreeDir:      os.TempDir(),
			ReadDirs:         merged.ReadDirs,
			WriteDirs:        merged.WriteDirs,
			Features:         merged.Features,
			EnvKeys:          envKeys,
			SafehouseCommand: merged.Command,
		}
		command, args = sandbox.Wrap(serverCfg.Command, serverCfg.Args, opts)
	}

	cmd := exec.Command(command, args...)
	cmd.Stderr = stderrFile
	if len(serverCfg.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range serverCfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		stderrFile.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		stderrFile.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		stderrFile.Close()
		return nil, fmt.Errorf("start process: %w", err)
	}

	proc := &MCPProcess{
		cmd:    cmd,
		stdin:  stdinPipe,
		stdout: bufio.NewReader(stdoutPipe),
		stderr: stderrFile,
		done:   make(chan struct{}),
	}

	go func() {
		cmd.Wait()
		stderrFile.Close()
		close(proc.done)
	}()

	return proc, nil
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *MCPManager) killProcess(proc *MCPProcess) {
	proc.stdin.Close()

	if proc.cmd.Process != nil {
		proc.cmd.Process.Signal(os.Interrupt)
		select {
		case <-proc.done:
		case <-time.After(5 * time.Second):
			proc.cmd.Process.Kill()
			<-proc.done
		}
	}
}
