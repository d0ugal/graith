//go:build linux

package daemon

import (
	"context"
	"fmt"
	"os"
)

func openFDCounts(ctx context.Context, pids []int) map[int]int {
	counts := make(map[int]int, len(pids))
	for _, pid := range pids {
		if ctx.Err() != nil {
			return counts
		}

		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err == nil {
			counts[pid] = len(entries)
		}
	}

	return counts
}
