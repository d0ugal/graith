//go:build darwin

package daemon

import (
	"context"
	"os/exec"
	"testing"
	"time"
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

	lsofOutput = func(context.Context, string) ([]byte, error) {
		return []byte("p101\nf0\nf1\n"), &exec.ExitError{}
	}

	got := openFDCounts(t.Context(), []int{101, 202})
	if got[101] != 2 {
		t.Fatalf("openFDCounts partial output = %#v", got)
	}
}

func TestOpenFDCountsCancelsLsofSampling(t *testing.T) {
	original := lsofOutput

	t.Cleanup(func() { lsofOutput = original })

	started := make(chan struct{})
	lsofOutput = func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		openFDCounts(ctx, []int{101})
		close(done)
	}()

	<-started
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lsof sampling did not stop after cancellation")
	}
}
