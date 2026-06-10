package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	defer f.Close()

	_, err = fmt.Fprintf(f, "%d\n", os.Getpid())
	return err
}

func ReleasePIDFile(path string) {
	_ = os.Remove(path)
}

func isPIDAlive(pid int) bool {
	return isProcessAlive(pid)
}
