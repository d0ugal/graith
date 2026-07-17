package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/approvals"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/tools"
)

// ReloadConfig loads the config from disk and swaps it in, logging what changed.
func (sm *SessionManager) ReloadConfig() error {
	cfg, err := config.LoadOrDefault(sm.configFile)
	if err != nil {
		return err
	}

	return sm.applyConfig(cfg)
}

func (sm *SessionManager) applyConfig(newCfg *config.Config) error {
	// Manual reload and the fsnotify watcher can fire together. Serialize the
	// complete reserve/prepare/publish sequence so neither can prepare a remote
	// generation against a stale old config and then overwrite the other.
	sm.configApplyMu.Lock()
	defer sm.configApplyMu.Unlock()

	sm.mu.RLock()
	old := sm.cfg
	sm.mu.RUnlock()

	if newCfg.DataDir != old.DataDir {
		return fmt.Errorf("data_dir changed from %q to %q: run 'gr daemon restart' to apply", old.DataDir, newCfg.DataDir)
	}

	// Remote transport preparation performs network/filesystem work without
	// sm.mu. It first closes the old generation, then synchronously binds the
	// replacement. A failure leaves the remote surface off and aborts config
	// publication, so config introspection never advertises an unapplied runtime.
	remoteChange, err := sm.prepareRemoteConfig(old.Remote, newCfg.Remote)
	if err != nil {
		return err
	}

	sm.mu.Lock()
	sm.cfg = newCfg
	remoteChange.publishLocked(sm)
	// Resize the launch throttle under the same lock that publishes the config so
	// the two can't diverge if two reloads (fsnotify + SIGHUP) interleave — the
	// live limit always matches the currently-published cfg. resize only takes the
	// throttle's own mutex, so the sm.mu -> launch.mu order introduces no cycle.
	if sm.launch != nil {
		sm.launch.resize(newCfg.Launch.MaxConcurrentOrDefault())
	}
	sm.mu.Unlock()

	remoteChange.commit(sm, newCfg.Remote)

	// Re-install the external-tool resolver so a changed [tools] block takes
	// effect on reload without a daemon restart (git timeouts are read live from
	// sm.cfg, so they need no explicit re-apply). The resolver is process-global,
	// so this runs outside sm.mu.
	if old.Tools != newCfg.Tools {
		tools.Configure(newCfg.Tools.Resolved())
		sm.log.Info("config changed", "key", "tools")
	}

	// Re-apply the transcript scanner buffer caps so a changed [transcript] block
	// takes effect on reload without a daemon restart (issue #1250). Like the
	// tools resolver, the caps are process-global, so this runs outside sm.mu.
	if old.Transcript != newCfg.Transcript {
		transcript.Configure(newCfg.Transcript.MaxLineBytesOrDefault(), newCfg.Transcript.MaxMetadataLineBytesOrDefault())
		sm.log.Info("config changed", "key", "transcript")
	}

	if old.DefaultAgent != newCfg.DefaultAgent {
		sm.log.Info("config changed", "key", "default_agent", "old", old.DefaultAgent, "new", newCfg.DefaultAgent)
	}

	if old.BranchPrefix != newCfg.BranchPrefix {
		sm.log.Info("config changed", "key", "branch_prefix", "old", old.BranchPrefix, "new", newCfg.BranchPrefix)
	}

	if old.FetchOnCreate != newCfg.FetchOnCreate {
		sm.log.Info("config changed", "key", "fetch_on_create", "old", old.FetchOnCreate, "new", newCfg.FetchOnCreate)
	}

	if old.GitHubUsername != newCfg.GitHubUsername {
		sm.log.Info("config changed", "key", "github_username", "old", old.GitHubUsername, "new", newCfg.GitHubUsername)
	}

	if old.Keybindings != newCfg.Keybindings {
		sm.log.Info("config changed", "key", "keybindings")
	}

	if old.Notifications != newCfg.Notifications {
		sm.log.Info("config changed", "key", "notifications")
	}

	for name, agent := range newCfg.Agents {
		if oldAgent, ok := old.Agents[name]; !ok {
			sm.log.Info("config changed", "key", "agents", "action", "added", "agent", name)
		} else if oldAgent.Command != agent.Command || oldAgent.IdleTimeout != agent.IdleTimeout {
			sm.log.Info("config changed", "key", "agents", "action", "modified", "agent", name)
		}
	}

	for name := range old.Agents {
		if _, ok := newCfg.Agents[name]; !ok {
			sm.log.Info("config changed", "key", "agents", "action", "removed", "agent", name)
		}
	}

	if old.GitPull.Enabled != newCfg.GitPull.Enabled {
		sm.log.Info("config changed", "key", "git_pull.enabled", "old", old.GitPull.Enabled, "new", newCfg.GitPull.Enabled)
	}

	if old.GitPull.Interval != newCfg.GitPull.Interval {
		sm.log.Info("config changed", "key", "git_pull.interval", "old", old.GitPull.Interval, "new", newCfg.GitPull.Interval)
	}

	// The throttle was already resized atomically with the cfg swap above; here we
	// only log the change for observability.
	if oldMax, newMax := old.Launch.MaxConcurrentOrDefault(), newCfg.Launch.MaxConcurrentOrDefault(); oldMax != newMax {
		sm.log.Info("config changed", "key", "launch.max_concurrent", "old", oldMax, "new", newMax)
	}

	if old.Launch.StartupTimeout != newCfg.Launch.StartupTimeout {
		sm.log.Info("config changed", "key", "launch.startup_timeout", "old", old.Launch.StartupTimeout, "new", newCfg.Launch.StartupTimeout)
	}

	if old.Launch.SettleTimeout != newCfg.Launch.SettleTimeout {
		sm.log.Info("config changed", "key", "launch.settle_timeout", "old", old.Launch.SettleTimeout, "new", newCfg.Launch.SettleTimeout)
	}

	if old.Sandbox.Enabled != newCfg.Sandbox.Enabled {
		sm.log.Info("config changed", "key", "sandbox.enabled", "old", old.Sandbox.Enabled, "new", newCfg.Sandbox.Enabled)
	}

	if fmt.Sprint(old.Sandbox.ReadDirs) != fmt.Sprint(newCfg.Sandbox.ReadDirs) {
		sm.log.Info("config changed", "key", "sandbox.read_dirs", "old", old.Sandbox.ReadDirs, "new", newCfg.Sandbox.ReadDirs)
	}

	if fmt.Sprint(old.Sandbox.WriteDirs) != fmt.Sprint(newCfg.Sandbox.WriteDirs) {
		sm.log.Info("config changed", "key", "sandbox.write_dirs", "old", old.Sandbox.WriteDirs, "new", newCfg.Sandbox.WriteDirs)
	}

	if fmt.Sprint(old.Sandbox.ReadFiles) != fmt.Sprint(newCfg.Sandbox.ReadFiles) {
		sm.log.Info("config changed", "key", "sandbox.read_files", "old", old.Sandbox.ReadFiles, "new", newCfg.Sandbox.ReadFiles)
	}

	if fmt.Sprint(old.Sandbox.WriteFiles) != fmt.Sprint(newCfg.Sandbox.WriteFiles) {
		sm.log.Info("config changed", "key", "sandbox.write_files", "old", old.Sandbox.WriteFiles, "new", newCfg.Sandbox.WriteFiles)
	}

	if fmt.Sprint(old.Sandbox.Features) != fmt.Sprint(newCfg.Sandbox.Features) {
		sm.log.Info("config changed", "key", "sandbox.features", "old", old.Sandbox.Features, "new", newCfg.Sandbox.Features)
	}

	if sm.mcpManager != nil {
		sm.mcpManager.Reload(newCfg)
		sm.log.Info("MCP manager config reloaded")
	}

	// Push the hot-reloadable jail/todo enforcement limits into the live stores so
	// the next operation observes the new caps without reopening the databases
	// (issue #1291). The setters take the stores' own atomics/mutex and clamp to
	// the database hard ceilings, so they run outside sm.mu and can never widen a
	// limit past what the schema accepts.
	if sm.messages != nil {
		if oldLimit, newLimit := old.Messages.JailListLimitOrDefault(), newCfg.Messages.JailListLimitOrDefault(); oldLimit != newLimit {
			sm.log.Info("config changed", "key", "messages.jail_list_limit", "old", oldLimit, "new", newLimit)
		}

		sm.messages.SetJailListLimit(newCfg.Messages.JailListLimitOrDefault())
	}

	if sm.todos != nil {
		if oldTitle, newTitle := old.Todo.MaxTitleOrDefault(), newCfg.Todo.MaxTitleOrDefault(); oldTitle != newTitle {
			sm.log.Info("config changed", "key", "todo.max_title", "old", oldTitle, "new", newTitle)
		}

		if oldNote, newNote := old.Todo.MaxNoteOrDefault(), newCfg.Todo.MaxNoteOrDefault(); oldNote != newNote {
			sm.log.Info("config changed", "key", "todo.max_note", "old", oldNote, "new", newNote)
		}

		sm.todos.SetMaxTitle(newCfg.Todo.MaxTitleOrDefault())
		sm.todos.SetMaxNote(newCfg.Todo.MaxNoteOrDefault())
	}

	// Push a reloaded [lifecycle] input_delay into every live PTY so the next
	// gr type observes it, matching the documented read-at-each-use contract
	// (issue #1294). Snapshot the drivers under the read lock, then update them
	// outside sm.mu: SetInputDelay briefly contends the PTY write mutex (held
	// across the input-submit pause), and blocking on it under the
	// session-manager lock would violate the no-slow-work-under-lock invariant.
	// Headless drivers have no submit pause and don't implement the setter, so
	// the type assertion simply skips them.
	if old.Lifecycle.InputDelayDuration() != newCfg.Lifecycle.InputDelayDuration() {
		delay := newCfg.Lifecycle.InputDelayDuration()

		sm.mu.RLock()

		drivers := make([]SessionDriver, 0, len(sm.sessions))
		for _, d := range sm.sessions {
			drivers = append(drivers, d)
		}

		sm.mu.RUnlock()

		for _, d := range drivers {
			if setter, ok := d.(interface{ SetInputDelay(d time.Duration) }); ok {
				setter.SetInputDelay(delay)
			}
		}

		sm.log.Info("config changed", "key", "lifecycle.input_delay",
			"old", old.Lifecycle.InputDelayDuration(), "new", delay)
	}

	// Retune existing PR ref watchers in place. A pending ref event is retimed
	// from its original last-change timestamp, so neither shortening nor
	// lengthening the debounce drops the kick (issue #1308).
	if old.PRWatch.RefDebounceDuration() != newCfg.PRWatch.RefDebounceDuration() {
		sm.updateActivePRRefWatcherDebounce(newCfg)
		sm.log.Info("config changed", "key", "pr_watch.advanced.ref_debounce",
			"old", old.PRWatch.RefDebounceDuration(), "new", newCfg.PRWatch.RefDebounceDuration())
	}

	oldBuiltinIgnores := old.TriggersRuntime.WatchBuiltinIgnores()

	newBuiltinIgnores := newCfg.TriggersRuntime.WatchBuiltinIgnores()
	if !slices.Equal(oldBuiltinIgnores, newBuiltinIgnores) {
		// Update each live matcher and its directory registrations in place. The
		// binding keeps receiving fsnotify events while its event loop is briefly
		// gated, avoiding the teardown/recreate blind spot (issue #1309).
		sm.updateActiveWatchBuiltinIgnores(newCfg)
		sm.log.Info("config changed", "key", "triggers.advanced.watch_builtin_ignores",
			"old", oldBuiltinIgnores, "new", newBuiltinIgnores)
	}

	// If the PR-comment author-trust config changed, re-evaluate jailed comments
	// against the new config and auto-release any whose author is now trusted
	// (issue #1082). A config reload is a local-human action, so this release is
	// implicitly human-authorized. Run detached: it hits the message DB and may
	// auto-resume stopped sessions, which must outlive the reload request.
	if prWatchTrustChanged(old.PRWatch, newCfg.PRWatch) {
		sm.log.Info("config changed", "key", "pr_watch.comment_trust")

		// autoReleaseNewlyTrusted reads the current config itself (sm.cfg was set
		// above), so a later reload that tightens trust wins over this worker.
		go sm.autoReleaseNewlyTrusted()
	}

	if old.Orchestrator.Enabled != newCfg.Orchestrator.Enabled {
		if newCfg.Orchestrator.Enabled {
			sm.log.Info("config changed", "key", "orchestrator.enabled", "old", false, "new", true)
			go sm.ensureOrchestrator(context.Background())
		} else if orchID := func() string {
			sm.mu.RLock()
			defer sm.mu.RUnlock()

			return sm.findOrchestratorID()
		}(); orchID != "" {
			// Do not discard the stop result: if signalling the orchestrator fails
			// the session can keep running, so the published "disabled" config would
			// diverge from the live runtime (issue #1324). Roll the orchestrator flag
			// back to enabled — coherent with the still-running session — surface the
			// error to a manual reload, and let a later reload retry the stop.
			if stopErr := sm.stopWithReason(orchID, StopReasonUser, "orchestrator-disabled"); stopErr != nil {
				sm.mu.Lock()
				reverted := *sm.cfg
				reverted.Orchestrator.Enabled = old.Orchestrator.Enabled
				sm.cfg = &reverted
				sm.mu.Unlock()

				sm.log.Error("orchestrator disable failed; left marked enabled for retry",
					"key", "orchestrator.enabled", "session", orchID, "err", stopErr)

				return fmt.Errorf("disable orchestrator: session %q did not stop: %w", orchID, stopErr)
			}

			sm.log.Info("config changed", "key", "orchestrator.enabled", "old", true, "new", false)
		} else {
			sm.log.Info("config changed", "key", "orchestrator.enabled", "old", true, "new", false)
		}
	}

	return nil
}

func (sm *SessionManager) teardownIncludes(mainRepoPath, mainWorktreePath, mainBranch string, includes []IncludedRepoState) error {
	var errs []error

	for i := len(includes) - 1; i >= 0; i-- {
		inc := includes[i]
		if err := git.TeardownSession(inc.RepoPath, inc.WorktreePath, inc.Branch); err != nil {
			sm.log.Warn("failed to teardown included worktree", "repo", inc.RepoName, "path", inc.WorktreePath, "err", err)
			errs = append(errs, err)
		}
	}

	if err := git.TeardownSession(mainRepoPath, mainWorktreePath, mainBranch); err != nil {
		sm.log.Warn("failed to teardown main worktree", "path", mainWorktreePath, "err", err)
		errs = append(errs, err)
	}

	if len(includes) > 0 {
		if err := os.RemoveAll(filepath.Dir(mainWorktreePath)); err != nil {
			sm.log.Warn("failed to remove session directory", "path", filepath.Dir(mainWorktreePath), "err", err)
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (sm *SessionManager) deriveSandboxIncludesWriteDirs(includes []IncludedRepoState) []string {
	var dirs []string
	for _, inc := range includes {
		dirs = append(dirs, inc.WorktreePath)

		gitDir, commonDir, err := git.WorktreeGitDirs(inc.WorktreePath)
		if err != nil {
			sm.log.Warn("failed to resolve git dirs for included repo", "repo", inc.RepoName, "err", err)
			continue
		}

		dirs = append(dirs, gitDir, commonDir)
	}

	return dirs
}

// includeAddDirArgs builds the add-directory flags that make each included
// repo's co-located worktree visible to the agent at launch. The flag template
// comes from the agent's add_dir_args config (see config.Agent.AddDirArgsFor):
// an agent whose CLI has no such flag leaves it unset and gets nothing, so a
// repo's includes never inject an unknown flag into an agent that would reject
// it and fail to launch. Included worktrees are still exposed via the
// GRAITH_INCLUDE_*_PATH env vars for every agent regardless. Worktrees without a
// path are skipped defensively; the result is nil (not an empty slice) when
// nothing is emitted.
func includeAddDirArgs(agent config.Agent, vars config.TemplateVars, includes []IncludedRepoState) ([]string, error) {
	dirs := make([]string, 0, len(includes))
	for _, inc := range includes {
		dirs = append(dirs, inc.WorktreePath)
	}

	return agent.AddDirArgsFor(vars, dirs)
}

// codexOptsForAgent enforces the codex-only invariant when a lifecycle op
// changes a session's agent: it returns opts unchanged for a codex target and
// nil otherwise, so codex-only options never persist on a non-codex session
// (a cross-agent fork or a migrate away from codex). The create path enforces
// the same rule up front with an explicit error; fork/migrate silently drop
// them because the options belonged to the source agent, not a user choice for
// the new one. Keeps state consistent with what Create would accept (#1186).
func codexOptsForAgent(agentType string, opts *config.CodexOptions) *config.CodexOptions {
	if agentType != "codex" {
		return nil
	}

	return opts
}

// cloneCodexOptions returns an independent copy of opts (or nil), so a fork's or
// resume's persisted options don't alias the source session's struct.
func cloneCodexOptions(opts *config.CodexOptions) *config.CodexOptions {
	if opts == nil {
		return nil
	}

	o := *opts

	return &o
}

// codexStatePtr returns a heap copy of opts for persisting on SessionState, or
// nil when nothing is set so a non-codex (or option-less) session stores no
// `codex` block.
func codexStatePtr(opts config.CodexOptions) *config.CodexOptions {
	if opts.IsZero() {
		return nil
	}

	o := opts

	return &o
}

// codexOptsFromMsg flattens the wire pointer to a value for CreateOpts, treating
// nil as "no options set".
func codexOptsFromMsg(opts *config.CodexOptions) config.CodexOptions {
	if opts == nil {
		return config.CodexOptions{}
	}

	return *opts
}

// optionArgs builds an agent's conditional launch flags from its option_args
// config, folding the session's model and typed Codex options (issue #1186) into
// the template variables the groups gate on and expand. Each group is emitted
// only when its `when` variable is set, so an unset option leaves the agent's own
// default untouched — the reason these can't just live as `{model}` templates in
// the base args (an empty model would expand to a literal `--model ""`). A
// non-codex agent's opts are nil, so only its model rides through; agents with no
// option_args yield nil, making this safe to call unconditionally on every launch
// path (create/resume/fork). Codex's option_args are accepted on the bare
// invocation and on the `resume`/`fork` subcommands, so appending them after the
// existing args is valid (issue #1236).
func optionArgs(agent config.Agent, vars config.TemplateVars, opts *config.CodexOptions) ([]string, error) {
	if opts != nil {
		vars.Profile = opts.Profile
		vars.ReasoningEffort = opts.ReasoningEffort
		vars.ServiceTier = opts.ServiceTier
		vars.ApprovalPolicy = opts.ApprovalPolicy
		vars.WebSearch = opts.WebSearch
	}

	return agent.OptionArgsFor(vars)
}

// resumeIncludeSet picks the includes a resuming session should re-grant (both
// as GRAITH_INCLUDE_*_PATH env vars and --add-dir flags). A mirror session
// persists none of its own — its git setup is skipped at create — so it takes
// the source session's includes (snapshotted as sharedSourceIncludes). Every
// other session uses its own. This keeps a mirror's sibling visibility across a
// restart, matching how Create seeds a mirror from the source's includes.
func resumeIncludeSet(mirror bool, sessIncludes, sharedSourceIncludes []IncludedRepoState) []IncludedRepoState {
	if mirror {
		return sharedSourceIncludes
	}

	return sessIncludes
}

func (sm *SessionManager) resolveSandbox(agentName string) (bool, error) {
	return sm.resolveSandboxFromConfig(sm.Config(), agentName)
}

// approvalsConfigDir returns the directory holding graith's config file, used to
// resolve a relative [approvals.builtin] config path deterministically (rather
// than against the daemon's working directory). It prefers the explicit global
// --config override (sm.configFile, the file the daemon actually loaded) over
// the default resolved path, mirroring the CLI's approvalsConfigDir so
// `gr --config X approvals validate` and daemon enforcement resolve a relative
// path against the same directory. Returns "" when no config path is known, in
// which case a relative path is left for the caller to resolve against the
// working directory as before.
func (sm *SessionManager) approvalsConfigDir() string {
	if f := strings.TrimSpace(sm.configFile); f != "" {
		return filepath.Dir(f)
	}

	if sm.paths.ConfigFile == "" {
		return ""
	}

	return filepath.Dir(sm.paths.ConfigFile)
}

// validateApprovalsBackend fails closed at session-create when the configured
// approvals backend can't enforce — a command backend with no command, a
// missing localmost binary, or an unreadable/invalid builtin config. This
// mirrors the sandbox availability check (resolveSandboxFromConfig) so a
// misconfigured approvals backend errors loudly at create time instead of
// silently deferring every request to the human. The default (prompt) backend
// always enforces. Lifecycle callers pass their immutable config snapshot to
// validateApprovalsBackendFromConfig so later launch phases cannot mix reload
// generations.
//
// A yolo session resolves every request through the auto backend, which always
// enforces, so the global [approvals] backend is irrelevant to it — validating
// (and failing on) an unavailable global backend would contradict yolo's
// per-session override. Yolo sessions therefore skip the global check.
func (sm *SessionManager) validateApprovalsBackend(yolo bool) error {
	return sm.validateApprovalsBackendFromConfig(sm.Config(), yolo)
}

func (sm *SessionManager) validateApprovalsBackendFromConfig(cfg *config.Config, yolo bool) error {
	if yolo {
		return nil
	}

	acfg := cfg.Approvals

	backend, _, err := acfg.ResolveBackend()
	if err != nil {
		return err
	}

	if backend == "" || backend == approvals.BackendPrompt {
		return nil
	}

	be, err := approvals.BackendByName(backend)
	if err != nil {
		return err
	}

	beCfg, err := approvalsBackendConfig(backend, acfg, sm.approvalsConfigDir())
	if err != nil {
		return err
	}

	if av := be.Availability(beCfg); !av.CanEnforce {
		return fmt.Errorf("approvals backend %q cannot enforce: %s", backend, av.Detail)
	}

	return nil
}

func (sm *SessionManager) resolveSandboxFromConfig(cfg *config.Config, agentName string) (bool, error) {
	if sm.resolveSandboxTest != nil {
		return sm.resolveSandboxTest(cfg, agentName)
	}

	merged := cfg.Sandbox.Merge(cfg.Agents[agentName].Sandbox)
	if !merged.Enabled {
		return false, nil
	}

	avail, err := validateSandboxBackend(merged, fmt.Sprintf("agent %q", agentName))
	if err != nil {
		return false, err
	}

	if avail.Degraded {
		sm.log.Warn("sandbox enforcement degraded", "agent", agentName, "backend", merged.Backend, "detail", avail.Detail)
	}

	return true, nil
}

func (sm *SessionManager) wrapSessionCommand(command string, args []string, opts sandbox.WrapOpts) (string, []string, error) {
	if sm.sandboxWrapTest != nil {
		return sm.sandboxWrapTest(command, args)
	}

	return sandbox.Wrap(command, args, opts)
}

// validateSandboxBackend enforces the explicit-backend rule and availability
// check for an already-enabled merged sandbox config, returning the resolved
// availability on success. subject names the process being sandboxed (e.g.
// `agent "claude"` or `MCP server chrome`) and is interpolated into the
// fail-closed errors. It is shared by the session (resolveSandboxFromConfig)
// and MCP-server (MCPManager.startProcess) startup paths so both fail closed
// identically — in particular, neither may silently fall back to safehouse
// when no backend is selected (see #787). sandbox.Wrap keeps its empty-backend
// compatibility only for low-level helpers that don't represent user config.
func validateSandboxBackend(merged config.SandboxConfig, subject string) (sandbox.Availability, error) {
	// Backend must be chosen explicitly — there is no default. Fail closed with
	// an actionable error rather than silently picking one.
	if merged.Backend == "" {
		return sandbox.Availability{}, fmt.Errorf(
			"sandbox enabled for %s but no backend selected — set [sandbox] backend = %q (macOS) or %q (Linux/macOS) in config",
			subject, sandbox.BackendSafehouse, sandbox.BackendNono)
	}

	req := sandbox.Requirements{Network: merged.Network.IsSet()}

	avail, err := sandbox.CheckAvailability(merged.Backend, merged.Command, req)
	if err != nil {
		return sandbox.Availability{}, fmt.Errorf("sandbox enabled for %s: %w", subject, err)
	}

	if !avail.CanEnforce {
		return sandbox.Availability{}, fmt.Errorf(
			"sandbox enabled for %s with backend %q but it cannot enforce: %s",
			subject, merged.Backend, avail.Detail)
	}

	return avail, nil
}

// nonoProfilePath returns the location of the per-session nono sandbox profile
// for the given session ID. The nono backend writes this file under RuntimeDir
// (see sandboxOptsFromConfig); session teardown removes it here so the small
// JSON files don't accumulate for the lifetime of the daemon's data dir.
func (sm *SessionManager) nonoProfilePath(sessionID string) string {
	return filepath.Join(sm.paths.RuntimeDir, "nono", sessionID+".json")
}

// resolveSocketPath returns the symlink-resolved daemon socket path. Seatbelt
// and Landlock match canonical (symlink-resolved) paths, but sm.paths.SocketPath
// comes from filepath.Join and is not resolved — so a data/runtime dir under a
// symlinked prefix (e.g. macOS /tmp -> /private/tmp, /var -> /private/var) would
// make the sandbox grant's path-literal miss and silently re-deny the connect,
// reintroducing the original bug with a green test. Resolve here, at the single
// choke point, so every backend gets the canonical path. Falls back to resolving
// the parent dir + basename (the socket's own inode is a live AF_UNIX node), then
// to the raw path if resolution fails (e.g. before the socket file exists).
func resolveSocketPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}

	dir, base := filepath.Split(p)
	if rdir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(rdir, base)
	}

	return p
}

// safehouseFragmentPath returns the location of the per-session safehouse
// Seatbelt fragment (the --append-profile file that grants the daemon socket
// connect access) for the given session ID. Written under RuntimeDir by the
// safehouse backend; session teardown removes it alongside the nono profile.
func (sm *SessionManager) safehouseFragmentPath(sessionID string) string {
	return filepath.Join(sm.paths.RuntimeDir, "safehouse", sessionID+".sb")
}

func (sm *SessionManager) sandboxOptsFromConfig(merged config.SandboxConfig, sessionID, worktreePath, agentCommand string, envKeys []string, grantHookDir bool) sandbox.WrapOpts {
	readDirs := expandPaths(merged.ReadDirs, sm.log, "read")
	writeDirs := expandPaths(merged.WriteDirs, sm.log, "write")
	readFiles := expandFilePaths(merged.ReadFiles, sm.log, "read")
	writeFiles := expandFilePaths(merged.WriteFiles, sm.log, "write")

	// The hook dir holds both the generated settings (hooks) file and the MCP
	// config file, so grant it read whenever either was injected (see #1135).
	if grantHookDir {
		hd := sm.hookDir(sessionID)
		if _, err := os.Stat(hd); err == nil {
			readDirs = append(readDirs, hd)
		}
	}

	readDirs = append(readDirs, filepath.Dir(sm.paths.ConfigFile))
	if dir, ok := grBinReadDir(resolveGrBin()); ok {
		readDirs = append(readDirs, dir)
	}

	readDirs = append(readDirs, sm.paths.RuntimeDir)

	// The runtime dir grant above is read-only, which lets the agent see the
	// daemon socket but NOT connect() to it (Seatbelt/Landlock gate socket
	// connect separately from file read). Grant the socket explicitly so
	// sandboxed agents can reach the daemon for `gr msg`, `gr status`, etc.
	// This is scoped to the single socket file, not the whole runtime/data dir.
	// The path is symlink-resolved (see resolveSocketPath): Seatbelt/Landlock
	// match canonical paths, so a data/runtime dir under a symlinked prefix
	// would otherwise make the grant's path-literal miss and silently re-deny.
	unixSockets := []string{resolveSocketPath(sm.paths.SocketPath)}

	// nono does not auto-grant the launched command's location (only system
	// paths like /usr/bin). Grant read on the agent binary's directory so the
	// sandboxed process can exec it. safehouse is unaffected by the extra dir.
	if dir := agentBinaryDir(agentCommand); dir != "" {
		readDirs = append(readDirs, dir)
	}

	// Under nono, a non-empty env allowlist scrubs everything else, so the vars
	// the agent needs to function (PATH, HOME) must be present. safehouse
	// re-adds these itself; including them in EnvKeys is harmless there.
	envKeys = ensureEnvKeys(envKeys, "PATH", "HOME")

	// The nono backend writes a per-session profile under RuntimeDir, which is
	// already granted read access above, so the profile is readable inside the
	// sandbox and lives for the process lifetime (incl. resume). The safehouse
	// backend likewise writes its --append-profile socket fragment under
	// RuntimeDir (read by safehouse before the sandbox is applied).
	profilePath := sm.nonoProfilePath(sessionID)
	fragmentPath := sm.safehouseFragmentPath(sessionID)

	return sandbox.WrapOpts{
		Backend:               merged.Backend,
		WorktreeDir:           worktreePath,
		ReadDirs:              readDirs,
		WriteDirs:             writeDirs,
		ReadFiles:             readFiles,
		WriteFiles:            writeFiles,
		UnixSockets:           unixSockets,
		Features:              merged.Features,
		EnvKeys:               envKeys,
		SignalMode:            merged.SignalMode,
		Profile:               merged.Profile,
		Network:               networkPolicy(merged.Network),
		BackendCommand:        merged.Command,
		ProfilePath:           profilePath,
		SafehouseFragmentPath: fragmentPath,
	}
}

// networkPolicy converts a config network policy into the sandbox package's
// resolved NetworkPolicy. Nil (or an empty policy) yields nil so the backend
// leaves nono's allow-by-default posture untouched.
func networkPolicy(n *config.SandboxNetworkConfig) *sandbox.NetworkPolicy {
	if !n.IsSet() {
		return nil
	}

	return &sandbox.NetworkPolicy{
		Block:        n.Block,
		AllowDomains: n.AllowDomains,
	}
}

// agentBinaryDir resolves the directory containing the agent command so it can
// be granted read access in the sandbox. It resolves bare command names via
// PATH; returns "" if it cannot be resolved (e.g. a shell builtin).
func agentBinaryDir(command string) string {
	if command == "" {
		return ""
	}

	if strings.ContainsRune(command, filepath.Separator) {
		return filepath.Dir(command)
	}

	if resolved, err := exec.LookPath(command); err == nil {
		return filepath.Dir(resolved)
	}

	return ""
}

// ensureEnvKeys appends any of want not already present in keys.
func ensureEnvKeys(keys []string, want ...string) []string {
	have := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		have[k] = struct{}{}
	}

	for _, w := range want {
		if _, ok := have[w]; !ok {
			keys = append(keys, w)
			have[w] = struct{}{}
		}
	}

	return keys
}

func expandPaths(paths []string, log *slog.Logger, kind string) []string {
	if len(paths) == 0 {
		return nil
	}

	var out []string

	for _, p := range paths {
		expanded := config.ExpandPath(p)
		if strings.ContainsAny(expanded, "*?[") {
			if matches, err := filepath.Glob(expanded); err == nil && len(matches) > 0 {
				out = append(out, matches...)
				continue
			}
		}

		if _, err := os.Stat(expanded); err != nil {
			log.Warn("sandbox: skipping non-existent path", "kind", kind, "path", expanded)
			continue
		}

		out = append(out, expanded)
	}

	return out
}

// expandFilePaths expands ~ and globs in single-file grant paths (read_files /
// write_files). Unlike expandPaths (for directories), it does NOT drop a path
// that doesn't exist on disk: a writable file grant is routinely for a file the
// agent creates at runtime — e.g. Claude's ~/.claude.json.lock / ~/.claude.lock
// lockfiles, which don't exist until a write happens. Stat-filtering those would
// silently drop the grant (and under nono deny the agent from creating the
// file). Globs that match nothing are still skipped (there is nothing to grant);
// a literal path whose parent directory is missing is kept but warned, since
// nono cannot create the file without a grantable parent.
func expandFilePaths(paths []string, log *slog.Logger, kind string) []string {
	if len(paths) == 0 {
		return nil
	}

	var out []string

	for _, p := range paths {
		expanded := config.ExpandPath(p)
		if strings.ContainsAny(expanded, "*?[") {
			if matches, err := filepath.Glob(expanded); err == nil && len(matches) > 0 {
				out = append(out, matches...)
			} else {
				log.Warn("sandbox: file grant glob matched nothing", "kind", kind, "path", expanded)
			}

			continue
		}

		if _, err := os.Stat(filepath.Dir(expanded)); err != nil {
			log.Warn("sandbox: file grant parent dir does not exist", "kind", kind, "path", expanded)
		}

		out = append(out, expanded)
	}

	return out
}
