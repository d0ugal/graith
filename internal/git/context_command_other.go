//go:build !(darwin || linux)

package git

import "os/exec"

func configureContextCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = contextCommandWaitDelay
}
