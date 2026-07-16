package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
)

// --- shared scripted controlConn fake ---------------------------------------

// sentControl records one SendControl call for assertions.
type sentControl struct {
	Type    string
	Payload any
}

// scriptedResp is one queued reply for ReadControlResponse: either an envelope
// or a transport error.
type scriptedResp struct {
	env protocol.Envelope
	err error
}

// scriptedConn is a deterministic controlConn/reconnectConn: it records every
// SendControl and replays queued responses in order. Reading past the end
// returns io.EOF so an under-scripted test fails loudly rather than blocking.
type scriptedConn struct {
	responses []scriptedResp
	readIdx   int

	// sendErr, when set, makes every SendControl fail without recording it.
	sendErr error

	sends  []sentControl
	closed int

	// passthroughs counts RunPassthrough calls; passthroughResult, when set,
	// supplies each call's result (for tests that enter run()).
	passthroughs      int
	passthroughResult func() client.PassthroughResult
}

func (s *scriptedConn) SendControl(msgType string, payload any) error {
	if s.sendErr != nil {
		return s.sendErr
	}

	s.sends = append(s.sends, sentControl{Type: msgType, Payload: payload})

	return nil
}

func (s *scriptedConn) ReadControlResponse() (protocol.Envelope, error) {
	if s.readIdx >= len(s.responses) {
		return protocol.Envelope{}, io.EOF
	}

	r := s.responses[s.readIdx]
	s.readIdx++

	return r.env, r.err
}

func (s *scriptedConn) Close() { s.closed++ }

// RunPassthrough lets scriptedConn satisfy attachConn so it can stand in as the
// attach loop's live connection. It records the call and returns a preset
// result (ResultDetached by default, which ends the loop cleanly). The handler
// tests drive handlers directly rather than through run(), so this is only
// exercised when a test explicitly enters the loop.
func (s *scriptedConn) RunPassthrough(_ context.Context, _ client.PassthroughOpts) client.PassthroughResult {
	s.passthroughs++

	if s.passthroughResult != nil {
		return s.passthroughResult()
	}

	return client.ResultDetached
}

// sentTypes returns the control types sent, in order, for assertions.
func (s *scriptedConn) sentTypes() []string {
	types := make([]string, len(s.sends))
	for i, sc := range s.sends {
		types[i] = sc.Type
	}

	return types
}

// --- envelope builders ------------------------------------------------------

func okResp(env protocol.Envelope) scriptedResp { return scriptedResp{env: env} }

func errResp(err error) scriptedResp { return scriptedResp{err: err} }

func typeEnv(msgType string) protocol.Envelope { return protocol.Envelope{Type: msgType} }

func mustMarshal(v any) []byte {
	p, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return p
}

func errEnv(msg string) protocol.Envelope {
	return protocol.Envelope{Type: "error", Payload: mustMarshal(protocol.ErrorMsg{Message: msg})}
}

func payloadEnv(msgType string, payload any) protocol.Envelope {
	return protocol.Envelope{Type: msgType, Payload: mustMarshal(payload)}
}

// --- tests ------------------------------------------------------------------

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		env  protocol.Envelope
		want string
	}{
		{"error with message", errEnv("thrawn daemon"), "thrawn daemon"},
		{"error with empty message", errEnv(""), ""},
		{"non-error type", typeEnv("ok"), ""},
		{"error type but no payload", protocol.Envelope{Type: "error"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorMessage(tt.env); got != tt.want {
				t.Errorf("errorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestControlOp(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		c := &scriptedConn{responses: []scriptedResp{okResp(typeEnv("ok"))}}

		if err := controlOp(c, "stop", protocol.StopMsg{SessionID: "braw"}); err != nil {
			t.Fatalf("controlOp: unexpected error %v", err)
		}

		if len(c.sends) != 1 || c.sends[0].Type != "stop" {
			t.Fatalf("sent = %+v, want one stop", c.sends)
		}

		if pl, ok := c.sends[0].Payload.(protocol.StopMsg); !ok || pl.SessionID != "braw" {
			t.Errorf("payload = %+v, want StopMsg{braw}", c.sends[0].Payload)
		}
	})

	t.Run("daemon error is surfaced", func(t *testing.T) {
		c := &scriptedConn{responses: []scriptedResp{okResp(errEnv("nae luck"))}}

		err := controlOp(c, "stop", protocol.StopMsg{SessionID: "dreich"})
		if err == nil || err.Error() != "nae luck" {
			t.Fatalf("err = %v, want \"nae luck\"", err)
		}
	})

	t.Run("empty-message error still fails", func(t *testing.T) {
		c := &scriptedConn{responses: []scriptedResp{okResp(errEnv(""))}}

		if err := controlOp(c, "stop", nil); err == nil {
			t.Fatal("expected a non-nil error for an empty-message error envelope")
		}
	})

	t.Run("read error propagates", func(t *testing.T) {
		sentinel := errors.New("broken pipe")
		c := &scriptedConn{responses: []scriptedResp{errResp(sentinel)}}

		if err := controlOp(c, "stop", nil); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})

	t.Run("send error is ignored, read surfaces failure", func(t *testing.T) {
		// A send failure is swallowed; the subsequent read (here EOF from an
		// empty script) is what fails the op — matching the historical behaviour.
		c := &scriptedConn{sendErr: errors.New("write failed")}

		if err := controlOp(c, "stop", nil); err == nil {
			t.Fatal("expected the read to fail after a swallowed send error")
		}

		if len(c.sends) != 0 {
			t.Errorf("a failed send should not be recorded, got %+v", c.sends)
		}
	})
}

func TestAttachDecode(t *testing.T) {
	t.Run("decodes attached info", func(t *testing.T) {
		info := protocol.SessionInfo{ID: "old"}
		want := protocol.SessionInfo{ID: "braw", Name: "bonnie"}
		c := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("attached", want))}}

		attachDecode(c, "braw", &info)

		if info.ID != "braw" || info.Name != "bonnie" {
			t.Fatalf("info = %+v, want %+v", info, want)
		}

		if len(c.sends) != 1 || c.sends[0].Type != "attach" {
			t.Fatalf("sent = %v, want one attach", c.sentTypes())
		}
	})

	t.Run("read error leaves info unchanged", func(t *testing.T) {
		info := protocol.SessionInfo{ID: "kept"}
		c := &scriptedConn{responses: []scriptedResp{errResp(io.EOF)}}

		attachDecode(c, "braw", &info)

		if info.ID != "kept" {
			t.Errorf("info.ID = %q, want unchanged \"kept\"", info.ID)
		}
	})
}

func TestFetchSessionList(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		want := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "a"}, {ID: "b"}}}
		c := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", want))}}

		got, err := fetchSessionList(c, struct{}{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(got.Sessions) != 2 || got.Sessions[0].ID != "a" {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("read error propagates", func(t *testing.T) {
		sentinel := errors.New("no daemon")
		c := &scriptedConn{responses: []scriptedResp{errResp(sentinel)}}

		if _, err := fetchSessionList(c, struct{}{}); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})

	t.Run("decode error propagates", func(t *testing.T) {
		// An envelope with no payload can't decode into SessionListMsg.
		c := &scriptedConn{responses: []scriptedResp{okResp(typeEnv("session_list"))}}

		if _, err := fetchSessionList(c, struct{}{}); err == nil {
			t.Fatal("expected a decode error for a payload-less list envelope")
		}
	})
}
