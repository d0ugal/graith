package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

func upgradeMsg() protocol.UpgradeMsg {
	execPath, _ := os.Executable()
	return protocol.UpgradeMsg{ExecPath: execPath, ClientVersion: version.Version}
}

var (
	adoptFrom  string
	forceClean bool
)

var daemonCmd = &cobra.Command{
	Use:     "daemon",
	Aliases: []string{"d"},
	Short:   "Manage the graith daemon",
}

var daemonStartCmd = &cobra.Command{
	Use:    "start",
	Short:  "Start the daemon",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.Run(cfg, paths, cfgFile, adoptFrom)
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := daemon.StopDaemon(paths.PIDFile); err != nil {
			return err
		}

		out.Printf("Daemon stopped\n")

		return nil
	},
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the daemon (preserves sessions by default)",
	Long: `Restart the daemon, picking up the latest binary and config.

By default, live sessions are preserved via exec.
Use --force to do a clean stop/start, which kills running agent sessions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if forceClean {
			return restartClean()
		}

		return restartDaemonPreservingSessions(restartClean, startCleanDaemon)
	},
}

var daemonReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Reload config without restarting the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			conn, startErr := client.EnsureDaemon(paths, cfgFile)
			if startErr != nil {
				return fmt.Errorf("start daemon: %w", startErr)
			}

			_ = conn.Close()

			out.Printf("Daemon started with current config\n")

			return nil
		}
		defer c.Close()

		if err := c.SendControl("reload", struct{}{}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		out.Printf("Config reloaded\n")

		return nil
	},
}

// upgradeExchangeConn is the connection surface execUpgrade drives for the
// handshake + upgrade round-trip. It is an interface (satisfied by
// *client.Client) so the deadline installation and readiness handoff can be
// tested against a scripted connection without a live daemon.
type upgradeExchangeConn interface {
	SetDeadline(deadline time.Time) error
	Handshake() error
	SendControl(messageType string, payload any) error
	ReadControlResponse() (protocol.Envelope, error)
	Close()
}

// dialUpgradeClient opens the daemon connection execUpgrade drives. A package
// var so tests can substitute a scripted connection.
var dialUpgradeClient = func() (upgradeExchangeConn, error) {
	return client.New(cfg, paths, cfgFile)
}

// preserveRestartUnconfirmedError means the upgrade request reached the daemon,
// but the requested replacement generation was not observed. The old process
// can still be preparing its exec/adoption at this point, so callers must not
// infer that it is safe to send SIGTERM to the PID retained across exec.
type preserveRestartUnconfirmedError struct {
	cause           error
	priorInstanceID string
	priorPID        int
}

func (e *preserveRestartUnconfirmedError) Error() string { return e.cause.Error() }

func (e *preserveRestartUnconfirmedError) Unwrap() error { return e.cause }

var daemonProcessAlive = func(pid int) bool {
	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

func restartDaemonPreservingSessions(cleanRestart, startAfterDeadPreserve func() error) error {
	const successMsg = "Daemon restarted (sessions preserved)"

	err := execUpgrade(successMsg)
	if err == nil {
		return nil
	}

	var unconfirmed *preserveRestartUnconfirmedError
	if !errors.As(err, &unconfirmed) {
		out.Printf("Preserve failed: %s\nFalling back to clean restart...\n", err)

		return cleanRestart()
	}

	// The aggregate readiness budget expiring is not proof that the preserve
	// restart failed. Re-probe with the normal bounded dial/handshake policy
	// immediately before considering fallback. The authenticated socket identity
	// is authoritative even if the old PID disappeared or its PID file is stale.
	// Keep the generation check: a matching version with the old instance ID is
	// still the inherited old daemon, not a successful replacement.
	v, id := probeDaemonIdentityFn(time.Time{})
	if isNewDaemonGeneration(v, id, version.Version, unconfirmed.priorInstanceID) {
		out.Printf("%s\n", successMsg)

		return nil
	}

	// If the exact PID captured before the request has exited, it cannot later
	// exec into the replacement. Remove only its unchanged stale PID file; a
	// missing file is also safe, while a changed/invalid file may belong to a
	// concurrent daemon and therefore blocks fallback.
	if reconcileDeadPreservePID(unconfirmed.priorPID) {
		err := startAfterDeadPreserve()
		if err == nil {
			return nil
		}

		// A concurrent client can start or reach the valid replacement while the
		// clean start is checking the socket. Turn that otherwise contradictory
		// "daemon is still running" result into preserve success.
		v, id := probeDaemonIdentityFn(time.Time{})
		if isNewDaemonGeneration(v, id, version.Version, unconfirmed.priorInstanceID) {
			out.Printf("%s\n", successMsg)

			return nil
		}

		return err
	}

	// Once the request was accepted, the old and new generation can be the same
	// PID. There is no race-free way for a subsequent PID signal to prove which
	// generation it will hit, so leave clean restart as an explicit --force
	// choice instead of risking the sessions the preserve path was meant to keep.
	return fmt.Errorf(
		"%w; automatic clean fallback skipped because the preserve restart may still be in progress (retry later or use --force)",
		err,
	)
}

func readDaemonPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0, errors.New("invalid daemon PID file")
	}

	return pid, nil
}

func reconcileDeadPreservePID(priorPID int) bool {
	if priorPID <= 1 || daemonProcessAlive(priorPID) {
		return false
	}

	currentPID, err := readDaemonPID(paths.PIDFile)
	if err != nil {
		return os.IsNotExist(err)
	}

	if currentPID != priorPID {
		return false
	}

	return os.Remove(paths.PIDFile) == nil
}

func execUpgrade(successMsg string) error {
	c, err := dialUpgradeClient()
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}

	// client.New leaves the connection deadline-free, so the raw handshake and
	// reads below would hang forever against a stale/foreign daemon that accepts
	// but never replies. Bound the whole handshake + upgrade exchange with the
	// configured local handshake deadline — distinct from the post-exec startup
	// budget the readiness wait uses (issue #1242). A connection that can't accept
	// the deadline can't be safely bounded, so fail rather than proceed unbounded.
	if err := c.SetDeadline(connectionNow().Add(localDaemonHandshakeTimeout())); err != nil {
		c.Close()
		return fmt.Errorf("set upgrade handshake deadline: %w", err)
	}

	if err := c.Handshake(); err != nil {
		c.Close()
		return err
	}

	// Capture the daemon's pre-upgrade instance ID from its handshake_ok so
	// readiness below can prove the NEW generation is serving rather than the
	// inherited listener (issue #1319). This capture MUST succeed: if the read
	// fails, the reply isn't handshake_ok, or it doesn't decode, we cannot
	// establish the pre-upgrade generation — and a silently-empty priorInstanceID
	// would let the unchanged old daemon (any non-empty instance ID) satisfy the
	// "id != prior" readiness check and be falsely accepted as the replacement.
	// Fail the exchange instead. A successfully decoded (possibly legacy)
	// handshake_ok with a genuinely absent instance ID may keep "" for backward
	// compatibility.
	hsResp, err := c.ReadControlResponse()
	if err != nil {
		c.Close()
		return fmt.Errorf("read daemon handshake before upgrade: %w", err)
	}

	if hsResp.Type != "handshake_ok" {
		c.Close()
		return fmt.Errorf("unexpected pre-upgrade handshake response %q", hsResp.Type)
	}

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(hsResp, &hsOk); err != nil {
		c.Close()
		return fmt.Errorf("decode daemon handshake before upgrade: %w", err)
	}

	priorInstanceID := hsOk.DaemonInstanceID
	priorPID, _ := readDaemonPID(paths.PIDFile)

	// Propagate a send failure rather than waiting for the readiness of an upgrade
	// that was not confirmed. A failed write does not prove the daemon received no
	// complete frame, so keep the fallback guarded just as for a readiness timeout.
	if err := c.SendControl("upgrade", upgradeMsg()); err != nil {
		c.Close()

		return &preserveRestartUnconfirmedError{
			cause:           fmt.Errorf("send upgrade request: %w", err),
			priorInstanceID: priorInstanceID,
			priorPID:        priorPID,
		}
	}

	resp, err := c.ReadControlResponse()
	c.Close()

	// A decodable "error" reply means the daemon refused the upgrade outright;
	// surface it. Otherwise the connection either dropped (the daemon exec'd
	// itself) or we got "upgrading" — both mean the replacement daemon is coming
	// up, so fall through to the readiness wait.
	if err == nil && resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return &preserveRestartUnconfirmedError{
			cause:           errors.New(e.Message),
			priorInstanceID: priorInstanceID,
			priorPID:        priorPID,
		}
	}

	// Wait for the NEW daemon generation to report our version AND a different
	// boot instance ID. exec preserves the inherited listening socket and a
	// same-version rebuild keeps the version string, so neither a bare dial nor a
	// version match distinguishes the old daemon from the new one — only a
	// changed instance ID proves the replacement is serving (issue #1319). Bound
	// the wait by the effective [connection] start policy (start_timeout /
	// start_poll_interval) instead of a fixed 20×250ms retry count. The probe's
	// own dial and handshake deadlines stay distinct from this budget.
	if v, ready := waitForNewLocalDaemonGeneration(version.Version, priorInstanceID); !ready {
		// Old daemons that don't understand ExecPath exec back into the old binary;
		// report that concrete mismatch. Otherwise the replacement never presented a
		// new generation within the budget (an inherited/old listener kept answering,
		// or it never became reachable) — a failure, not a silent success, so the
		// caller reconciles the live generation and pre-upgrade PID before deciding
		// whether a clean fallback is safe.
		if v != "" && v != version.Version {
			return &preserveRestartUnconfirmedError{
				cause:           fmt.Errorf("daemon exec'd into %s instead of %s", v, version.Version),
				priorInstanceID: priorInstanceID,
				priorPID:        priorPID,
			}
		}

		return &preserveRestartUnconfirmedError{
			cause:           fmt.Errorf("daemon did not present a new %s generation within the start budget", version.Version),
			priorInstanceID: priorInstanceID,
			priorPID:        priorPID,
		}
	}

	out.Printf("%s\n", successMsg)

	return nil
}

func probeDaemonIdentity() (daemonVersion, instanceID string) {
	return probeDaemonIdentityWithDeadline(time.Time{})
}

func probeDaemonIdentityUntil(aggregateDeadline time.Time) (daemonVersion, instanceID string) {
	return probeDaemonIdentityWithDeadline(aggregateDeadline)
}

func probeDaemonIdentityWithDeadline(aggregateDeadline time.Time) (daemonVersion, instanceID string) {
	var (
		conn net.Conn
		err  error
	)

	if aggregateDeadline.IsZero() {
		conn, err = dialLocalDaemonSocket()
	} else {
		conn, err = dialLocalDaemonSocketUntil(aggregateDeadline)
	}

	if err != nil {
		return "", ""
	}

	defer func() { _ = conn.Close() }()

	// A stale/foreign socket that accepts but never responds must not hang the
	// post-upgrade version check. This is the same configured local handshake
	// policy used by normal client connections and gr doctor.
	if aggregateDeadline.IsZero() {
		setLocalDaemonHandshakeDeadline(conn)
	} else if !setLocalDaemonHandshakeDeadlineUntil(conn, aggregateDeadline) {
		return "", ""
	}

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := client.BuildHandshake(paths, 0, 0, "")
	hs.ClientID = fmt.Sprintf("upgrade-check-%d", os.Getpid())
	hsData, _ := encodeLocalControl("handshake", hs, localAuthToken())
	_ = writer.WriteFrame(protocol.ChannelControl, hsData)

	frame, err := reader.ReadFrame()
	if err != nil || frame.Channel != protocol.ChannelControl {
		return "", ""
	}

	env, _ := protocol.DecodeControl(frame.Payload)

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(env, &hsOk); err != nil {
		return "", ""
	}

	return hsOk.DaemonVersion, hsOk.DaemonInstanceID
}

func restartClean() error {
	if err := daemon.StopDaemon(paths.PIDFile); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	return startCleanDaemon()
}

// startCleanDaemon completes a clean restart after the old daemon is known to
// have stopped. Keeping this separate lets preserve fallback avoid signalling a
// PID that was proven dead while still using the normal socket/start checks.
func startCleanDaemon() error {
	// Wait for the stopped daemon's socket to disappear, bounded by the effective
	// [connection] start policy rather than a fixed 20×100ms retry count (issue
	// #1319).
	pollLocalDaemon(func(time.Time) bool {
		_, err := os.Stat(paths.SocketPath)

		return os.IsNotExist(err)
	})

	if _, err := os.Stat(paths.SocketPath); err == nil {
		if conn, err := dialLocalDaemonSocket(); err == nil {
			_ = conn.Close()
			return errors.New("daemon is still running, cannot restart cleanly")
		}
	}

	_ = os.Remove(paths.SocketPath)

	conn, err := client.EnsureDaemon(paths, cfgFile)
	if err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}

	_ = conn.Close()

	out.Printf("Daemon restarted (sessions killed)\n")

	return nil
}

// registerDaemonCmd registers this command on rootCmd. Called from registerCommands.
func registerDaemonCmd() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonReloadCmd)
	daemonStartCmd.Flags().StringVar(&adoptFrom, "adopt-from", "", "")
	_ = daemonStartCmd.Flags().MarkHidden("adopt-from")

	daemonRestartCmd.Flags().BoolVar(&forceClean, "force", false, "Kill sessions and do a clean stop/start")
}
