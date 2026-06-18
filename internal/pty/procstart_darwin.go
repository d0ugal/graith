package pty

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processStartTime returns a value that uniquely identifies the process with
// the given PID (microseconds since the Unix epoch). If the PID is recycled,
// the new process will have a different start time.
func processStartTime(pid int) (int64, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("sysctl kern.proc.pid.%d: %w", pid, err)
	}
	tv := info.Proc.P_starttime
	return tv.Sec*1_000_000 + int64(tv.Usec), nil
}
