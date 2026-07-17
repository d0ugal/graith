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

// connectionSleep is the lifecycle-wait sleep, a seam so start-policy tests can
// advance a fake clock instead of sleeping real time.
var connectionSleep = time.Sleep

// probeDaemonVersionFn is a seam so waitForLocalDaemonVersion can be tested
// without a live daemon socket.
var probeDaemonVersionFn = probeDaemonVersion

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

// localDaemonStartTimeout is the effective aggregate budget for a post-exec or
// post-stop lifecycle wait, from [connection] start_timeout.
func localDaemonStartTimeout() time.Duration {
	if cfg == nil {
		return config.ConnectionStartTimeoutDefault
	}

	return cfg.Connection.StartTimeoutDuration()
}

// localDaemonStartPollInterval is how often a lifecycle wait re-probes, from
// [connection] start_poll_interval.
func localDaemonStartPollInterval() time.Duration {
	if cfg == nil {
		return config.ConnectionStartPollIntervalDefault
	}

	return cfg.Connection.StartPollIntervalDuration()
}

// pollLocalDaemon polls ready at the effective start-poll interval until it
// returns true or the effective start-timeout budget elapses, checking once
// before the first sleep. It shares the [connection] start policy with the
// client's EnsureDaemon so the CLI's post-exec readiness and socket-disappearance
// lifecycle waits honour a configured startup allowance rather than a fixed
// retry count (issue #1319). Any dial or handshake performed inside ready keeps
// its own distinct deadline, separate from this aggregate budget.
func pollLocalDaemon(ready func() bool) bool {
	deadline := connectionNow().Add(localDaemonStartTimeout())
	interval := localDaemonStartPollInterval()

	for {
		if ready() {
			return true
		}

		remaining := deadline.Sub(connectionNow())
		if remaining <= 0 {
			return false
		}

		// Cap the sleep to the remaining budget so a poll interval larger than the
		// start timeout can't overshoot the aggregate deadline (#1319 review).
		sleep := interval
		if sleep > remaining {
			sleep = remaining
		}

		connectionSleep(sleep)
	}
}

// waitForLocalDaemonVersion polls the version probe until the daemon reports the
// wanted version or the start budget elapses. It returns the last value observed
// and whether the wanted version was reached. Because an exec upgrade preserves
// the inherited listening socket, a bare dial cannot distinguish the old daemon
// from the new one — the version probe is the real readiness signal. A ready=false
// result means the replacement never reported the wanted version within the
// budget: either it exec'd into a different (last != "") version, or it never
// became probeable at all (last == ""). Both are upgrade failures, not success.
func waitForLocalDaemonVersion(want string) (last string, ready bool) {
	ready = pollLocalDaemon(func() bool {
		last = probeDaemonVersionFn()

		return last == want
	})

	return last, ready
}
