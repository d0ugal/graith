//go:build !darwin

package pty

import "time"

func terminalBenchmarkProcessUsage() (time.Duration, int64, bool) {
	return 0, 0, false
}

func terminalBenchmarkCurrentRSS([]int) ([]int64, bool) {
	return nil, false
}
