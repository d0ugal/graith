package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
)

const (
	agentInfoTimeoutDefault   = 30 * time.Second
	agentInfoCommandWaitDelay = 100 * time.Millisecond
	agentInfoProcessKillGrace = 250 * time.Millisecond
	agentInfoOutputMaxBytes   = 1 << 20
)

var agentInfoInheritedEnv = []string{
	"HOME",
	"PATH",
	"TERM",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TMPDIR",
	"TEMP",
	"TMP",
	"USER",
	"LOGNAME",
	"SHELL",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
}

func handleAgentInfo(ctx context.Context, sm *SessionManager, send func(string, any), msg protocol.Envelope) {
	req, ok := decodePayload[protocol.AgentInfoMsg](msg, send, "invalid agent_info message")
	if !ok {
		return
	}

	resp, err := sm.AgentInfo(ctx, req)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	send("agent_info_response", resp)
}

// AgentInfo runs configured provider info commands for an agent. It snapshots
// the current config before running any external command and does not hold the
// manager lock while commands execute.
func (sm *SessionManager) AgentInfo(ctx context.Context, req protocol.AgentInfoMsg) (protocol.AgentInfoResponseMsg, error) {
	agentName := strings.TrimSpace(req.Agent)
	if agentName == "" {
		return protocol.AgentInfoResponseMsg{}, errors.New("agent name is required")
	}

	if err := sm.beginLifecycleOperation(); err != nil {
		return protocol.AgentInfoResponseMsg{}, err
	}
	defer sm.endLifecycleOperation()

	cfg := sm.Config()

	agent, ok := cfg.Agents[agentName]
	if !ok {
		return protocol.AgentInfoResponseMsg{}, fmt.Errorf("unknown agent %q", agentName)
	}

	keys, err := selectAgentInfoKeys(agentName, agent.Info, req.Key)
	if err != nil {
		return protocol.AgentInfoResponseMsg{}, err
	}

	results := make([]protocol.AgentInfoResult, 0, len(keys))
	for _, key := range keys {
		result, err := sm.runAgentInfoCommand(ctx, cfg, agentName, agent, key, agent.Info[key], agentInfoTimeoutDefault)
		if err != nil {
			return protocol.AgentInfoResponseMsg{}, err
		}

		results = append(results, result)
	}

	return protocol.AgentInfoResponseMsg{Agent: agentName, Results: results}, nil
}

func selectAgentInfoKeys(agentName string, info map[string][]string, key string) ([]string, error) {
	if len(info) == 0 {
		return nil, fmt.Errorf("agent %q has no info commands configured", agentName)
	}

	key = strings.TrimSpace(key)
	if key != "" {
		if _, ok := info[key]; !ok {
			keys := sortedAgentInfoKeys(info)

			return nil, fmt.Errorf("unknown info key %q for agent %q (available: %s)", key, agentName, strings.Join(keys, ", "))
		}

		return []string{key}, nil
	}

	return sortedAgentInfoKeys(info), nil
}

func sortedAgentInfoKeys(info map[string][]string) []string {
	keys := make([]string, 0, len(info))
	for key := range info {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func (sm *SessionManager) runAgentInfoCommand(
	ctx context.Context,
	cfg *config.Config,
	agentName string,
	agent config.Agent,
	key string,
	infoArgs []string,
	timeout time.Duration,
) (protocol.AgentInfoResult, error) {
	return sm.runAgentInfoCommandWithOutputLimit(ctx, cfg, agentName, agent, key, infoArgs, timeout, agentInfoOutputMaxBytes)
}

func (sm *SessionManager) runAgentInfoCommandWithOutputLimit(
	ctx context.Context,
	cfg *config.Config,
	agentName string,
	agent config.Agent,
	key string,
	infoArgs []string,
	timeout time.Duration,
	outputMaxBytes int,
) (protocol.AgentInfoResult, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return protocol.AgentInfoResult{}, fmt.Errorf("agent %q has no command configured", agentName)
	}

	if len(infoArgs) == 0 {
		return protocol.AgentInfoResult{}, fmt.Errorf("agent %q info key %q has no args configured", agentName, key)
	}

	if timeout <= 0 {
		timeout = agentInfoTimeoutDefault
	}

	id := generateID()
	sessionName := "agent-info-" + agentName

	scratchDir, err := sm.agentInfoScratchDir(id)
	if err != nil {
		return protocol.AgentInfoResult{}, err
	}

	defer func() {
		_ = os.RemoveAll(scratchDir)
		_ = os.Remove(sm.nonoProfilePath(id))
		_ = os.Remove(sm.safehouseFragmentPath(id))
	}()

	vars := config.TemplateVars{
		AgentSessionID: id,
		SessionName:    sessionName,
		SessionID:      id,
		WorktreePath:   scratchDir,
	}

	displayArgs, err := config.ExpandSlice(infoArgs, vars)
	if err != nil {
		return protocol.AgentInfoResult{}, fmt.Errorf("expand agent info args for %s.%s: %w", agentName, key, err)
	}

	result := protocol.AgentInfoResult{
		Key:     key,
		Command: agent.Command,
		Args:    displayArgs,
	}

	envMap := agentInfoEnvMap(agent, agentName, sessionName, id, scratchDir, sm.paths.Profile)
	command := agent.Command
	finalArgs := displayArgs

	sandboxed, err := sm.resolveSandboxFromConfig(cfg, agentName)
	if err != nil {
		return protocol.AgentInfoResult{}, err
	}

	if sandboxed {
		merged := cfg.Sandbox.Merge(agent.Sandbox)

		opts, err := sm.sandboxOptsFromConfig(merged, id, scratchDir, agent.Command, sortedMapKeys(envMap), false)
		if err != nil {
			return protocol.AgentInfoResult{}, fmt.Errorf("configure sandbox for agent info %s.%s: %w", agentName, key, err)
		}

		opts = sandbox.EnforceSignalIsolation(opts)
		if tmpDir := envMap["GRAITH_TMPDIR"]; tmpDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, tmpDir)
		}

		if err := sm.validateAutomaticSandboxGrants(opts, merged); err != nil {
			return protocol.AgentInfoResult{}, fmt.Errorf("validate sandbox grants for agent info %s.%s: %w", agentName, key, err)
		}

		wrappedCommand, wrappedArgs, err := sandbox.Wrap(agent.Command, displayArgs, opts)
		if err != nil {
			return protocol.AgentInfoResult{}, fmt.Errorf("sandbox wrap for agent info %s.%s: %w", agentName, key, err)
		}

		command = wrappedCommand
		finalArgs = wrappedArgs
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, command, finalArgs...)
	cmd.Dir = scratchDir
	cmd.Env = envSlice(envMap)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = agentInfoCommandWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}

		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()

		return nil
	}

	stdout := newAgentInfoOutput(outputMaxBytes)
	stderr := newAgentInfoOutput(outputMaxBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.StdoutTruncated = stdout.Truncated()
	result.StderrTruncated = stderr.Truncated()
	result.ExitCode = commandExitCode(err)

	cleanupErr := cleanupAgentInfoProcessGroup(pid)

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.Error = fmt.Sprintf("agent info %s.%s timed out after %s", agentName, key, timeout)
		appendAgentInfoCleanupError(&result, cleanupErr)

		return result, nil
	}

	if errors.Is(err, exec.ErrWaitDelay) {
		result.Error = fmt.Sprintf("agent info %s.%s left output streams open after exit", agentName, key)
		appendAgentInfoCleanupError(&result, cleanupErr)

		return result, nil
	}

	if err != nil {
		result.Error = agentInfoRunErrorMessage(agentName, key, err, result)
		appendAgentInfoCleanupError(&result, cleanupErr)

		return result, nil
	}

	if cleanupErr != nil {
		result.Error = fmt.Sprintf("agent info %s.%s cleanup failed: %v", agentName, key, cleanupErr)
	}

	return result, nil
}

type agentInfoOutput struct {
	data      []byte
	limit     int
	truncated bool
}

func newAgentInfoOutput(limit int) agentInfoOutput {
	if limit <= 0 {
		limit = agentInfoOutputMaxBytes
	}

	return agentInfoOutput{limit: limit}
}

func (w *agentInfoOutput) Write(p []byte) (int, error) {
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		w.data = append(w.data, p[:min(len(p), remaining)]...)
	}

	if len(p) > remaining {
		w.truncated = true
	}

	return len(p), nil
}

func (w *agentInfoOutput) String() string {
	return string(w.data)
}

func (w *agentInfoOutput) Truncated() bool {
	return w.truncated
}

func cleanupAgentInfoProcessGroup(pid int) error {
	if pid <= 1 || exactProcessGroupGone(pid) {
		return nil
	}

	return killProcessGroup(pid, agentInfoProcessKillGrace)
}

func appendAgentInfoCleanupError(result *protocol.AgentInfoResult, err error) {
	if err == nil {
		return
	}

	if result.Error == "" {
		result.Error = err.Error()

		return
	}

	result.Error += "; cleanup failed: " + err.Error()
}

func (sm *SessionManager) agentInfoScratchDir(id string) (string, error) {
	if sm.paths.DataDir == "" {
		dir, err := os.MkdirTemp("", "graith-agent-info-"+id+"-")
		if err != nil {
			return "", fmt.Errorf("create agent info scratch dir: %w", err)
		}

		return dir, nil
	}

	dir := filepath.Join(sm.paths.DataDir, "scratch", "agent-info-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create agent info scratch dir: %w", err)
	}

	return dir, nil
}

func agentInfoEnvMap(agent config.Agent, agentName, sessionName, id, worktreePath, profile string) map[string]string {
	env := make(map[string]string, len(agent.Env)+len(agentInfoInheritedEnv)+8)
	for _, key := range agentInfoInheritedEnv {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}

	if env["TERM"] == "" {
		env["TERM"] = "xterm-256color"
	}

	for key, value := range agent.Env {
		env[key] = value
	}

	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = sessionName
	env["GRAITH_AGENT_TYPE"] = agentName
	env["GRAITH_WORKTREE_PATH"] = worktreePath

	env["GRAITH_TMPDIR"] = worktreePath
	if _, ok := agent.Env["TMPDIR"]; !ok {
		env["TMPDIR"] = worktreePath
	}

	if profile != "" {
		env["GRAITH_PROFILE"] = profile
	}

	return env
}

func envSlice(env map[string]string) []string {
	keys := sortedMapKeys(env)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}

	return out
}

func sortedMapKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}

func agentInfoRunErrorMessage(agentName, key string, err error, result protocol.AgentInfoResult) string {
	msg := fmt.Sprintf("agent info %s.%s failed", agentName, key)
	if result.ExitCode >= 0 {
		msg += fmt.Sprintf(" with exit code %d", result.ExitCode)
	}

	msg += ": " + err.Error()

	return msg
}
