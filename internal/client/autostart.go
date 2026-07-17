package client

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// Timeouts for talking to the daemon socket live in timeouts.go as package vars
// so ConfigureConnection can install config-derived values (issue #1242) and
// tests can shorten them without waiting real seconds.

// startDaemonFn spawns a fresh daemon. It's a package var so tests can
// substitute a stub instead of exec'ing a real daemon process.
var startDaemonFn = startDaemon

// EnsureDaemon returns a connection to a live graith daemon, starting one if
// necessary.
//
// A successful Unix socket dial proves *something* is listening, but not that
// it's a live graith daemon: the socket may be stale (left behind by a stuck or
// crashed process) or owned by an unrelated server. Rather than trust the dial
// and then block forever on a handshake that never comes, EnsureDaemon probes
// the socket with a handshake under a deadline. If the probe fails, a fresh
// daemon is started — its startup removes any stale/foreign socket before
// binding (daemon.Listen), while its PID-file guard (daemon.AcquirePIDFile)
// refuses to start if a live graith daemon already owns the path. EnsureDaemon
// deliberately does not unlink the socket itself: doing so could orphan a
// live-but-slow daemon that merely lost the probe race — its socket would be
// gone but the PID guard would then block the replacement from rebinding.
func EnsureDaemon(paths config.Paths, configFile string) (net.Conn, error) {
	sockPath := paths.SocketPath
	// Present the caller's credential (session token or human token) in the
	// probe, matching the real handshake and the other probes (probeDaemonIdentity,
	// doctor). A current daemon exempts the handshake from its fail-closed auth
	// gate (PR #1066), so the token is not needed to reach one — but a pre-#1066
	// daemon still auth-gates the handshake, so presenting it keeps the probe
	// working against those versions during an upgrade. Send the caller's profile
	// too so a daemon on a non-default profile answers handshake_ok rather than a
	// spurious handshake_err (which still counts as alive, but is noisier and
	// diverges from the real handshake).
	token := resolveClientToken(paths)

	if daemonResponds(sockPath, token, paths.Profile) {
		if conn, err := dialLocalDaemon("unix", sockPath, daemonDialTimeout); err == nil {
			return conn, nil
		}
	}

	if err := startDaemonFn(configFile); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	// Wait for the freshly spawned daemon, bounded by the effective [connection]
	// start policy. Each probe's dial and handshake is capped at the remaining
	// aggregate budget so a socket that accepts but never handshakes can't overrun
	// start_timeout on the first probe (issue #1319).
	var readyConn net.Conn

	ready := pollDaemonReady(func(deadline time.Time) bool {
		if !daemonRespondsUntil(sockPath, token, paths.Profile, deadline) {
			return false
		}

		conn, err := dialLocalDaemonBefore("unix", sockPath, daemonDialTimeout, deadline)
		if err != nil {
			return false
		}

		readyConn = conn

		return true
	})
	if !ready {
		return nil, errors.New("daemon did not start in time")
	}

	return readyConn, nil
}

// daemonResponds reports whether a live graith daemon is listening on sockPath.
// It dials with a short timeout and performs a throwaway handshake under a
// deadline, presenting token and profile to match the real handshake (a current
// daemon exempts the handshake from its auth gate, but a pre-#1066 daemon does
// not, so the token keeps the probe working against those older versions). A
// socket that accepts the connection but never completes a graith handshake — a
// stale socket from a stuck process, or a non-graith server — is reported as not
// responding, so callers treat it as stale instead of blocking on it forever.
//
// The reply type is matched against an explicit allowlist, NOT "any decodable
// control frame": handshake_ok, handshake_err (a protocol-level rejection, e.g.
// profile or version mismatch), and error (an auth rejection) each prove a live
// graith daemon and count as responding. A non-graith server fails DecodeControl
// and is reported as not responding. The allowlist is deliberately narrow — it
// mirrors exactly the first-frame replies the daemon's handshake case can emit
// (see internal/daemon/handler.go), so a stray/unexpected control type is still
// treated as not-alive rather than masking a broken daemon. Accepting error is
// what keeps a tokenless probe from misreading a live, auth-gating daemon as
// dead and triggering a doomed autostart.
func daemonResponds(sockPath, token, profile string) bool {
	return daemonRespondsWithDeadline(sockPath, token, profile, time.Time{})
}

// daemonRespondsUntil is daemonResponds with each dial and handshake capped at
// the remaining aggregate startup budget (issue #1319).
func daemonRespondsUntil(sockPath, token, profile string, aggregateDeadline time.Time) bool {
	return daemonRespondsWithDeadline(sockPath, token, profile, aggregateDeadline)
}

func daemonRespondsWithDeadline(sockPath, token, profile string, aggregateDeadline time.Time) bool {
	var (
		conn net.Conn
		err  error
	)

	if aggregateDeadline.IsZero() {
		conn, err = dialLocalDaemon("unix", sockPath, daemonDialTimeout)
	} else {
		conn, err = dialLocalDaemonBefore("unix", sockPath, daemonDialTimeout, aggregateDeadline)
	}

	if err != nil {
		return false
	}

	defer func() { _ = conn.Close() }()

	handshakeDeadline := time.Now().Add(daemonHandshakeTimeout)

	if !aggregateDeadline.IsZero() {
		var ok bool

		handshakeDeadline, ok = localOperationDeadline(aggregateDeadline, daemonHandshakeTimeout)
		if !ok {
			return false
		}
	}

	if err := conn.SetDeadline(handshakeDeadline); err != nil {
		return false
	}

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := protocol.HandshakeMsg{
		Version:  protocol.Version,
		ClientID: fmt.Sprintf("probe-%d", os.Getpid()),
		Profile:  profile,
	}

	var data []byte
	if token != "" {
		data, err = protocol.EncodeControlWithToken("handshake", hs, token)
	} else {
		data, err = protocol.EncodeControl("handshake", hs)
	}

	if err != nil {
		return false
	}

	if err := writer.WriteFrame(protocol.ChannelControl, data); err != nil {
		return false
	}

	frame, err := reader.ReadFrame()
	if err != nil || frame.Channel != protocol.ChannelControl {
		return false
	}

	env, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		return false
	}

	return env.Type == "handshake_ok" || env.Type == "handshake_err" || env.Type == "error"
}

func startDaemon(configFile string) error {
	return startDaemonWithLauncher(configFile, launchDaemon)
}

func startDaemonWithLauncher(configFile string, launch func(string, []string) error) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	return startDaemonExecutable(configFile, self, testing.Testing(), launch)
}

func startDaemonExecutable(configFile, executable string, runningUnderGoTest bool, launch func(string, []string) error) error {
	if err := validateDaemonExecutable(executable, runningUnderGoTest); err != nil {
		return err
	}

	return launch(executable, daemonStartArgs(configFile))
}

func launchDaemon(executable string, args []string) error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = devNull.Close() }()

	cmd := exec.Command(executable, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	return cmd.Start()
}

func validateDaemonExecutable(path string, runningUnderGoTest bool) error {
	if runningUnderGoTest || strings.HasSuffix(filepath.Base(path), ".test") {
		return fmt.Errorf("refusing to start daemon by re-executing Go test binary %q", path)
	}

	return nil
}

func daemonStartArgs(configFile string) []string {
	args := []string{"daemon", "start"}

	_, inSession := os.LookupEnv("GRAITH_SESSION_ID")
	if configFile != "" && !inSession {
		args = append(args, "--config", configFile)
	}

	return args
}
