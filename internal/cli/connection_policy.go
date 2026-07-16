package cli

import (
	"net"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// dialLocalDaemon is a test seam shared by the CLI-only daemon probes. Normal
// client connections have the equivalent seam in internal/client.
var dialLocalDaemon = net.DialTimeout

// connectionNow makes configured deadline installation deterministic in tests.
var connectionNow = time.Now

func localDaemonDialTimeout() time.Duration {
	if cfg == nil {
		return config.ConnectionDialTimeoutDefault
	}

	return cfg.Connection.DialTimeoutDuration()
}

func localDaemonHandshakeTimeout() time.Duration {
	if cfg == nil {
		return config.ConnectionHandshakeTimeoutDefault
	}

	return cfg.Connection.HandshakeTimeoutDuration()
}

func dialLocalDaemonSocket() (net.Conn, error) {
	return dialLocalDaemon("unix", paths.SocketPath, localDaemonDialTimeout())
}

func setLocalDaemonHandshakeDeadline(conn net.Conn) {
	_ = conn.SetDeadline(connectionNow().Add(localDaemonHandshakeTimeout()))
}
