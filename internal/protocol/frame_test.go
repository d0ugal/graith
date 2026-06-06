package protocol

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	r := NewFrameReader(&buf)
	if err := w.WriteFrame(ChannelControl, []byte(`{"type":"list"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(ChannelData, []byte("hello pty")); err != nil {
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
	if f2.Channel != ChannelData || string(f2.Payload) != "hello pty" {
		t.Errorf("frame 2: channel=%d payload=%q", f2.Channel, f2.Payload)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	r := NewFrameReader(&buf)
	w.WriteFrame(ChannelControl, []byte{})
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
