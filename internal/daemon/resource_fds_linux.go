//go:build linux

package daemon

import (
	"fmt"
	"os"
)

func openFDCounts(pids []int) map[int]int {
	counts := make(map[int]int, len(pids))
	for _, pid := range pids {
		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err == nil {
			counts[pid] = len(entries)
		}
	}
	return counts
}
