package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

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
func EnsureDaemon(sockPath, configFile string) (net.Conn, error) {
	if daemonResponds(sockPath) {
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
		if daemonResponds(sockPath) {
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
// deadline. A socket that accepts the connection but never completes a graith
// handshake — a stale socket from a stuck process, or a non-graith server — is
// reported as not responding, so callers treat it as stale instead of blocking
// on it forever. A protocol-level rejection (handshake_err, e.g. a profile or
// version mismatch) still proves a graith daemon is present, so it counts as
// responding.
func daemonResponds(sockPath string) bool {
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

	data, err := protocol.EncodeControl("handshake", hs)
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

	return env.Type == "handshake_ok" || env.Type == "handshake_err"
}

func startDaemon(configFile string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = devNull.Close() }()

	args := daemonStartArgs(configFile)
	cmd := exec.Command(self, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	return cmd.Start()
}

func daemonStartArgs(configFile string) []string {
	args := []string{"daemon", "start"}

	_, inSession := os.LookupEnv("GRAITH_SESSION_ID")
	if configFile != "" && !inSession {
		args = append(args, "--config", configFile)
	}

	return args
}
