package daemon

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
)

// MCPProcess represents a running MCP server process for a single proxy connection.
type MCPProcess struct {
	serverName string
	proxyID    string
	startedAt  time.Time
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderr     *os.File
	done       chan struct{}
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
	limits    config.LimitsConfig // output/log display caps (issue #1252)
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
		limits:    cfg.Limits,
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

// Disconnect kills the MCP server process for the given proxy. It is
// identity-checked against proc: it only removes and kills the process still
// registered under proxyID if it is the *same* process the caller owns. This
// prevents an ABA race where Restart/Reload kills a process, the proxy
// reconnects and Connect installs a replacement under the same deterministic
// proxyID, and this (stale) deferred cleanup would otherwise kill the fresh
// replacement.
func (m *MCPManager) Disconnect(proxyID string, proc *MCPProcess) {
	m.mu.Lock()

	current, ok := m.processes[proxyID]
	if ok && (proc == nil || current == proc) {
		delete(m.processes, proxyID)
	} else {
		ok = false
	}

	m.mu.Unlock()

	if ok {
		m.killProcess(current)
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

	// A change to the global sandbox policy (enabling it, switching backend,
	// adding read/write grants, network block, signal mode, …) must restart
	// every running MCP server so a *tightened* policy actually applies to
	// already-running processes — the launch path reads sandbox config only at
	// start (see #788).
	configChanged := len(newServers) != len(m.servers) || !reflect.DeepEqual(m.globalSbx, cfg.Sandbox)
	if !configChanged {
		for name, newCfg := range newServers {
			oldCfg, ok := m.servers[name]
			if !ok || oldCfg.Command != newCfg.Command || !slicesEqual(oldCfg.Args, newCfg.Args) || !mapsEqual(oldCfg.Env, newCfg.Env) || !mcpSandboxEqual(oldCfg, newCfg) {
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
	m.limits = cfg.Limits
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

// isRunning reports whether the process has not yet exited.
func (p *MCPProcess) isRunning() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// List returns the status of every configured MCP server, including any live
// proxy processes. Auto-injected servers (e.g. graith) are flagged.
func (m *MCPManager) List() []protocol.MCPServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	autoInjected := make(map[string]bool, len(m.extraSvrs))
	for _, s := range m.extraSvrs {
		autoInjected[s.Name] = true
	}

	// Group live processes by server name.
	byServer := make(map[string][]protocol.MCPConnectionInfo)

	for _, proc := range m.processes {
		pid := 0
		if proc.cmd.Process != nil {
			pid = proc.cmd.Process.Pid
		}

		uptime := time.Since(proc.startedAt).Round(time.Second)
		byServer[proc.serverName] = append(byServer[proc.serverName], protocol.MCPConnectionInfo{
			ProxyID:   proc.proxyID,
			PID:       pid,
			Running:   proc.isRunning(),
			Uptime:    uptime.String(),
			UptimeSec: int(uptime.Seconds()),
		})
	}

	statuses := make([]protocol.MCPServerStatus, 0, len(m.servers))

	for name, cfg := range m.servers {
		// Report the *effective* sandbox state, not the per-server config
		// intent: a process is only confined when the global sandbox is enabled
		// AND the per-server flag is on (see startProcess, which wraps only when
		// `sbxEnabled && m.globalSbx.Enabled`). Reporting the raw per-server flag
		// would tell an operator a tool is sandboxed when the global sandbox is
		// off and it actually runs unconfined.
		perServer := true
		if cfg.Sandbox != nil {
			perServer = *cfg.Sandbox
		}

		sandboxed := m.globalSbx.Enabled && perServer

		conns := byServer[name]
		sort.Slice(conns, func(i, j int) bool { return conns[i].ProxyID < conns[j].ProxyID })

		statuses = append(statuses, protocol.MCPServerStatus{
			Name:         name,
			Sandboxed:    sandboxed,
			AutoInjected: autoInjected[name],
			Connections:  conns,
		})
	}

	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	return statuses
}

// Restart stops all running processes for the named server. Proxies detect the
// broken connection and reconnect, at which point the daemon starts fresh
// processes with the current config. It returns the number of processes
// stopped, or an error if the server is not configured.
func (m *MCPManager) Restart(name string) (int, error) {
	m.mu.Lock()

	if _, ok := m.servers[name]; !ok {
		m.mu.Unlock()
		return 0, fmt.Errorf("unknown MCP server %q", name)
	}

	killed := make([]*MCPProcess, 0)

	for proxyID, proc := range m.processes {
		if proc.serverName == name {
			killed = append(killed, proc)

			delete(m.processes, proxyID)
		}
	}

	m.mu.Unlock()

	for _, proc := range killed {
		m.killProcess(proc)
		m.log.Info("MCP server stopped (restart)", "server", name, "proxy_id", proc.proxyID)
	}

	return len(killed), nil
}

// LogFiles returns the captured stderr for the named server. It reads every
// per-proxy log file for that server (both live and historical), returning the
// last `lines` lines of each. It errors if the server is not configured.
func (m *MCPManager) LogFiles(name string, lines int) ([]protocol.MCPLogFile, error) {
	m.mu.Lock()
	_, ok := m.servers[name]
	limits := m.limits
	m.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", name)
	}

	if lines <= 0 {
		lines = limits.LogLinesOrDefault()
	}

	maxRead := int64(limits.MCPLogReadBytesOrDefault())

	mcpLogDir := filepath.Join(m.logDir, "mcp")

	entries, err := os.ReadDir(mcpLogDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("read MCP log dir: %w", err)
	}

	// startProcess names each log "<server>-<proxyID>.log", and the daemon
	// always builds proxyID as "<sessionID>-<server>" (handler.go). So a log for
	// this server both starts with "<name>-" and ends with "-<name>". Requiring
	// both is a structural, config-independent test: it disambiguates a
	// prefix-colliding sibling (e.g. "graith" vs "graith-x") and — unlike a check
	// against the current config — still attributes historical logs correctly
	// after a colliding server is removed from config.
	prefix := name + "-"
	suffix := "-" + name

	files := make([]protocol.MCPLogFile, 0)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		base := e.Name()
		if !strings.HasSuffix(base, ".log") || !strings.HasPrefix(base, prefix) {
			continue
		}

		trimmed := strings.TrimSuffix(base, ".log")
		if !strings.HasSuffix(trimmed, suffix) {
			continue
		}

		path := filepath.Join(mcpLogDir, base)

		content, rerr := tailFile(path, lines, maxRead)
		if rerr != nil {
			return nil, fmt.Errorf("read MCP log %s: %w", base, rerr)
		}

		files = append(files, protocol.MCPLogFile{
			ProxyID: strings.TrimPrefix(trimmed, prefix),
			Path:    path,
			Content: content,
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].ProxyID < files[j].ProxyID })

	return files, nil
}

// tailFile returns the last n lines of the file at path. To bound memory it
// reads at most the final maxRead bytes of the file before splitting into
// lines; a maxRead <= 0 falls back to the config default so a caller can't
// accidentally read the whole file (issue #1252).
func tailFile(path string, n int, maxRead int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}

	if maxRead <= 0 {
		maxRead = config.LimitsMCPLogReadBytesDefault
	}

	size := info.Size()
	start := int64(0)

	if size > maxRead {
		start = size - maxRead
	}

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}

	// After a non-zero seek the read almost certainly starts mid-line; drop that
	// partial leading fragment so we never present it as a whole log line.
	if start > 0 {
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
			data = data[nl+1:]
		} else {
			data = nil
		}
	}

	text := string(data)

	linesArr := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(linesArr) == 1 && linesArr[0] == "" {
		return "", nil
	}

	if len(linesArr) > n {
		linesArr = linesArr[len(linesArr)-n:]
	}

	return strings.Join(linesArr, "\n") + "\n", nil
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

		// Enforce the same explicit-backend + availability rule as agent
		// sessions (resolveSandboxFromConfig). Without this, an enabled sandbox
		// with no backend selected would silently fall back to safehouse at the
		// dispatch layer instead of failing closed like sessions do (see #787).
		avail, err := validateSandboxBackend(merged, "MCP server "+serverCfg.Name)
		if err != nil {
			_ = stderrFile.Close()
			return nil, err
		}

		if avail.Degraded {
			m.log.Warn("sandbox enforcement degraded", "server", serverCfg.Name, "backend", merged.Backend, "detail", avail.Detail)
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
			// UnixSockets is deliberately NOT set: MCP servers are tools, not
			// daemon clients, so a sandboxed MCP server intentionally cannot reach
			// the daemon socket. The only auto-injected server that talks to the
			// daemon (the `graith` server) runs unsandboxed (see hooks.go), so it
			// needs no grant. A user-configured sandboxed server that shells out to
			// `gr` would hit the original bug — accepted, pre-existing limitation.
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
		serverName: serverCfg.Name,
		proxyID:    proxyID,
		startedAt:  time.Now(),
		cmd:        cmd,
		stdin:      stdinPipe,
		stdout:     bufio.NewReader(stdoutPipe),
		stderr:     stderrFile,
		done:       make(chan struct{}),
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

// mcpSandboxEqual reports whether two MCP server configs carry identical
// per-server sandbox settings — the enable flag and the config override.
// Reload compares these so tightening a per-server `[…sandbox]` override
// restarts the running process instead of leaving it under the old, looser
// policy (see #788).
func mcpSandboxEqual(a, b config.MCPServerConfig) bool {
	return reflect.DeepEqual(a.Sandbox, b.Sandbox) && reflect.DeepEqual(a.SandboxConfig, b.SandboxConfig)
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
