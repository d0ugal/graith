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
		if s.Disabled {
			delete(servers, s.Name)
		} else {
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
// JSON-RPC). The caller owns the I/O loop. vars supplies the per-session
// template values ({session_id}, {session_name}, {worktree_path}) expanded in
// the server's args and env, giving each session an isolated process — e.g. a
// per-session Chrome profile dir for chrome-devtools-mcp.
func (m *MCPManager) Connect(serverName, proxyID string, vars config.TemplateVars) (*MCPProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	serverCfg, ok := m.servers[serverName]
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", serverName)
	}

	if _, exists := m.processes[proxyID]; exists {
		return nil, fmt.Errorf("proxy %q already connected", proxyID)
	}

	proc, err := m.startProcess(serverCfg, proxyID, vars)
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
		if s.Disabled {
			delete(newServers, s.Name)
		} else {
			newServers[s.Name] = s
		}
	}

	var toKill []string

	configChanged := len(newServers) != len(m.servers)
	if !configChanged {
		for name, newCfg := range newServers {
			oldCfg, ok := m.servers[name]
			if !ok || oldCfg.Command != newCfg.Command || !slicesEqual(oldCfg.Args, newCfg.Args) || !mapsEqual(oldCfg.Env, newCfg.Env) {
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

func (m *MCPManager) startProcess(serverCfg config.MCPServerConfig, proxyID string, vars config.TemplateVars) (*MCPProcess, error) {
	mcpLogDir := filepath.Join(m.logDir, "mcp")
	if err := os.MkdirAll(mcpLogDir, 0o700); err != nil {
		return nil, fmt.Errorf("create MCP log dir: %w", err)
	}

	stderrPath := filepath.Join(mcpLogDir, fmt.Sprintf("%s-%s.log", serverCfg.Name, proxyID))

	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open stderr log: %w", err)
	}

	// Expand per-session template vars in command, args, and env so each
	// session gets an isolated process (e.g. a unique Chrome profile dir).
	// Fall back to proxyID when there's no session so {session_id} never
	// collapses to a shared empty value.
	if vars.SessionID == "" {
		vars.SessionID = proxyID
	}

	command, err := config.Expand(serverCfg.Command, vars)
	if err != nil {
		_ = stderrFile.Close()
		return nil, fmt.Errorf("expand command for MCP server %s: %w", serverCfg.Name, err)
	}

	args, err := config.ExpandSlice(serverCfg.Args, vars)
	if err != nil {
		_ = stderrFile.Close()
		return nil, fmt.Errorf("expand args for MCP server %s: %w", serverCfg.Name, err)
	}

	serverEnv := serverCfg.Env
	if len(serverEnv) > 0 {
		expanded := make(map[string]string, len(serverEnv))
		for k, v := range serverEnv {
			ev, expErr := config.Expand(v, vars)
			if expErr != nil {
				_ = stderrFile.Close()
				return nil, fmt.Errorf("expand env %s for MCP server %s: %w", k, serverCfg.Name, expErr)
			}

			expanded[k] = ev
		}

		serverEnv = expanded
	}

	sbxEnabled := true
	if serverCfg.Sandbox != nil {
		sbxEnabled = *serverCfg.Sandbox
	}

	if sbxEnabled && m.globalSbx.Enabled {
		merged := m.globalSbx
		if serverCfg.SandboxConfig != nil {
			merged = merged.Merge(*serverCfg.SandboxConfig)
		}

		// Honour the configured backend (not just safehouse). Fail closed if it
		// cannot enforce, matching the session sandbox semantics.
		req := sandbox.Requirements{Network: merged.Network.IsSet()}

		avail, err := sandbox.CheckAvailability(merged.Backend, merged.Command, req)
		if err != nil {
			_ = stderrFile.Close()
			return nil, fmt.Errorf("sandbox for MCP server %s: %w", serverCfg.Name, err)
		}

		if !avail.CanEnforce {
			_ = stderrFile.Close()
			return nil, fmt.Errorf("sandbox enabled for MCP server %s with backend %q but it cannot enforce: %s", serverCfg.Name, merged.Backend, avail.Detail)
		}

		merged.ReadDirs = expandPaths(merged.ReadDirs, m.log, "read")
		merged.WriteDirs = expandPaths(merged.WriteDirs, m.log, "write")
		merged.ReadFiles = expandFilePaths(merged.ReadFiles, m.log, "read")
		merged.WriteFiles = expandFilePaths(merged.WriteFiles, m.log, "write")

		envKeys := make([]string, 0, len(serverEnv)+1)

		envKeys = append(envKeys, "PATH", "HOME", "TERM")
		for k := range serverEnv {
			envKeys = append(envKeys, k)
		}

		opts := sandbox.WrapOpts{
			Backend:        merged.Backend,
			WorktreeDir:    os.TempDir(),
			ReadDirs:       merged.ReadDirs,
			WriteDirs:      merged.WriteDirs,
			ReadFiles:      merged.ReadFiles,
			WriteFiles:     merged.WriteFiles,
			Features:       merged.Features,
			EnvKeys:        envKeys,
			SignalMode:     merged.SignalMode,
			Profile:        merged.Profile,
			Network:        networkPolicy(merged.Network),
			BackendCommand: merged.Command,
			// No session ID here; nono writes a temp profile (empty ProfilePath).
		}

		var wrapErr error

		command, args, wrapErr = sandbox.Wrap(command, args, opts)
		if wrapErr != nil {
			_ = stderrFile.Close()
			return nil, fmt.Errorf("sandbox wrap for MCP server %s: %w", serverCfg.Name, wrapErr)
		}
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = os.TempDir()

	cmd.Stderr = stderrFile
	if len(serverEnv) > 0 {
		cmd.Env = os.Environ()
		for k, v := range serverEnv {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		_ = stderrFile.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		_ = stderrFile.Close()

		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stderrFile.Close()

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
		_ = cmd.Wait()
		_ = stderrFile.Close()

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

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}

	return true
}

func (m *MCPManager) killProcess(proc *MCPProcess) {
	_ = proc.stdin.Close()

	if proc.cmd.Process != nil {
		_ = proc.cmd.Process.Signal(os.Interrupt)

		select {
		case <-proc.done:
		case <-time.After(5 * time.Second):
			_ = proc.cmd.Process.Kill()
			<-proc.done
		}
	}
}
