package daemon

import (
	"context"
	"encoding/json"
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

var cleanupAgentInfoProcessGroupFunc = cleanupAgentInfoProcessGroup

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

	if req.Refresh && req.NoCache {
		return protocol.AgentInfoResponseMsg{}, errors.New("agent info refresh and no_cache are mutually exclusive")
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

	defaultCacheTTL := cfg.AgentInfo.CacheTTLDuration()
	globalCacheDisabled := strings.TrimSpace(cfg.AgentInfo.CacheTTL) != "" && defaultCacheTTL == 0

	results := make([]protocol.AgentInfoResult, 0, len(keys))
	for _, key := range keys {
		info, _, err := agent.Info.Command(key)
		if err != nil {
			return protocol.AgentInfoResponseMsg{}, err
		}

		result, err := sm.agentInfoResult(ctx, cfg, agentName, agent, key, info, defaultCacheTTL, globalCacheDisabled, req)
		if err != nil {
			return protocol.AgentInfoResponseMsg{}, err
		}

		results = append(results, result)
	}

	return protocol.AgentInfoResponseMsg{Agent: agentName, Results: results}, nil
}

func selectAgentInfoKeys(agentName string, info config.AgentInfoCommands, key string) ([]string, error) {
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

func sortedAgentInfoKeys(info config.AgentInfoCommands) []string {
	keys := make([]string, 0, len(info))
	for key := range info {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

type agentInfoCacheKey struct {
	Agent     string
	Key       string
	Signature string
}

type agentInfoFlightKey struct {
	CacheKey agentInfoCacheKey
	Mode     string
}

type agentInfoCacheEntry struct {
	Result    protocol.AgentInfoResult
	FetchedAt time.Time
	ExpiresAt time.Time
}

type agentInfoFlight struct {
	done   chan struct{}
	result agentInfoFreshResult
}

type agentInfoFreshResult struct {
	Result    protocol.AgentInfoResult
	FetchedAt time.Time
	Err       error
}

func (sm *SessionManager) agentInfoResult(
	ctx context.Context,
	cfg *config.Config,
	agentName string,
	agent config.Agent,
	key string,
	info config.AgentInfoCommand,
	defaultCacheTTL time.Duration,
	globalCacheDisabled bool,
	req protocol.AgentInfoMsg,
) (protocol.AgentInfoResult, error) {
	cacheTTL := info.CacheTTLDuration(defaultCacheTTL)
	if globalCacheDisabled {
		cacheTTL = 0
	}

	cacheEnabled := cacheTTL > 0
	cacheKey := agentInfoCacheKey{
		Agent:     agentName,
		Key:       key,
		Signature: agentInfoCacheSignature(agent, info, cfg.Sandbox),
	}

	if cacheEnabled && !req.Refresh && !req.NoCache {
		if entry, ok := sm.agentInfoCacheHit(cacheKey, time.Now()); ok {
			result := cloneAgentInfoResult(entry.Result)
			result.Cache = agentInfoCacheMetadata(true, true, false, entry.FetchedAt, entry.ExpiresAt)

			return result, nil
		}
	}

	fresh, err := sm.refreshAgentInfoResult(ctx, cfg, agentName, agent, key, info, agentInfoFlightMode(req))
	if err != nil {
		return protocol.AgentInfoResult{}, err
	}

	if fresh.Err != nil {
		return protocol.AgentInfoResult{}, fresh.Err
	}

	expiresAt := time.Time{}

	cacheable := fresh.Result.Error == "" && fresh.Result.ExitCode == 0
	if cacheEnabled && !req.NoCache && cacheable {
		expiresAt = fresh.FetchedAt.Add(cacheTTL)
		sm.storeAgentInfoCache(cacheKey, agentInfoCacheEntry{
			Result:    fresh.Result,
			FetchedAt: fresh.FetchedAt,
			ExpiresAt: expiresAt,
		})
	}

	result := cloneAgentInfoResult(fresh.Result)
	if cacheEnabled {
		result.Cache = agentInfoCacheMetadata(true, false, req.Refresh || req.NoCache, fresh.FetchedAt, expiresAt)
	} else {
		result.Cache = agentInfoCacheMetadata(false, false, false, fresh.FetchedAt, time.Time{})
	}

	return result, nil
}

func (sm *SessionManager) agentInfoCacheHit(key agentInfoCacheKey, now time.Time) (agentInfoCacheEntry, bool) {
	sm.agentInfoCacheMu.Lock()
	defer sm.agentInfoCacheMu.Unlock()

	if sm.agentInfoCache == nil {
		sm.agentInfoCache = make(map[agentInfoCacheKey]agentInfoCacheEntry)
	}

	entry, ok := sm.agentInfoCache[key]
	if !ok {
		return agentInfoCacheEntry{}, false
	}

	if !now.Before(entry.ExpiresAt) {
		delete(sm.agentInfoCache, key)

		return agentInfoCacheEntry{}, false
	}

	entry.Result = cloneAgentInfoResult(entry.Result)

	return entry, true
}

func (sm *SessionManager) storeAgentInfoCache(key agentInfoCacheKey, entry agentInfoCacheEntry) {
	sm.agentInfoCacheMu.Lock()
	defer sm.agentInfoCacheMu.Unlock()

	if sm.agentInfoCache == nil {
		sm.agentInfoCache = make(map[agentInfoCacheKey]agentInfoCacheEntry)
	}

	sm.sweepAgentInfoCacheLocked(time.Now(), key)

	entry.Result = cloneAgentInfoResult(entry.Result)
	entry.Result.Cache = nil
	entry.Result.Warnings = nil
	sm.agentInfoCache[key] = entry
}

func (sm *SessionManager) sweepAgentInfoCacheLocked(now time.Time, storing agentInfoCacheKey) {
	for key, entry := range sm.agentInfoCache {
		if !now.Before(entry.ExpiresAt) {
			delete(sm.agentInfoCache, key)

			continue
		}

		if key.Agent == storing.Agent && key.Key == storing.Key && key.Signature != storing.Signature {
			delete(sm.agentInfoCache, key)
		}
	}
}

func (sm *SessionManager) refreshAgentInfoResult(
	ctx context.Context,
	cfg *config.Config,
	agentName string,
	agent config.Agent,
	key string,
	info config.AgentInfoCommand,
	flightMode string,
) (agentInfoFreshResult, error) {
	cacheKey := agentInfoCacheKey{
		Agent:     agentName,
		Key:       key,
		Signature: agentInfoCacheSignature(agent, info, cfg.Sandbox),
	}
	flightKey := agentInfoFlightKey{CacheKey: cacheKey, Mode: flightMode}

	sm.agentInfoCacheMu.Lock()
	if sm.agentInfoFlights == nil {
		sm.agentInfoFlights = make(map[agentInfoFlightKey]*agentInfoFlight)
	}

	if flight := sm.agentInfoFlights[flightKey]; flight != nil {
		sm.agentInfoCacheMu.Unlock()

		select {
		case <-flight.done:
			return cloneAgentInfoFreshResult(flight.result), nil
		case <-ctx.Done():
			return agentInfoFreshResult{}, ctx.Err()
		}
	}

	flight := &agentInfoFlight{done: make(chan struct{})}
	sm.agentInfoFlights[flightKey] = flight
	sm.agentInfoCacheMu.Unlock()

	// The shared provider process is owned by the daemon and bounded by
	// agentInfoTimeoutDefault. A leader request cancellation must not cancel the
	// command for waiters that already joined the same flight.
	result, err := sm.runAgentInfoCommand(context.WithoutCancel(ctx), cfg, agentName, agent, key, info, agentInfoTimeoutDefault)
	fresh := agentInfoFreshResult{
		Result:    result,
		FetchedAt: time.Now(),
		Err:       err,
	}

	sm.agentInfoCacheMu.Lock()
	flight.result = fresh

	delete(sm.agentInfoFlights, flightKey)
	close(flight.done)
	sm.agentInfoCacheMu.Unlock()

	return cloneAgentInfoFreshResult(fresh), nil
}

func agentInfoFlightMode(req protocol.AgentInfoMsg) string {
	switch {
	case req.NoCache:
		return "no_cache"
	case req.Refresh:
		return "refresh"
	default:
		return "cacheable"
	}
}

func agentInfoCacheSignature(agent config.Agent, info config.AgentInfoCommand, globalSandbox config.SandboxConfig) string {
	signature := struct {
		Command          string
		Env              map[string]string
		Info             config.AgentInfoCommand
		EffectiveSandbox config.SandboxConfig
	}{
		Command:          agent.Command,
		Env:              agent.Env,
		Info:             info,
		EffectiveSandbox: globalSandbox.Merge(agent.Sandbox),
	}

	data, err := json.Marshal(signature)
	if err != nil {
		return fmt.Sprintf("%s:%v:%v", agent.Command, info.Args, info)
	}

	return string(data)
}

func agentInfoCacheMetadata(enabled, hit, bypassed bool, fetchedAt, expiresAt time.Time) *protocol.AgentInfoCacheMetadata {
	meta := &protocol.AgentInfoCacheMetadata{
		Enabled:  enabled,
		Hit:      hit,
		Bypassed: bypassed,
	}

	if !fetchedAt.IsZero() {
		meta.FetchedAt = fetchedAt.UTC().Format(time.RFC3339Nano)
	}

	if !expiresAt.IsZero() {
		meta.ExpiresAt = expiresAt.UTC().Format(time.RFC3339Nano)
	}

	return meta
}

func cloneAgentInfoFreshResult(result agentInfoFreshResult) agentInfoFreshResult {
	result.Result = cloneAgentInfoResult(result.Result)

	return result
}

func cloneAgentInfoResult(result protocol.AgentInfoResult) protocol.AgentInfoResult {
	result.Args = append([]string(nil), result.Args...)
	result.Lines = append([]string(nil), result.Lines...)
	result.Models = append([]protocol.AgentInfoModel(nil), result.Models...)
	result.Warnings = append([]string(nil), result.Warnings...)

	if result.Cache != nil {
		cache := *result.Cache
		result.Cache = &cache
	}

	return result
}

func (sm *SessionManager) runAgentInfoCommand(
	ctx context.Context,
	cfg *config.Config,
	agentName string,
	agent config.Agent,
	key string,
	info config.AgentInfoCommand,
	timeout time.Duration,
) (protocol.AgentInfoResult, error) {
	return sm.runAgentInfoCommandWithOutputLimit(ctx, cfg, agentName, agent, key, info, timeout, agentInfoOutputMaxBytes)
}

func (sm *SessionManager) runAgentInfoCommandWithOutputLimit(
	ctx context.Context,
	cfg *config.Config,
	agentName string,
	agent config.Agent,
	key string,
	info config.AgentInfoCommand,
	timeout time.Duration,
	outputMaxBytes int,
) (protocol.AgentInfoResult, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return protocol.AgentInfoResult{}, fmt.Errorf("agent %q has no command configured", agentName)
	}

	if len(info.Args) == 0 {
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

	displayArgs, err := config.ExpandSlice(info.Args, vars)
	if err != nil {
		return protocol.AgentInfoResult{}, fmt.Errorf("expand agent info args for %s.%s: %w", agentName, key, err)
	}

	result := protocol.AgentInfoResult{
		Key:     key,
		Command: agent.Command,
		Args:    displayArgs,
		Format:  info.FormatOrDefault(),
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
	normalizeAgentInfoResult(&result)

	cleanupErr := cleanupAgentInfoProcessGroupFunc(pid)

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.Error = fmt.Sprintf("agent info %s.%s timed out after %s", agentName, key, timeout)
		appendAgentInfoCleanupWarning(&result, cleanupErr)

		return result, nil
	}

	if errors.Is(err, exec.ErrWaitDelay) {
		result.Error = fmt.Sprintf("agent info %s.%s left output streams open after exit", agentName, key)
		appendAgentInfoCleanupWarning(&result, cleanupErr)

		return result, nil
	}

	if err != nil {
		result.Error = agentInfoRunErrorMessage(agentName, key, err, result)
		appendAgentInfoCleanupWarning(&result, cleanupErr)

		return result, nil
	}

	if cleanupErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("agent info %s.%s cleanup failed: %v", agentName, key, cleanupErr))
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

func normalizeAgentInfoResult(result *protocol.AgentInfoResult) {
	switch result.Format {
	case "", config.AgentInfoFormatRaw:
		result.Format = config.AgentInfoFormatRaw
	case config.AgentInfoFormatLines:
		result.Lines = agentInfoLines(result.Stdout)
	case config.AgentInfoFormatModelList:
		result.Models = parseAgentInfoModelList(result.Stdout)
	default:
		// Config validation rejects unknown formats. This fallback keeps direct
		// unit construction from losing raw diagnostics.
		result.Format = config.AgentInfoFormatRaw
	}
}

func agentInfoLines(stdout string) []string {
	if stdout == "" {
		return nil
	}

	normalized := strings.ReplaceAll(stdout, "\r\n", "\n")

	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	return lines
}

func parseAgentInfoModelList(stdout string) []protocol.AgentInfoModel {
	lines := agentInfoLines(stdout)
	models := make([]protocol.AgentInfoModel, 0, len(lines))

	for _, line := range lines {
		id, description, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}

		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		models = append(models, protocol.AgentInfoModel{
			ID:          id,
			Description: strings.TrimSpace(description),
		})
	}

	return models
}

func cleanupAgentInfoProcessGroup(pid int) error {
	if pid <= 1 || exactProcessGroupGone(pid) {
		return nil
	}

	return killProcessGroup(pid, agentInfoProcessKillGrace)
}

func appendAgentInfoCleanupWarning(result *protocol.AgentInfoResult, err error) {
	if err == nil {
		return
	}

	result.Warnings = append(result.Warnings, "cleanup failed: "+err.Error())
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
