package cli

import (
	"errors"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// withScriptedControl installs a scriptedConn as the connection newControlConn
// hands to the one-shot session operations, restoring the real dialer on
// cleanup. It returns the fake so tests can assert on what was sent.
func withScriptedControl(t *testing.T, resp ...scriptedResp) *scriptedConn {
	t.Helper()

	c := &scriptedConn{responses: resp}
	orig := newControlConn
	newControlConn = func() (controlConn, func(), error) {
		return c, c.Close, nil
	}

	t.Cleanup(func() { newControlConn = orig })

	return c
}

func TestToggleStar(t *testing.T) {
	t.Run("star sends star", func(t *testing.T) {
		c := withScriptedControl(t, okResp(typeEnv("ok")))

		if err := toggleStar("braw", true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := c.sentTypes(); len(got) != 1 || got[0] != "star" {
			t.Errorf("sent = %v, want [star]", got)
		}

		if c.closed != 1 {
			t.Errorf("connection closed %d times, want 1", c.closed)
		}
	})

	t.Run("unstar sends unstar", func(t *testing.T) {
		c := withScriptedControl(t, okResp(typeEnv("ok")))

		if err := toggleStar("braw", false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := c.sentTypes(); len(got) != 1 || got[0] != "unstar" {
			t.Errorf("sent = %v, want [unstar]", got)
		}
	})

	t.Run("daemon error surfaced", func(t *testing.T) {
		withScriptedControl(t, okResp(errEnv("no such session")))

		if err := toggleStar("dreich", true); err == nil || err.Error() != "no such session" {
			t.Fatalf("err = %v, want \"no such session\"", err)
		}
	})

	t.Run("dial failure surfaced", func(t *testing.T) {
		orig := newControlConn
		sentinel := errors.New("daemon down")
		newControlConn = func() (controlConn, func(), error) { return nil, nil, sentinel }

		t.Cleanup(func() { newControlConn = orig })

		if err := toggleStar("braw", true); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})
}

// TestSimpleSessionOps drives the one-line session ops that all funnel through
// controlOp, checking each sends the expected control type and maps a daemon
// error to a Go error.
func TestSimpleSessionOps(t *testing.T) {
	ops := []struct {
		name    string
		call    func() error
		msgType string
	}{
		{"restart", func() error { return restartSession("braw") }, "restart"},
		{"stop", func() error { return stopSession("braw") }, "stop"},
		{"delete", func() error { return deleteSession("braw") }, "delete"},
		{"restore", func() error { return restoreSession("braw") }, "restore"},
	}

	for _, op := range ops {
		t.Run(op.name+" success", func(t *testing.T) {
			c := withScriptedControl(t, okResp(typeEnv("ok")))

			if err := op.call(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := c.sentTypes(); len(got) != 1 || got[0] != op.msgType {
				t.Errorf("sent = %v, want [%s]", got, op.msgType)
			}
		})

		t.Run(op.name+" daemon error", func(t *testing.T) {
			withScriptedControl(t, okResp(errEnv("fashed")))

			if err := op.call(); err == nil || err.Error() != "fashed" {
				t.Fatalf("err = %v, want \"fashed\"", err)
			}
		})
	}
}

func TestRenameSession(t *testing.T) {
	t.Run("valid name sends rename with payload", func(t *testing.T) {
		c := withScriptedControl(t, okResp(typeEnv("ok")))

		if err := renameSession("braw", "bonnie"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(c.sends) != 1 || c.sends[0].Type != "rename" {
			t.Fatalf("sent = %v, want [rename]", c.sentTypes())
		}

		pl, ok := c.sends[0].Payload.(protocol.RenameMsg)
		if !ok || pl.SessionID != "braw" || pl.NewName != "bonnie" {
			t.Errorf("payload = %+v, want RenameMsg{braw, bonnie}", c.sends[0].Payload)
		}
	})

	t.Run("invalid name rejected before dialing", func(t *testing.T) {
		// A name that fails validation must never open a connection.
		orig := newControlConn
		dialed := false
		newControlConn = func() (controlConn, func(), error) {
			dialed = true
			return &scriptedConn{}, func() {}, nil
		}

		t.Cleanup(func() { newControlConn = orig })

		if err := renameSession("braw", "has spaces and/slashes"); err == nil {
			t.Fatal("expected a validation error for an invalid name")
		}

		if dialed {
			t.Error("renameSession dialed the daemon despite an invalid name")
		}
	})
}

// TestSessionRefreshers verifies the overlay refreshers return the daemon's
// session list on success and nil on any failure, via the newControlConn seam.
func TestSessionRefreshers(t *testing.T) {
	list := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{{ID: "a"}, {ID: "b"}}}

	t.Run("sessionRefresher success", func(t *testing.T) {
		withScriptedControl(t, okResp(payloadEnv("session_list", list)))

		got := sessionRefresher()()
		if len(got) != 2 {
			t.Fatalf("got %d sessions, want 2", len(got))
		}
	})

	t.Run("sessionRefresher read error yields nil", func(t *testing.T) {
		withScriptedControl(t, errResp(errors.New("boom")))

		if got := sessionRefresher()(); got != nil {
			t.Errorf("got %v, want nil on error", got)
		}
	})

	t.Run("deletedRefresher sends Deleted list request", func(t *testing.T) {
		c := withScriptedControl(t, okResp(payloadEnv("session_list", list)))

		got := deletedRefresher()()
		if len(got) != 2 {
			t.Fatalf("got %d sessions, want 2", len(got))
		}

		pl, ok := c.sends[0].Payload.(protocol.ListMsg)
		if !ok || !pl.Deleted {
			t.Errorf("payload = %+v, want ListMsg{Deleted:true}", c.sends[0].Payload)
		}
	})

	t.Run("error envelope yields nil (preserves overlay state)", func(t *testing.T) {
		// Regression: the old refreshers explicitly returned nil on an "error"
		// envelope so the overlay kept its prior list (an empty slice would clear
		// it). The consolidated path must stay equivalent — a daemon error must
		// not decode into an empty, non-nil session list.
		withScriptedControl(t, okResp(errEnv("daemon fashed")))

		if got := sessionRefresher()(); got != nil {
			t.Errorf("got %v, want nil on an error envelope", got)
		}
	})

	t.Run("deletedRefresher dial failure yields nil", func(t *testing.T) {
		orig := newControlConn
		newControlConn = func() (controlConn, func(), error) { return nil, nil, errors.New("down") }

		t.Cleanup(func() { newControlConn = orig })

		if got := deletedRefresher()(); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

// TestAgentChoicesEmptyConfig guards agentChoices against an empty config
// (no agents, no default).
func TestAgentChoicesEmptyConfig(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	cfg = &config.Config{}

	names, def := agentChoices()
	if len(names) != 0 || def != "" {
		t.Errorf("agentChoices() = %v, %q, want empty", names, def)
	}
}
