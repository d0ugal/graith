package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/tools"
)

// cleanupLegacyDaemon stops an old daemon that may be listening on the
// pre-v0.11 socket path ($TMPDIR or /tmp). Without this, upgrading would
// leave an orphaned daemon since the new CLI can't reach the old socket.
func cleanupLegacyDaemon(log *slog.Logger) {
	cleanupLegacyDaemonDirs(config.LegacyRuntimeDirs(), log, net.DialTimeout, IsGraithDaemon, syscall.Kill)
}

type legacyDialer func(network, address string, timeout time.Duration) (net.Conn, error)
type legacyProcessChecker func(pid int) bool
type legacyKiller func(pid int, signal syscall.Signal) error

func cleanupLegacyDaemonDirs(
	dirs []string,
	log *slog.Logger,
	dial legacyDialer,
	isDaemon legacyProcessChecker,
	kill legacyKiller,
) {
	for _, dir := range dirs {
		sock := filepath.Join(dir, "graith.sock")
		pid := filepath.Join(dir, "graith.pid")

		if _, err := os.Stat(sock); err != nil {
			continue
		}

		conn, err := dial("unix", sock, 500*time.Millisecond)
		if err != nil {
			_ = os.Remove(sock)
			_ = os.Remove(pid)

			log.Info("removed stale legacy socket", "path", sock)

			continue
		}

		_ = conn.Close()

		data, err := os.ReadFile(pid)
		if err == nil {
			var legacyPID int
			if _, err := fmt.Sscanf(string(data), "%d", &legacyPID); err == nil && isDaemon(legacyPID) {
				log.Info("stopping legacy daemon", "pid", legacyPID, "socket", sock)
				_ = kill(legacyPID, syscall.SIGTERM)
			}
		}

		_ = os.Remove(sock)
		_ = os.Remove(pid)
	}
}

// Run starts the daemon: acquires PID file, listens on the Unix socket,
// serves connections, and blocks until SIGTERM/SIGINT or an upgrade signal.
func Run(cfg *config.Config, paths config.Paths, configFile, adoptFrom string) (returnErr error) {
	if adoptFrom != "" {
		return RunAdoptBootstrap(configFile, adoptFrom)
	}

	effectiveConfigFile, _, err := config.ResolveConfigPath(configFile)
	if err != nil {
		return fmt.Errorf("resolve effective config source: %w", err)
	}

	if err := grpty.PreparePinnedTerminalExecutable(); err != nil {
		return fmt.Errorf("prepare terminal helper executable: %w", err)
	}

	defer grpty.ClosePinnedTerminalExecutable()

	return run(cfg, paths, effectiveConfigFile, adoptFrom, nil, nil)
}

// RunAdoptBootstrap establishes post-exec ownership before config loading or
// path derivation. The hidden CLI adoption path bypasses Cobra's ordinary root
// pre-run and enters here directly so even a missing or invalid replacement
// config cannot strand transferred descriptors or processes.
func RunAdoptBootstrap(configFile, adoptFrom string) (returnErr error) {
	capsuleRaw, ownershipFDRaw := captureUpgradeBootstrapEnvironment()

	owned, capsuleErr := readInheritedOwnershipCapsule(capsuleRaw)
	if owned == nil {
		return fmt.Errorf("read upgrade ownership capsule: %w", capsuleErr)
	}

	adoptionDeadline := time.Now().Add(upgradeAdoptionTimeout)

	ownership := newUpgradeOwnershipGuard(owned, adoptionDeadline)
	defer func() { returnErr = errors.Join(returnErr, ownership.Cleanup()) }()
	// The immutable capsule is the first trustworthy enumeration of every
	// transferred resource. Restore CLOEXEC before reading the manifest or
	// touching config, paths, MCP, or any other subsystem that could fork.
	if err := secureUpgradeManifestDescriptors(owned); err != nil {
		return err
	}

	if capsuleErr != nil {
		return fmt.Errorf("read upgrade ownership capsule: %w", capsuleErr)
	}

	readDeadline := time.Now().Add(upgradeManifestReadTimeout)
	if readDeadline.After(adoptionDeadline) {
		readDeadline = adoptionDeadline
	}

	readCtx, cancelRead := context.WithDeadline(context.Background(), readDeadline)
	headerOwned, manifest, err := readInheritedManifestHandoff(readCtx, ownershipFDRaw)

	cancelRead()

	if err == nil {
		err = validateInheritedManifestBinding(owned, headerOwned, manifest)
	}

	if err != nil {
		return fmt.Errorf("read upgrade manifest body: %w", err)
	}

	if err := ownership.refine(headerOwned); err != nil {
		return err
	}

	manifest.adoptionDeadline = adoptionDeadline

	if err := grpty.PreparePinnedTerminalExecutable(); err != nil {
		return fmt.Errorf("prepare terminal helper executable: %w", err)
	}

	var cfg *config.Config
	if manifest.ConfigPresent {
		cfg, err = config.LoadBytes(configFile, manifest.ConfigSnapshot)
	} else {
		cfg = config.Default()
	}

	if err != nil {
		return fmt.Errorf("loading exact upgrade config snapshot: %w", err)
	}

	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}

	if cfg.DataDir != "" {
		paths = paths.WithDataDir(cfg.DataDir)
	}

	return run(cfg, paths, configFile, adoptFrom, manifest, ownership)
}

func adoptUpgradeListener(
	manifest *UpgradeManifest,
	ownership *upgradeOwnershipGuard,
	afterConversion func() error,
) (net.Listener, error) {
	if manifest == nil || ownership == nil {
		return nil, errors.New("upgrade listener ownership is unavailable")
	}

	f := os.NewFile(uintptr(manifest.ListenerFd), "listener")
	if f == nil {
		return nil, errors.New("inherited listener descriptor is invalid")
	}

	listener, conversionErr := net.FileListener(f)
	closeErr := f.Close()

	ownershipErr := ownership.consumeListener(closeErr)
	if ownershipErr != nil {
		if listener != nil {
			_ = listener.Close()
		}

		return nil, errors.Join(
			errors.New("inherited listener descriptor close could not be proven"),
			conversionErr,
		)
	}

	if conversionErr != nil {
		return nil, errors.New("inherited listener conversion failed")
	}

	if afterConversion != nil {
		if err := afterConversion(); err != nil {
			_ = listener.Close()

			return nil, err
		}
	}

	return listener, nil
}

func run(
	cfg *config.Config,
	paths config.Paths,
	configFile, adoptFrom string,
	manifest *UpgradeManifest,
	ownership *upgradeOwnershipGuard,
) (returnErr error) {
	defer grpty.ClosePinnedTerminalExecutable()

	if configFile == "" {
		var err error

		configFile, _, err = config.ResolveConfigPath("")
		if err != nil {
			return fmt.Errorf("resolve effective config source: %w", err)
		}
	}

	if adoptFrom != "" {
		if manifest == nil {
			var err error

			manifest, err = ReadManifest(adoptFrom)
			if err != nil {
				return fmt.Errorf("read upgrade manifest: %w", err)
			}

			ownership = newUpgradeOwnershipGuard(manifest)
			defer func() { returnErr = errors.Join(returnErr, ownership.Cleanup()) }()
		}

		if manifest.Version == upgradeManifestVersion {
			if err := validateUpgradeTargetDescriptor(manifest.Target); err != nil {
				return err
			}

			if err := cleanupRetainedUpgradeTarget(manifest.Target.ExecPath); err != nil {
				return err
			}

			if err := validateUpgradePathDescriptor(manifest, paths, configFile); err != nil {
				return err
			}
		}

		if manifest.Profile != paths.Profile {
			return fmt.Errorf("profile mismatch: manifest has %q but daemon is %q", manifest.Profile, paths.Profile)
		}
	}

	if err := paths.EnsureDirs(); err != nil {
		return err
	}

	logFile, err := os.OpenFile(paths.DaemonLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	log := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Install the configured external-tool resolver before anything shells out
	// to git/gh/ps/etc. Validation already ran at config load, so this only fills
	// defaults for unset fields (issue #1238).
	tools.Configure(cfg.Tools.Resolved(cfg.SourceDir))

	sm := NewSessionManager(cfg, paths, log)
	sm.configFile = configFile
	sm.upgradeCh = make(chan *upgradeRequest, 1)

	msgStore, err := NewMsgStore(paths.MessagesDB, MsgStoreSettings{
		BusyTimeout:      cfg.Messages.BusyTimeoutDuration(),
		SubscriberBuffer: cfg.Messages.SubscriberBufferOrDefault(),
		JailListLimit:    cfg.Messages.JailListLimitOrDefault(),
	})
	if err != nil {
		return fmt.Errorf("open message store: %w", err)
	}
	defer func() { _ = msgStore.Close() }()

	sm.messages = msgStore

	todoStore, err := NewTodoStore(paths.TodosDB, TodoStoreSettings{
		BusyTimeout: cfg.Todo.BusyTimeoutDuration(),
		MaxTitle:    cfg.Todo.MaxTitleOrDefault(),
		MaxNote:     cfg.Todo.MaxNoteOrDefault(),
		ListLimit:   cfg.Todo.ListLimitOrDefault(),
	})
	if err != nil {
		return fmt.Errorf("open todo store: %w", err)
	}
	defer func() { _ = todoStore.Close() }()

	sm.todos = todoStore

	mcpMgr := NewMCPManager(cfg, []config.MCPServerConfig{graithMCPServer()}, paths.LogDir, log)

	sm.mcpManager = mcpMgr
	defer mcpMgr.Shutdown()

	var l net.Listener
	defer func() {
		if l != nil {
			_ = l.Close()
		}
	}()

	if adoptFrom != "" {
		// Current manifests were preflighted against this binary, but validate
		// again before any state migration or write. Legacy v0 manifests have no
		// capacity contract; preserve their live PTYs with degraded screens when
		// helper construction reaches the native limit.
		if manifest.Version == upgradeManifestVersion {
			if err := validateAdoptionCapacity(manifest); err != nil {
				return err
			}
		}

		originalStateVersion, err := sm.loadStateSnapshotForAdoption(manifest.StateSnapshot)
		if err != nil {
			var ve *StateVersionError
			if errors.As(err, &ve) {
				return fmt.Errorf("refusing to start: %w (downgrade would discard the newer state)", err)
			}

			return fmt.Errorf("refusing upgrade adoption with unreadable durable state: %w", err)
		}

		if err := sm.restoreUpgradeCleanup(); err != nil {
			return err
		}

		sm.reconcileUpgradeCleanup()
		// Exec preserves child parenthood but destroys the old Wait goroutines.
		// Resolve every registry entry before any new helper can be created.
		if err := ownership.reapHelpers(); err != nil {
			return fmt.Errorf("reap inherited terminal helpers: %w", err)
		}

		l, err = adoptUpgradeListener(manifest, ownership, sm.loadOrCreateHumanToken)
		if err != nil {
			return fmt.Errorf("adopt upgrade listener: %w", err)
		}

		adoption, err := sm.adoptSessions(
			manifest,
			func(id string, scrollback bool, closeErr error) error {
				if scrollback {
					return ownership.consumeScrollbackFD(id, closeErr)
				}

				return ownership.consumeSessionFD(id, closeErr)
			},
			ownership.moveSessionDescriptors,
			func(original, resolved UpgradeSession) {
				ownership.disarmSession(original)
				ownership.armSession(resolved)
			},
			false,
		)
		if err != nil {
			return fmt.Errorf("persist upgrade adoption state: %w", err)
		}

		if len(adoption.UnresolvedSessions) > 0 {
			return errors.New("upgrade adoption process cleanup remains unresolved")
		}

		if originalStateVersion < CurrentStateVersion {
			if err := backupStateBeforeMigration(paths.StateFile, originalStateVersion, manifest.StateSnapshot); err != nil {
				return fmt.Errorf("persist pre-migration upgrade state backup: %w", err)
			}
		}

		if err := sm.saveState(); err != nil {
			return fmt.Errorf("persist adopted upgrade state: %w", err)
		}

		if err := writeUpgradeJournalMarker(adoptFrom, manifest, upgradeJournalCommitted); err != nil {
			return fmt.Errorf("persist committed upgrade adoption marker: %w", err)
		}

		for _, session := range adoption.ResolvedSessions {
			ownership.disarmSession(session)
		}
		// Only the committed marker plus disarmed cleanup guard makes manager
		// publication irreversible. Until now the daemon remains the sole waiter,
		// preserving exact-child identity for any later-session rollback.
		for _, session := range adoption.adoptedSessions {
			session.StartAdoptedWaiter()
		}
		// Resolve processes which exited during adoption immediately. Anything
		// still live remains registered for the recurring cleanup loop below;
		// the ownership guard is disarmed only after exact cleanup succeeds.
		sm.reconcileUpgradeCleanup()
		sm.mu.RLock()

		adoptedDrivers := make(map[string]SessionDriver, len(manifest.Sessions))
		for _, session := range manifest.Sessions {
			if driver := sm.sessions[session.ID]; driver != nil {
				adoptedDrivers[session.ID] = driver
			}
		}

		sm.mu.RUnlock()

		for id, driver := range adoptedDrivers {
			sm.startWatcher(id, driver)
		}
		// Exec can interrupt a durable delete or soft-delete kill just as a
		// process restart can. Finish those transactions before serving lifecycle
		// requests from the inherited listener.
		sm.resumeTombstones()
		sm.reconcileSoftDeletedOrphans()
		sm.cleanupOrphanedProcesses()

		if err := removeUpgradeJournal(adoptFrom, manifest); err != nil {
			// Adoption is already committed and its live PTYs have left the
			// rollback guard. A stale private manifest is safer than turning a
			// cleanup failure into orphaned live agents.
			log.Warn("failed to remove consumed upgrade manifest", "err", err)
		} else if err := removeUpgradeJournalMarker(adoptFrom, upgradeJournalCommitted); err != nil {
			log.Warn("failed to remove committed upgrade marker", "err", err)
		}

		log.Info("daemon upgraded", "adopted_sessions", len(manifest.Sessions), "pid", os.Getpid())
	} else {
		if paths.Profile == "" {
			cleanupLegacyDaemon(log)
		}

		if err := AcquirePIDFile(paths.PIDFile); err != nil {
			return err
		}

		if err := recoverPendingUpgradeJournals(paths.RuntimeDir); err != nil {
			ReleasePIDFile(paths.PIDFile)

			return fmt.Errorf("recover pending daemon upgrade: %w", err)
		}

		var listenErr error

		l, listenErr = Listen(paths.SocketPath)
		if listenErr != nil {
			ReleasePIDFile(paths.PIDFile)
			return listenErr
		}

		if err := sm.LoadState(); err != nil {
			var ve *StateVersionError
			if errors.As(err, &ve) {
				ReleasePIDFile(paths.PIDFile)
				return fmt.Errorf("refusing to start: %w (downgrade would discard the newer state)", err)
			}

			log.Warn("failed to load state", "err", err)
		}

		if err := sm.loadOrCreateHumanToken(); err != nil {
			_ = l.Close()

			ReleasePIDFile(paths.PIDFile)

			return fmt.Errorf("initialize human authentication: %w", err)
		}

		if err := sm.restoreUpgradeCleanup(); err != nil {
			_ = l.Close()

			ReleasePIDFile(paths.PIDFile)

			return err
		}

		sm.reconcileUpgradeCleanup()

		// Finish any deletes interrupted mid-flight (crash/kill/power loss)
		// before reaping orphaned processes, so a half-deleted session's
		// worktree and state entry are removed together.
		sm.resumeTombstones()

		sm.reconcileSoftDeletedOrphans()
		sm.cleanupOrphanedProcesses()

		logAttrs := []any{"socket", paths.SocketPath, "pid", os.Getpid()}
		if paths.Profile != "" {
			logAttrs = append(logAttrs, "profile", paths.Profile)
		}

		log.Info("daemon started", logAttrs...)
	}

	defer func() { _ = l.Close() }()

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	srv := NewServer(l, func(ctx context.Context, conn net.Conn) {
		HandleConnection(ctx, conn, ConnOrigin{}, sm, log)
	}, log)

	go func() { _ = srv.Serve(serverCtx) }()

	// Optional tailnet-facing remote control surface (design §A). The controller
	// owns listener generations for startup, hot reload, and shutdown; the local
	// Unix socket above is unaffected when remote access is disabled or fails.
	if sm.Config().Remote.Enabled {
		if len(sm.Config().Remote.AllowTailnetUsers) == 0 {
			log.Warn("[remote] enabled with empty allow_tailnet_users — all remote connections denied (Gate 1 fail-closed)")
		}
	}

	sm.configFile = configFile

	var (
		backgroundMu       sync.Mutex
		backgroundGroup    *daemonTaskGroup
		backgroundShutdown bool
	)

	startBackground := func() bool {
		group := newDaemonTaskGroup()
		jobs := []func(context.Context){
			sm.recoverTerminalScreensAfterUpgrade,
			sm.RunDetectionLoop,
			sm.RunStartupWatchdogLoop,
			sm.RunResourceMonitorLoop,
			sm.RunMessageCleanupLoop,
			sm.RunGitPullLoop,
			sm.RunPRWatchLoop,
			sm.RunPurgeLoop,
			sm.RunTodoSweepLoop,
			sm.RunScenarioPolicyLoop,
			sm.RunTriggerLoop,
			sm.RunFileWatchLoop,
			sm.RunGCXTriggerLoop,
			sm.RunScenarioCompletionLoop,
			sm.RunTokenLoop,
			sm.RunUpgradeCleanupLoop,
			func(ctx context.Context) { sm.orchestratorSupervisor(ctx, sm.orchestratorExitCh) },
			sm.RunOrchestratorReconcileLoop,
			sm.ensureOrchestrator,
			func(ctx context.Context) {
				w := config.NewWatcher(configFile, sm.applyWatchedConfig, log, sm.Config().ConfigReload.ReloadDebounceDuration())
				if err := w.Run(ctx); err != nil && ctx.Err() == nil {
					log.Error("config watcher stopped", "err", err)
				}
			},
		}
		// Register the complete top-level generation before publication. Nested
		// jobs use the same group's admission lock, which closes before drain.
		group.Go(func(ctx context.Context) {
			if err := sm.startRemoteRuntime(ctx); err != nil {
				log.Error("[remote] startup failed; remote surface disabled", "err", err)
			}

			<-ctx.Done()
			sm.stopRemoteRuntime()
		})

		for _, fn := range jobs {
			group.Go(fn)
		}

		backgroundMu.Lock()
		if backgroundShutdown || backgroundGroup != nil || !sm.installBackgroundTasks(group) {
			backgroundMu.Unlock()
			group.BeginDrain()
			group.Activate()

			return false
		}

		backgroundGroup = group
		backgroundMu.Unlock()
		group.Activate()
		// Adoption deliberately does not launch detached notification/capture
		// work before Run owns a generation. Reconstruct those best-effort tasks
		// only after the complete group is published.
		sm.mu.RLock()

		type recoveredCapture struct {
			id, agent, worktree, stateRoot string
			since                          time.Time
			pid                            int
			pidStartTime                   int64
		}

		var (
			runningIDs []string
			captures   []recoveredCapture
		)

		for id, state := range sm.state.Sessions {
			if state.Status == StatusRunning && sm.sessions[id] != nil {
				runningIDs = append(runningIDs, id)
				if scrapesID(state.Agent) && state.AgentSessionID == "" && state.NativeCaptureStartedAt != nil {
					captures = append(captures, recoveredCapture{
						id: id, agent: state.Agent, worktree: state.WorktreePath,
						stateRoot: state.NativeStateRoot, since: *state.NativeCaptureStartedAt,
						pid: state.PID, pidStartTime: state.PIDStartTime,
					})
				}
			}
		}

		sm.mu.RUnlock()

		for _, id := range runningIDs {
			sm.startBackgroundTask(context.Background(), func(taskCtx context.Context) {
				sm.notifyUnreadInboxContext(taskCtx, id)
			})
		}

		for _, capture := range captures {
			sm.startBackgroundTask(context.Background(), func(taskCtx context.Context) {
				sm.captureNativeSessionIDContext(taskCtx, capture.id, capture.agent, capture.worktree,
					capture.stateRoot, capture.since, capture.pid, capture.pidStartTime)
			})
		}

		return true
	}
	stopBackground := func(ctx context.Context) error {
		backgroundMu.Lock()

		group := backgroundGroup
		if group == nil {
			backgroundMu.Unlock()
			return nil
		}
		backgroundMu.Unlock()
		group.BeginDrain()

		if err := group.Wait(ctx); err != nil {
			return err
		}

		backgroundMu.Lock()
		if backgroundGroup == group {
			backgroundGroup = nil

			sm.clearBackgroundTasks(group)
		}
		backgroundMu.Unlock()

		return nil
	}
	restartBackground := func(after func()) {
		backgroundMu.Lock()
		group := backgroundGroup
		shuttingDown := backgroundShutdown
		backgroundMu.Unlock()

		if group == nil {
			if !shuttingDown {
				sm.finishBackgroundPublication(startBackground(), after)
			}

			return
		}
		go func(expected *daemonTaskGroup) {
			_ = expected.Wait(context.Background())

			backgroundMu.Lock()
			if backgroundGroup != expected {
				backgroundMu.Unlock()
				return
			}

			backgroundGroup = nil

			sm.clearBackgroundTasks(expected)

			shuttingDown := backgroundShutdown
			backgroundMu.Unlock()

			if !shuttingDown {
				sm.finishBackgroundPublication(startBackground(), after)
			}
		}(group)
	}
	beginBackgroundShutdown := func() {
		backgroundMu.Lock()
		backgroundShutdown = true
		group := backgroundGroup
		backgroundMu.Unlock()

		if group != nil {
			group.BeginDrain()
		}
	}
	_ = startBackground()

	defer func() {
		beginBackgroundShutdown()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_ = stopBackground(ctx)
	}()

	sigCh := make(chan os.Signal, 1)

	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	return runControlLoop(sigCh, sm.upgradeCh, log, sm.ReloadConfig, func() {
		log.Info("shutting down")
		sm.beginShutdownBarrier()
		beginBackgroundShutdown()
		serverCancel()
		srv.Shutdown()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)

		drainErr := stopBackground(shutdownCtx)
		if drainErr == nil {
			drainErr = sm.waitLifecycleIdle(shutdownCtx)
		}

		if drainErr == nil {
			drainErr = sm.waitMutationIdle(shutdownCtx)
		}
		shutdownCancel()

		if drainErr != nil {
			// Never close stores or publish a completed shutdown while admitted
			// work can still mutate them. The bounded phase above cancels every
			// owned surface; this fail-closed join handles a non-context-aware OS
			// child without permitting late publication into closed state.
			log.Warn("shutdown drain exceeded deadline; waiting for owned work", "err", drainErr)

			_ = stopBackground(context.Background())
			_ = sm.waitLifecycleIdle(context.Background())
			_ = sm.waitMutationIdle(context.Background())
		}
		// Every admitted lifecycle operation has now either rolled back at the
		// shared pre-spawn/publication barrier or registered its watcher. Take the
		// sole stop snapshot only after that join so no child or watcher can appear
		// behind StopAll.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		sm.StopAll(stopCtx)
		stopCancel()

		_ = os.Remove(paths.SocketPath)
		ReleasePIDFile(paths.PIDFile)
	}, func(request *upgradeRequest) (returnErr error) {
		defer sm.endUpgradeAttempt()

		clientExecPath := request.execPath

		log.Info("preparing upgrade")

		fail := func(public string, err error) error {
			request.ready <- errors.New(public)

			return err
		}

		configSnapshot, configPresent, err := captureUpgradeConfigSnapshot(configFile)
		if err != nil {
			return fail("upgrade config snapshot could not be captured", err)
		}

		configSource := makeUpgradeConfigSource(configFile, configSnapshot, configPresent)

		target, err := probeUpgradeTarget(clientExecPath, upgradeProbeExpectation{
			profile:      paths.Profile,
			paths:        makeUpgradePathDescriptor(paths, configFile),
			configSource: configSource,
		})
		if err != nil {
			return fail(err.Error(), err)
		}
		defer func() {
			returnErr = errors.Join(returnErr, target.pin.close())
		}()

		if err := sm.beginUpgradeReservation(); err != nil {
			return fail(err.Error(), err)
		}

		var (
			listenerFile             *os.File
			manifest                 *UpgradeManifest
			manifestPath             string
			backgroundDrainAttempted bool
		)
		defer func() {
			if manifest != nil {
				returnErr = errors.Join(returnErr, rollbackUpgradeDescriptors(manifest))
			}

			if listenerFile != nil {
				_ = listenerFile.Close()
			}

			if manifestPath != "" {
				if markerErr := writeUpgradeJournalMarker(manifestPath, manifest, upgradeJournalRolledBack); markerErr != nil {
					returnErr = errors.Join(returnErr, unsafeUpgradeDescriptor(
						fmt.Errorf("persist upgrade rollback marker: %w", markerErr),
					))
				} else if removeErr := removeUpgradeJournal(manifestPath, manifest); removeErr == nil {
					_ = removeUpgradeJournalMarker(manifestPath, upgradeJournalRolledBack)
				}
			}

			if hasUnsafeUpgradeDescriptor(returnErr) {
				// An inheritable descriptor may still be live. Do not thaw any
				// surface which can fork a child before runControlLoop performs
				// fail-closed shutdown and the OS closes the descriptor table.
				return
			}

			grpty.ThawTerminalHelpers()

			if !backgroundDrainAttempted {
				// The active generation owns retries until recovery succeeds or the
				// generation is canceled. A drained generation is replaced below and
				// its startup recovery job assumes the same ownership.
				sm.startBackgroundTask(context.Background(), sm.recoverTerminalScreensAfterUpgrade)
			}

			if backgroundDrainAttempted {
				// A timed-out drain remains the active generation and keeps the
				// lifecycle reservation closed until every owned descendant exits
				// and a replacement generation is fully published.
				restartBackground(sm.endUpgradeReservation)
			} else {
				sm.endUpgradeReservation()
			}
		}()

		admissionCtx, admissionCancel := context.WithTimeout(context.Background(), upgradeAdoptionTimeout)

		admissionErr := sm.waitMutationIdle(admissionCtx)
		if admissionErr == nil {
			admissionErr = sm.waitLifecycleIdle(admissionCtx)
		}

		admissionCancel()

		if admissionErr != nil {
			return fail("accepted daemon mutations did not drain before upgrade", admissionErr)
		}

		if err := sm.preflightUpgradeSessions(target.capacity); err != nil {
			return fail(err.Error(), err)
		}

		freezeCtx, freezeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		freezeComplete := make(chan struct{})

		go func() {
			select {
			case <-request.canceled:
				freezeCancel()
			case <-freezeComplete:
			}
		}()

		helpers, err := grpty.FreezeTerminalHelpers(freezeCtx)

		close(freezeComplete)
		freezeCancel()

		if err != nil {
			return fail("terminal helper handoff could not be frozen", err)
		}

		if err := validateUpgradeHelperHandoff(target, helpers); err != nil {
			return fail(err.Error(), err)
		}

		sm.mu.RLock()
		sessionCount := len(sm.upgradePTYSessionIDsLocked())
		sm.mu.RUnlock()

		if err := validateUpgradeDescriptorBudget(sessionCount); err != nil {
			return fail(err.Error(), err)
		}

		unixL, ok := l.(*net.UnixListener)
		if !ok {
			err := errors.New("upgrade failed: listener type mismatch")

			return fail("upgrade listener could not be preserved", err)
		}

		listenerFile, err = unixL.File()
		if err != nil {
			return fail("upgrade listener could not be preserved", err)
		}

		listenerFd := listenerFile.Fd()

		manifest, err = sm.prepareUpgrade(listenerFd, configFile, target.capacity, helpers, true)
		if err != nil {
			return fail("upgrade state could not be prepared", err)
		}

		manifest.Target = target.descriptor()
		if err := sm.persistFrozenUpgradeState(manifest); err != nil {
			return fail("upgrade state could not be persisted", err)
		}

		manifest.ConfigSnapshot = configSnapshot
		manifest.ConfigPresent = configPresent

		snapshotCfg := config.Default()
		if manifest.ConfigPresent {
			snapshotCfg, err = config.LoadBytes(configFile, manifest.ConfigSnapshot)
			if err != nil {
				return fail("upgrade config snapshot could not be validated", err)
			}
		}
		// Recompute from the replacement process's fresh defaults. Starting from
		// the already-overridden running paths would incorrectly preserve a
		// removed data_dir even though RunAdoptBootstrap resolves defaults first.
		snapshotDescriptor, err := resolvedUpgradeSnapshotPaths(snapshotCfg, configFile)
		if err != nil {
			return fail("upgrade config snapshot paths could not be resolved", err)
		}

		if snapshotDescriptor != manifest.Paths {
			return fail("upgrade config snapshot changes the effective daemon paths",
				refuseUpgrade("upgrade config changed during preparation"))
		}

		manifestPath, err = WriteManifest(paths.RuntimeDir, manifest)
		if err != nil {
			return fail("upgrade manifest could not be written", err)
		}

		if err := prepareManifestHandoff(manifestPath, manifest); err != nil {
			return fail("upgrade ownership handoff could not be prepared", err)
		}

		if err := prepareOwnershipCapsule(manifest); err != nil {
			return fail("upgrade cleanup capsule could not be prepared", err)
		}

		if err := target.validateFileIdentity(); err != nil {
			return fail(err.Error(), err)
		}

		request.ready <- nil

		select {
		case <-request.proceed:
		case <-request.canceled:
			return refuseUpgrade("upgrade requester disconnected before acknowledgement")
		}

		log.Info("exec-ing new binary", "sessions", len(manifest.Sessions))

		backgroundCtx, backgroundCancel := context.WithTimeout(context.Background(), 10*time.Second)
		backgroundDrainAttempted = true

		if err := stopBackground(backgroundCtx); err != nil {
			backgroundCancel()
			return fmt.Errorf("upgrade background drain failed: %w", err)
		}

		backgroundCancel()

		// Defers do not run across syscall.Exec. Stop lazy MCP children only
		// after every reversible preflight; if Exec returns the manager remains
		// usable and proxy clients can reconnect on demand.
		mcpCtx, mcpCancel := context.WithTimeout(context.Background(), 10*time.Second)
		mcpDone := make(chan struct{})

		go func() {
			select {
			case <-request.canceled:
				mcpCancel()
			case <-mcpDone:
			}
		}()

		err = mcpMgr.FreezeAndDrain(mcpCtx)

		close(mcpDone)
		mcpCancel()

		if err != nil {
			mcpMgr.Thaw()
			return fmt.Errorf("upgrade MCP drain failed: %w", err)
		}

		// Stop PTY reads and input only at the last reversible moment, after
		// acknowledgement and every potentially slow child drain. This keeps
		// terminal I/O flowing throughout preflight and bounds the final gap.
		ioCtx, ioCancel := context.WithTimeout(context.Background(), 5*time.Second)
		ioComplete := make(chan struct{})

		go func() {
			select {
			case <-request.canceled:
				ioCancel()
			case <-ioComplete:
			}
		}()

		releaseSessionIO, err := sm.quiesceSessionIO(ioCtx)

		close(ioComplete)
		ioCancel()

		if err != nil {
			mcpMgr.Thaw()
			return fmt.Errorf("upgrade session I/O drain failed: %w", err)
		}

		execErr := sm.execPreparedUpgrade(target, manifest, manifestPath, configFile)
		// Even fail-closed shutdown must let the PTY reader reach readDone;
		// otherwise StopAll can wait forever on a read loop paused at the
		// upgrade safe point. The manager reservation remains closed and every
		// child-creating surface stays frozen on the unsafe path.
		releaseSessionIO()

		if !hasUnsafeUpgradeDescriptor(execErr) {
			mcpMgr.Thaw()
		}

		if execErr != nil {
			return fmt.Errorf("upgrade exec failed: %w", execErr)
		}

		return nil
	})
}

func resolvedUpgradeSnapshotPaths(snapshotCfg *config.Config, configFile string) (UpgradePathDescriptor, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return UpgradePathDescriptor{}, err
	}

	if snapshotCfg != nil && snapshotCfg.DataDir != "" {
		paths = paths.WithDataDir(snapshotCfg.DataDir)
	}

	return makeUpgradePathDescriptor(paths, configFile), nil
}

// runControlLoop owns the daemon's two terminal control paths. The operating
// system wiring and expensive shutdown/upgrade work stay in Run, while this
// small dispatcher can be driven deterministically in tests.
func runControlLoop(
	signals <-chan os.Signal,
	upgrades <-chan *upgradeRequest,
	log *slog.Logger,
	reload func() error,
	shutdown func(),
	upgrade func(*upgradeRequest) error,
) error {
	for {
		select {
		case sig := <-signals:
			if sig == syscall.SIGHUP {
				log.Info("received SIGHUP, reloading config")

				if err := reload(); err != nil {
					log.Error("config reload failed", "err", err)
				}

				continue
			}

			shutdown()

			return nil

		case request := <-upgrades:
			if err := upgrade(request); err != nil {
				var unsafeDescriptor *upgradeDescriptorSafetyError
				if errors.As(err, &unsafeDescriptor) {
					log.Error("upgrade descriptor rollback could not be made safe; shutting down", "err", err)
					shutdown()

					return err
				}

				log.Error("upgrade attempt failed; old daemon remains active", "err", err)
			}
		}
	}
}
