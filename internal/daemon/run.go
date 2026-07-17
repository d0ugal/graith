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
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
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
func Run(cfg *config.Config, paths config.Paths, configFile, adoptFrom string) error {
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
	sm.upgradeCh = make(chan string, 1)

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

	if adoptFrom != "" {
		manifest, err := ReadManifest(adoptFrom)
		if err != nil {
			return fmt.Errorf("read upgrade manifest: %w", err)
		}

		_ = os.Remove(adoptFrom)

		if manifest.Profile != paths.Profile {
			return fmt.Errorf("profile mismatch: manifest has %q but daemon is %q", manifest.Profile, paths.Profile)
		}

		f := os.NewFile(uintptr(manifest.ListenerFd), "listener")
		l, err = net.FileListener(f)
		_ = f.Close()

		if err != nil {
			return fmt.Errorf("adopt listener from fd %d: %w", manifest.ListenerFd, err)
		}

		if err := sm.LoadState(); err != nil {
			var ve *StateVersionError
			if errors.As(err, &ve) {
				return fmt.Errorf("refusing to start: %w (downgrade would discard the newer state)", err)
			}

			log.Warn("failed to load state", "err", err)
		}

		if err := sm.loadOrCreateHumanToken(); err != nil {
			_ = l.Close()
			return fmt.Errorf("initialize human authentication: %w", err)
		}

		if err := sm.AdoptSessions(manifest); err != nil {
			log.Warn("failed to adopt sessions", "err", err)
		}

		log.Info("daemon upgraded", "adopted_sessions", len(manifest.Sessions), "pid", os.Getpid())
	} else {
		if paths.Profile == "" {
			cleanupLegacyDaemon(log)
		}

		if err := AcquirePIDFile(paths.PIDFile); err != nil {
			return err
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

		// Finish any deletes interrupted mid-flight (crash/kill/power loss)
		// before reaping orphaned processes, so a half-deleted session's
		// worktree and state entry are removed together.
		sm.resumeTombstones()

		sm.cleanupOrphanedProcesses()

		logAttrs := []any{"socket", paths.SocketPath, "pid", os.Getpid()}
		if paths.Profile != "" {
			logAttrs = append(logAttrs, "profile", paths.Profile)
		}

		log.Info("daemon started", logAttrs...)
	}

	defer func() { _ = l.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(l, func(ctx context.Context, conn net.Conn) {
		HandleConnection(ctx, conn, ConnOrigin{}, sm, log)
	}, log)

	go func() { _ = srv.Serve(ctx) }()

	// Optional tailnet-facing remote control surface (design §A). The controller
	// owns listener generations for startup, hot reload, and shutdown; the local
	// Unix socket above is unaffected when remote access is disabled or fails.
	if sm.Config().Remote.Enabled {
		if len(sm.Config().Remote.AllowTailnetUsers) == 0 {
			log.Warn("[remote] enabled with empty allow_tailnet_users — all remote connections denied (Gate 1 fail-closed)")
		}
	}

	if err := sm.startRemoteRuntime(ctx); err != nil {
		log.Error("[remote] startup failed; remote surface disabled", "err", err)
	}
	defer sm.stopRemoteRuntime()

	go sm.RunDetectionLoop(ctx)
	go sm.RunStartupWatchdogLoop(ctx)
	go sm.RunResourceMonitorLoop(ctx)
	go sm.RunMessageCleanupLoop(ctx)
	go sm.RunGitPullLoop(ctx)
	go sm.RunPRWatchLoop(ctx)
	go sm.RunPurgeLoop(ctx)
	go sm.RunTodoSweepLoop(ctx)
	go sm.RunTriggerLoop(ctx)
	go sm.RunFileWatchLoop(ctx)
	go sm.RunGCXTriggerLoop(ctx)
	go sm.RunScenarioCompletionLoop(ctx)
	go sm.RunTokenLoop(ctx)

	go sm.orchestratorSupervisor(ctx, sm.orchestratorExitCh)
	go sm.RunOrchestratorReconcileLoop(ctx)
	go sm.ensureOrchestrator(ctx)

	if configFile == "" {
		configFile = paths.ConfigFile
	}

	sm.configFile = configFile

	w := config.NewWatcher(configFile, sm.applyWatchedConfig, log, sm.Config().ConfigReload.ReloadDebounceDuration())
	go func() {
		if err := w.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("config watcher stopped", "err", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)

	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	return runControlLoop(sigCh, sm.upgradeCh, log, sm.ReloadConfig, func() {
		log.Info("shutting down")
		cancel()
		sm.stopRemoteRuntime()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		sm.StopAll(shutdownCtx)
		shutdownCancel()
		srv.Shutdown()

		_ = os.Remove(paths.SocketPath)
		ReleasePIDFile(paths.PIDFile)
	}, func(clientExecPath string) error {
		log.Info("preparing upgrade", "client_exec_path", clientExecPath)

		// Kill MCP server processes before exec — defers don't
		// run when syscall.Exec replaces the process image.
		mcpMgr.Shutdown()

		unixL, ok := l.(*net.UnixListener)
		if !ok {
			log.Error("listener is not a unix listener, cannot upgrade")
			return errors.New("upgrade failed: listener type mismatch")
		}

		listenerFile, err := unixL.File()
		if err != nil {
			log.Error("get listener fd", "err", err)
			return fmt.Errorf("upgrade failed: %w", err)
		}

		listenerFd := listenerFile.Fd()

		manifest, err := sm.PrepareUpgrade(listenerFd, configFile)
		if err != nil {
			_ = listenerFile.Close()

			log.Error("prepare upgrade", "err", err)

			return fmt.Errorf("upgrade failed: %w", err)
		}

		manifestPath, err := WriteManifest(paths.RuntimeDir, manifest)
		if err != nil {
			_ = listenerFile.Close()

			log.Error("write manifest", "err", err)

			return fmt.Errorf("upgrade failed: %w", err)
		}

		log.Info("exec-ing new binary", "manifest", manifestPath, "sessions", len(manifest.Sessions))

		if err := ExecUpgrade(manifestPath, configFile, clientExecPath); err != nil {
			_ = listenerFile.Close()
			_ = os.Remove(manifestPath)

			log.Error("exec failed", "err", err)

			return fmt.Errorf("upgrade exec failed: %w", err)
		}

		return nil
	})
}

// runControlLoop owns the daemon's two terminal control paths. The operating
// system wiring and expensive shutdown/upgrade work stay in Run, while this
// small dispatcher can be driven deterministically in tests.
func runControlLoop(
	signals <-chan os.Signal,
	upgrades <-chan string,
	log *slog.Logger,
	reload func() error,
	shutdown func(),
	upgrade func(string) error,
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

		case clientExecPath := <-upgrades:
			return upgrade(clientExecPath)
		}
	}
}
