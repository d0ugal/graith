package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// prefixKeyOpts mirrors the real key bindings so every prefix branch is
// reachable from a test.
var prefixKeyOpts = PassthroughOpts{
	Keys: PassthroughKeys{
		Prefix:              0x02, // ctrl+b
		NextSession:         'n',
		PrevSession:         'p',
		LastSession:         'l',
		NewSession:          'c',
		ForkSession:         'f',
		OrchestratorSession: 'o',
	},
}

// runPrefixSequence feeds the raw prefix byte followed by the given key(s) into
// a passthrough loop and returns the resulting action. A background writer
// keeps the daemon side of the pipe drained so the loop stays alive until the
// key is processed.
func runPrefixSequence(t *testing.T, keys []byte) PassthroughResult {
	t.Helper()

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	// Drain anything the client sends so SendData never blocks on net.Pipe.
	go func() {
		r := protocol.NewFrameReader(daemonConn)
		for {
			if _, err := r.ReadFrame(); err != nil {
				return
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02}) // prefix

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write(keys)
	}()

	return c.runPassthroughLoop(context.Background(), prefixKeyOpts, stdinR, stdout, nil)
}

func TestPrefixKeyActions2(t *testing.T) {
	cases := []struct {
		name string
		key  byte
		want PassthroughResult
	}{
		{"messages", 'm', ResultMessageOverlay},
		{"approvals", 'a', ResultApprovalOverlay},
		{"restart", 'r', ResultRestart},
		{"next", 'n', ResultNextSession},
		{"prev", 'p', ResultPrevSession},
		{"last", 'l', ResultLastSession},
		{"new", 'c', ResultNewSession},
		{"fork", 'f', ResultForkSession},
		{"orchestrator", 'o', ResultOrchestratorSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runPrefixSequence(t, []byte{tc.key}); got != tc.want {
				t.Fatalf("prefix+%q = %d, want %d", tc.key, got, tc.want)
			}
		})
	}
}

// TestPrefixKeyDoublePrefixSendsRawByte verifies that prefix+prefix forwards a
// single raw prefix byte to the daemon (the escape hatch to send ctrl+b to the
// agent), and does not change the passthrough action.
func TestPrefixKeyDoublePrefixSendsRawByte2(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	got := make(chan []byte, 1)

	go func() {
		r := protocol.NewFrameReader(daemonConn)
		for {
			frame, err := r.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData && bytes.Contains(frame.Payload, []byte{0x02}) {
				select {
				case got <- frame.Payload:
				default:
				}
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 0x02}) // prefix, prefix → forwards raw prefix byte

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'}) // prefix+d detaches to end the loop
	}()

	res := c.runPassthroughLoop(context.Background(), prefixKeyOpts, stdinR, stdout, nil)
	if res != ResultDetached {
		t.Fatalf("expected detach after double-prefix then d, got %d", res)
	}

	select {
	case payload := <-got:
		if !bytes.Contains(payload, []byte{0x02}) {
			t.Errorf("expected raw prefix byte forwarded, got %v", payload)
		}
	case <-time.After(time.Second):
		t.Error("prefix byte was not forwarded to the daemon")
	}
}

// TestPrefixKeyUnknownForwardsBoth verifies that an unrecognized key after the
// prefix forwards both the prefix byte and the key to the daemon (the default
// case), rather than being swallowed.
func TestPrefixKeyUnknownForwardsBoth2(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	got := make(chan []byte, 4)

	go func() {
		r := protocol.NewFrameReader(daemonConn)
		for {
			frame, err := r.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData {
				select {
				case got <- append([]byte(nil), frame.Payload...):
				default:
				}
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'Z'}) // prefix + unbound key

		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{0x02, 'd'}) // detach to end
	}()

	res := c.runPassthroughLoop(context.Background(), prefixKeyOpts, stdinR, stdout, nil)
	if res != ResultDetached {
		t.Fatalf("expected detach, got %d", res)
	}

	found := false

	timeout := time.After(time.Second)

	for !found {
		select {
		case p := <-got:
			if bytes.Contains(p, []byte{0x02, 'Z'}) {
				found = true
			}
		case <-timeout:
			t.Fatal("did not observe prefix+Z forwarded to daemon")
		}
	}
}

func TestProcessKittyPrefixNonMatching2(t *testing.T) {
	// A CSI u sequence for a different key (codepoint 122 = 'z') must be left
	// untouched.
	in := []byte("\x1b[122;5u")
	if got := processKittyPrefix(in, 0x02); !bytes.Equal(got, in) {
		t.Errorf("non-matching sequence altered: %q", got)
	}

	// A matching press sequence for ctrl+b (98) is replaced by the raw byte.
	press := []byte("\x1b[98;5u")
	if got := processKittyPrefix(press, 0x02); !bytes.Equal(got, []byte{0x02}) {
		t.Errorf("matching press not replaced: %q", got)
	}

	// A matching release sequence (event type 3) is stripped entirely.
	release := []byte("\x1b[98;5:3u")
	if got := processKittyPrefix(release, 0x02); len(got) != 0 {
		t.Errorf("release should be stripped, got %q", got)
	}

	// Surrounding data is preserved around a replaced sequence.
	mixed := []byte("ab\x1b[98;5ucd")
	if got := processKittyPrefix(mixed, 0x02); !bytes.Equal(got, []byte("ab\x02cd")) {
		t.Errorf("mixed surrounding data wrong: %q", got)
	}

	// Input with no escape byte returns the original slice.
	plain := []byte("hello")
	if got := processKittyPrefix(plain, 0x02); !bytes.Equal(got, plain) {
		t.Errorf("plain input altered: %q", got)
	}
}

func TestParseKittyCSIuEdgeCases2(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"too short", "\x1b[", false},
		{"not escape", "X[98u", false},
		{"no digits", "\x1b[;u", false},
		{"missing u terminator", "\x1b[98;5", false},
		{"valid with modifiers", "\x1b[98;5u", true},
		{"valid with event type", "\x1b[98;5:3u", true},
		{"valid no modifiers", "\x1b[98u", true},
		{"bad modifier no digits", "\x1b[98;u", false},
		{"bad event no digits", "\x1b[98;5:u", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, ok := parseKittyCSIu([]byte(tc.in), 0)
			if ok != tc.ok {
				t.Errorf("parseKittyCSIu(%q) ok=%v, want %v", tc.in, ok, tc.ok)
			}
		})
	}
}

func TestKittyCtrlSeqOutOfRange2(t *testing.T) {
	if kittyCtrlSeq(0) != nil {
		t.Error("prefix 0 should yield nil (out of ctrl-letter range)")
	}

	if kittyCtrlSeq(27) != nil {
		t.Error("prefix 27 should yield nil (out of ctrl-letter range)")
	}
}

func TestSyncWriterConcurrent2(t *testing.T) {
	var buf bytes.Buffer

	sw := &syncWriter{w: &buf}

	done := make(chan struct{})

	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				_, _ = sw.Write([]byte("x"))
			}

			done <- struct{}{}
		}()
	}

	for i := 0; i < 4; i++ {
		<-done
	}

	if buf.Len() != 200 {
		t.Fatalf("syncWriter lost writes under concurrency: got %d bytes, want 200", buf.Len())
	}
}
