package commandpolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/commandpolicy/localmost"
)

func TestBuiltinBackendHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (builtinBackend{}).Evaluate(ctx, Request{
		ToolName: "Bash", ToolInput: `{"command":"echo braw"}`,
	}, Config{BuiltinInline: []byte(`{"rules":[{"command":"echo @*","policy":"allow"}]}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate error = %v, want context cancellation", err)
	}
}

func TestBuiltinBackendHonorsConfiguredTimeout(t *testing.T) {
	start := time.Now()

	_, err := (builtinBackend{}).Evaluate(context.Background(), Request{
		ToolName: "Bash", ToolInput: `{"command":"echo braw"}`,
	}, Config{
		BuiltinInline: []byte(`{"rules":[{"command":"echo @*","policy":"allow"}]}`),
		ExecTimeout:   time.Nanosecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Evaluate error = %v, want deadline exceeded", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("configured timeout returned after %v", elapsed)
	}
}

func TestBuiltinBackendBoundsUninterruptibleWork(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	backend := builtinBackend{loadEngine: func(Config) (*localmost.Engine, error) {
		close(started)
		<-release
		close(finished)

		return localmost.Parse([]byte(`{"rules":[]}`))
	}}

	start := time.Now()

	_, err := backend.Evaluate(context.Background(), Request{
		ToolName: "Bash", ToolInput: `{"command":"echo braw"}`,
	}, Config{BuiltinInline: []byte(`{"rules":[]}`), ExecTimeout: 100 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Evaluate error = %v, want deadline exceeded", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("blocked worker returned after %v", elapsed)
	}

	select {
	case <-started:
	default:
		t.Fatal("test loader never started")
	}

	close(release)

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("timed-out worker did not finish after release")
	}
}
