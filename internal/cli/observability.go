package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/tools"
	"github.com/spf13/cobra"
)

const (
	alloyShutdownGrace  = 10 * time.Second
	alloyTerminateGrace = 2 * time.Second
)

type alloySignalContextKey struct{}

var (
	alloyStoragePath string
	runAlloySignals  = configAlloyDefaultSignals
	runAlloyProcess  = runAlloyForeground
)

type alloyRunPlan struct {
	Executable string
	Args       []string
	ConfigPath string
	StorageDir string
}

type alloyProcessStreams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

var observabilityCmd = &cobra.Command{
	Use:   "observability",
	Short: "Run local observability helpers",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var observabilityRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run observability helpers in the foreground",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var observabilityRunAlloyCmd = &cobra.Command{
	Use:   "alloy [config-path]",
	Short: "Run installed Grafana Alloy in the foreground",
	Long: "Run an already-installed Grafana Alloy binary in the foreground with generated config or a supplied config file or directory.\n\n" +
		"This command is for testing and development. It does not install Alloy, register a service, or keep Alloy running after the CLI exits.",
	Args: cobra.MaximumNArgs(1),
	RunE: runObservabilityAlloy,
}

func runObservabilityAlloy(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && cmd.Flags().Changed("signals") {
		return errors.New("--signals applies only to generated Alloy config; omit the config path to use it")
	}

	deps := commandDeps(cmd.Context())

	runPaths, configPath, err := resolveAlloyRunConfig(deps.cfg, deps.paths, args)
	if err != nil {
		return err
	}

	plan, err := planAlloyRun(runPaths, configPath, alloyStoragePath)
	if err != nil {
		return err
	}

	ctx := withAlloySignalContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopAlloySignalContext(ctx)

	return runAlloyProcess(ctx, plan.Executable, plan.Args, alloyProcessStreams{
		Stdin:  cmd.InOrStdin(),
		Stdout: cmd.OutOrStdout(),
		Stderr: cmd.ErrOrStderr(),
	})
}

func withAlloySignalContext(parent context.Context, signals ...os.Signal) context.Context {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, signals...)

	return context.WithValue(parent, alloySignalContextKey{}, sigCh)
}

func stopAlloySignalContext(ctx context.Context) {
	if sigCh, ok := ctx.Value(alloySignalContextKey{}).(chan os.Signal); ok {
		signal.Stop(sigCh)
	}
}

func alloySignalContext(ctx context.Context) <-chan os.Signal {
	if sigCh, ok := ctx.Value(alloySignalContextKey{}).(chan os.Signal); ok {
		return sigCh
	}

	return nil
}

func resolveAlloyRunConfig(cfg *config.Config, paths config.Paths, args []string) (config.Paths, string, error) {
	if len(args) > 0 {
		return paths, args[0], nil
	}

	generatedPath, resolvedPaths, err := writeGeneratedAlloyRunConfig(cfg, paths, runAlloySignals)
	if err != nil {
		return config.Paths{}, "", err
	}

	return resolvedPaths, generatedPath, nil
}

func writeGeneratedAlloyRunConfig(cfg *config.Config, paths config.Paths, rawSignals string) (string, config.Paths, error) {
	if cfg == nil {
		cfg = config.Default()
	}

	signals, err := parseAlloySignals(rawSignals)
	if err != nil {
		return "", config.Paths{}, err
	}

	resolvedPaths, err := resolvedAlloyPaths(paths, cfg)
	if err != nil {
		return "", config.Paths{}, err
	}

	text, err := renderAlloyConfig(alloyRenderInput{
		Config:  cfg,
		Paths:   resolvedPaths,
		Signals: signals,
	})
	if err != nil {
		return "", config.Paths{}, err
	}

	dir := filepath.Join(resolvedPaths.TmpDir, "observability")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", config.Paths{}, fmt.Errorf("create generated Alloy config directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, "alloy.generated.alloy")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", config.Paths{}, fmt.Errorf("write generated Alloy config %s: %w", path, err)
	}

	return path, resolvedPaths, nil
}

func planAlloyRun(paths config.Paths, rawConfigPath, rawStoragePath string) (alloyRunPlan, error) {
	executable, err := resolveAlloyExecutable(tools.Alloy())
	if err != nil {
		return alloyRunPlan{}, err
	}

	configPath, err := resolveAlloyConfigPath(rawConfigPath)
	if err != nil {
		return alloyRunPlan{}, err
	}

	storageDir, err := ensureAlloyStorageDir(paths, rawStoragePath)
	if err != nil {
		return alloyRunPlan{}, err
	}

	return alloyRunPlan{
		Executable: executable,
		Args: []string{
			"run",
			"--disable-reporting",
			"--storage.path=" + storageDir,
			configPath,
		},
		ConfigPath: configPath,
		StorageDir: storageDir,
	}, nil
}

func resolveAlloyExecutable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("alloy binary %q not found; install Grafana Alloy separately or set tools.alloy: %w", name, err)
	}

	return path, nil
}

func resolveAlloyConfigPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("alloy config path is required")
	}

	path := config.ExpandPath(raw)

	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Alloy config path: %w", err)
	}

	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("stat Alloy config path %s: %w", absolute, err)
	}

	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", fmt.Errorf("alloy config path %s must be a file or directory", absolute)
	}

	return filepath.Clean(absolute), nil
}

func ensureAlloyStorageDir(paths config.Paths, raw string) (string, error) {
	path := raw
	if path == "" {
		if paths.TmpDir == "" {
			return "", errors.New("graith temporary directory is not configured")
		}

		path = filepath.Join(paths.TmpDir, "observability", "alloy")
	}

	path = config.ExpandPath(path)

	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Alloy storage path: %w", err)
	}

	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", fmt.Errorf("create Alloy storage path %s: %w", absolute, err)
	}

	return filepath.Clean(absolute), nil
}

func runAlloyForeground(ctx context.Context, executable string, args []string, streams alloyProcessStreams) error {
	cmd := exec.Command(executable, args...) //nolint:gosec // executable is resolved through tools.alloy; args are fixed.
	cmd.Stdin = streams.Stdin
	cmd.Stdout = streams.Stdout
	cmd.Stderr = streams.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	sigCh := alloySignalContext(ctx)

	select {
	case err := <-done:
		if ctx.Err() != nil {
			return nil
		}

		return err
	case sig := <-sigCh:
		return stopAlloyForeground(cmd, done, sig)
	case <-ctx.Done():
		return stopAlloyForeground(cmd, done, os.Interrupt)
	}
}

func stopAlloyForeground(cmd *exec.Cmd, done <-chan error, sig os.Signal) error {
	if sig == nil {
		sig = os.Interrupt
	}

	if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-done:
		return nil
	case <-time.After(alloyShutdownGrace):
		if sig != syscall.SIGTERM {
			_ = cmd.Process.Signal(syscall.SIGTERM)

			select {
			case <-done:
				return nil
			case <-time.After(alloyTerminateGrace):
			}
		}

		_ = cmd.Process.Kill()
		err := <-done

		return fmt.Errorf("alloy did not exit within %s after interrupt: %w", alloyShutdownGrace+alloyTerminateGrace, err)
	}
}

func registerObservabilityCmd() {
	rootCmd.AddCommand(observabilityCmd)
	observabilityCmd.AddCommand(observabilityRunCmd)
	observabilityRunCmd.AddCommand(observabilityRunAlloyCmd)
	observabilityRunAlloyCmd.Flags().StringVar(
		&runAlloySignals,
		"signals",
		configAlloyDefaultSignals,
		"comma-separated signals for generated config (daemon-logs,metrics,traces,all)",
	)
	observabilityRunAlloyCmd.Flags().StringVar(&alloyStoragePath, "storage-path", "", "Alloy storage directory (default: <data_dir>/tmp/observability/alloy)")
}
