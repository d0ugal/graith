//go:build darwin

package daemon

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

var lsofOutput = func(pids string) ([]byte, error) {
	return exec.Command("/usr/sbin/lsof", "-nP", "-a", "-p", pids, "-Fpf").Output()
}

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

	out, err := lsofOutput(strings.Join(parts, ","))
	// lsof exits 1 when any requested PID vanished, while still returning
	// useful records for the live PIDs. Preserve that partial snapshot. Other
	// execution failures (or an empty result) leave counts unknown.
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || len(out) == 0) {
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
				// lsof also emits pseudo-descriptors such as cwd, txt, mem,
				// and rtd. Linux /proc/<pid>/fd contains only numeric file
				// descriptors, so count numeric values here for comparable data.
				if _, err := strconv.Atoi(line[1:]); err == nil {
					counts[current]++
				}
			}
		}
	}

	return counts
}
