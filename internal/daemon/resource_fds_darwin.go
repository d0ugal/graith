//go:build darwin

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
)

// macOS does not expose per-process descriptors through /proc. One lsof call
// covers every process in all session groups for the sampling pass.
func openFDCounts(pids []int) map[int]int {
	counts := make(map[int]int, len(pids))
	if len(pids) == 0 {
		return counts
	}
	parts := make([]string, len(pids))
	for i, pid := range pids {
		parts[i] = strconv.Itoa(pid)
	}
	out, err := exec.Command("/usr/sbin/lsof", "-nP", "-a", "-p", strings.Join(parts, ","), "-Fpf").Output()
	if err != nil {
		return counts
	}
	return parseLsofFDCounts(string(out))
}

func parseLsofFDCounts(out string) map[int]int {
	counts := make(map[int]int)
	current := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			current, _ = strconv.Atoi(line[1:])
			counts[current] = 0
		case 'f':
			if current > 0 {
				counts[current]++
			}
		}
	}
	return counts
}
