package cli

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestFormatInboxSystemMessageRecommendsAll guards against a regression where
// the check-inbox hint recommended `gr msg inbox --ack`. The hook fetches
// messages with Ack: true, so they are already read by the time the agent acts
// on the hint; an unread-only `--ack` read would return nothing. The hint must
// therefore recommend `--all`, and the system-message preview uses the terse
// "System notice:" prefix.
func TestFormatInboxSystemMessageRecommendsAll(t *testing.T) {
	msg := formatInboxSystemMessage([]inboxMessage{
		{SenderName: "Ailsa", Body: "braw news"},
		{SenderName: "graith notifications", Body: "CI is green", System: true},
	}, 0)

	if !strings.Contains(msg, "gr msg inbox --all") {
		t.Errorf("hint must recommend --all (messages are pre-acked by the hook); got:\n%s", msg)
	}

	// A bare --ack read (unread-only) would surface nothing here.
	if strings.Contains(msg, "inbox --ack") {
		t.Errorf("hint must not recommend --ack on the pre-acked hook path; got:\n%s", msg)
	}

	if !strings.Contains(msg, "System notice: CI is green") {
		t.Errorf("system message should use the terse 'System notice:' prefix; got:\n%s", msg)
	}

	if !strings.Contains(msg, "From Ailsa: braw news") {
		t.Errorf("non-system message should keep the sender prefix; got:\n%s", msg)
	}

	if !strings.Contains(msg, "2 unread message(s)") {
		t.Errorf("count should reflect number of messages; got:\n%s", msg)
	}
}

// TestFormatInboxSystemMessagePreviewTruncation guards issue #1252: the preview
// is bounded by the configurable byte cap, and a value < 1 falls back to the
// config default.
func TestFormatInboxSystemMessagePreviewTruncation(t *testing.T) {
	long := strings.Repeat("z", 5000)

	// A small custom cap truncates and appends the ellipsis.
	small := formatInboxSystemMessage([]inboxMessage{{SenderName: "Ailsa", Body: long}}, 50)
	if !strings.HasSuffix(small, "...") {
		t.Errorf("expected truncation ellipsis with a small cap; got tail %q", tail(small, 8))
	}

	// The default cap (via 0) keeps more than the small cap but still truncates a
	// 5000-byte body.
	def := formatInboxSystemMessage([]inboxMessage{{SenderName: "Ailsa", Body: long}}, 0)
	if !strings.HasSuffix(def, "...") {
		t.Errorf("expected truncation ellipsis at the default cap; got tail %q", tail(def, 8))
	}

	if len(def) <= len(small) {
		t.Errorf("default cap (%d) should retain more than the small cap (%d)", len(def), len(small))
	}
}

func TestFormatInboxSystemMessagePreviewTruncationIsUTF8Safe(t *testing.T) {
	const prefix = "From Ailsa: "

	tests := []struct {
		name string
		body string
		cut  int
	}{
		{"accented", "éclair and more", 1},
		{"emoji", "🙂 braw news", 3},
		{"combining", "e\u0301lan has news", 2},
		{"CJK", "编辑完成", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := len(prefix) + tt.cut

			got := formatInboxSystemMessage([]inboxMessage{{SenderName: "Ailsa", Body: tt.body}}, limit)
			if !utf8.ValidString(got) {
				t.Fatalf("preview is invalid UTF-8: %q", got)
			}

			_, preview, ok := strings.Cut(got, "\n\n")
			if !ok || !strings.HasSuffix(preview, "...") {
				t.Fatalf("truncated preview missing ellipsis: %q", got)
			}

			retained := strings.TrimSuffix(preview, "...")
			if len(retained) > limit {
				t.Errorf("retained preview uses %d bytes, want at most configured %d", len(retained), limit)
			}
		})
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[len(s)-n:]
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

// TestReadInboxMessagesEOF treats a clean (bare io.EOF) end of stream as normal.
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

// TestReadInboxMessagesTruncatedFrame is the regression test for treating a
// wrapped EOF as a clean end of stream. FrameReader wraps a truncated-payload
// read as "read frame payload: EOF"; that is a real error and must be surfaced,
// not mistaken for a clean close (which would re-swallow the failure #206 is
// about).
func TestReadInboxMessagesTruncatedFrame(t *testing.T) {
	fr := &fakeFrameReader{
		frames: []protocol.Frame{
			controlFrame(t, "msg_message", inboxMessage{SenderName: "haar", Body: "cut off"}),
		},
		err: fmt.Errorf("read frame payload: %w", io.EOF),
	}

	messages, err := readInboxMessages(fr)
	if err == nil {
		t.Fatal("expected a wrapped-EOF (truncated frame) to surface as an error, got nil")
	}

	if err == io.EOF {
		t.Fatalf("wrapped EOF must not be reported as a bare io.EOF, got %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("expected the 1 message read before truncation, got %d", len(messages))
	}
}

// TestReadInboxMessagesErrorFrame surfaces a daemon "error" frame and preserves
// the daemon's message text so the hook's stderr diagnostic is actionable.
func TestReadInboxMessagesErrorFrame(t *testing.T) {
	fr := &fakeFrameReader{frames: []protocol.Frame{
		controlFrame(t, "error", protocol.ErrorMsg{Message: "thrawn: not authorized"}),
	}}

	_, err := readInboxMessages(fr)
	if err == nil {
		t.Fatal("expected an error for a daemon error frame, got nil")
	}

	if !strings.Contains(err.Error(), "thrawn: not authorized") {
		t.Fatalf("expected the daemon error message to be preserved, got %v", err)
	}
}

// timeoutError is a net.Error whose Timeout reports true, matching what a
// connection read deadline produces.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
