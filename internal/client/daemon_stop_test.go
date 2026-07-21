package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const daemonStopHelperEnv = "GRAITH_DAEMON_STOP_HELPER"

func TestStopDaemonByPIDRefusesForeignProcess(t *testing.T) {
	process := startDaemonStopHelper(t)
	pidFile := filepath.Join(t.TempDir(), "bothy.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if stopDaemonByPID(pidFile) {
		t.Fatal("stopDaemonByPID reported stopping a foreign process")
	}

	if err := process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("foreign process was no longer running: %v", err)
	}
}

func TestStopDaemonByPIDRefusesStaleProcess(t *testing.T) {
	process := startDaemonStopHelper(t)
	pid := process.Pid
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}

	state, err := process.Wait()
	if err != nil {
		t.Fatal(err)
	}

	if state.Success() {
		t.Fatal("killed helper process unexpectedly exited successfully")
	}

	pidFile := filepath.Join(t.TempDir(), "dreich.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if stopDaemonByPID(pidFile) {
		t.Fatal("stopDaemonByPID reported stopping a stale process")
	}
}

func startDaemonStopHelper(t *testing.T) *os.Process {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(executable, "-test.run=^TestStopDaemonByPIDHelperProcess$")
	command.Env = append(os.Environ(), daemonStopHelperEnv+"=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})

	return command.Process
}

func TestStopDaemonByPIDHelperProcess(t *testing.T) {
	if os.Getenv(daemonStopHelperEnv) != "1" {
		return
	}

	time.Sleep(time.Hour)
}
