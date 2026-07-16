package client

import "time"

// Connection deadlines and retry cadence for talking to a daemon (issue #1242).
// These are package vars rather than consts so ConfigureConnection can install
// config-derived values at CLI startup, and so tests can shorten a timeout
// without waiting real seconds. The initial values reproduce the behaviour that
// was hard-coded before the [connection] config block existed.
var (
	// Local daemon (Unix socket): used by EnsureDaemon, daemonResponds,
	// probeDaemonVersion, and the connect handshake in client.go.
	daemonDialTimeout       = 500 * time.Millisecond
	daemonHandshakeTimeout  = 5 * time.Second
	daemonStartTimeout      = 5 * time.Second
	daemonStartPollInterval = 50 * time.Millisecond

	// Remote daemon (TLS over TCP): used by ConnectRemote and PairRemote.
	remoteDialTimeout      = 10 * time.Second
	remoteHandshakeTimeout = 15 * time.Second
	remotePairingTimeout   = 11 * time.Minute
)

// ConnectionTimeouts carries the client-side connection deadlines resolved from
// the [connection] config block. It is defined here (not in internal/config) so
// config need not import client; the CLI maps config accessors into this struct
// and hands it to ConfigureConnection.
type ConnectionTimeouts struct {
	Dial            time.Duration
	Handshake       time.Duration
	Start           time.Duration
	StartPoll       time.Duration
	RemoteDial      time.Duration
	RemoteHandshake time.Duration
	RemotePairing   time.Duration
}

// ConfigureConnection installs the configured connection deadlines into the
// package vars the dial/handshake paths read. It is called once from the CLI's
// pre-run after config is loaded. A non-positive field is ignored so a partially
// populated struct can't silently disable a timeout; callers pass values already
// defaulted by the config accessors. Attach reconnect timing lives in the cli
// package and is configured there.
func ConfigureConnection(t ConnectionTimeouts) {
	if t.Dial > 0 {
		daemonDialTimeout = t.Dial
	}

	if t.Handshake > 0 {
		daemonHandshakeTimeout = t.Handshake
	}

	if t.Start > 0 {
		daemonStartTimeout = t.Start
	}

	if t.StartPoll > 0 {
		daemonStartPollInterval = t.StartPoll
	}

	if t.RemoteDial > 0 {
		remoteDialTimeout = t.RemoteDial
	}

	if t.RemoteHandshake > 0 {
		remoteHandshakeTimeout = t.RemoteHandshake
	}

	if t.RemotePairing > 0 {
		remotePairingTimeout = t.RemotePairing
	}
}
