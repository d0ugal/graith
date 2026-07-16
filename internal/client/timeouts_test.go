package client

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// saveConnectionTimeouts snapshots the package-level connection vars and
// restores them on cleanup so a test can install overrides without leaking into
// sibling tests that read the same globals.
func saveConnectionTimeouts(t *testing.T) {
	t.Helper()

	orig := ConnectionTimeouts{
		Dial:            daemonDialTimeout,
		Handshake:       daemonHandshakeTimeout,
		Start:           daemonStartTimeout,
		StartPoll:       daemonStartPollInterval,
		RemoteDial:      remoteDialTimeout,
		RemoteHandshake: remoteHandshakeTimeout,
		RemotePairing:   remotePairingTimeout,
	}

	t.Cleanup(func() {
		daemonDialTimeout = orig.Dial
		daemonHandshakeTimeout = orig.Handshake
		daemonStartTimeout = orig.Start
		daemonStartPollInterval = orig.StartPoll
		remoteDialTimeout = orig.RemoteDial
		remoteHandshakeTimeout = orig.RemoteHandshake
		remotePairingTimeout = orig.RemotePairing
	})
}

func TestConfigureConnectionInstallsAllTimeouts(t *testing.T) {
	saveConnectionTimeouts(t)

	want := ConnectionTimeouts{
		Dial:            1 * time.Second,
		Handshake:       2 * time.Second,
		Start:           3 * time.Second,
		StartPoll:       4 * time.Millisecond,
		RemoteDial:      5 * time.Second,
		RemoteHandshake: 6 * time.Second,
		RemotePairing:   7 * time.Minute,
	}

	ConfigureConnection(want)

	checks := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"dial", daemonDialTimeout, want.Dial},
		{"handshake", daemonHandshakeTimeout, want.Handshake},
		{"start", daemonStartTimeout, want.Start},
		{"start_poll", daemonStartPollInterval, want.StartPoll},
		{"remote_dial", remoteDialTimeout, want.RemoteDial},
		{"remote_handshake", remoteHandshakeTimeout, want.RemoteHandshake},
		{"remote_pairing", remotePairingTimeout, want.RemotePairing},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestConfigureConnectionIgnoresNonPositive verifies a zero/negative field
// leaves the current value untouched, so a partially populated struct can't
// silently disable a deadline.
func TestConfigureConnectionIgnoresNonPositive(t *testing.T) {
	saveConnectionTimeouts(t)

	daemonDialTimeout = 123 * time.Millisecond
	remotePairingTimeout = 9 * time.Minute

	ConfigureConnection(ConnectionTimeouts{Dial: 0, RemotePairing: -1})

	if daemonDialTimeout != 123*time.Millisecond {
		t.Errorf("zero Dial changed daemonDialTimeout to %v, want unchanged", daemonDialTimeout)
	}

	if remotePairingTimeout != 9*time.Minute {
		t.Errorf("negative RemotePairing changed remotePairingTimeout to %v, want unchanged", remotePairingTimeout)
	}
}

// TestFastPathsHonourConfiguredDialTimeout proves ConnectFast and
// ConnectForApproval dial with the configured local dial timeout rather than a
// hard-coded literal. The test installs a non-default duration via
// ConfigureConnection and captures the timeout each path passes to the dialer
// through the dialLocalDaemon seam (issue #1286).
func TestFastPathsHonourConfiguredDialTimeout(t *testing.T) {
	saveConnectionTimeouts(t)

	const configured = 1234 * time.Millisecond

	ConfigureConnection(ConnectionTimeouts{Dial: configured})

	origDial := dialLocalDaemon

	t.Cleanup(func() { dialLocalDaemon = origDial })

	var captured time.Duration

	dialLocalDaemon = func(_, _ string, timeout time.Duration) (net.Conn, error) {
		captured = timeout
		// Fail the dial so the handshake path is never reached; only the dial
		// timeout is under test here.
		return nil, errors.New("dial refused (test seam)")
	}

	paths := config.Paths{SocketPath: "/nonexistent/graith-test.sock"}

	t.Run("ConnectFast", func(t *testing.T) {
		captured = 0

		if _, err := ConnectFast(paths); err == nil {
			t.Fatal("ConnectFast with a failing dialer = nil error, want error")
		}

		if captured != configured {
			t.Errorf("ConnectFast dial timeout = %v, want configured %v", captured, configured)
		}
	})

	t.Run("ConnectForApproval", func(t *testing.T) {
		captured = 0

		if _, err := ConnectForApproval(paths, 30*time.Second); err == nil {
			t.Fatal("ConnectForApproval with a failing dialer = nil error, want error")
		}

		if captured != configured {
			t.Errorf("ConnectForApproval dial timeout = %v, want configured %v", captured, configured)
		}
	})
}
