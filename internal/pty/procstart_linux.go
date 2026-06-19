package pty

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProcessStartTime returns a value that uniquely identifies the process with
// the given PID (clock ticks since boot from /proc/[pid]/stat field 22). If
// the PID is recycled, the new process will have a different start time.
func ProcessStartTime(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(data)
	// Field 2 (comm) is parenthesized and may contain spaces; skip past it.
	closeParen := strings.LastIndex(s, ")")
	if closeParen < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	// After ")  " the remaining fields start at field 3 (state).
	// starttime is field 22, so it's at index 22-3 = 19 in the split.
	fields := strings.Fields(s[closeParen+2:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("too few fields in /proc/%d/stat", pid)
	}
	return strconv.ParseInt(fields[19], 10, 64)
}
