package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/headless"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/store"
)

// Create starts a new agent session, either in a git worktree, in-place
// in an existing repo, or as a standalone scratch session (when noRepo is true).
//
// The method uses three-phase locking to avoid holding the daemon mutex during
// potentially blocking git/network operations (fetch, GitHub API calls, PTY spawn):
//  1. Lock: validate, reserve session as StatusCreating, unlock
//  2. Git setup and PTY spawn (no lock held)
//  3. Lock: commit to StatusRunning, unlock
func (sm *SessionManager) Create(opts CreateOpts) (SessionState, error) {
	// Destructure into local names so the body below stays unchanged.
	name := opts.Name
	agentName := opts.AgentName
	repoPath := opts.RepoPath
	baseBranch := opts.BaseBranch
	prompt := opts.Prompt
	model := opts.Model
	parentID := opts.ParentID
	noRepo := opts.NoRepo
	mirror := opts.Mirror
	agentHooks := opts.AgentHooks
	inPlace := opts.InPlace
	allowConcurrent := opts.AllowConcurrent
	skipModelValidation := opts.SkipModelValidation
	yolo := opts.Yolo
	rows := opts.Rows
	cols := opts.Cols
	envExtra := opts.EnvExtra

	if err := ValidateSessionName(name); err != nil {
		return SessionState{}, err
	}

	cfgSnapshot := sm.Config()

	agent, ok := cfgSnapshot.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	if !skipModelValidation {
		if err := validateModel(agent, model); err != nil {
			return SessionState{}, err
		}
	}

	// Typed options (profile, reasoning effort, service tier, web search, approval
	// policy) are accepted only when the selected agent's option_args declare a
	// matching group — the capability comes from the agent definition, not a literal
	// "codex" name, so a custom alias that declares those groups accepts them too
	// (issue #1236). An option the agent can't consume is rejected rather than
	// silently dropped (issue #1186). Their *values* are validated by the agent CLI
	// itself (version/model-dependent enums), not here.
	codexOpts := opts.Codex
	if bad := agent.UnsupportedOptionVars(codexOpts); len(bad) > 0 {
		return SessionState{}, fmt.Errorf("agent %q does not support option(s) %s: no matching option_args group", agentName, strings.Join(bad, ", "))
	}

	// Early validation that doesn't require the lock.
	if inPlace && noRepo {
		return SessionState{}, errors.New("--in-place and --no-repo are mutually exclusive")
	}

	if inPlace && mirror != "" {
		return SessionState{}, errors.New("--in-place and --mirror are mutually exclusive")
	}

	if inPlace && baseBranch != "" {
		return SessionState{}, errors.New("--in-place and --base are mutually exclusive (in-place sessions don't create branches)")
	}

	// --- Pre-lock: resolve repo root and discover GitHub username ---
	// These can involve network calls (gh api) and must not hold the mutex.
	var preRepoRoot string

	if !noRepo && mirror == "" && repoPath != "" {
		if !git.IsInsideGitRepo(repoPath) {
			if inPlace {
				return SessionState{}, fmt.Errorf("not inside a git repository: %s", repoPath)
			}

			return SessionState{}, fmt.Errorf("not inside a git repository: %s (use --no-repo for sessions without a repo)", repoPath)
		}

		var err error

		preRepoRoot, err = git.RepoRootPath(repoPath)
		if err != nil {
			return SessionState{}, fmt.Errorf("find repo root: %w", err)
		}
	}

	preUsername := cfgSnapshot.GitHubUsername
	if preUsername == "" && preRepoRoot != "" && !inPlace {
		ctx, cancel := context.WithTimeout(context.Background(), cfgSnapshot.Git.UsernameTimeoutDuration())
		preUsername, _ = git.DiscoverGitHubUsername(ctx, preRepoRoot)

		cancel()
	}

	if preUsername == "" {
		preUsername = "user"
	}

	// Validate a caller-supplied ID before taking the lock; uniqueness is
	// checked under the lock below, atomically with the reservation.
	if opts.ID != "" {
		if err := validateSessionID(opts.ID); err != nil {
			return SessionState{}, err
		}
	}

	// --- Phase 1: Lock, validate state, reserve session ---
	sm.mu.Lock()

	id := opts.ID
	if id == "" {
		id = sm.uniqueSessionIDLocked()
	} else if _, exists := sm.state.Sessions[id]; exists {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session id %q already in use", id)
	}

	token, err := generateToken()
	if err != nil {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("generate session token: %w", err)
	}

	var (
		repoRoot, repoName, worktreePath, branchName string
		isMirror                                     bool
		mirrorSourceID                               string
		fetchOnCreate                                bool
		rcIncludes                                   []string
		sourceIncludes                               []IncludedRepoState
	)

	switch {
	case mirror != "":
		var source *SessionState

		// Skip soft-deleted sessions: a hidden session must not be pickable as a
		// worktree source — its worktree is scheduled for purge.
		for _, s := range sm.state.Sessions {
			if s.IsSoftDeleted() {
				continue
			}

			if s.Name == mirror || s.ID == mirror {
				source = s
				break
			}
		}

		if source == nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("session %q not found for --mirror", mirror)
		}

		if source.WorktreePath == "" {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("session %q has no worktree to mirror", mirror)
		}

		worktreePath = source.WorktreePath
		repoRoot = source.RepoPath
		repoName = source.RepoName
		baseBranch = source.BaseBranch
		isMirror = true
		mirrorSourceID = source.ID
		sourceIncludes = make([]IncludedRepoState, len(source.Includes))
		copy(sourceIncludes, source.Includes)
	case noRepo:
		worktreePath = filepath.Join(sm.paths.DataDir, "scratch", id)
		if err := os.MkdirAll(worktreePath, 0o700); err != nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
		}
	case inPlace:
		repoRoot = preRepoRoot

		rc, ok := cfgSnapshot.FindRepo(repoRoot)
		if !ok {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("repo root %q is not configured in [[repos]] — add it to config to use --in-place", repoRoot)
		}

		if len(rc.Includes) > 0 || len(opts.Includes) > 0 {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("repo %q has includes configured — drop --in-place to create an includes session with worktrees", repoRoot)
		}

		if !allowConcurrent && !rc.AllowConcurrent {
			canonicalRoot := config.ResolvePath(repoRoot)
			for _, s := range sm.state.Sessions {
				if s.InPlace && config.ResolvePath(s.WorktreePath) == canonicalRoot && (s.Status == StatusRunning || s.Status == StatusCreating) {
					sm.mu.Unlock()
					return SessionState{}, fmt.Errorf("an in-place session %q is already running in %q — use --allow-concurrent to override", s.Name, repoRoot)
				}
			}
		}

		repoName = filepath.Base(repoRoot)
		worktreePath = repoRoot
	default:
		repoRoot = preRepoRoot

		if !cfgSnapshot.RepoPathAllowed(repoPath) {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("repo path %q is not under any allowed_repo_paths", repoPath)
		}

		rc, _ := cfgSnapshot.FindRepo(repoRoot)

		if rc.Singleton {
			canonicalRoot := config.ResolvePath(repoRoot)
			for _, s := range sm.state.Sessions {
				if config.ResolvePath(s.RepoPath) == canonicalRoot && (s.Status == StatusRunning || s.Status == StatusCreating) {
					sm.mu.Unlock()
					return SessionState{}, fmt.Errorf("repo %q has singleton = true and session %q is already running — stop it first", repoRoot, s.Name)
				}
			}
		}

		if baseBranch == "" {
			var err error

			baseBranch, err = git.DiscoverDefaultBranch(repoRoot)
			if err != nil {
				sm.mu.Unlock()
				return SessionState{}, err
			}
		}

		repoName = filepath.Base(repoRoot)

		branchPrefix, _ := config.Expand(cfgSnapshot.BranchPrefix, config.TemplateVars{Username: preUsername})
		branchName = fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

		sessionDir := filepath.Join(sm.paths.DataDir, "worktrees", repoName, repoHash(repoRoot), id)

		rcIncludes = mergeIncludes(rc.Includes, opts.Includes)

		// Per-session includes (e.g. from a scenario) don't go through
		// RepoConfig.Validate, so validate the merged set here for the same
		// collisions (self-include, duplicate basename, env-var clash) — otherwise
		// they'd surface as a confusing low-level git error mid worktree setup.
		if err := config.ValidateIncludes(repoRoot, rcIncludes); err != nil {
			sm.mu.Unlock()
			return SessionState{}, fmt.Errorf("invalid includes: %w", err)
		}

		if len(rcIncludes) > 0 {
			worktreePath = filepath.Join(sessionDir, repoName)
		} else {
			worktreePath = sessionDir
		}

		fetchOnCreate = cfgSnapshot.FetchOnCreate && !opts.NoFetch
	}

	agentSessionID := ""
	if cfgSnapshot.Agents[agentName].ForcesNativeID() {
		agentSessionID = newAgentSessionID()
	}

	// Resolve sandbox under the lock (reads config, fast).
	sandboxed, err := sm.resolveSandboxFromConfig(cfgSnapshot, agentName)
	if err != nil {
		if noRepo {
			_ = os.RemoveAll(worktreePath)
		}
		sm.mu.Unlock()

		return SessionState{}, err
	}

	// Fail closed if the configured approvals backend can't enforce.
	if err := sm.validateApprovalsBackendFromConfig(cfgSnapshot, yolo); err != nil {
		if noRepo {
			_ = os.RemoveAll(worktreePath)
		}
		sm.mu.Unlock()

		return SessionState{}, err
	}

	if isMirror && !sandboxed {
		sm.mu.Unlock()
		return SessionState{}, errors.New("--mirror requires sandbox to be enabled so the mirrored worktree can be mounted read-only; set sandbox.enabled = true in config and ensure safehouse is installed (gr doctor)")
	}

	// Yolo requires the PreToolUse approval hook to function, so it forces agent
	// hooks on regardless of the requested value — otherwise a Yolo session with
	// hooks disabled would auto-mark itself yolo but never install the hook that
	// routes tool calls to the auto backend.
	hooksEnabled := agentHooks || yolo

	// MCP config injection is now a mechanism distinct from lifecycle-hook
	// injection (injectMCPConfig vs injectHooks — see issue #1135). The policy
	// gates still coincide (mcpEnabled == hooksEnabled), so PTY behaviour is
	// unchanged and yolo still transitively governs MCP for now; the separate
	// variable is the seam a later headless phase widens to inject MCP without
	// generated hooks.
	mcpEnabled := hooksEnabled

	// Resolve MCP servers under the lock (reads config).
	var mcpServers []config.MCPServerConfig
	if mcpEnabled {
		mcpServers = sm.resolveMCPServersFromConfig(cfgSnapshot, agentName)
	}

	// Snapshot config values needed for Phase 2.
	sandboxMerged := cfgSnapshot.Sandbox.Merge(cfgSnapshot.Agents[agentName].Sandbox)

	// Reserve the session with StatusCreating so concurrent operations
	// (list, singleton checks) see it exists.
	placeholder := &SessionState{
		ID:              id,
		ParentID:        parentID,
		Name:            name,
		RepoPath:        repoRoot,
		RepoName:        repoName,
		WorktreePath:    worktreePath,
		Branch:          branchName,
		BaseBranch:      baseBranch,
		Agent:           agentName,
		AgentSessionID:  agentSessionID,
		NativeIDLocator: agent.NativeIDLocator(),
		Model:           model,
		Codex:           codexStatePtr(codexOpts),
		Mirror:          isMirror,
		MirrorSourceID:  mirrorSourceID,
		InPlace:         inPlace,
		AgentHooks:      hooksEnabled,
		Yolo:            yolo,
		TriggerID:       opts.TriggerID,
		TriggerReactor:  opts.TriggerReactor,
		TrackerIssue:    opts.TrackerIssue,
		AutoCleanup:     opts.AutoCleanup,
		IdleTimeoutSecs: opts.IdleTimeoutSecs,
		Status:          StatusCreating,
		CreatedAt:       time.Now().UTC(),
		StatusChangedAt: time.Now().UTC(),
		Token:           token,
	}

	sm.state.Sessions[id] = placeholder
	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)

		if noRepo {
			_ = os.RemoveAll(worktreePath)
		}
		sm.mu.Unlock()

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	sm.mu.Unlock()

	// --- Phase 2: External work (no lock held) ---
	// Git setup, hook injection, sandbox wrapping, PTY spawn.
	if sm.launchPhase2Hook != nil {
		sm.launchPhase2Hook("create", cfgSnapshot)
	}

	var includes []IncludedRepoState

	cleanupOnError := func() {
		sm.cleanupHooks(id, agentName, worktreePath)
		// A per-session nono profile may already have been written by
		// sandbox.Wrap before this error path ran; rollbackState deletes the
		// session from state, so no later Delete would remove it. Harmless if
		// no profile was written (os.Remove ignores a missing file).
		_ = os.Remove(sm.nonoProfilePath(id))
		_ = os.Remove(sm.safehouseFragmentPath(id))

		if isMirror || inPlace {
			return
		}

		switch {
		case noRepo:
			_ = os.RemoveAll(worktreePath)
		case len(includes) > 0:
			_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)
		case repoRoot != "":
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
	}
	rollbackState := func() {
		sm.mu.Lock()
		delete(sm.state.Sessions, id)
		_ = sm.saveState()
		sm.mu.Unlock()
	}

	// Git worktree setup (default path only — includes fetch which can block).
	if repoRoot != "" && !isMirror && !inPlace {
		gitCtx, gitCancel := context.WithTimeout(context.Background(), cfgSnapshot.Git.FetchTimeoutDuration())
		defer gitCancel()

		branchPrefix, _ := config.Expand(cfgSnapshot.BranchPrefix, config.TemplateVars{Username: preUsername})

		if len(rcIncludes) > 0 {
			if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
				rollbackState()
				return SessionState{}, fmt.Errorf("setup main repo git session: %w", err)
			}

			for _, incPath := range rcIncludes {
				resolved := config.ResolvePath(incPath)
				if !cfgSnapshot.RepoPathAllowed(resolved) {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("included repo %q is not under any allowed_repo_paths", incPath)
				}

				if !git.IsInsideGitRepo(resolved) {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("included repo %q is not a git repository", incPath)
				}

				incRoot, err := git.RepoRootPath(resolved)
				if err != nil {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("find included repo root for %q: %w", incPath, err)
				}

				incName := filepath.Base(incRoot)

				incBaseBranch, err := git.DiscoverDefaultBranchOrHEAD(incRoot)
				if err != nil {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("discover default branch for included repo %q: %w", incPath, err)
				}

				incBranch := fmt.Sprintf("%s/%s-%s/%s", branchPrefix, name, id, incName)
				sessionDir := filepath.Dir(worktreePath)
				incWorktreePath := filepath.Join(sessionDir, incName)

				if err := git.SetupSession(gitCtx, incRoot, incWorktreePath, incBranch, incBaseBranch, fetchOnCreate); err != nil {
					_ = sm.teardownIncludes(repoRoot, worktreePath, branchName, includes)

					rollbackState()

					return SessionState{}, fmt.Errorf("setup included repo %q: %w", incName, err)
				}

				includes = append(includes, IncludedRepoState{
					RepoPath:     incRoot,
					RepoName:     incName,
					WorktreePath: incWorktreePath,
					Branch:       incBranch,
					BaseBranch:   incBaseBranch,
				})
			}
		} else {
			if err := git.SetupSession(gitCtx, repoRoot, worktreePath, branchName, baseBranch, fetchOnCreate); err != nil {
				rollbackState()
				return SessionState{}, fmt.Errorf("setup git session: %w", err)
			}
		}

		// Post-create hook: rewrite absolute source-repo paths in known
		// orchestrator config files to the session's worktree paths, so an
		// includes orchestrator that hard-codes sibling paths (and can't use the
		// GRAITH_INCLUDE_*_PATH env vars) sees the session's code, not the main
		// checkout (#1033). Config files present in the worktrees are rewritten;
		// best-effort and never fatal.
		sm.applyIncludePathRewrites(repoRoot, worktreePath, includes)
	}

	// Build template vars, env, args, hooks, sandbox — all fast, no lock needed.
	vars := config.TemplateVars{
		Username:       preUsername,
		AgentSessionID: agentSessionID,
		SessionName:    name,
		SessionID:      id,
		WorktreePath:   worktreePath,
		Model:          model,
	}

	expandedArgs, err := config.ExpandSlice(agent.Args, vars)
	if err != nil {
		cleanupOnError()
		rollbackState()

		return SessionState{}, fmt.Errorf("expand agent args: %w", err)
	}

	driverKind, err := resolveDriverKind(opts.Headless, agent, cfgSnapshot.Headless, sandboxed)
	if err != nil {
		cleanupOnError()
		rollbackState()

		return SessionState{}, err
	}

	// A headless session needs a prompt to run one-shot. An explicit --headless
	// without one is an error; a headless preference coming only from [headless]
	// default yields to PTY (matching resolveDriverKind's soft-preference rule).
	if driverKind == DriverHeadless && prompt == "" {
		if opts.Headless {
			cleanupOnError()
			rollbackState()

			return SessionState{}, errors.New("headless session requires a prompt (-p)")
		}

		driverKind = DriverPTY
	}

	// Conditional option flags (model + typed Codex options) precede the
	// positional prompt so options come before it (issue #1186); the agent's
	// option_args config decides which are emitted (issue #1236), nil for agents
	// that declare none.
	optArgs, err := optionArgs(agent, vars, codexStatePtr(codexOpts))
	if err != nil {
		cleanupOnError()
		rollbackState()

		return SessionState{}, fmt.Errorf("expand agent option args: %w", err)
	}

	expandedArgs = append(expandedArgs, optArgs...)

	if driverKind == DriverHeadless {
		// The prompt is delivered as an initial stdin user message by the
		// headless driver (the control-channel launch takes no positional
		// prompt), so it is not appended to argv here.
		expandedArgs, err = headlessArgs(agent, vars, expandedArgs)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, fmt.Errorf("expand headless args: %w", err)
		}
	} else if prompt != "" {
		expandedArgs = append(expandedArgs, prompt)
	}

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

	if inPlace {
		env["GRAITH_IN_PLACE"] = "true"
	}

	for _, extra := range envExtra {
		for k, v := range extra {
			env[k] = v
		}
	}

	var storeDir string

	if repoRoot != "" {
		tmpDir, err := sm.repoTmpDir(repoRoot)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, err
		}

		env["GRAITH_TMPDIR"] = tmpDir
		if _, ok := env["TMPDIR"]; !ok {
			env["TMPDIR"] = tmpDir
		}

		storeDir, err = sm.repoStoreDir(repoRoot)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, err
		}
	}

	for _, inc := range includes {
		env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
	}

	if isMirror {
		for _, inc := range sourceIncludes {
			env[config.IncludeEnvVarName(inc.RepoName)] = inc.WorktreePath
		}
	}

	for _, extra := range envExtra {
		for k, v := range extra {
			env[k] = v
		}
	}

	// Headless sessions skip graith's generated status/approval hooks: the typed
	// stream is the status/approval feed.
	if hooksEnabled && driverKind != DriverHeadless {
		hookArgs, hookEnv, err := sm.injectHooksFromConfig(cfgSnapshot, agentName, id, worktreePath, yolo)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, fmt.Errorf("inject agent hooks: %w", err)
		}

		expandedArgs = append(expandedArgs, hookArgs...)

		for k, v := range hookEnv {
			env[k] = v
		}
	}

	// MCP config is injected by its own block, separate from the hook block above
	// (issue #1135). The gate (mcpEnabled) still tracks hooksEnabled for PTY, so
	// today this fires under the same condition as the hook block — the split is a
	// no-op for PTY. Widening mcpEnabled and dropping the headless guard (so a
	// hooks-disabled or headless session gets MCP) is a deliberate follow-up
	// (issue #1075).
	if mcpEnabled && driverKind != DriverHeadless {
		mcpArgs, err := sm.injectMCPConfigFromConfig(cfgSnapshot, agentName, id, mcpServers)
		if err != nil {
			cleanupOnError()
			rollbackState()

			return SessionState{}, fmt.Errorf("inject mcp config: %w", err)
		}

		expandedArgs = append(expandedArgs, mcpArgs...)
	}

	if agent.PromptInjectionEnabled() && driverKind != DriverHeadless {
		promptArgs, err := sm.injectPromptFromConfig(cfgSnapshot, agentName, worktreePath)
		if err != nil {
			sm.log.Warn("failed to inject prompt", "session_id", id, "err", err)
		} else {
			expandedArgs = append(expandedArgs, promptArgs...)
		}
	}

	// Make each included repo's co-located worktree visible to the agent via
	// --add-dir. For a mirror session the includes live on the source session
	// (its own git setup is skipped), so use those. Appended last — after any
	// positional prompt — because Claude's --add-dir is variadic and would
	// otherwise swallow a following prompt argument as another directory.
	effectiveIncludes := includes
	if isMirror {
		effectiveIncludes = sourceIncludes
	}

	addDirArgs, err := includeAddDirArgs(agent, vars, effectiveIncludes)
	if err != nil {
		cleanupOnError()
		rollbackState()

		return SessionState{}, fmt.Errorf("expand add-dir args: %w", err)
	}

	expandedArgs = append(expandedArgs, addDirArgs...)

	command := agent.Command
	finalArgs := expandedArgs

	var (
		scratchDir    string
		mergedSandbox *config.SandboxConfig
	)

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

		opts := sm.sandboxOptsFromConfig(merged, id, worktreePath, agent.Command, envKeys, hooksEnabled || mcpEnabled)
		if tmpDir := env["GRAITH_TMPDIR"]; tmpDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, tmpDir)
		}

		if storeDir != "" {
			opts.WriteDirs = append(opts.WriteDirs, storeDir)
		}

		opts.WriteDirs = append(opts.WriteDirs, store.SharedStorePath(sm.paths.DataDir))
		if len(includes) > 0 {
			opts.WriteDirs = append(opts.WriteDirs, sm.deriveSandboxIncludesWriteDirs(includes)...)
		}

		if isMirror {
			scratchDir = filepath.Join(sm.paths.DataDir, "scratch", id)
			if err := os.MkdirAll(scratchDir, 0o700); err != nil {
				cleanupOnError()
				rollbackState()

				return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
			}

			opts.ReadDirs = append(opts.ReadDirs, worktreePath)
			for _, inc := range sourceIncludes {
				opts.ReadDirs = append(opts.ReadDirs, inc.WorktreePath)
			}

			opts.WorktreeDir = scratchDir
		}

		var wrapErr error

		command, finalArgs, wrapErr = sandbox.Wrap(agent.Command, expandedArgs, opts)
		if wrapErr != nil {
			cleanupOnError()

			if scratchDir != "" {
				_ = os.RemoveAll(scratchDir)
			}

			rollbackState()

			return SessionState{}, fmt.Errorf("sandbox wrap: %w", wrapErr)
		}

		sm.log.Info("sandboxing session", "id", id, "agent", agentName,
			"command", command, "read_dirs", opts.ReadDirs, "write_dirs", opts.WriteDirs,
			"unix_sockets", opts.UnixSockets,
			"features", opts.Features, "workdir", opts.WorktreeDir)
	}

	// Throttle concurrent launches so a burst doesn't stampede (#1092). The slot
	// is held across the agent's startup window and released on first output or a
	// settle timeout (releaseLaunchSlotWhenSettled), or immediately on spawn error.
	slot, err := sm.acquireLaunchSlot(context.Background(), id, name)
	if err != nil {
		cleanupOnError()

		if scratchDir != "" {
			_ = os.RemoveAll(scratchDir)
		}

		rollbackState()

		return SessionState{}, fmt.Errorf("acquire launch slot: %w", err)
	}

	// Record the pre-spawn time so native session-id capture only matches
	// transcript files this start creates (race-safe against stale rollouts).
	startedAt := time.Now()

	var ptySess SessionDriver

	lc := cfgSnapshot.Lifecycle

	if driverKind == DriverHeadless {
		ptySess, err = headless.New(headless.Opts{
			ID:               id,
			Command:          command,
			Args:             finalArgs,
			Dir:              worktreePath,
			Env:              env,
			LogPath:          logPath,
			MaxLogSize:       lc.MaxLogBytesOrDefault(),
			Prompt:           prompt,
			Control:          true,
			OnPermission:     sm.headlessPermissionFunc(id),
			MaxLineBytes:     cfgSnapshot.Headless.MaxLineBytesOrDefault(),
			ControlTimeout:   cfgSnapshot.Headless.ControlTimeoutDuration(),
			InterruptTimeout: cfgSnapshot.Headless.InterruptTimeoutDuration(),
			PreviewBytes:     cfgSnapshot.Headless.PreviewBytesOrDefault(),
		})
	} else {
		ptySess, err = grpty.NewSession(grpty.SessionOpts{
			ID:         id,
			Command:    command,
			Args:       finalArgs,
			Dir:        worktreePath,
			Env:        env,
			Rows:       rows,
			Cols:       cols,
			LogPath:    logPath,
			MaxLogSize: lc.MaxLogBytesOrDefault(),
			InputDelay: lc.InputDelayDuration(),
			Logger:     sm.log,
		})
	}

	if err != nil {
		slot.release()
		cleanupOnError()

		if scratchDir != "" {
			_ = os.RemoveAll(scratchDir)
		}

		rollbackState()

		return SessionState{}, fmt.Errorf("start %s session: %w", driverKind, err)
	}

	sm.releaseLaunchSlotWhenSettled(slot, id, name, ptySess)

	// --- Phase 3: Lock, commit to running ---
	sm.mu.Lock()

	// Check the session wasn't deleted while we were setting up.
	if _, ok := sm.state.Sessions[id]; !ok {
		sm.mu.Unlock()

		sm.logStopping(id, name, "rollback", "create-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()
		cleanupOnError()

		if scratchDir != "" {
			_ = os.RemoveAll(scratchDir)
		}

		_ = os.Remove(logPath)

		return SessionState{}, errors.New("session was deleted during creation")
	}

	sessState := sm.state.Sessions[id]
	sessState.Sandboxed = sandboxed
	sessState.SandboxConfig = mergedSandbox
	sessState.Includes = includes
	sessState.DriverKind = driverKind
	sessState.Status = StatusRunning
	sessState.StatusChangedAt = time.Now()

	if opts.Starred {
		sessState.Starred = true
	}

	sessState.PID = ptySess.ProcessPID()
	if st, err := grpty.ProcessStartTime(sessState.PID); err == nil {
		sessState.PIDStartTime = st
	}

	sessState.CreationCfg = &CreationConfig{
		Agent:         agent,
		SandboxConfig: cfgSnapshot.Sandbox.Merge(agent.Sandbox),
	}

	sm.sessions[id] = ptySess
	sm.tokenIndex[token] = id

	if sessState.ParentID != "" {
		if parent, ok := sm.state.Sessions[sessState.ParentID]; ok {
			applyLifecycleSummaryLocked(sessState, "Created by "+parent.Name)
		}
	}

	if err := sm.saveState(); err != nil {
		delete(sm.state.Sessions, id)
		delete(sm.sessions, id)
		delete(sm.tokenIndex, token)
		sm.mu.Unlock()

		sm.logStopping(id, name, "rollback", "create-rollback", ptySess)
		_ = ptySess.Kill()
		ptySess.Close()
		cleanupOnError()

		if scratchDir != "" {
			_ = os.RemoveAll(scratchDir)
		}

		sm.cleanupHooks(id, agentName, worktreePath)

		_ = os.Remove(logPath)

		return SessionState{}, fmt.Errorf("persist session state: %w", err)
	}

	result := cloneSessionState(sessState)
	sm.mu.Unlock()

	// Record the fresh-session spawn symmetrically with "resume: pty spawned"
	// (issue #1104): pid/pgid for OS-level signal forensics, sandboxed so a
	// later peak-RSS reading can be interpreted against the wrapper.
	sm.log.Info("session spawned",
		"id", id, "name", name, "agent", agentName,
		"pid", result.PID, "pgid", ptySess.Pgid(), "sandboxed", sandboxed,
		"scrollback_path", logPath)

	sm.startWatcher(id, ptySess)

	// Best-effort native session-id capture for self-minting agents (Codex):
	// graith didn't force the id, so read it from the agent's on-disk state so
	// later resume is deterministic. Skipped when the id was forced (agentSessionID
	// non-empty). Uses the session's effective state root (e.g. CODEX_HOME) and the
	// agent's config-declared locator, so a custom alias scrapes like the built-in.
	if agent := cfgSnapshot.Agents[agentName]; agent.ScrapesNativeID() && agentSessionID == "" {
		go sm.captureNativeSessionID(id, agentName, agent.NativeIDLocator(), worktreePath, env["CODEX_HOME"], startedAt, result.PID, result.PIDStartTime)
	}

	return result, nil
}
