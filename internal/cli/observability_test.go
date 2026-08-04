package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/tools"
	"github.com/spf13/cobra"
)

func writeFakeAlloyBinary(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "alloy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: executable test stub
		t.Fatalf("write fake alloy binary: %v", err)
	}

	return path
}

func writeAlloyConfig(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "braw.alloy")
	if err := os.WriteFile(path, []byte("// braw alloy fixture\n"), 0o600); err != nil {
		t.Fatalf("write alloy config: %v", err)
	}

	return path
}

func TestPlanAlloyRunUsesGraithOwnedStorage(t *testing.T) {
	dir := t.TempDir()
	alloyBin := writeFakeAlloyBinary(t, dir)
	configPath := writeAlloyConfig(t, dir)
	tmpDir := filepath.Join(dir, "graith-tmp")

	tools.Configure(tools.Config{Alloy: alloyBin})
	t.Cleanup(tools.Reset)

	plan, err := planAlloyRun(config.Paths{TmpDir: tmpDir}, configPath, "")
	if err != nil {
		t.Fatalf("planAlloyRun: %v", err)
	}

	wantStorage := filepath.Join(tmpDir, "observability", "alloy")
	wantArgs := []string{
		"run",
		"--disable-reporting",
		"--storage.path=" + wantStorage,
		configPath,
	}

	if plan.Executable != alloyBin {
		t.Errorf("executable = %q, want %q", plan.Executable, alloyBin)
	}

	if !equalStringSlices(plan.Args, wantArgs) {
		t.Errorf("args = %#v, want %#v", plan.Args, wantArgs)
	}

	info, err := os.Stat(wantStorage)
	if err != nil {
		t.Fatalf("storage dir was not created: %v", err)
	}

	if !info.IsDir() {
		t.Fatalf("storage path is not a directory")
	}
}

func TestPlanAlloyRunHonoursStoragePath(t *testing.T) {
	dir := t.TempDir()
	alloyBin := writeFakeAlloyBinary(t, dir)
	configPath := writeAlloyConfig(t, dir)
	storagePath := filepath.Join(dir, "custom-storage")

	tools.Configure(tools.Config{Alloy: alloyBin})
	t.Cleanup(tools.Reset)

	plan, err := planAlloyRun(config.Paths{TmpDir: filepath.Join(dir, "tmp")}, configPath, storagePath)
	if err != nil {
		t.Fatalf("planAlloyRun: %v", err)
	}

	if plan.StorageDir != storagePath {
		t.Errorf("storage dir = %q, want %q", plan.StorageDir, storagePath)
	}

	if got := plan.Args[2]; got != "--storage.path="+storagePath {
		t.Errorf("storage arg = %q, want custom storage path", got)
	}
}

func TestRunObservabilityAlloyInvokesRunner(t *testing.T) {
	dir := t.TempDir()
	alloyBin := writeFakeAlloyBinary(t, dir)
	configPath := writeAlloyConfig(t, dir)
	storagePath := filepath.Join(dir, "dreich-storage")

	tools.Configure(tools.Config{Alloy: alloyBin})
	t.Cleanup(tools.Reset)

	oldStoragePath := alloyStoragePath
	oldSignals := runAlloySignals
	oldRunner := runAlloyProcess

	t.Cleanup(func() {
		alloyStoragePath = oldStoragePath
		runAlloySignals = oldSignals
		runAlloyProcess = oldRunner
	})

	alloyStoragePath = storagePath

	var (
		gotExecutable string
		gotArgs       []string
		gotStreams    alloyProcessStreams
	)

	runAlloyProcess = func(ctx context.Context, executable string, args []string, streams alloyProcessStreams) error {
		if ctx == nil {
			t.Fatal("runner received nil context")
		}

		gotExecutable = executable

		gotArgs = append([]string{}, args...)
		gotStreams = streams

		return nil
	}

	var (
		stdin  bytes.Buffer
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	cmd := &cobra.Command{}
	cmd.SetIn(&stdin)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetContext(withCommandDependencies(context.Background(), commandDependencies{
		cfg:   config.Default(),
		paths: config.Paths{TmpDir: filepath.Join(dir, "tmp")},
		out:   output.NewWithWriter(false, &stdout),
	}))

	if err := runObservabilityAlloy(cmd, []string{configPath}); err != nil {
		t.Fatalf("runObservabilityAlloy: %v", err)
	}

	wantArgs := []string{"run", "--disable-reporting", "--storage.path=" + storagePath, configPath}

	if gotExecutable != alloyBin {
		t.Errorf("executable = %q, want %q", gotExecutable, alloyBin)
	}

	if !equalStringSlices(gotArgs, wantArgs) {
		t.Errorf("args = %#v, want %#v", gotArgs, wantArgs)
	}

	if gotStreams.Stdin != &stdin {
		t.Errorf("stdin stream was not forwarded")
	}

	if gotStreams.Stdout != &stdout {
		t.Errorf("stdout stream was not forwarded")
	}

	if gotStreams.Stderr != &stderr {
		t.Errorf("stderr stream was not forwarded")
	}
}

func TestRunObservabilityAlloyRejectsSignalsWithSuppliedConfig(t *testing.T) {
	oldSignals := runAlloySignals

	t.Cleanup(func() {
		runAlloySignals = oldSignals
	})

	cmd := &cobra.Command{}
	cmd.Flags().StringVar(&runAlloySignals, "signals", configAlloyDefaultSignals, "")

	if err := cmd.Flags().Set("signals", "metrics"); err != nil {
		t.Fatalf("set signals flag: %v", err)
	}

	err := runObservabilityAlloy(cmd, []string{filepath.Join(t.TempDir(), "braw.alloy")})
	if err == nil {
		t.Fatal("runObservabilityAlloy succeeded, want --signals rejection")
	}

	if !strings.Contains(err.Error(), "--signals applies only to generated Alloy config") {
		t.Fatalf("error = %q, want --signals rejection", err)
	}
}

func TestRunObservabilityAlloyGeneratesConfigWhenPathOmitted(t *testing.T) {
	dir := t.TempDir()
	alloyBin := writeFakeAlloyBinary(t, dir)
	dataDir := filepath.Join(dir, "data")
	tmpDir := filepath.Join(dir, "tmp")

	tools.Configure(tools.Config{Alloy: alloyBin})
	t.Cleanup(tools.Reset)

	oldStoragePath := alloyStoragePath
	oldSignals := runAlloySignals
	oldRunner := runAlloyProcess

	t.Cleanup(func() {
		alloyStoragePath = oldStoragePath
		runAlloySignals = oldSignals
		runAlloyProcess = oldRunner
	})

	alloyStoragePath = ""
	runAlloySignals = "daemon-logs"

	var (
		gotExecutable string
		gotArgs       []string
	)

	runAlloyProcess = func(_ context.Context, executable string, args []string, _ alloyProcessStreams) error {
		gotExecutable = executable

		gotArgs = append([]string{}, args...)

		return nil
	}

	var stdout bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetContext(withCommandDependencies(context.Background(), commandDependencies{
		cfg: config.Default(),
		paths: config.Paths{
			DataDir: dataDir,
			TmpDir:  tmpDir,
		},
		out: output.NewWithWriter(false, &stdout),
	}))

	if err := runObservabilityAlloy(cmd, nil); err != nil {
		t.Fatalf("runObservabilityAlloy: %v", err)
	}

	generatedConfig := filepath.Join(tmpDir, "observability", "alloy.generated.alloy")
	wantArgs := []string{
		"run",
		"--disable-reporting",
		"--storage.path=" + filepath.Join(tmpDir, "observability", "alloy"),
		generatedConfig,
	}

	if gotExecutable != alloyBin {
		t.Errorf("executable = %q, want %q", gotExecutable, alloyBin)
	}

	if !equalStringSlices(gotArgs, wantArgs) {
		t.Errorf("args = %#v, want %#v", gotArgs, wantArgs)
	}

	data, err := os.ReadFile(generatedConfig)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	text := string(data)
	for _, want := range []string{
		"Generated by `gr config alloy`",
		filepath.Join(dataDir, "daemon.log"),
		filepath.Join(dataDir, "daemon.stderr.log"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated config missing %q:\n%s", want, text)
		}
	}
}

func TestRunAlloyForegroundInterruptsChildCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal semantics differ on Windows")
	}

	t.Setenv("GRAITH_ALLOY_HELPER", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		stdout lockedBuffer
		stderr lockedBuffer
	)

	errCh := make(chan error, 1)

	go func() {
		errCh <- runAlloyForeground(ctx, os.Args[0], []string{"-test.run=^TestAlloyForegroundSignalHelper$"}, alloyProcessStreams{
			Stdout: &stdout,
			Stderr: &stderr,
		})
	}()

	waitForBufferContains(t, &stdout, "ready")
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runAlloyForeground after interrupt = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runAlloyForeground did not return after interrupt")
	}

	if !strings.Contains(stderr.String(), "interrupted") {
		t.Fatalf("stderr = %q, want child interrupt marker", stderr.String())
	}
}

func TestRunAlloyForegroundForwardsTerminationSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal semantics differ on Windows")
	}

	t.Setenv("GRAITH_ALLOY_HELPER", "1")

	sigCh := make(chan os.Signal, 1)
	ctx := context.WithValue(context.Background(), alloySignalContextKey{}, sigCh)

	var (
		stdout lockedBuffer
		stderr lockedBuffer
	)

	errCh := make(chan error, 1)

	go func() {
		errCh <- runAlloyForeground(ctx, os.Args[0], []string{"-test.run=^TestAlloyForegroundSignalHelper$"}, alloyProcessStreams{
			Stdout: &stdout,
			Stderr: &stderr,
		})
	}()

	waitForBufferContains(t, &stdout, "ready")

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runAlloyForeground after terminate = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runAlloyForeground did not return after termination")
	}

	if !strings.Contains(stderr.String(), "terminated") {
		t.Fatalf("stderr = %q, want child termination marker", stderr.String())
	}
}

func TestAlloyForegroundSignalHelper(t *testing.T) {
	if os.Getenv("GRAITH_ALLOY_HELPER") != "1" {
		return
	}

	sigCh := make(chan os.Signal, 1)

	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	fmt.Println("ready")

	sig := <-sigCh

	switch sig {
	case os.Interrupt:
		fmt.Fprintln(os.Stderr, "interrupted")
	case syscall.SIGTERM:
		fmt.Fprintln(os.Stderr, "terminated")
	default:
		fmt.Fprintf(os.Stderr, "signal %v\n", sig)
	}

	os.Exit(0)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func waitForBufferContains(t *testing.T, buf *lockedBuffer, want string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("buffer = %q, want %q", buf.String(), want)
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}
