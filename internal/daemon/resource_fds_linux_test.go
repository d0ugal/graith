//go:build linux

package daemon

import (
	"os"
	"testing"
)

func TestOpenFDCounts(t *testing.T) {
	got := openFDCounts([]int{os.Getpid(), 4_000_000})
	if got[os.Getpid()] <= 0 {
		t.Fatalf("openFDCounts(self) = %#v", got)
	}

	if _, ok := got[4_000_000]; ok {
		t.Errorf("openFDCounts included missing PID: %#v", got)
	}
}
