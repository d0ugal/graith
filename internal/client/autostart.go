package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/agent"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
	"github.com/d0ugal/graith/internal/processidentity"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/testprocess"
	"github.com/d0ugal/graith/internal/version"
)

// Timeouts for talking to the daemon socket live in timeouts.go as package vars
// so ConfigureConnection can install config-derived values (issue #1242) and
// tests can shorten them without waiting real seconds.

// startDaemonFn spawns a fresh daemon. It's a package var so tests can
// substitute a stub instead of exec'ing a real daemon process.
var startDaemonFn = startDaemon

var (
	recoveryIsDaemon     = processidentity.IsGraithDaemon
	recoveryStartTime    = grpty.ProcessStartTime
	recoveryStopIdentity = stopDaemonIdentity
	recoverySocketGone   = waitForSocketGone
)

var (
	detectDaemonServiceModeForCleanRestart = daemonservice.DetectMode
	cleanRestartSecurityBoundaryDetected   = func() bool { return agent.SecurityBoundaryDetectedEnviron(os.Environ()) }
)

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
	return EnsureDaemonConfigured(config.Default(), paths, configFile)
}

// EnsureDaemonConfigured is EnsureDaemon with the loaded config needed by the
// managed macOS launcher. The compatibility wrapper above preserves direct
// callers and tests; CLI connections use this form.
func EnsureDaemonConfigured(cfg *config.Config, paths config.Paths, configFile string) (net.Conn, error) {
	return EnsureDaemonConfiguredContext(context.Background(), cfg, paths, configFile)
}

// EnsureDaemonConfiguredContext is the context-aware managed launcher used by
// long-lived frontends. The configured start timeout remains the upper bound;
// an earlier caller deadline or cancellation shortens it.
func EnsureDaemonConfiguredContext(parent context.Context, cfg *config.Config, paths config.Paths, configFile string) (net.Conn, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}

	startupDeadline := time.Now().Add(daemonStartTimeout)
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(startupDeadline) {
		startupDeadline = parentDeadline
	}

	startupContext, cancel := context.WithDeadline(parent, startupDeadline)
	defer cancel()

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
	probeDeadline := initialDaemonProbeDeadline(startupDeadline)
	priorDaemon := daemonIdentityFromPIDFile(paths)
	expectedDaemon := priorDaemon

	if daemonRespondsUntil(sockPath, token, paths.Profile, probeDeadline) {
		if conn, err := dialLocalDaemonBefore("unix", sockPath, daemonDialTimeout, probeDeadline); err == nil {
			return conn, nil
		}
	}

	if err := startupContext.Err(); err != nil {
		return nil, err
	}

	// An interrupted preserve upgrade can leave its old process alive after
	// the inherited listener was closed. Reconcile after any failed launch;
	// the reconciliation itself proves the socket is unresponsive, the PID
	// marker names a graith daemon, and the process generation is stable. This
	// avoids relying on a child-process error that fire-and-forget launchers
	// cannot report to the client.
	recoverAndRestart := func(startErr error) (context.CancelFunc, error) {
		recovered, recoveryErr := reconcileUnresponsiveDaemonGeneration(paths, expectedDaemon)
		if !recovered {
			return nil, fmt.Errorf("start daemon: %w", startErr)
		}

		if recoveryErr != nil {
			return nil, fmt.Errorf("start daemon: %w (reconcile wedged daemon: %w)", startErr, recoveryErr)
		}

		retryDeadline := time.Now().Add(daemonStartTimeout)
		if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(retryDeadline) {
			retryDeadline = parentDeadline
		}

		retryContext, retryCancel := context.WithDeadline(parent, retryDeadline)
		if retryErr := startDaemonFn(retryContext, cfg, paths, configFile); retryErr != nil {
			retryCancel()
			return nil, fmt.Errorf("start daemon after recovery: %w", retryErr)
		}

		startupDeadline = retryDeadline

		return retryCancel, nil
	}

	if err := startDaemonFn(startupContext, cfg, paths, configFile); err != nil {
		retryCancel, recoveryErr := recoverAndRestart(err)
		if recoveryErr != nil {
			return nil, recoveryErr
		}
		defer retryCancel()
	} else if expectedDaemon == nil {
		// A cold start has no prior generation to reconcile. Capture the
		// generation we just launched so a later timeout cannot kill a daemon
		// started concurrently by another client.
		expectedDaemon = daemonIdentityFromPIDFile(paths)
	}

	// Wait for the freshly spawned daemon, bounded by the effective [connection]
	// start policy. Each probe's dial and handshake is capped at the remaining
	// aggregate budget so a socket that accepts but never handshakes can't overrun
	// start_timeout on the first probe (issue #1319).
	var readyConn net.Conn

	ready := pollDaemonReadyBefore(startupDeadline, func(deadline time.Time) bool {
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
		retryCancel, recoveryErr := recoverAndRestart(errors.New("daemon did not start in time"))
		if recoveryErr != nil {
			return nil, recoveryErr
		}
		defer retryCancel()

		readyConn = nil

		ready = pollDaemonReadyBefore(startupDeadline, func(deadline time.Time) bool {
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
	}

	return readyConn, nil
}

// reconcileUnresponsiveDaemon stops a daemon process which owns the PID marker
// but no longer serves the configured socket. The bool reports whether the
// marker identified a candidate; a false result means callers must preserve the
// original startup error. The process identity and start-time checks prevent a
// stale or recycled PID from being signalled.
func reconcileUnresponsiveDaemon(paths config.Paths) (bool, error) {
	return reconcileUnresponsiveDaemonGeneration(paths, nil)
}

func daemonIdentityFromPIDFile(paths config.Paths) *DaemonIdentity {
	data, err := os.ReadFile(paths.PIDFile)
	if err != nil {
		return nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || !recoveryIsDaemon(pid) {
		return nil
	}

	start, err := recoveryStartTime(pid)
	if err != nil {
		return nil
	}

	return &DaemonIdentity{PID: pid, StartTime: start}
}

func reconcileUnresponsiveDaemonGeneration(paths config.Paths, expected *DaemonIdentity) (bool, error) {
	data, err := os.ReadFile(paths.PIDFile)
	if err != nil {
		return false, nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || !recoveryIsDaemon(pid) {
		return false, nil
	}

	start, err := recoveryStartTime(pid)
	if err != nil {
		return true, fmt.Errorf("verify daemon PID %d start time: %w", pid, err)
	}

	if expected != nil && *expected != (DaemonIdentity{PID: pid, StartTime: start}) {
		return false, nil
	}

	if err := recoveryStopIdentity(pid, start); err != nil {
		return true, err
	}

	if !recoverySocketGone(paths.SocketPath) {
		return true, fmt.Errorf("daemon socket %s remained after PID %d exited", paths.SocketPath, pid)
	}

	return true, nil
}

func initialDaemonProbeDeadline(startupDeadline time.Time) time.Time {
	now := time.Now()

	remaining := startupDeadline.Sub(now)
	if remaining <= 0 {
		return startupDeadline
	}

	// A stale listener must not consume the entire first-command budget before
	// the managed/direct starter gets a chance to reconcile it. Reserve at least
	// half of the aggregate deadline for launch and readiness.
	probeBudget := remaining / 2
	if daemonHandshakeTimeout > 0 && daemonHandshakeTimeout < probeBudget {
		probeBudget = daemonHandshakeTimeout
	}

	return now.Add(probeBudget)
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

func startDaemon(ctx context.Context, cfg *config.Config, paths config.Paths, configFile string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	resolution, err := daemonservice.ResolveContext(ctx, self, version.Version, version.CommitSHA, os.Getuid())
	if err != nil {
		return err
	}

	if resolution.Mode == daemonservice.ModeManaged {
		lifetime := daemonStartTimeout + daemonHandshakeTimeout + 2*time.Second
		if deadline, ok := ctx.Deadline(); ok {
			lifetime = time.Until(deadline) + daemonHandshakeTimeout + 2*time.Second
		}

		if lifetime < 15*time.Second {
			lifetime = 15 * time.Second
		}

		return resolution.Manager.Launch(ctx, cfg, paths, configFile, lifetime, os.Environ())
	}

	return startDaemonWithLauncher(configFile, launchDaemon)
}

// PrepareDaemonCleanRestart reserves the managed service identity before a
// destructive stop. Fallback installations have nothing to reserve.
func PrepareDaemonCleanRestart(ctx context.Context, paths config.Paths) error {
	return prepareDaemonCleanRestartWithGuard(ctx, paths, testprocess.RefuseDaemonLifecycleMutation)
}

func prepareDaemonCleanRestartWithGuard(ctx context.Context, paths config.Paths, guard func(string) error) error {
	if err := guard("prepare clean daemon restart"); err != nil {
		return err
	}

	mode, _, err := detectDaemonServiceModeForCleanRestart()
	if err != nil {
		return err
	}

	// A managed clean restart can reserve/rotate service state and then stop all
	// sessions. Enforce the same first-human boundary as Launch before resolving
	// a manager or mutating the receipt.
	if mode == daemonservice.ModeManaged && cleanRestartSecurityBoundaryDetected() {
		return errors.New("an agent-mode caller cannot reserve a managed daemon clean restart")
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	resolution, err := daemonservice.ResolveContext(ctx, self, version.Version, version.CommitSHA, os.Getuid())
	if err != nil {
		return err
	}

	if resolution.Manager == nil {
		return nil
	}

	return resolution.Manager.ReserveForCleanRestart(ctx, paths.Profile)
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
	cmd.Env = agent.ScrubSecurityBoundaryEnvironment(os.Environ())
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	return cmd.Start()
}

func validateDaemonExecutable(path string, runningUnderGoTest bool) error {
	if testprocess.IsGoTestBinary(path, runningUnderGoTest) {
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
