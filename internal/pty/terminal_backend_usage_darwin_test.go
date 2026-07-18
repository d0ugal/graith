//go:build darwin

package pty

import (
	"time"

	"golang.org/x/sys/unix"
)

func terminalBenchmarkProcessUsage() (time.Duration, int64, bool) {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0, 0, false
	}

	cpu := time.Duration(usage.Utime.Sec+usage.Stime.Sec)*time.Second +
		time.Duration(usage.Utime.Usec+usage.Stime.Usec)*time.Microsecond

	return cpu, usage.Maxrss, true
}
