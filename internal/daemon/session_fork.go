package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/store"
)

// Fork creates a new session/worktree that natively continues the source
// agent's conversation (same agent type), using the agent's fork_args to carry
// over the history. It is a thin wrapper over ForkWithAgent with no override.
func (sm *SessionManager) Fork(name, sourceSessionID string, rows, cols uint16) (SessionState, error) {
	return sm.ForkWithAgent(name, sourceSessionID, "", "", rows, cols)
}

// ForkWithAgent forks a session into a new worktree. When targetAgent is empty
// or equal to the source's agent, this is a native same-agent fork (the source
// agent's conversation is resumed via fork_args). When targetAgent differs, it
// is a CROSS-AGENT fork: the source's on-disk conversation is rendered to a
// neutral Markdown file and the new agent is seeded with it (reusing the
// migration reader/renderer), while the source session keeps running.
//
// Git state: like any fork, the new worktree branches from the base branch, so
// the source's uncommitted edits are dropped. For a cross-agent fork the seed
// prompt says so explicitly (BuildForkSeedPrompt).
//
// Uses three-phase locking like Create to avoid holding the mutex during git
// fetch and PTY spawn.
//
// See docs/design/2026-06-24-cross-agent-conversation-migration-design.md
// ("Future: cross-agent fork").
func (sm *SessionManager) ForkWithAgent(name, sourceSessionID, targetAgent, targetModel string, rows, cols uint16) (SessionState, error) {
	if err := ValidateSessionName(name); err != nil {
		return SessionState{}, err
	}

	// --- Pre-lock: discover GitHub username ---
	sm.mu.RLock()
	cfgSnapshot := sm.cfg
	source, sourceOk := sm.state.Sessions[sourceSessionID]

	var (
		sourceRepoPath string
		srcAgentPre    string
	)

	if sourceOk {
		sourceRepoPath = source.RepoPath
		srcAgentPre = source.Agent
	}

	sm.mu.RUnlock()

	// Validate the target model outside the lock — validateModel may exec an
	// external validator (up to a 10s timeout), and holding sm.mu across it would
	// freeze the whole control plane (Create/Migrate validate pre-lock too). The
	// cheap agent/transcript checks are re-done under the lock below.
	crossAgentPre := targetAgent != "" && sourceOk && targetAgent != srcAgentPre
	if targetModel != "" && !crossAgentPre {
		return SessionState{}, errors.New("--model requires forking to a different agent (--agent); it is ignored for a same-agent fork")
	}

	if crossAgentPre && targetModel != "" {
		tgtCfg, ok := cfgSnapshot.Agents[targetAgent]
		if !ok {
			return SessionState{}, fmt.Errorf("unknown target agent %q", targetAgent)
		}

		if err := validateModel(tgtCfg, targetModel); err != nil {
			return SessionState{}, err
		}
	}

	preUsername := cfgSnapshot.GitHubUsername
	if preUsername == "" && sourceRepoPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), gitUsernameTimeout)
		preUsername, _ = git.DiscoverGitHubUsername(ctx, sourceRepoPath)

		cancel()
	}

	if preUsername == "" {
		preUsername = "user"
	}

	// --- Phase 1: Lock, validate, reserve ---
	sm.mu.Lock()

	source, ok := sm.state.Sessions[sourceSessionID]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("source session %q not found", sourceSessionID)
	}

	if IsSystemSession(source) {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork system session %q", source.Name)
	}

	// A fork acts on a raw source ID; a soft-deleted source is stopped and would
	// otherwise fork fine, resurrecting hidden state into a live child.
	if source.IsSoftDeleted() {
		sm.mu.Unlock()
		return SessionState{}, errSoftDeleted(source.Name)
	}

	if source.RepoPath == "" {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork session %q: source has no repo (fork requires a git repository)", source.Name)
	}

	if source.InPlace {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork session %q: in-place sessions cannot be forked", source.Name)
	}

	if rc, ok := sm.cfg.FindRepo(source.RepoPath); ok && rc.Singleton {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("cannot fork session %q: repo %q has singleton = true — stop the source session first or remove the singleton constraint", source.Name, source.RepoPath)
	}

	srcAgent := source.Agent
	srcWorktree := source.WorktreePath

	// A cross-agent fork changes the agent type; empty or equal to the source is
	// a native same-agent fork.
	crossAgent := targetAgent != "" && targetAgent != srcAgent

	agentName := srcAgent
	if crossAgent {
		agentName = targetAgent
	}

	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		sm.mu.Unlock()

		if crossAgent {
			return SessionState{}, fmt.Errorf("unknown target agent %q", agentName)
		}

		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	if crossAgent {
		// The source's conversation must be readable to seed the new agent.
		// (targetModel was validated pre-lock to avoid exec under sm.mu.)
		if !transcript.Supported(srcAgent) {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("cannot fork session %q to agent %q: forking from %q is not supported (no transcript reader)", source.Name, targetAgent, srcAgent)
		}
	}

	id := generateID()

	token, err := generateToken()
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("generate session token: %w", err)
	}

	repoRoot := source.RepoPath
	repoName := source.RepoName
	baseBranch := source.BaseBranch
	// A same-agent fork inherits the source model; a cross-agent fork uses the
	// requested target model (empty = the target agent's default).
	sourceModel := source.Model

	effectiveModel := sourceModel
	if crossAgent {
		effectiveModel = targetModel
	}

	// A same-agent codex fork replays the source's typed Codex options (issue
	// #1186); a cross-agent fork to another agent drops them (codexOptsForAgent),
	// so a non-codex target never persists an orphan codex block.
	sourceCodex := codexOptsForAgent(agentName, cloneCodexOptions(source.Codex))

	sourceAgentSessionID := source.AgentSessionID
	sourceYolo := source.Yolo
	// Yolo forces agent hooks on (see Create) so a forked yolo session always
	// installs the approval hook, even if the source had hooks disabled.
	sourceAgentHooks := source.AgentHooks || sourceYolo
	// MCP config injection is decided separately from hooks (see #1135). Fork is
	// PTY-only, so the two coincide here.
	sourceMCPEnabled := sourceAgentHooks
	sourceForkIncludes := make([]IncludedRepoState, len(source.Includes))
	copy(sourceForkIncludes, source.Includes)

	branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: preUsername})
	branchName := fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

	sessionDir := filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

	var worktreePath string
	if len(sourceForkIncludes) > 0 {
		worktreePath = filepath.Join(sessionDir, repoName)
	} else {
		worktreePath = sessionDir
	}

	agentSessionID := ""
	if forcesID(agentName) {
		agentSessionID = newAgentSessionID()
	}

	sandboxed, err := sm.resolveSandbox(agentName)
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, err
	}

	if err := sm.validateApprovalsBackend(sourceYolo); err != nil {
		sm.mu.Unlock()
		return SessionState{}, err
	}

	var mcpServers []config.MCPServerConfig
	if sourceMCPEnabled {
		mcpServers = sm.resolveMCPServers(agentName)
	}

	sandboxMerged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)
	fetchOnCreate := sm.cfg.FetchOnCreate

	placeholder := &SessionState{
		ID:              id,
		ParentID:        sourceSessionID,
		Name:            name,
		RepoPath:        repoRoot,
		RepoName:        repoName,
		WorktreePath:    worktreePath,
		Branch:          branchName,
		BaseBranch:      baseBranch,
		Agent:           agentName,
		AgentSessionID:  agentSessionID,
		Model:           effectiveModel,
		Codex:           sourceCodex,
		AgentHooks:      sourceAgentHooks,
		Yolo:            sourceYolo,
		Status:          StatusCreating,
		CreatedAt:       time.Now().UTC(),
		StatusChangedAt: time.Now().UTC(),
		Token:           token,
	}

	sm.state.Sessions[id] = placeholder
	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		sm.mu.Unlock()

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	sm.mu.Unlock()

	// --- Phase 2: Git setup and PTY spawn (no lock) ---
	var forkIncludes []IncludedRepoState

	// Cross-agent fork: rendered source conversation + its staging dir + the
	// seed prompt pointing the new agent at it. Empty for a same-agent fork.
	var (
		forkContextDir       string
		forkContextPath      string
		seedPrompt           string
		forkContextCommitted bool
	)

	forkCleanup := func() {
		sm.cleanupHooks(id, agentName, worktreePath)
		// See cleanupOnError in Create: remove any nono profile Wrap wrote
		// before the error path so it isn't orphaned when state is rolled back.
		_ = os.Remove(sm.nonoProfilePath(id))
		_ = os.Remove(sm.safehouseFragmentPath(id))

		if len(forkIncludes) > 0 {
			_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)
		} else {
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
	}
	rollbackState := func() {
		sm.mu.Lock()
		delete(sm.state.Sessions, id)
		_ = sm.saveState()
		sm.mu.Unlock()
	}

	// Guarantee the staged context dir is removed on ANY early return before the
	// Phase 3 commit — not every failure path calls forkCleanup (git-setup errors
	// call only rollbackState), and a leaked dir holds the full source
	// conversation. Disarmed only once the swap is persisted (forkContextCommitted).
	defer func() {
		if forkContextDir != "" && !forkContextCommitted {
			_ = os.RemoveAll(forkContextDir)
		}
	}()

	// --- Phase 1.5: render + stage the source transcript (cross-agent only) ---
	// Done before the (expensive) git worktree setup so a doomed cross-agent
	// fork — unsupported/missing/empty source transcript — fails fast. The
	// source session keeps running throughout; we only read its on-disk history.
	if crossAgent {
		conv, err := transcript.Read(srcAgent, sourceAgentSessionID, srcWorktree)
		if err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("read source transcript: %w", err)
		}

		rendered := conv.Render(transcript.RenderOptions{Kind: transcript.RenderFork})

		tmpDir, err := sm.repoTmpDir(repoRoot)
		if err != nil {
			rollbackState()
			return SessionState{}, err
		}

		// Staged in a per-session subdir under the repo tmp dir (already on the
		// new session's sandbox write-list, so the target can read it). NOTE: this
		// does NOT isolate the file from sibling sessions on the same repo — they
		// share GRAITH_TMPDIR and run as the same user, so 0o700/0o600 don't gate
		// them. The subdir is for tidy per-session cleanup, not confidentiality.
		// Same trade-off as Migrate's migrate-<id> dir; true isolation would need
		// a per-session grant outside the shared root (tracked separately).
		forkContextDir = filepath.Join(tmpDir, "fork-"+id)
		if err := os.MkdirAll(forkContextDir, 0o700); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("create fork context dir: %w", err)
		}

		forkContextPath = filepath.Join(forkContextDir, "context.md")
		if err := writeFileAtomic(forkContextPath, []byte(rendered)); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("write fork context: %w", err)
		}

		seedPrompt = transcript.BuildForkSeedPrompt(srcAgent, forkContextPath)
	}

	gitCtx, gitCancel := context.WithTimeout(context.Background(), gitFetchTimeout)
	defer gitCancel()

	if len(sourceForkIncludes) > 0 {
		if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("setup main repo git session for fork: %w", err)
		}

		for _, srcInc := range sourceForkIncludes {
			incBranch := fmt.Sprintf("%s/%s-%s/%s", branchPrefix, name, id, srcInc.RepoName)

			incWorktreePath := filepath.Join(sessionDir, srcInc.RepoName)
			if err := git.SetupSession(gitCtx, srcInc.RepoPath, incWorktreePath, incBranch, srcInc.Branch, fetchOnCreate); err != nil {
				_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, forkIncludes)

				rollbackState()

				return SessionState{}, fmt.Errorf("setup included repo %q for fork: %w", srcInc.RepoName, err)
			}

			forkIncludes = append(forkIncludes, IncludedRepoState{
				RepoPath:     srcInc.RepoPath,
				RepoName:     srcInc.RepoName,
				WorktreePath: incWorktreePath,
				Branch:       incBranch,
				BaseBranch:   srcInc.Branch,
			})
		}

		// Same post-create hook as Create: a forked includes session gets fresh
		// worktrees, so its config files still hold the source paths and must be
		// rewritten too (#1033).
		sm.applyIncludePathRewrites(repoRoot, worktreePath, forkIncludes)
	} else {
		if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
			rollbackState()
			return SessionState{}, fmt.Errorf("setup git session: %w", err)
		}
	}

	// For a cross-agent fork the source's native id belongs to a different agent
	// and is meaningless to the target, so it never templates into the target's
	// args; the model is the requested target model.
	forkSourceID := sourceAgentSessionID
	if crossAgent {
		forkSourceID = ""
	}

	vars := config.TemplateVars{
		Username:                 preUsername,
		AgentSessionID:           agentSessionID,
		SessionName:              name,
		SessionID:                id,
		WorktreePath:             worktreePath,
		ForkSourceAgentSessionID: forkSourceID,
		Model:                    effectiveModel,
	}

	var args []string
	if crossAgent {
		// A cross-agent fork cannot natively resume the source's conversation, so
		// start the target fresh (agent.Args, not fork_args) and seed it with the
		// rendered history via seedPrompt below.
		args = agent.Args
	} else {
		args = agent.ForkArgs
		if len(args) == 0 {
			args = agent.Args
		}
		// Empty-source guard: if fork_args templates the source's native id but the
		// source never captured one (e.g. a pre-feature or capture-timed-out Codex
		// session), expanding would emit a literal empty arg (`codex fork ""`).
		// Start a fresh conversation instead; capture below records the new id.
		if argsNeedForkSourceID(args) && sourceAgentSessionID == "" {
			args = agent.Args
		}
	}

	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		forkCleanup()
		rollbackState()

		return SessionState{}, fmt.Errorf("expand fork args: %w", err)
	}

	// Replay the conditional Codex flags after the fork/args (issue #1186); no-op
	// for other agents. Codex accepts these on its `fork` subcommand too.
	expandedArgs = append(expandedArgs, codexExtraArgs(agentName, effectiveModel, sourceCodex)...)

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+6)
	for k, v := range agent.Env {
		env[k] = v
	}

	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
	env["GRAITH_AGENT_TYPE"] = agentName
	env["GRAITH_WORKTREE_PATH"] = worktreePath

	env["GRAITH_TOKEN"] = token
	if repoRoot != "" {
		env["GRAITH_REPO_PATH"] = repoRoot
	}

	if sm.paths.Profile != "" {
		env["GRAITH_PROFILE"] = sm.paths.Profile
	}

	var forkStoreDir string

	if repoRoot != "" {
		tmpDir, err := sm.repoTmpDir(repoRoot)
		if err != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, err
		}

		env["GRAITH_TMPDIR"] = tmpDir
		if _, ok := env["TMPDIR"]; !ok {
			env["TMPDIR"] = tmpDir
		}

		forkStoreDir, err = sm.repoStoreDir(repoRoot)
		if err != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, err
		}
	}

	for _, inc := range forkIncludes {
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if sourceAgentHooks {
		hookArgs, hookEnv, err := sm.injectHooks(agentName, id, worktreePath, sourceYolo)
		if err != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}

		expandedArgs = append(expandedArgs, hookArgs...)

		for k, v := range hookEnv {
			env[k] = v
		}
	}

	if sourceMCPEnabled {
		mcpArgs, err := sm.injectMCPConfig(agentName, id, mcpServers)
		if err != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, fmt.Errorf("inject mcp config: %w", err)
		}

		expandedArgs = append(expandedArgs, mcpArgs...)
	}

	if agent.PromptInjectionEnabled() {
		promptArgs, err := sm.injectPrompt(agentName, worktreePath)
		if err != nil {
			sm.log.Warn("failed to inject prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
	}

	// Cross-agent fork seed: the rendered-history pointer is delivered as the new
	// agent's opening positional prompt (like `gr new --prompt`). Appended after
	// any injected prompt but before includeAddDirArgs, because Claude's variadic
	// --add-dir would otherwise swallow it as another directory (see Create).
	if seedPrompt != "" {
		expandedArgs = append(expandedArgs, seedPrompt)
	}

	// Make each included repo's forked worktree visible to the agent via
	// --add-dir (a fork re-creates the source's includes as forkIncludes).
	// Appended last, after any injected prompt (see Create for why).
	expandedArgs = append(expandedArgs, includeAddDirArgs(agentName, forkIncludes)...)

	command := agent.Command
	finalArgs := expandedArgs

	var mergedSandbox *config.SandboxConfig

	if sandboxed {
		merged := sandboxMerged
		merged.ReadDirs = expandPaths(merged.ReadDirs, sm.log, "read")
		merged.WriteDirs = expandPaths(merged.WriteDirs, sm.log, "write")
		mergedSandbox = &merged

		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_AGENT_TYPE", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}

		for k := range env {
			envKeys = append(envKeys, k)
		}

		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, agent.Command, envKeys, sourceAgentHooks || sourceMCPEnabled)
		if tmpDir := env["GRAITH_TMPDIR"]; tmpDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, tmpDir)
		}

		if forkStoreDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, forkStoreDir)
		}

		opts.WriteDirs = append(opts.WriteDirs, store.SharedStorePath(sm.paths.DataDir))
		if len(forkIncludes) > 0 {
			opts.WriteDirs = append(opts.WriteDirs, sm.deriveSandboxIncludesWriteDirs(forkIncludes)...)
		}

		var wrapErr error

		command, finalArgs, wrapErr = sandbox.Wrap(agent.Command, expandedArgs, opts)
		if wrapErr != nil {
			forkCleanup()
			rollbackState()

			return SessionState{}, fmt.Errorf("sandbox wrap: %w", wrapErr)
		}

		sm.log.Info("sandboxing forked session", "id", id,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"unix_sockets", opts.UnixSockets,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	// Throttle concurrent launches (#1092); see Create for the slot lifecycle.
	slot, err := sm.acquireLaunchSlot(context.Background(), id, name)
	if err != nil {
		forkCleanup()
		rollbackState()

		return SessionState{}, fmt.Errorf("acquire launch slot: %w", err)
	}

	// Pre-spawn time for native session-id capture (see Create).
	startedAt := time.Now()

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		Dir:        worktreePath,
		Env:        env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
		Logger:     sm.log,
	})
	if err != nil {
		slot.release()
		forkCleanup()
		rollbackState()

		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	sm.releaseLaunchSlotWhenSettled(slot, id, name, ptySess)

	// --- Phase 3: Lock, commit ---
	sm.mu.Lock()

	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()

		sm.logStopping(id, name, "rollback", "fork-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()
		forkCleanup()

		_ = os.Remove(logPath)

		return SessionState{}, errors.New("session was deleted during creation")
	}

	sessState := sm.state.Sessions[id]
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox
	sessState.Includes = forkIncludes
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()

	sessState.PID = ptySess.Cmd.Process.Pid
	if st, err := grpty.ProcessStartTime(sessState.PID); err == nil {
		sessState.PIDStartTime = st
	}

	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: cfgSnapshot.Sandbox.Merge(agent.Sandbox),
	}

	// Record cross-agent provenance (surfaced via SessionInfo.MigratedFrom) and,
	// crucially, the RenderedPath so the staged context file is cleaned up on
	// delete (removeMigrationContext keys off it). MigrationInfo is shared with
	// Migrate; a fork is distinguished by having a live ParentID.
	if crossAgent {
		sessState.MigratedFrom = &MigrationInfo{
			Agent:          srcAgent,
			Model:          sourceModel,
			AgentSessionID: sourceAgentSessionID,
			RenderedPath:   forkContextPath,
			At:             time.Now().UTC(),
		}
	}

	sm.sessions[id] = ptySess
	sm.tokenIndex[token] = id

	if src, ok := sm.state.Sessions[sessState.ParentID]; ok {
		applyLifecycleSummaryLocked(sessState, "Forked from "+src.Name)
	}

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		delete(sm.tokenIndex, token)
		sm.mu.Unlock()

		sm.logStopping(id, name, "rollback", "fork-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()
		forkCleanup()

		_ = os.Remove(logPath)

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	// The swap is persisted: the staged context is now owned by the session
	// (cleaned up on delete), so disarm the early-return cleanup guard.
	forkContextCommitted = true

	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	// Symmetric with Create/resume spawn logging (issue #1104).
	sm.log.Info("session spawned",
		"id", id, "name", name, "agent", agentName, "forked", true,
		"pid", result.PID, "pgid", ptySess.Pgid(), "sandboxed", sandboxed,
		"scrollback_path", logPath)

	sm.startWatcher(id, ptySess)

	// Capture the forked child's native id for self-minting agents (Codex): the
	// fork mints a new conversation graith didn't choose, so read it from disk
	// for deterministic later resume. Skipped when the id was forced (Claude).
	if scrapesID(agentName) && agentSessionID == "" {
		go sm.captureNativeSessionID(id, agentName, worktreePath, env["CODEX_HOME"], startedAt, result.PID, result.PIDStartTime)
	}

	return result, nil
}
