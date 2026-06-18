//go:build !linux && !darwin

package pty

import "fmt"

func processStartTime(pid int) (int64, error) {
	return 0, fmt.Errorf("process start time not supported on this platform")
}
