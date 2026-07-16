//go:build darwin

package daemon

import "testing"

func TestParseLsofFDCounts(t *testing.T) {
	got := parseLsofFDCounts("p101\nf0\nf1\np202\nf4\nf5\nf6\n")
	if got[101] != 2 || got[202] != 3 {
		t.Fatalf("parseLsofFDCounts = %#v", got)
	}
}
