//go:build darwin || linux

package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const contextCommandTerminateDelay = 100 * time.Millisecond

func configureContextCommand(cmd *exec.Cmd) {
	// Put git in its own session so group cancellation never reaches the daemon
	// and git cannot block on the daemon's controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = contextCommandWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}

		pid := cmd.Process.Pid
		if pid <= 1 {
			return fmt.Errorf("refusing to signal git process group for pid %d", pid)
		}

		err := syscall.Kill(-pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		if err != nil {
			return err
		}

		time.Sleep(contextCommandTerminateDelay)

		err = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()

		if err == nil {
			return nil
		}

		if errors.Is(err, syscall.ESRCH) {
			return nil
		}

		return err
	}
}
