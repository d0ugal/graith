package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/daemonservice"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

func upgradeMsg() protocol.UpgradeMsg {
	execPath, _ := os.Executable()
	return protocol.UpgradeMsg{ExecPath: execPath, ClientVersion: version.Version}
}

func managedUpgradeMsg() (protocol.UpgradeMsg, error) {
	msg := upgradeMsg()

	// Bundle discovery, signature validation, and the first immutable cache copy
	// happen before the socket exchange. Keep that work bounded, but do not force
	// it through a deliberately tiny dial/handshake test or user setting.
	preparationTimeout := localDaemonStartTimeout() + localDaemonHandshakeTimeout() + 2*time.Second
	if preparationTimeout < 15*time.Second {
		preparationTimeout = 15 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), preparationTimeout)
	defer cancel()

	execPath, _, err := daemonservice.ResolveUpgradeCandidateContext(ctx, msg.ExecPath, version.Version, version.CommitSHA, os.Getuid())
	if err != nil {
		return protocol.UpgradeMsg{}, err
	}

	msg.ExecPath = execPath

	return msg, nil
}

var (
	adoptFrom            string
	forceClean           bool
	internalServiceLabel string
	internalServiceSlot  string
	serviceAllProfiles   bool
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
		guard, _ := cmd.Context().Value(upgradeGuardContextKey{}).(*daemon.UpgradeFailureGuard)
		return daemon.Run(cfg, paths, cfgFile, adoptFrom, guard)
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := connectExistingForCLI(cfg, paths)
		if err != nil {
			return fmt.Errorf("daemon not running: %w", err)
		}

		identity, err := c.DaemonIdentity()
		c.Close()

		if err != nil {
			return fmt.Errorf("identify daemon peer: %w", err)
		}

		if err := stopDaemonIdentityForCLI(identity); err != nil {
			return err
		}

		if !waitForDaemonSocketGoneForCLI(paths.SocketPath) {
			return fmt.Errorf("daemon socket %s remained after PID %d exited", paths.SocketPath, identity.PID)
		}

		manager, resolveErr := currentServiceManager(false)
		if resolveErr != nil {
			return fmt.Errorf("daemon stopped but managed-service state could not be resolved: %w", resolveErr)
		}

		if manager != nil {
			if markErr := manager.MarkStopped(paths.Profile, identity.PID); markErr != nil {
				return fmt.Errorf("record dormant daemon service: %w", markErr)
			}
		}

		out.Printf("Daemon stopped\n")

		return nil
	},
}

var daemonServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "Inspect and remove the macOS per-user daemon service",
}

var daemonServiceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the per-user daemon service state",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := currentServiceManager(false)
		if err != nil {
			return err
		}

		if manager == nil {
			self, _ := os.Executable()

			resolution, resolveErr := daemonservice.Resolve(self, version.Version, version.CommitSHA, os.Getuid())
			if resolveErr != nil {
				return resolveErr
			}

			status := map[string]any{"mode": resolution.Mode, "reason": resolution.Reason, "profile": paths.Profile}
			if out.IsJSON() {
				return out.JSON(status)
			}

			out.Printf("Mode: %s\nReason: %s\n", resolution.Mode, resolution.Reason)

			return nil
		}

		reports, err := manager.Reports(cmd.Context(), paths.Profile, serviceAllProfiles)
		if err != nil {
			return err
		}

		if out.IsJSON() {
			return out.JSON(map[string]any{"mode": daemonservice.ModeManaged, "services": reports})
		}

		for _, report := range reports {
			profile := report.Profile
			if profile == "" {
				profile = "default"
			}

			out.Printf("%s: label=%s slot=%s status=%s job_running=%t pid=%d registered=%s running=%s lease=%s\n",
				profile, report.Label, report.Slot, report.Status, report.JobRunning,
				report.PID, report.RegisteredGeneration, report.RunningGeneration, report.LeaseState)

			if report.QuarantineReason != "" {
				out.Printf("  quarantine: %s\n", report.QuarantineReason)
			}
		}

		return nil
	},
}

var daemonServiceRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Stop and unregister the per-user daemon service without deleting user data",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := currentServiceManager(false)
		if err != nil {
			return err
		}

		if manager == nil {
			return errors.New("this installation does not manage a macOS daemon service")
		}

		if err := manager.Remove(cmd.Context(), paths.Profile, serviceAllProfiles, stopManagedServiceDaemon); err != nil {
			return err
		}

		out.Printf("Daemon service registration removed; config, state, worktrees, tokens, and logs were preserved.\n")

		return nil
	},
}

func stopManagedServiceDaemon(report daemonservice.ServiceReport) error {
	if report.Paths.SocketPath == "" {
		return fmt.Errorf("cannot authenticate running daemon service %s without its recorded socket path", report.Label)
	}

	c, err := connectExistingForCLI(cfg, report.Paths)
	if err != nil {
		return fmt.Errorf("authenticate running daemon service %s: %w", report.Label, err)
	}

	identity, err := c.DaemonIdentity()
	c.Close()

	if err != nil {
		return fmt.Errorf("identify running daemon service %s: %w", report.Label, err)
	}

	expectedPID := report.PID
	identitySource := "launchd"

	if expectedPID <= 1 {
		expectedPID = report.RecordedPID
		identitySource = "receipt"
	}

	if expectedPID <= 1 {
		return fmt.Errorf("daemon service %s has no usable launchd or receipt PID", report.Label)
	}

	if identity.PID != expectedPID {
		return fmt.Errorf("daemon service %s %s PID %d does not match authenticated socket peer PID %d", report.Label, identitySource, expectedPID, identity.PID)
	}

	if err := stopDaemonIdentityForCLI(identity); err != nil {
		return err
	}

	if !waitForDaemonSocketGoneForCLI(report.Paths.SocketPath) {
		return fmt.Errorf("daemon service %s socket remained after PID %d exited", report.Label, identity.PID)
	}

	return nil
}

var daemonServiceRepairCmd = &cobra.Command{
	Use:   "repair",
	Short: "Diagnose and repair safe dormant service-registration state",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := currentServiceManager(true)
		if err != nil {
			return err
		}

		if manager == nil {
			return errors.New("this installation does not carry a managed macOS daemon service")
		}

		actions, err := manager.Repair(cmd.Context())
		if err != nil {
			return err
		}

		if out.IsJSON() {
			return out.JSON(map[string]any{"actions": actions})
		}

		for _, action := range actions {
			out.Printf("%s\n", action)
		}

		return nil
	},
}

func currentServiceManager(repair bool) (*daemonservice.Manager, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}

	var resolution daemonservice.Resolution
	if repair {
		resolution, err = daemonservice.ResolveForRepair(self, version.Version, version.CommitSHA, os.Getuid())
	} else {
		resolution, err = daemonservice.Resolve(self, version.Version, version.CommitSHA, os.Getuid())
	}

	if err != nil {
		return nil, err
	}

	return resolution.Manager, nil
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

		return restartDaemonPreservingSessions(startCleanDaemon)
	},
}

var daemonReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Reload config without restarting the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			conn, startErr := client.EnsureDaemonConfigured(cfg, paths, cfgFile)
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
	DaemonPID() (int, error)
	SendControl(messageType string, payload any) error
	ReadControlResponse() (protocol.Envelope, error)
	Close()
}

// dialUpgradeClient opens the daemon connection execUpgrade drives. A package
// var so tests can substitute a scripted connection.
var dialUpgradeClient = func() (upgradeExchangeConn, error) {
	return client.New(cfg, paths, cfgFile)
}

// preserveRestartUnconfirmedError means the upgrade request may have reached
// the daemon, but the requested replacement generation was not observed. The
// old process can still be preparing its exec/adoption at this point, so callers
// must not infer that it is safe to send SIGTERM to the PID retained across exec.
type preserveRestartUnconfirmedError struct {
	cause           error
	priorInstanceID string
	priorPID        int
}

func (e *preserveRestartUnconfirmedError) Error() string { return e.cause.Error() }

func (e *preserveRestartUnconfirmedError) Unwrap() error { return e.cause }

// preserveRestartRejectedError means the daemon definitively refused the
// preserve request. The request is not in progress, but an automatic clean
// restart would turn a refusal (for example, an agent-session permission
// boundary) into a destructive action, so the caller must stop.
type preserveRestartRejectedError struct {
	cause error
}

func (e *preserveRestartRejectedError) Error() string { return e.cause.Error() }

func (e *preserveRestartRejectedError) Unwrap() error { return e.cause }

// protocolBoundaryRestartError means the running daemon speaks an older,
// incompatible wire protocol (e.g. a protocol-1 daemon still running after the
// binary upgraded to protocol 2) and rejected the preserve handshake before any
// upgrade request could be sent. Sessions are never preserved across this
// protocol security boundary by design, so the restart must fall through to a
// clean stop/start — mirroring the client connect() path's
// restartAcrossProtocolBoundary — rather than aborting with a confusing error.
type protocolBoundaryRestartError struct {
	serverProtocol string
	priorPID       int
}

func (e *protocolBoundaryRestartError) Error() string {
	return fmt.Sprintf(
		"daemon speaks incompatible protocol %s; sessions cannot be preserved across the protocol boundary",
		e.serverProtocol,
	)
}

var daemonProcessAlive = func(pid int) bool {
	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

const (
	preserveRestartSuccessMsg = "Daemon restarted (sessions preserved)"
	cleanRestartSuccessMsg    = "Daemon restarted after preserve process exited (sessions not preserved)"
)

func restartDaemonPreservingSessions(startAfterDeadPreserve func() error) error {
	err := execUpgrade(preserveRestartSuccessMsg)
	if err == nil {
		return nil
	}

	// A protocol-incompatible daemon rejects the preserve handshake before any
	// upgrade request is sent, so no session is ever at risk from a fallback here.
	// Preservation across the protocol security boundary is impossible by design,
	// so do the clean stop/start automatically instead of forcing the user to
	// re-run with --force.
	var boundary *protocolBoundaryRestartError
	if errors.As(err, &boundary) {
		fmt.Fprintf(
			os.Stderr,
			"Daemon protocol changed (daemon=%s, cli=%s); stopping old daemon and its sessions before clean restart...\n",
			boundary.serverProtocol, protocol.Version,
		)

		return restartAfterProtocolBoundary(boundary.priorPID, startAfterDeadPreserve)
	}

	var rejected *preserveRestartRejectedError
	if errors.As(err, &rejected) {
		return fmt.Errorf(
			"%w; automatic clean fallback skipped because the daemon rejected the preserve restart (use --force for an intentional clean restart)",
			err,
		)
	}

	var unconfirmed *preserveRestartUnconfirmedError
	if !errors.As(err, &unconfirmed) {
		return fmt.Errorf(
			"%w; automatic clean fallback skipped because no preserve request was safely initiated (use --force for an intentional clean restart)",
			err,
		)
	}

	// The aggregate readiness budget expiring is not proof that the preserve
	// restart failed. Re-probe with the normal bounded dial/handshake policy
	// immediately before considering fallback. The authenticated socket identity
	// is authoritative even if the old PID disappeared or its PID file is stale.
	// Keep the generation check: a matching version with the old instance ID is
	// still the inherited old daemon, not a successful replacement.
	v, id := probeDaemonIdentityFn(time.Time{})
	if isNewDaemonGeneration(v, id, version.Version, unconfirmed.priorInstanceID) {
		out.Printf("%s\n", preserveRestartSuccessMsg)

		return nil
	}

	// The PID comes from the peer credentials of the same Unix socket that
	// supplied priorInstanceID, not from the independently mutable PID file. If
	// that exact process has exited, it cannot later exec into the replacement.
	// A reused PID or a zombie conservatively blocks fallback as still alive.
	if preserveProcessExited(unconfirmed.priorPID) {
		startErr := startAfterDeadPreserve()

		// A concurrent client can start or reach the requested version while the
		// clean start is checking the socket. Validate the generation after both
		// success and failure so neither a wrong-version daemon nor a contradictory
		// "daemon is still running" error can be reported as restart success.
		return reconcileCleanStart(startErr, unconfirmed.priorInstanceID)
	}

	// Once the request was accepted, the old and new generation can be the same
	// PID. There is no race-free way for a subsequent PID signal to prove which
	// generation it will hit, so leave clean restart as an explicit --force
	// choice instead of risking the sessions the preserve path was meant to keep.
	return fmt.Errorf(
		"%w; automatic clean fallback skipped because the daemon process that received the preserve request may still be serving or restarting (use --force for an intentional clean restart)",
		err,
	)
}

func preserveProcessExited(priorPID int) bool {
	return priorPID > 1 && !daemonProcessAlive(priorPID)
}

func reconcileCleanStart(startErr error, priorInstanceID string) error {
	v, id := probeDaemonIdentityFn(time.Time{})
	if isNewDaemonGeneration(v, id, version.Version, priorInstanceID) {
		out.Printf("%s\n", cleanRestartSuccessMsg)

		return nil
	}

	if startErr != nil {
		return startErr
	}

	if v != "" && v != version.Version {
		return fmt.Errorf("clean restart presented daemon version %s instead of %s", v, version.Version)
	}

	return fmt.Errorf("clean restart did not present a new %s generation", version.Version)
}

func execUpgrade(successMsg string) error {
	// Resolve and cache the managed candidate before the socket deadline starts;
	// codesign and the first immutable bundle copy can legitimately outlive a
	// single handshake exchange.
	msg, err := managedUpgradeMsg()
	if err != nil {
		return fmt.Errorf("prepare managed upgrade candidate: %w", err)
	}

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

	// An older-protocol daemon rejects the protocol-2 handshake with handshake_err
	// before it can report handshake_ok. Recognize that exact rejection (the same
	// one the client connect() path routes into restartAcrossProtocolBoundary) and
	// signal a clean, non-preserving restart rather than a generic failure.
	if hsResp.Type == "handshake_err" {
		var hsErr protocol.HandshakeErrMsg

		_ = protocol.DecodePayload(hsResp, &hsErr)
		if serverProtocol, ok := client.OlderServerProtocolFromHandshakeError(hsErr.Reason); ok {
			priorPID, pidErr := c.DaemonPID()
			c.Close()

			if pidErr != nil {
				return fmt.Errorf("identify incompatible daemon peer: %w", pidErr)
			}

			return &protocolBoundaryRestartError{serverProtocol: serverProtocol, priorPID: priorPID}
		}

		c.Close()

		return fmt.Errorf("daemon rejected upgrade handshake: %s", hsErr.Reason)
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

	priorPID, err := c.DaemonPID()
	if err != nil {
		c.Close()

		return fmt.Errorf("identify daemon process before upgrade: %w", err)
	}

	// Propagate a send failure rather than waiting for the readiness of an upgrade
	// that was not confirmed. A failed write does not prove the daemon received no
	// complete frame, so keep the fallback guarded just as for a readiness timeout.
	if err := c.SendControl("upgrade", msg); err != nil {
		c.Close()

		return &preserveRestartUnconfirmedError{
			cause:           fmt.Errorf("send upgrade request: %w", err),
			priorInstanceID: priorInstanceID,
			priorPID:        priorPID,
		}
	}

	resp, err := c.ReadControlResponse()
	c.Close()

	// A decodable "error" reply usually means the daemon refused the upgrade
	// outright. "upgrade already in progress" is the exception: another client
	// has initiated the same transition, so its readiness remains unconfirmed.
	// Otherwise the connection either dropped (the daemon exec'd itself) or we
	// got "upgrading" — both mean the replacement daemon is coming up, so fall
	// through to the readiness wait.
	if err == nil && resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		if e.Message != "upgrade already in progress" {
			message := e.Message
			if message == "" {
				message = "daemon rejected preserve restart"
			}

			return &preserveRestartRejectedError{cause: errors.New(message)}
		}

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

// restartAfterProtocolBoundary performs the clean stop/start when the running
// daemon speaks an incompatible older protocol. StopDaemon signals the old PID
// and waits for it to exit (its graceful StopAll terminates protocol-1 sessions
// the new daemon cannot adopt); startAfterStop then acquires the freed socket via
// EnsureDaemon, which owns stale-socket and PID-file reconciliation. A stop error
// is a warning, not fatal: the old daemon may already be gone, and startAfterStop
// still fails closed if the socket is not actually free.
func restartAfterProtocolBoundary(priorPID int, startAfterStop func() error) error {
	if err := prepareCleanRestart(); err != nil {
		return fmt.Errorf("reserve managed service before protocol-boundary stop: %w", err)
	}

	if err := stopDaemonPIDForCLI(priorPID); err != nil {
		if daemonProcessAlive(priorPID) {
			return fmt.Errorf("stop incompatible daemon peer: %w", err)
		}
	}

	if err := startAfterStop(); err != nil {
		return err
	}

	out.Printf("%s\n", cleanRestartSuccessMsg)

	return nil
}

func restartClean() error {
	if err := prepareCleanRestart(); err != nil {
		return fmt.Errorf("reserve managed service before clean restart: %w", err)
	}

	if err := stopExistingDaemon(); err != nil {
		return err
	}

	if err := startCleanDaemon(); err != nil {
		return err
	}

	out.Printf("Daemon restarted (sessions killed)\n")

	return nil
}

var (
	prepareDaemonCleanRestartForCLI = client.PrepareDaemonCleanRestart
	stopDaemonPIDForCLI             = daemon.StopDaemonPID
	stopDaemonIdentityForCLI        = client.StopDaemonIdentity
	waitForDaemonSocketGoneForCLI   = client.WaitForDaemonSocketGone
	connectExistingForCLI           = func(cfg *config.Config, paths config.Paths) (existingDaemonConnection, error) {
		return client.ConnectExisting(cfg, paths)
	}
)

type existingDaemonConnection interface {
	DaemonIdentity() (client.DaemonIdentity, error)
	Close()
}

func prepareCleanRestart() error {
	ctx, cancel := context.WithTimeout(context.Background(), localDaemonStartTimeout())
	defer cancel()

	return prepareDaemonCleanRestartForCLI(ctx, paths)
}

func stopExistingDaemon() error {
	if _, err := os.Stat(paths.SocketPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect daemon socket before clean restart: %w", err)
	}

	c, err := connectExistingForCLI(cfg, paths)
	if err != nil {
		var rejected *client.ExistingDaemonHandshakeError
		if errors.As(err, &rejected) {
			return fmt.Errorf("authenticate daemon before clean restart: %w", err)
		}

		// A stale or foreign socket cannot supply an authenticated daemon
		// identity. Leave it in place for EnsureDaemon's existing fail-closed
		// ownership/reconciliation path instead of making --force unrecoverable.
		return nil
	}

	identity, err := c.DaemonIdentity()
	c.Close()

	if err != nil {
		return fmt.Errorf("identify daemon before clean restart: %w", err)
	}

	if err := stopDaemonIdentityForCLI(identity); err != nil {
		return fmt.Errorf("stop daemon peer: %w", err)
	}

	if !waitForDaemonSocketGoneForCLI(paths.SocketPath) {
		return fmt.Errorf("daemon socket %s remained after PID %d exited", paths.SocketPath, identity.PID)
	}

	return nil
}

// startCleanDaemon completes a clean restart after the old daemon is known to
// have stopped. Keeping this separate lets preserve fallback avoid signalling a
// PID that was proven dead. EnsureDaemon owns stale-socket and PID-file
// reconciliation: doing either unlink here would race a concurrent starter.
func startCleanDaemon() error {
	conn, err := client.EnsureDaemonConfigured(cfg, paths, cfgFile)
	if err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}

	_ = conn.Close()

	return nil
}

// registerDaemonCmd registers this command on rootCmd. Called from registerCommands.
func registerDaemonCmd() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonReloadCmd)
	daemonCmd.AddCommand(daemonServiceCmd)
	daemonServiceCmd.AddCommand(daemonServiceStatusCmd)
	daemonServiceCmd.AddCommand(daemonServiceRemoveCmd)
	daemonServiceCmd.AddCommand(daemonServiceRepairCmd)
	daemonServiceStatusCmd.Flags().BoolVar(&serviceAllProfiles, "all-profiles", false, "show every registered profile and quarantined slot")
	daemonServiceRemoveCmd.Flags().BoolVar(&serviceAllProfiles, "all-profiles", false, "remove every registered profile service")
	daemonStartCmd.Flags().StringVar(&adoptFrom, "adopt-from", "", "")
	_ = daemonStartCmd.Flags().MarkHidden("adopt-from")
	daemonStartCmd.Flags().StringVar(&internalServiceLabel, "internal-service-label", "", "")
	daemonStartCmd.Flags().StringVar(&internalServiceSlot, "internal-service-slot", "", "")
	_ = daemonStartCmd.Flags().MarkHidden("internal-service-label")
	_ = daemonStartCmd.Flags().MarkHidden("internal-service-slot")

	daemonRestartCmd.Flags().BoolVar(&forceClean, "force", false, "Kill sessions and do a clean stop/start")
}
