package daemon

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
	grpty "github.com/d0ugal/graith/internal/pty"
)

func TestResolvedUpgradeSnapshotPathsDoesNotRetainRemovedDataDir(t *testing.T) {
	defaults, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}

	running := defaults.WithDataDir(filepath.Join(t.TempDir(), "croft-custom-data"))
	snapshot := config.Default() // data_dir deliberately removed

	got, err := resolvedUpgradeSnapshotPaths(snapshot, defaults.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}

	want := makeUpgradePathDescriptor(defaults, defaults.ConfigFile)
	if got != want {
		t.Fatalf("fresh snapshot paths = %+v, want replacement defaults %+v", got, want)
	}

	if got == makeUpgradePathDescriptor(running, defaults.ConfigFile) {
		t.Fatal("removed data_dir inherited the running daemon's custom paths")
	}
}

func TestValidateAdoptedServiceIdentity(t *testing.T) {
	original := validateRetainedAdoptedService

	t.Cleanup(func() { validateRetainedAdoptedService = original })

	var calls int

	validateRetainedAdoptedService = func(label, slot, profile, candidatePath string) (daemonservice.Definition, error) {
		calls++

		if label != "net.graith.daemon.profile.01" || slot != "01" || profile != "croft" || candidatePath != "/bothy/Graith.app/Contents/MacOS/graith" {
			return daemonservice.Definition{}, fmt.Errorf("unexpected validation arguments: label=%q slot=%q profile=%q candidate=%q", label, slot, profile, candidatePath)
		}

		return daemonservice.Definition{Slot: "01"}, nil
	}

	if err := validateAdoptedServiceIdentity(NewUnmanagedAdoptedServiceIdentity(), "croft", ""); err != nil {
		t.Fatalf("explicit unmanaged identity: %v", err)
	}

	if calls != 0 {
		t.Fatalf("unmanaged validation calls = %d, want 0", calls)
	}

	if err := validateAdoptedServiceIdentity(AdoptedServiceIdentity{}, "croft", ""); err == nil ||
		!strings.Contains(err.Error(), "not explicitly selected") {
		t.Fatalf("zero identity error = %v, want explicit-selection failure", err)
	}

	if calls != 0 {
		t.Fatalf("zero identity reached service validator %d times, want 0", calls)
	}

	for _, fields := range [][2]string{
		{"", ""},
		{"net.graith.daemon.profile.01", ""},
		{"", "01"},
	} {
		if _, err := NewManagedAdoptedServiceIdentity(fields[0], fields[1]); err == nil ||
			!strings.Contains(err.Error(), "both service label and slot") {
			t.Fatalf("managed constructor (%q, %q) error = %v, want fail-closed error", fields[0], fields[1], err)
		}
	}

	identity, err := NewManagedAdoptedServiceIdentity("net.graith.daemon.profile.01", "01")
	if err != nil {
		t.Fatal(err)
	}

	if err := validateAdoptedServiceIdentity(identity, "croft", "/bothy/Graith.app/Contents/MacOS/graith"); err != nil {
		t.Fatalf("valid managed identity: %v", err)
	}

	if calls != 1 {
		t.Fatalf("managed validation calls = %d, want 1", calls)
	}

	wantErr := errors.New("profile mismatch")
	validateRetainedAdoptedService = func(_, _, _, _ string) (daemonservice.Definition, error) {
		return daemonservice.Definition{}, wantErr
	}

	if err := validateAdoptedServiceIdentity(identity, "dreich", "/bothy/Graith.app/Contents/MacOS/graith"); !errors.Is(err, wantErr) {
		t.Fatalf("bad bound profile error = %v, want %v", err, wantErr)
	}
}

func TestRunCleansUpgradeProcessesWhenBootstrapFails(t *testing.T) {
	dir := t.TempDir()

	listenerR, listenerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closeUpgradeTestFile(t, listenerW)

	listenerFD := duplicateTransferredFileFD(t, listenerR)

	sessionR, sessionW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closeUpgradeTestFile(t, sessionW)

	sessionFD := duplicateTransferredFileFD(t, sessionR)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	manifest := validUpgradeManifestForBoundaryTest(t, listenerFD, []UpgradeSession{{
		ID: "thrawn-headless", Fd: sessionFD,
		ScrollbackFd: openUpgradeScrollbackFD(t, filepath.Join(dir, "thrawn.log")),
		PID:          pid, PIDStartTime: start,
	}})

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	executableInfo, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}

	manifest.Target = UpgradeTargetDescriptor{
		ResolvedPath: executable,
		Size:         executableInfo.Size(),
		Mode:         uint32(executableInfo.Mode()),
		ModTimeNanos: executableInfo.ModTime().UnixNano(),
		SHA256:       mustDigestUpgradeFile(t, executable),
	}
	manifest.ConfigPresent = true
	manifest.ConfigSnapshot = []byte("dreich = [")

	manifestPath, err := WriteManifest(dir, manifest)
	if err != nil {
		t.Fatal(err)
	}

	if err := prepareManifestHandoff(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	if err := prepareOwnershipCapsule(manifest); err != nil {
		t.Fatal(err)
	}

	t.Setenv(upgradeOwnershipCapsuleEnv, manifest.ownershipCapsule)
	t.Setenv(upgradeOwnershipFDEnv, strconv.Itoa(manifest.ownershipFD))

	err = runUnmanagedAdoptBootstrap("", manifestPath)
	if err == nil || !strings.Contains(err.Error(), "loading exact upgrade config snapshot") {
		t.Fatalf("RunAdoptBootstrap error = %v, want config bootstrap failure", err)
	}

	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("inherited process remains after bootstrap failure: %v", err)
	}
}

func mustDigestUpgradeFile(t *testing.T, path string) string {
	t.Helper()

	digest, err := digestFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return digest
}

func TestRunControlLoopReloadsThenShutsDown(t *testing.T) {
	signals := make(chan os.Signal)
	upgrades := make(chan *upgradeRequest)
	wantReloadErr := errors.New("invalid config")
	reloaded := make(chan struct{}, 1)
	shutdown := make(chan struct{}, 1)
	unexpected := make(chan string, 1)
	done := make(chan error, 1)

	go func() {
		done <- runControlLoop(signals, upgrades, discardLogger(), func() error {
			reloaded <- struct{}{}
			return wantReloadErr
		}, func() {
			shutdown <- struct{}{}
		}, func(*upgradeRequest) error {
			unexpected <- "upgrade callback ran"
			return nil
		})
	}()

	signals <- syscall.SIGHUP

	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("reload callback did not run")
	}

	signals <- syscall.SIGTERM

	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback did not run")
	}

	if err := <-done; err != nil {
		t.Fatalf("runControlLoop returned %v, want nil", err)
	}

	assertNoUnexpectedCallback(t, unexpected)
}

func TestRunControlLoopKeepsServingAfterUpgradeError(t *testing.T) {
	signals := make(chan os.Signal)
	upgrades := make(chan *upgradeRequest)
	wantErr := errors.New("upgrade failed")
	callback := make(chan struct{}, 1)
	shutdown := make(chan struct{}, 1)
	done := make(chan error, 1)

	go func() {
		done <- runControlLoop(signals, upgrades, discardLogger(), func() error {
			return nil
		}, func() {
			shutdown <- struct{}{}
		}, func(request *upgradeRequest) error {
			if request.execPath != "/tmp/new-gr" {
				t.Errorf("upgrade callback path = %q", request.execPath)
			}

			callback <- struct{}{}

			return wantErr
		})
	}()

	upgrades <- newUpgradeRequest("/tmp/new-gr")

	<-callback

	signals <- syscall.SIGTERM

	<-shutdown

	if got := <-done; got != nil {
		t.Fatalf("runControlLoop error = %v, want nil", got)
	}
}

func TestRunControlLoopFailsClosedAfterUnsafeDescriptorRollback(t *testing.T) {
	signals := make(chan os.Signal)
	upgrades := make(chan *upgradeRequest)
	shutdown := make(chan struct{}, 1)
	done := make(chan error, 1)
	wantErr := unsafeUpgradeDescriptor(errors.New("descriptor flags remain inheritable"))

	go func() {
		done <- runControlLoop(signals, upgrades, discardLogger(), func() error {
			return nil
		}, func() {
			shutdown <- struct{}{}
		}, func(*upgradeRequest) error {
			return wantErr
		})
	}()

	upgrades <- newUpgradeRequest("/tmp/new-gr")

	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("unsafe rollback did not shut down the daemon")
	}

	var unsafeErr *upgradeDescriptorSafetyError
	if err := <-done; !errors.As(err, &unsafeErr) {
		t.Fatalf("runControlLoop error = %v, want unsafe descriptor error", err)
	}
}

func TestRunControlLoopContinuesAfterPreMutationUpgradeRejection(t *testing.T) {
	signals := make(chan os.Signal)
	upgrades := make(chan *upgradeRequest)
	shutdown := make(chan struct{}, 1)
	done := make(chan error, 1)

	go func() {
		done <- runControlLoop(signals, upgrades, discardLogger(), func() error { return nil }, func() {
			shutdown <- struct{}{}
		}, func(*upgradeRequest) error {
			return fmt.Errorf("%w: dreich candidate", errUpgradeRejected)
		})
	}()

	upgrades <- newUpgradeRequest("/bothy/unrecorded-gr")

	select {
	case err := <-done:
		t.Fatalf("daemon exited after rejected upgrade: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	signals <- syscall.SIGTERM

	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("daemon did not remain available for clean shutdown")
	}

	if err := <-done; err != nil {
		t.Fatalf("runControlLoop returned %v, want nil", err)
	}
}

func TestRunControlLoopTerminalSignalsShutDown(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(signal.String(), func(t *testing.T) {
			signals := make(chan os.Signal)
			upgrades := make(chan *upgradeRequest)
			shutdown := make(chan struct{}, 1)
			unexpected := make(chan string, 2)
			done := make(chan error, 1)

			go func() {
				done <- runControlLoop(signals, upgrades, discardLogger(), func() error {
					unexpected <- "reload callback ran"

					return nil
				}, func() {
					shutdown <- struct{}{}
				}, func(*upgradeRequest) error {
					unexpected <- "upgrade callback ran"

					return nil
				})
			}()

			signals <- signal

			select {
			case <-shutdown:
			case <-time.After(time.Second):
				t.Fatal("shutdown callback did not run")
			}

			if err := <-done; err != nil {
				t.Fatalf("runControlLoop returned %v, want nil", err)
			}

			assertNoUnexpectedCallback(t, unexpected)
		})
	}
}

func TestCleanupLegacyDaemonRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(dir, "graith.sock")
	pidPath := filepath.Join(dir, "graith.pid")

	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupLegacyDaemonDirs([]string{dir}, discardLogger(), func(_, _ string, _ time.Duration) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}, func(int) bool { return false }, func(int, syscall.Signal) error { return nil })
	assertNotExist(t, socketPath)
	assertNotExist(t, pidPath)
}

func TestCleanupLegacyDaemonRemovesReachableSocket(t *testing.T) {
	dir := t.TempDir()

	socketPath := filepath.Join(dir, "graith.sock")
	if err := os.WriteFile(socketPath, []byte("socket-placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}

	pidPath := filepath.Join(dir, "graith.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}

	peer, daemon := net.Pipe()

	t.Cleanup(func() { _ = peer.Close() })
	cleanupLegacyDaemonDirs([]string{dir}, discardLogger(), func(network, address string, timeout time.Duration) (net.Conn, error) {
		if network != "unix" || address != socketPath || timeout != 500*time.Millisecond {
			t.Fatalf("dial(%q, %q, %v)", network, address, timeout)
		}

		return daemon, nil
	}, func(int) bool { return false }, func(int, syscall.Signal) error { return nil })

	assertNotExist(t, socketPath)
	assertNotExist(t, pidPath)
}

func TestCleanupLegacyDaemonSignalsVerifiedProcess(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "graith.sock")
	pidPath := filepath.Join(dir, "graith.pid")

	if err := os.WriteFile(socketPath, []byte("socket-placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(pidPath, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	peer, daemon := net.Pipe()

	t.Cleanup(func() { _ = peer.Close() })

	signaled := make(chan struct{}, 1)

	cleanupLegacyDaemonDirs([]string{dir}, discardLogger(), func(_, _ string, _ time.Duration) (net.Conn, error) {
		return daemon, nil
	}, func(pid int) bool {
		return pid == 4242
	}, func(pid int, signal syscall.Signal) error {
		if pid != 4242 || signal != syscall.SIGTERM {
			t.Errorf("kill(%d, %v), want kill(4242, SIGTERM)", pid, signal)
		}

		signaled <- struct{}{}

		return nil
	})

	select {
	case <-signaled:
	case <-time.After(time.Second):
		t.Fatal("verified legacy daemon was not signaled")
	}

	assertNotExist(t, socketPath)
	assertNotExist(t, pidPath)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists or stat failed unexpectedly: %v", path, err)
	}
}

func assertNoUnexpectedCallback(t *testing.T, unexpected <-chan string) {
	t.Helper()

	select {
	case message := <-unexpected:
		t.Fatal(message)
	default:
	}
}
