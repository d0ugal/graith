//go:build darwin

package pty

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const terminalBenchmarkRSSProbeEnv = "GRAITH_TERMINAL_RSS_PROBE"

func terminalBenchmarkCurrentRSS(pids []int) ([]int64, bool) {
	probe := os.Getenv(terminalBenchmarkRSSProbeEnv)
	if probe == "" || len(pids) == 0 {
		return nil, false
	}

	args := make([]string, len(pids))
	for i, pid := range pids {
		args[i] = strconv.Itoa(pid)
	}

	// The measurement script builds this repository-owned testdata probe into
	// its disposable native work directory. macOS blocks ps(1) in sandboxed
	// tests, while proc_pid_rusage reports current resident bytes directly.
	output, err := exec.Command(probe, args...).Output() //nolint:gosec // Test-only repository-owned probe selected by the benchmark script.
	if err != nil {
		return nil, false
	}

	fields := strings.Fields(string(output))
	if len(fields) != len(pids) {
		return nil, false
	}

	rss := make([]int64, len(fields))
	for i, field := range fields {
		value, err := strconv.ParseInt(field, 10, 64)
		if err != nil || value <= 0 {
			return nil, false
		}

		rss[i] = value
	}

	return rss, true
}
