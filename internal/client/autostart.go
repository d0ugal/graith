package client

import (
	"context"
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

// Timeouts for talking to the daemon socket. These are package vars rather than
// consts so tests can shorten the handshake timeout without waiting seconds.
var (
	daemonDialTimeout      = 500 * time.Millisecond
	daemonHandshakeTimeout = 5 * time.Second
	daemonStartTimeout     = 5 * time.Second
)

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
	// probe so it passes the daemon's fail-closed local-auth gate, matching the
	// real handshake and the other probes (probeDaemonVersion, doctor).
	token := resolveClientToken(paths)

	if daemonResponds(sockPath, token) {
		if conn, err := net.DialTimeout("unix", sockPath, daemonDialTimeout); err == nil {
			return conn, nil
		}
	}

	if err := startDaemonFn(configFile); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), daemonStartTimeout)
	defer cancel()

	for {
		if daemonResponds(sockPath, token) {
			if conn, err := net.DialTimeout("unix", sockPath, daemonDialTimeout); err == nil {
				return conn, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("daemon did not start in time")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// daemonResponds reports whether a live graith daemon is listening on sockPath.
// It dials with a short timeout and performs a throwaway handshake under a
// deadline, presenting token so it clears the daemon's fail-closed local-auth
// gate. A socket that accepts the connection but never completes a graith
// handshake — a stale socket from a stuck process, or a non-graith server — is
// reported as not responding, so callers treat it as stale instead of blocking
// on it forever.
//
// Any decodable graith control reply proves a graith daemon is present, so
// handshake_ok, handshake_err (a protocol-level rejection, e.g. profile or
// version mismatch), and error (an auth rejection) all count as responding. A
// non-graith server fails DecodeControl and is reported as not responding.
// Accepting error is what keeps a tokenless probe from misreading a live,
// auth-gating daemon as dead and triggering a doomed autostart.
func daemonResponds(sockPath, token string) bool {
	conn, err := net.DialTimeout("unix", sockPath, daemonDialTimeout)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(daemonHandshakeTimeout))

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := protocol.HandshakeMsg{
		Version:  protocol.Version,
		ClientID: fmt.Sprintf("probe-%d", os.Getpid()),
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
