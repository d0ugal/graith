package protocol

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	w := NewFrameWriter(&buf)
	r := NewFrameReader(&buf)

	if err := w.WriteFrame(ChannelControl, []byte(`{"type":"list"}`)); err != nil {
		t.Fatal(err)
	}

	if err := w.WriteFrame(ChannelData, []byte("blether pty")); err != nil {
		t.Fatal(err)
	}

	f1, err := r.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if f1.Channel != ChannelControl || string(f1.Payload) != `{"type":"list"}` {
		t.Errorf("frame 1: channel=%d payload=%q", f1.Channel, f1.Payload)
	}

	f2, err := r.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if f2.Channel != ChannelData || string(f2.Payload) != "blether pty" {
		t.Errorf("frame 2: channel=%d payload=%q", f2.Channel, f2.Payload)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer

	w := NewFrameWriter(&buf)
	r := NewFrameReader(&buf)

	if err := w.WriteFrame(ChannelControl, []byte{}); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	f, err := r.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}

	if len(f.Payload) != 0 {
		t.Errorf("expected empty payload, got %d bytes", len(f.Payload))
	}
}

func TestFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer

	w := NewFrameWriter(&buf)
	big := make([]byte, MaxPayload+1)

	err := w.WriteFrame(ChannelData, big)
	if err == nil {
		t.Error("expected error for oversized payload")
	}
}

// serializedWriter makes each Write call atomic via a mutex but allows
// concurrent WriteFrame callers to interleave between separate Write calls.
// With the old two-write FrameWriter, goroutine A's header could be followed
// by goroutine B's header before A's payload, producing a torn frame.
type serializedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *serializedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.Write(p)
}

func TestWriteFrameAtomicity(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewFrameWriter(pw)
	r := NewFrameReader(pr)

	const (
		goroutines         = 8
		framesPerGoroutine = 200
	)

	writeErrs := make(chan error, goroutines*framesPerGoroutine)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := range goroutines {
		go func() {
			defer wg.Done()

			payload := fmt.Appendf(nil, "goroutine-%d", g)
			for range framesPerGoroutine {
				if err := w.WriteFrame(ChannelData, payload); err != nil {
					writeErrs <- fmt.Errorf("goroutine %d: %w", g, err)
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(writeErrs)

		_ = pw.Close()
	}()

	total := goroutines * framesPerGoroutine

	expected := make(map[string]bool, goroutines)
	for g := range goroutines {
		expected[fmt.Sprintf("goroutine-%d", g)] = true
	}

	for i := range total {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("failed to read frame %d/%d: %v", i+1, total, err)
		}

		if f.Channel != ChannelData {
			t.Fatalf("frame %d: expected channel %d, got %d", i+1, ChannelData, f.Channel)
		}

		if !expected[string(f.Payload)] {
			t.Fatalf("frame %d: corrupt payload %q", i+1, f.Payload)
		}
	}

	for err := range writeErrs {
		t.Errorf("write error: %v", err)
	}
}

func TestWriteFrameAtomicitySingleWrite(t *testing.T) {
	var w serializedWriter

	fw := NewFrameWriter(&w)

	const (
		goroutines         = 8
		framesPerGoroutine = 200
	)

	writeErrs := make(chan error, goroutines*framesPerGoroutine)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := range goroutines {
		go func() {
			defer wg.Done()

			padding := make([]byte, g*10)

			payload := fmt.Appendf(padding, "g%d-payload", g)
			for range framesPerGoroutine {
				if err := fw.WriteFrame(ChannelData, payload); err != nil {
					writeErrs <- fmt.Errorf("goroutine %d: %w", g, err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(writeErrs)

	for err := range writeErrs {
		t.Errorf("write error: %v", err)
	}

	r := NewFrameReader(&w.buf)
	total := goroutines * framesPerGoroutine

	expected := make(map[string]bool, goroutines)
	for g := range goroutines {
		padding := make([]byte, g*10)
		expected[string(fmt.Appendf(padding, "g%d-payload", g))] = true
	}

	for i := range total {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d/%d: read error: %v", i+1, total, err)
		}

		if f.Channel != ChannelData {
			t.Fatalf("frame %d: channel=%d, want %d", i+1, f.Channel, ChannelData)
		}

		if !expected[string(f.Payload)] {
			t.Fatalf("frame %d: corrupt payload %q", i+1, f.Payload)
		}
	}

	if _, err := r.ReadFrame(); err != io.EOF {
		t.Fatalf("expected EOF after all frames, got: %v", err)
	}
}
