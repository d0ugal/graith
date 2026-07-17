package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// runPrefixSequenceCapture drives one prefix+key sequence through the passthrough
// loop and returns both the result and every byte the client forwarded to the
// agent PTY (the payloads of ChannelData frames). It backs the remote-mapping
// regression: a configured prefix action must produce a result and NOT inject the
// raw prefix/key bytes into the agent (issue #1233).
func runPrefixSequenceCapture(t *testing.T, opts PassthroughOpts, keys []byte) (PassthroughResult, []byte) {
	t.Helper()

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	c := newTestClient(clientConn)

	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)

	go func() {
		r := protocol.NewFrameReader(daemonConn)

		for {
			frame, err := r.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelData {
				mu.Lock()
				buf.Write(frame.Payload)
				mu.Unlock()
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdout := &lockedWriter{}

	go func() {
		time.Sleep(30 * time.Millisecond)

		_, _ = stdinW.Write([]byte{opts.Keys.Prefix})

		time.Sleep(10 * time.Millisecond)

		_, _ = stdinW.Write(keys)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.runPassthroughLoop(ctx, opts, stdinR, stdout, nil)

	mu.Lock()
	defer mu.Unlock()

	return result, append([]byte(nil), buf.Bytes()...)
}

// remoteMappedKeys mirrors the bindings a remote attach installs (every prefix
// action mapped so none falls through to the agent PTY). It intentionally uses
// distinct bytes so each action can be exercised independently.
func remoteMappedKeys() PassthroughKeys {
	return PassthroughKeys{
		Prefix:         0x02,
		Detach:         boundKey('d'),
		SessionList:    boundKey('w'),
		Shell:          boundKey('s'),
		NextSession:    boundKey('n'),
		PrevSession:    boundKey('p'),
		LastSession:    boundKey('l'),
		NewSession:     boundKey('c'),
		ForkSession:    boundKey('f'),
		RenameSession:  boundKey(','),
		ScrollMode:     boundKey('['),
		Messages:       boundKey('m'),
		Approvals:      boundKey('a'),
		RestartSession: boundKey('r'),
	}
}

// TestRemotePrefixActionsInjectNoPTYBytes is the regression for the remote
// passthrough mapping gap (issue #1233): messages, approvals, and restart_session
// (and every other mapped prefix action) must resolve to a result and forward
// zero bytes to the agent PTY. Before the fix these three were omitted from the
// remote mapping, so prefix+key fell through the switch to the default arm and
// injected the raw {prefix, key} bytes into the agent.
func TestRemotePrefixActionsInjectNoPTYBytes(t *testing.T) {
	keys := remoteMappedKeys()

	cases := []struct {
		name string
		key  byte
		want PassthroughResult
	}{
		{"messages", 'm', ResultMessageOverlay},
		{"approvals", 'a', ResultApprovalOverlay},
		{"restart_session", 'r', ResultRestart},
		{"detach", 'd', ResultDetached},
		{"session_list", 'w', ResultOverlay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, sent := runPrefixSequenceCapture(t, PassthroughOpts{Keys: keys}, []byte{tc.key})
			if result != tc.want {
				t.Errorf("prefix+%q result = %d, want %d", tc.key, result, tc.want)
			}

			if len(sent) != 0 {
				t.Errorf("prefix+%q forwarded %q to the agent PTY, want no bytes", tc.key, sent)
			}
		})
	}
}

// TestUnmappedPrefixActionInjectsPTYBytes is the negative control proving the
// capture harness actually detects PTY injection: an UNmapped action key falls
// through to the default arm and forwards {prefix, key} to the agent. This is the
// exact behaviour the remote fix prevents for messages/approvals/restart_session.
func TestUnmappedPrefixActionInjectsPTYBytes(t *testing.T) {
	// Only detach is mapped; 'm' (messages) is deliberately left unbound.
	keys := PassthroughKeys{Prefix: 0x02, Detach: boundKey('d')}

	result, sent := runPrefixSequenceCapture(t, PassthroughOpts{Keys: keys}, []byte{'m'})
	if result != ResultQuit {
		t.Errorf("unmapped prefix+m result = %d, want ResultQuit (loop ran to timeout)", result)
	}

	if !bytes.Equal(sent, []byte{0x02, 'm'}) {
		t.Errorf("unmapped prefix+m forwarded %q, want the raw {prefix, key} bytes", sent)
	}
}
