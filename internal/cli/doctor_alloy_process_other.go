//go:build !darwin && !linux

package cli

import (
	"os/exec"
	"time"
)

var doctorCommandWaitDelay = 500 * time.Millisecond

func configureDoctorCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = doctorCommandWaitDelay
}
