package pty

import (
	"os"
	"testing"
)

func TestProcessStartTimeSelf(t *testing.T) {
	pid := os.Getpid()
	st, err := ProcessStartTime(pid)
	if err != nil {
		t.Fatalf("ProcessStartTime(%d) error: %v", pid, err)
	}
	if st == 0 {
		t.Fatal("ProcessStartTime returned 0 for current process")
	}

	st2, err := ProcessStartTime(pid)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if st != st2 {
		t.Errorf("start time changed between calls: %d vs %d", st, st2)
	}
}

func TestProcessStartTimeInvalidPID(t *testing.T) {
	// PID 0 is the kernel; negative PIDs should also fail. Use a very high
	// PID that is almost certainly not in use.
	_, err := ProcessStartTime(4_000_000)
	if err == nil {
		t.Error("expected error for non-existent PID, got nil")
	}
}
