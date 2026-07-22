package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/agent"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/daemonservice"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/tools"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

type serviceBootstrapContextKey struct{}
type adoptedServiceContextKey struct{}

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

		if agentMode {
			if err := os.Setenv("GR_AGENT_MODE", "1"); err != nil {
				return fmt.Errorf("enable explicit agent mode: %w", err)
			}
		}

		if err := rejectConfigInsideSession(cmd); err != nil {
			return err
		}

		var err error

		if cmd == mcpProxyCmd {
			// The managed proxy's explicitly named aliases are its complete
			// bootstrap context. Do not load configuration or resolve paths from
			// canonical GRAITH_* variables: Codex deep-merges lower-layer literal
			// env values, so even an invalid stale profile must not run before the
			// proxy can select its authenticated alias set and exact socket.
			identity, identityErr := mcpProxyIdentityFromEnv(args[1:], os.LookupEnv)
			if identityErr != nil {
				return identityErr
			}

			paths, err = mcpProxyConnectionPaths(config.Paths{}, identity.profile, identity.socketPath)
			if err != nil {
				return err
			}
			cfg = config.Default()
		} else {
			if cfgFile != "" {
				cfgFile, err = canonicalConfigFile(cfgFile)
				if err != nil {
					return err
				}
			}

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
		}

		if bootstrap, _ := cmd.Context().Value(serviceBootstrapContextKey{}).(*daemonservice.Bootstrap); bootstrap != nil {
			if err := bootstrap.ValidateResolvedConfig(cfgFile, paths); err != nil {
				return err
			}
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
		// picker, status bar, message viewer, and handshake honour them. The refresh
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

func canonicalConfigFile(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}

	return filepath.Clean(absolute), nil
}

func Execute() error {
	return executeWithArgs(os.Args[1:])
}

func executeWithArgs(args []string) (err error) {
	previousAgentMode, hadAgentMode := os.LookupEnv("GR_AGENT_MODE")
	defer func() {
		if hadAgentMode {
			_ = os.Setenv("GR_AGENT_MODE", previousAgentMode)
		} else {
			_ = os.Unsetenv("GR_AGENT_MODE")
		}
	}()

	label, slot, serviceMarker, markerErr := serviceMarkerArgument(args)
	if markerErr != nil {
		out = output.New(false)
		out.Error(markerErr)

		return markerErr
	}

	adoptManifestPath := adoptFromArgument(args)

	var serviceBootstrap *daemonservice.Bootstrap

	if serviceMarker && adoptManifestPath == "" {
		bootstrap, bootstrapErr := daemonservice.BootstrapFreshService(label, slot, time.Now())
		if bootstrapErr != nil {
			out = output.New(false)
			out.Error(bootstrapErr)

			return bootstrapErr
		}

		serviceBootstrap = &bootstrap
		cfgFile = bootstrap.Request.ConfigFile

		defer func() {
			if err == nil {
				return
			}

			if abortErr := bootstrap.Abort(); abortErr != nil {
				err = errors.Join(err, fmt.Errorf("clear failed daemon service bootstrap: %w", abortErr))
			}
		}()
	}

	registerCommands()
	applyServiceBootstrapConfig(serviceBootstrap)

	rootCtx := context.Background()

	if adoptManifestPath != "" {
		identity := daemon.NewUnmanagedAdoptedServiceIdentity()
		if serviceMarker {
			identity, err = daemon.NewManagedAdoptedServiceIdentity(label, slot)
			if err != nil {
				return err
			}
		}

		rootCtx = context.WithValue(rootCtx, adoptedServiceContextKey{}, identity)
	}

	if serviceBootstrap != nil {
		rootCtx = context.WithValue(rootCtx, serviceBootstrapContextKey{}, serviceBootstrap)
	}

	rootCmd.SetContext(rootCtx)
	// Cobra caches the selected child command's inherited context after its first
	// execution. Refresh the hidden adoption command explicitly so repeated
	// executeWithArgs calls cannot reuse a prior managed-service identity.
	daemonStartCmd.SetContext(rootCtx)
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

func applyServiceBootstrapConfig(serviceBootstrap *daemonservice.Bootstrap) {
	// Persistent flag registration writes each flag's default into its target.
	// The launchd bootstrap has already supplied the canonical config path, so
	// restore it after registration when the static service argv has no
	// --config flag of its own.
	if serviceBootstrap != nil {
		cfgFile = serviceBootstrap.Request.ConfigFile
	}
}

// serviceMarkerArgument recognizes launchd's exact immutable marker before
// Cobra or config/profile resolution. A partial, duplicate, or empty marker
// fails closed instead of falling through to a manual daemon start.
func serviceMarkerArgument(args []string) (label, slot string, present bool, err error) {
	if len(args) < 2 || args[0] != "daemon" || args[1] != "start" {
		return "", "", false, nil
	}

	for index := 2; index < len(args); index++ {
		arg := args[index]
		switch {
		case strings.HasPrefix(arg, "--internal-service-label="):
			if label != "" {
				return "", "", true, errors.New("duplicate internal daemon service label")
			}

			label = strings.TrimPrefix(arg, "--internal-service-label=")
		case arg == "--internal-service-label":
			if label != "" || index+1 >= len(args) {
				return "", "", true, errors.New("invalid internal daemon service label")
			}

			index++
			label = args[index]
		case strings.HasPrefix(arg, "--internal-service-slot="):
			if slot != "" {
				return "", "", true, errors.New("duplicate internal daemon service slot")
			}

			slot = strings.TrimPrefix(arg, "--internal-service-slot=")
		case arg == "--internal-service-slot":
			if slot != "" || index+1 >= len(args) {
				return "", "", true, errors.New("invalid internal daemon service slot")
			}

			index++
			slot = args[index]
		}
	}

	present = label != "" || slot != ""
	if present && (label == "" || slot == "") {
		return "", "", true, errors.New("internal daemon service marker requires both label and slot")
	}

	if present {
		if _, validateErr := daemonservice.ValidateMarker(label, slot); validateErr != nil {
			return "", "", true, validateErr
		}
	}

	return label, slot, present, nil
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
