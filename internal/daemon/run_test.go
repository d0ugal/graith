package daemon

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRunControlLoopReloadsThenShutsDown(t *testing.T) {
	signals := make(chan os.Signal)
	upgrades := make(chan string)
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
		}, func(string) error {
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

func TestRunControlLoopReturnsUpgradeResult(t *testing.T) {
	signals := make(chan os.Signal)
	upgrades := make(chan string)
	wantErr := errors.New("upgrade failed")
	unexpected := make(chan string, 2)
	done := make(chan error, 1)

	go func() {
		done <- runControlLoop(signals, upgrades, discardLogger(), func() error {
			unexpected <- "reload callback ran"
			return nil
		}, func() {
			unexpected <- "shutdown callback ran"
		}, func(path string) error {
			if path != "/tmp/new-gr" {
				unexpected <- "upgrade callback received wrong path"
			}

			return wantErr
		})
	}()

	upgrades <- "/tmp/new-gr"

	if got := <-done; !errors.Is(got, wantErr) {
		t.Fatalf("runControlLoop error = %v, want %v", got, wantErr)
	}

	assertNoUnexpectedCallback(t, unexpected)
}

func TestRunControlLoopTerminalSignalsShutDown(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(signal.String(), func(t *testing.T) {
			signals := make(chan os.Signal)
			upgrades := make(chan string)
			shutdown := make(chan struct{}, 1)
			unexpected := make(chan string, 2)
			done := make(chan error, 1)

			go func() {
				done <- runControlLoop(signals, upgrades, discardLogger(), func() error {
					unexpected <- "reload callback ran"

					return nil
				}, func() {
					shutdown <- struct{}{}
				}, func(string) error {
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
