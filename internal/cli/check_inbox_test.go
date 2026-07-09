package cli

import (
	"errors"
	"io"
	"net"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// TestCheckInboxCov2NoSessionIsNoOp verifies the hook command is a silent
// no-op outside a graith session: with GRAITH_SESSION_ID unset it must return
// nil immediately without attempting a daemon connection, so it never blocks or
// errors an agent hook running on a plain shell.
func TestCheckInboxCov2NoSessionIsNoOp(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	if err := checkInboxCmd.RunE(checkInboxCmd, nil); err != nil {
		t.Fatalf("check-inbox outside a session should be a no-op, got %v", err)
	}
}

// fakeFrameReader feeds a scripted sequence of frames (and optional terminal
// error) to readInboxMessages so the read loop can be tested without a daemon.
type fakeFrameReader struct {
	frames []protocol.Frame
	err    error
	idx    int
}

func (f *fakeFrameReader) ReadFrame() (protocol.Frame, error) {
	if f.idx < len(f.frames) {
		frame := f.frames[f.idx]
		f.idx++

		return frame, nil
	}

	if f.err != nil {
		return protocol.Frame{}, f.err
	}

	return protocol.Frame{}, io.EOF
}

func controlFrame(t *testing.T, msgType string, payload any) protocol.Frame {
	t.Helper()

	raw, err := protocol.EncodeControl(msgType, payload)
	if err != nil {
		t.Fatalf("EncodeControl(%q): %v", msgType, err)
	}

	return protocol.Frame{Channel: protocol.ChannelControl, Payload: raw}
}

// TestReadInboxMessagesHappyPath collects two messages and stops on msg_done.
func TestReadInboxMessagesHappyPath(t *testing.T) {
	fr := &fakeFrameReader{frames: []protocol.Frame{
		controlFrame(t, "msg_message", inboxMessage{SenderName: "braw", Body: "canny work"}),
		controlFrame(t, "msg_message", inboxMessage{SenderName: "bonnie", Body: "weel done"}),
		controlFrame(t, "msg_done", struct{}{}),
	}}

	messages, err := readInboxMessages(fr)
	if err != nil {
		t.Fatalf("readInboxMessages: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].SenderName != "braw" || messages[1].Body != "weel done" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
}

// TestReadInboxMessagesSkipsNonControlFrames ensures raw PTY frames are ignored.
func TestReadInboxMessagesSkipsNonControlFrames(t *testing.T) {
	fr := &fakeFrameReader{frames: []protocol.Frame{
		{Channel: protocol.ChannelData, Payload: []byte("haar")},
		controlFrame(t, "msg_message", inboxMessage{SenderName: "ken", Body: "speir"}),
		controlFrame(t, "msg_done", struct{}{}),
	}}

	messages, err := readInboxMessages(fr)
	if err != nil {
		t.Fatalf("readInboxMessages: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("expected 1 message after skipping non-control frame, got %d", len(messages))
	}
}

// TestReadInboxMessagesMalformedFrame is the regression test for the swallowed
// decode error: a malformed control payload must surface as an error instead
// of being silently ignored and leaving the loop waiting for a lost msg_done.
func TestReadInboxMessagesMalformedFrame(t *testing.T) {
	fr := &fakeFrameReader{frames: []protocol.Frame{
		controlFrame(t, "msg_message", inboxMessage{SenderName: "canny", Body: "guid"}),
		{Channel: protocol.ChannelControl, Payload: []byte("{ this is not json")},
		controlFrame(t, "msg_done", struct{}{}),
	}}

	messages, err := readInboxMessages(fr)
	if err == nil {
		t.Fatal("expected a decode error for a malformed frame, got nil")
	}

	// Messages collected before the malformed frame are still returned.
	if len(messages) != 1 {
		t.Fatalf("expected the 1 pre-error message to be returned, got %d", len(messages))
	}
}

// TestReadInboxMessagesTimeout is the regression test for the missing timeout:
// a read that times out (a net.Error with Timeout) must terminate the loop and
// return the error rather than blocking forever.
func TestReadInboxMessagesTimeout(t *testing.T) {
	timeoutErr := &net.OpError{Op: "read", Err: timeoutError{}}

	fr := &fakeFrameReader{
		frames: []protocol.Frame{
			controlFrame(t, "msg_message", inboxMessage{SenderName: "dreich", Body: "slow"}),
		},
		err: timeoutErr,
	}

	messages, err := readInboxMessages(fr)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected a timeout net.Error, got %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("expected the 1 message read before timeout, got %d", len(messages))
	}
}

// TestReadInboxMessagesEOF treats a clean EOF as a normal end of stream.
func TestReadInboxMessagesEOF(t *testing.T) {
	fr := &fakeFrameReader{frames: []protocol.Frame{
		controlFrame(t, "msg_message", inboxMessage{SenderName: "bide", Body: "stay"}),
	}}

	messages, err := readInboxMessages(fr)
	if err != nil {
		t.Fatalf("EOF should be a clean end of stream, got %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
}

// TestReadInboxMessagesErrorFrame surfaces a daemon "error" frame.
func TestReadInboxMessagesErrorFrame(t *testing.T) {
	fr := &fakeFrameReader{frames: []protocol.Frame{
		controlFrame(t, "error", map[string]string{"message": "thrawn"}),
	}}

	_, err := readInboxMessages(fr)
	if err == nil {
		t.Fatal("expected an error for a daemon error frame, got nil")
	}
}

// timeoutError is a net.Error whose Timeout reports true, matching what a
// connection read deadline produces.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
