package cli

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/d0ugal/graith/internal/agent"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
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
		if err := rejectConfigInsideSession(cmd); err != nil {
			return err
		}

		var err error

		cfg, err = config.LoadOrDefault(cfgFile)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		paths, err = config.ResolvePaths()
		if err != nil {
			return err
		}

		if cfg.DataDir != "" {
			paths = paths.WithDataDir(cfg.DataDir)
		}

		// Install the configured external-tool resolver so CLI-side git calls
		// (store repo discovery) use the same executables as the daemon (#1238).
		tools.Configure(cfg.Tools.Resolved())

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
		// picker, dashboard, message viewer, status bar, and handshake honour them.
		// The refresh cadence and summary width come from [terminal] (#1254); the fallback
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

func executeWithArgs(args []string) error {
	registerCommands()
	rootCmd.SetArgs(args)

	err := rootCmd.Execute()
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

		registerApprovalsCmd()
		registerApproveRequestCmd()
		registerAttachCmd()
		registerCheckInboxCmd()
		registerCompletionCmd()
		registerConfigCmd()
		registerDaemonCmd()
		registerDashboardCmd()
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
		registerRenameCmd()
		registerReportStatusCmd()
		registerRestartCmd()
		registerRestoreCmd()
		registerResumeCmd()
		registerPairCmd()
		registerPurgeCmd()
		registerRemoteCmd()
		registerSandboxCmd()
		registerScenarioCmd()
		registerTriggerCmd()
		registerStarCmd()
		registerStatusSummaryCmd()
		registerStopCmd()
		registerStoreCmd()
		registerTodoCmd()
		registerTokensCmd()
		registerTypeCmd()
		registerUpdateCmd()
		registerVersionCmd()
		registerWaitCmd()
	})
}
