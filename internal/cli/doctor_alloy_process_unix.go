//go:build darwin || linux

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const doctorCommandTerminateDelay = 100 * time.Millisecond

var doctorCommandWaitDelay = 500 * time.Millisecond

func configureDoctorCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = doctorCommandWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}

		pid := cmd.Process.Pid
		if pid <= 1 {
			return fmt.Errorf("refusing to signal doctor process group for pid %d", pid)
		}

		err := syscall.Kill(-pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		if err != nil {
			return err
		}

		time.Sleep(doctorCommandTerminateDelay)

		err = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()

		if err == nil || errors.Is(err, syscall.ESRCH) {
			return nil
		}

		return err
	}
}
