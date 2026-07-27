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
	if data, err := os.ReadFile(path); err == nil { // #nosec G703 -- pid file path comes from daemon-controlled config paths.
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && isPIDAlive(pid) {
			return fmt.Errorf("%w (pid %d)", ErrDaemonRunning, pid)
		}

		_ = os.Remove(path) // #nosec G703 -- stale pid file path is the same daemon-controlled path read above.
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G703 -- pid file path comes from daemon-controlled config paths.
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
