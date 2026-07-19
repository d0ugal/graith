package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/d0ugal/graith/internal/agent"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/tools"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

var (
	cfgFile    string
	jsonOutput bool
	agentMode  bool
	cfg        *config.Config
	paths      config.Paths
	out        *output.Writer
)

// updateSettings translates the [updates] config block into the settings the
// version package's checker consumes. A nil config yields the zero value, which
// reproduces the historical enabled/canonical-repo/1h/5s behaviour.
func updateSettings(c *config.Config) version.UpdateSettings {
	if c == nil {
		return version.UpdateSettings{}
	}

	return version.UpdateSettings{
		Disabled:   !c.Updates.Enabled,
		Repository: c.Updates.Repository,
		Interval:   c.Updates.IntervalDuration(),
		Timeout:    c.Updates.TimeoutDuration(),
	}
}

var rootCmd = &cobra.Command{
	Use:           "gr",
	Short:         "graith — AI agent session manager",
	Version:       version.Version,
	SilenceErrors: true,
	SilenceUsage:  true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		commandPolicyStartupError = nil

		if err := rejectConfigInsideSession(cmd); err != nil {
			return err
		}

		var err error

		cfg, err = config.LoadOrDefault(cfgFile)
		if err != nil {
			if cmd != commandPolicyCheckCmd {
				return fmt.Errorf("loading config: %w", err)
			}
			// A hook must always emit an agent-native deny response. Falling out
			// through Cobra with a plain exit error has agent-specific semantics
			// and could be treated as a non-blocking hook failure.
			commandPolicyStartupError = fmt.Errorf("loading config: %w", err)
			cfg = config.Default()
		}

		paths, err = config.ResolvePaths()
		if err != nil {
			if cmd != commandPolicyCheckCmd {
				return err
			}

			commandPolicyStartupError = errors.Join(commandPolicyStartupError, fmt.Errorf("resolving paths: %w", err))
		}

		if cfg.DataDir != "" && paths.DataDir != "" {
			paths = paths.WithDataDir(cfg.DataDir)
		}

		// Install the configured external-tool resolver so CLI-side git calls
		// (store repo discovery) use the same executables as the daemon (#1238).
		tools.Configure(cfg.Tools.Resolved(cfg.SourceDir))

		// Install the configured client connection deadlines and attach reconnect
		// cadence so every dial/handshake this invocation makes honours the
		// [connection] block (#1242).
		client.ConfigureConnection(client.ConnectionTimeouts{
			Dial:            cfg.Connection.DialTimeoutDuration(),
			Handshake:       cfg.Connection.HandshakeTimeoutDuration(),
			Start:           cfg.Connection.StartTimeoutDuration(),
			StartPoll:       cfg.Connection.StartPollIntervalDuration(),
			RemoteDial:      cfg.Connection.RemoteDialTimeoutDuration(),
			RemoteHandshake: cfg.Connection.RemoteHandshakeTimeoutDuration(),
			RemotePairing:   cfg.Connection.RemotePairingTimeoutDuration(),
		})
		ConfigureReconnect(
			cfg.Connection.ReconnectTimeoutDuration(),
			cfg.Connection.ReconnectIntervalDuration(),
		)

		// Install the configured terminal/TUI presentation preferences so the
		// picker, list watch, status bar, and handshake honour them. The refresh
		// cadence and summary width come from [terminal] (#1254); the fallback
		// geometry used when the client's real terminal size can't be probed
		// shares the daemon's [lifecycle] default_cols/default_rows (#1243) so
		// there is a single source of truth for default geometry.
		client.ConfigurePresentation(client.PresentationPrefs{
			RefreshInterval: cfg.Terminal.RefreshIntervalDuration(),
			DefaultCols:     int(cfg.Lifecycle.DefaultColsOrDefault()),
			DefaultRows:     int(cfg.Lifecycle.DefaultRowsOrDefault()),
			SummaryWidth:    cfg.Terminal.SummaryWidthValue(),
		})

		if !jsonOutput && (agentMode || agent.Detected()) {
			jsonOutput = true
		}

		out = output.New(jsonOutput)

		// Surface non-fatal config problems (e.g. conflicting keybindings)
		// without refusing to start (issue #1233). Skip in JSON mode so the
		// warnings can't corrupt machine-readable output.
		if !jsonOutput {
			for _, w := range cfg.Warnings {
				fmt.Fprintln(os.Stderr, "warning: "+w)
			}
		}

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func Execute() error {
	return executeWithArgs(os.Args[1:])
}

func executeWithArgs(args []string) (err error) {
	var upgradeGuard *daemon.UpgradeFailureGuard
	if manifestPath := adoptFromArgument(args); manifestPath != "" {
		upgradeGuard, err = daemon.ArmUpgradeFailureGuard(manifestPath)
		if err != nil {
			out = output.New(false)
			out.Error(err)

			return err
		}
		defer func() {
			if err == nil {
				return
			}

			if cleanupErr := upgradeGuard.Cleanup(); cleanupErr != nil {
				wrapped := fmt.Errorf("clean up inherited sessions after CLI bootstrap failure: %w", cleanupErr)

				if out == nil {
					out = output.New(false)
				}

				out.Error(wrapped)
				err = errors.Join(err, wrapped)
			}
		}()
	}

	registerCommands()
	rootCmd.SetArgs(args)

	err = rootCmd.Execute()
	if err != nil {
		w := out
		if w == nil {
			// Cobra skips persistent flag parsing for some errors (e.g.
			// unknown subcommand). Parse them here so --json is respected.
			_ = rootCmd.PersistentFlags().Parse(args)

			if !jsonOutput && (agentMode || agent.Detected()) {
				jsonOutput = true
			}

			w = output.New(jsonOutput)
		}

		w.Error(err)
	}

	return err
}

// adoptFromArgument recognizes the exact hidden replacement-daemon flag before
// Cobra loads configuration. ExecUpgrade emits the separated form, while the
// equals form keeps direct/internal invocations equally fail closed.
func adoptFromArgument(args []string) string {
	if len(args) < 2 || args[0] != "daemon" || args[1] != "start" {
		return ""
	}

	for i := 2; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--adopt-from=") {
			return strings.TrimPrefix(arg, "--adopt-from=")
		}

		if arg == "--adopt-from" && i+1 < len(args) {
			return args[i+1]
		}
	}

	return ""
}

// insideSession reports whether the current process is running inside a
// graith-managed agent session. This is a client-side best-effort check
// using environment variables set by the daemon; it is not a security
// boundary on its own (the sandbox profile enforces the hard boundary).
func insideSession() bool {
	_, set := os.LookupEnv("GRAITH_SESSION_ID")
	return set
}

func rejectConfigInsideSession(cmd *cobra.Command) error {
	if cmd.Flags().Changed("config") && insideSession() {
		return errors.New("--config is not allowed inside a graith session (sandbox policy)")
	}

	return nil
}

var registerOnce sync.Once

// registerCommands wires every subcommand and persistent flag onto rootCmd.
// It replaces the per-file init() functions with explicit, ordered
// registration invoked from executeWithArgs. It is idempotent (guarded by a
// sync.Once) so tests that call executeWithArgs repeatedly stay correct.
func registerCommands() {
	registerOnce.Do(func() {
		rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
		rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
		rootCmd.PersistentFlags().BoolVar(&agentMode, "agent-mode", false, "force agent mode (auto-enables JSON output)")

		registerCommandPolicyCheckCmd()
		registerAttachCmd()
		registerCheckInboxCmd()
		registerCompletionCmd()
		registerConfigCmd()
		registerDaemonCmd()
		registerDeleteCmd()
		registerDoctorCmd()
		registerForkCmd()
		registerInfoCmd()
		registerInterruptCmd()
		registerListCmd()
		registerLogsCmd()
		registerManCmd()
		registerMCPCmd()
		registerMCPProxyCmd()
		registerMigrateCmd()
		registerMsgCmd()
		registerNewCmd()
		registerNotifyCmd()
		registerPathCmd()
		registerReportStatusCmd()
		registerRestartCmd()
		registerRestoreCmd()
		registerResumeCmd()
		registerPurgeCmd()
		registerRemoteCmd()
		registerSandboxCmd()
		registerScenarioCmd()
		registerTriggerCmd()
		registerStatusSummaryCmd()
		registerStopCmd()
		registerStoreCmd()
		registerTodoCmd()
		registerTypeCmd()
		registerUpdateCmd()
		registerVersionCmd()
		registerWaitCmd()
	})
}
