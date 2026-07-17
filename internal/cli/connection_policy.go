package cli

import (
	"context"
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

// probeDaemonIdentityFn is a seam so waitForNewLocalDaemonGeneration can be
// tested without a live daemon socket. The deadline is the absolute aggregate
// start budget shared by every readiness probe.
var probeDaemonIdentityFn = probeDaemonIdentityUntil

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

func localDaemonOperationDeadline(aggregate time.Time, operationTimeout time.Duration) (time.Time, bool) {
	now := connectionNow()
	if !aggregate.After(now) {
		return time.Time{}, false
	}

	operation := now.Add(operationTimeout)
	if aggregate.Before(operation) {
		return aggregate, true
	}

	return operation, true
}

func dialLocalDaemonSocketUntil(aggregate time.Time) (net.Conn, error) {
	deadline, ok := localDaemonOperationDeadline(aggregate, localDaemonDialTimeout())
	if !ok {
		return nil, context.DeadlineExceeded
	}

	timeout := deadline.Sub(connectionNow())
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}

	return dialLocalDaemon("unix", paths.SocketPath, timeout)
}

func setLocalDaemonHandshakeDeadline(conn net.Conn) {
	_ = conn.SetDeadline(connectionNow().Add(localDaemonHandshakeTimeout()))
}

func setLocalDaemonHandshakeDeadlineUntil(conn net.Conn, aggregate time.Time) bool {
	deadline, ok := localDaemonOperationDeadline(aggregate, localDaemonHandshakeTimeout())
	if !ok {
		return false
	}

	return conn.SetDeadline(deadline) == nil
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
// retry count (issue #1319). The absolute aggregate deadline is passed into
// ready so each dial and handshake caps its distinct operation policy at the
// remaining startup budget.
func pollLocalDaemon(ready func(deadline time.Time) bool) bool {
	deadline := connectionNow().Add(localDaemonStartTimeout())
	interval := localDaemonStartPollInterval()

	for {
		remaining := deadline.Sub(connectionNow())
		if remaining <= 0 {
			return false
		}

		if ready(deadline) {
			return true
		}

		remaining = deadline.Sub(connectionNow())
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

// waitForNewLocalDaemonGeneration polls the daemon identity probe until the
// daemon reports the wanted version AND a boot instance ID different from
// priorInstanceID (the one captured before the upgrade was requested), or the
// start budget elapses. It returns the last version observed and whether the new
// generation was reached.
//
// An exec upgrade preserves the inherited listening socket and can keep the same
// version string (a same-version rebuild), so neither a bare dial nor a version
// match distinguishes the old daemon from the new one — only a CHANGED instance
// ID proves the replacement generation is actually serving (issue #1319). A
// ready=false result means the new generation never appeared within the budget:
// it exec'd into a different (lastVersion != want) version, kept answering with
// the pre-upgrade instance ID (an inherited/old listener), or never became
// probeable at all. All are upgrade failures, not success.
func waitForNewLocalDaemonGeneration(wantVersion, priorInstanceID string) (lastVersion string, ready bool) {
	ready = pollLocalDaemon(func(deadline time.Time) bool {
		v, id := probeDaemonIdentityFn(deadline)
		lastVersion = v

		return v == wantVersion && id != "" && id != priorInstanceID
	})

	return lastVersion, ready
}
