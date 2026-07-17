package client

import (
	"context"
	"net"
	"time"
)

// dialLocalDaemon dials the local daemon Unix socket. It is a package var (not a
// direct net.DialTimeout call) so tests can observe the timeout the readiness
// paths pass and drive the generation-aware upgrade probes against a scripted
// connection (issue #1319).
var dialLocalDaemon = net.DialTimeout

// localOperationDeadline caps one dial/handshake policy at an aggregate
// readiness deadline. The policies remain distinct, but neither operation may
// outlive the remaining startup budget, so a socket that accepts and then stalls
// can't overrun start_timeout (issue #1319).
func localOperationDeadline(aggregate time.Time, operationTimeout time.Duration) (time.Time, bool) {
	now := time.Now()
	if !aggregate.After(now) {
		return time.Time{}, false
	}

	operation := now.Add(operationTimeout)
	if aggregate.Before(operation) {
		return aggregate, true
	}

	return operation, true
}

// dialLocalDaemonBefore dials with the smaller of operationTimeout and the time
// remaining until the aggregate deadline, so a readiness dial never outlives the
// startup budget (issue #1319).
func dialLocalDaemonBefore(network, address string, operationTimeout time.Duration, aggregate time.Time) (net.Conn, error) {
	deadline, ok := localOperationDeadline(aggregate, operationTimeout)
	if !ok {
		return nil, context.DeadlineExceeded
	}

	timeout := time.Until(deadline)
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}

	return dialLocalDaemon(network, address, timeout)
}

// Connection deadlines and retry cadence for talking to a daemon (issue #1242).
// These are package vars rather than consts so ConfigureConnection can install
// config-derived values at CLI startup, and so tests can shorten a timeout
// without waiting real seconds. The initial values reproduce the behaviour that
// was hard-coded before the [connection] config block existed.
var (
	// Local daemon (Unix socket): used by EnsureDaemon, daemonResponds,
	// probeDaemonIdentity, and the connect handshake in client.go.
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
