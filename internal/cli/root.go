package cli

import (
	"fmt"
	"os"
	"sync"

	"github.com/d0ugal/graith/internal/agent"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
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

		if !jsonOutput && (agentMode || agent.Detected()) {
			jsonOutput = true
		}

		out = output.New(jsonOutput)

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
		return fmt.Errorf("--config is not allowed inside a graith session (sandbox policy)")
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
		registerListCmd()
		registerLogsCmd()
		registerManCmd()
		registerMCPCmd()
		registerMCPProxyCmd()
		registerMigrateCmd()
		registerMsgCmd()
		registerNewCmd()
		registerPathCmd()
		registerRenameCmd()
		registerReportStatusCmd()
		registerRestartCmd()
		registerResumeCmd()
		registerSandboxCmd()
		registerScenarioCmd()
		registerStarCmd()
		registerStatusSummaryCmd()
		registerStopCmd()
		registerStoreCmd()
		registerTypeCmd()
		registerUpdateCmd()
		registerVersionCmd()
		registerWaitCmd()
	})
}
