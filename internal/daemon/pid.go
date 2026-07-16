package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/d0ugal/graith/internal/tools"
)

var ErrDaemonRunning = errors.New("daemon already running")

func AcquirePIDFile(path string) error {
	if data, err := os.ReadFile(path); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && isPIDAlive(pid) {
			return fmt.Errorf("%w (pid %d)", ErrDaemonRunning, pid)
		}

		_ = os.Remove(path)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: concurrent start detected", ErrDaemonRunning)
		}

		return fmt.Errorf("create pid file: %w", err)
	}
	defer func() { _ = f.Close() }()

	_, err = fmt.Fprintf(f, "%d\n", os.Getpid())

	return err
}

func ReleasePIDFile(path string) {
	_ = os.Remove(path)
}

func isPIDAlive(pid int) bool {
	return isProcessAlive(pid)
}

func IsGraithDaemon(pid int) bool {
	if pid <= 1 {
		return false
	}

	if !isPIDAlive(pid) {
		return false
	}

	out, err := exec.Command(tools.PS(), "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}

	base := filepath.Base(strings.TrimSpace(string(out)))

	return base == "gr" || base == "graith"
}
