//go:build darwin

package daemon

import (
	"os/exec"
	"testing"
)

func TestParseLsofFDCounts(t *testing.T) {
	got := parseLsofFDCounts("p101\nfcwd\nftxt\nf0\nf1\np202\nfmem\nfrtd\nf4\nf5\nf6\n")
	if got[101] != 2 || got[202] != 3 {
		t.Fatalf("parseLsofFDCounts = %#v", got)
	}
}

func TestOpenFDCountsKeepsPartialLsofOutput(t *testing.T) {
	original := lsofOutput

	t.Cleanup(func() { lsofOutput = original })

	lsofOutput = func(string) ([]byte, error) {
		return []byte("p101\nf0\nf1\n"), &exec.ExitError{}
	}

	got := openFDCounts([]int{101, 202})
	if got[101] != 2 {
		t.Fatalf("openFDCounts partial output = %#v", got)
	}
}
