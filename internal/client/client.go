package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
	"github.com/d0ugal/graith/internal/processidentity"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/testprocess"
	"github.com/d0ugal/graith/internal/version"
	"golang.org/x/term"
)

type Client struct {
	conn   net.Conn
	reader *protocol.FrameReader
	writer *protocol.FrameWriter
	wmu    sync.Mutex
	cfg    *config.Config
	paths  config.Paths
	token  string
}

// DaemonIdentity is the authenticated Unix-socket peer PID together with the
// operating-system process start time that makes the identity safe against PID
// reuse between authentication and signalling.
type DaemonIdentity struct {
	PID       int
	StartTime int64
}

// ExistingDaemonHandshakeError means a well-formed control response rejected
// lifecycle authentication. Unlike a stale/foreign socket transport failure,
// callers must not treat this as an absent daemon and continue destructively.
type ExistingDaemonHandshakeError struct {
	ResponseType string
}

func (err *ExistingDaemonHandshakeError) Error() string {
	return "handshake rejected while connecting to existing daemon: " + err.ResponseType
}

func New(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	return NewContext(context.Background(), cfg, paths, configFile)
}

// NewContext constructs a client while allowing daemon startup to be bounded
// by the caller as well as the configured connection policy.
func NewContext(ctx context.Context, cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	conn, err := EnsureDaemonConfiguredContext(ctx, cfg, paths, configFile)
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		cfg:    cfg,
		paths:  paths,
		token:  resolveClientToken(paths),
	}

	return c, nil
}

// DaemonPID returns the operating-system peer PID for this client's local Unix
// socket. Unlike the daemon PID file, this identity is bound to the process
// that owns the connection and can therefore be used to guard restart races.
func (c *Client) DaemonPID() (int, error) {
	return daemonPID(c.conn)
}

// DaemonIdentity captures the exact process currently owning the authenticated
// daemon connection. Callers must capture it before closing the socket.
func (c *Client) DaemonIdentity() (DaemonIdentity, error) {
	pid, err := c.DaemonPID()
	if err != nil {
		return DaemonIdentity{}, err
	}

	startTime, err := grpty.ProcessStartTime(pid)
	if err != nil {
		return DaemonIdentity{}, fmt.Errorf("read daemon PID %d start time: %w", pid, err)
	}

	if startTime == 0 {
		return DaemonIdentity{}, fmt.Errorf("daemon PID %d has zero process start time", pid)
	}

	return DaemonIdentity{PID: pid, StartTime: startTime}, nil
}

// Connect creates a new client, performs the handshake, and reads the
// handshake response. If the daemon is running a different version, it
// automatically triggers a restart and reconnects. On failure the
// connection is closed automatically.
func Connect(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	return connect(context.Background(), cfg, paths, configFile, true)
}

// ConnectPassive creates a new client and performs the handshake, but never
// triggers daemon auto-upgrade on version mismatch. Use this for long-lived
// helper processes that may outlive a binary upgrade and
// should not race with the user's explicit daemon restart.
func ConnectPassive(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	return ConnectPassiveContext(context.Background(), cfg, paths, configFile)
}

// ConnectPassiveContext is ConnectPassive with caller cancellation applied to
// daemon startup. It intentionally retains passive version-mismatch behavior.
func ConnectPassiveContext(ctx context.Context, cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	return connect(ctx, cfg, paths, configFile, false)
}

// ConnectExisting connects and authenticates to an already-running daemon but
// never starts or upgrades one. Lifecycle controls such as `daemon stop` use it
// so a down service is not demand-started merely in order to stop it.
func ConnectExisting(cfg *config.Config, paths config.Paths) (*Client, error) {
	conn, err := dialLocalDaemon("unix", paths.SocketPath, daemonDialTimeout)
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn: conn, reader: protocol.NewFrameReader(conn), writer: protocol.NewFrameWriter(conn),
		cfg: cfg, paths: paths, token: resolveClientToken(paths),
	}
	_ = conn.SetDeadline(time.Now().Add(daemonHandshakeTimeout))

	if err := c.Handshake(); err != nil {
		c.Close()
		return nil, err
	}

	response, err := c.ReadControlResponse()
	if err != nil {
		c.Close()
		return nil, err
	}

	if response.Type != "handshake_ok" {
		c.Close()
		return nil, &ExistingDaemonHandshakeError{ResponseType: response.Type}
	}

	_ = conn.SetDeadline(time.Time{})

	return c, nil
}

func connect(ctx context.Context, cfg *config.Config, paths config.Paths, configFile string, autoUpgrade bool) (*Client, error) {
	c, err := NewContext(ctx, cfg, paths, configFile)
	if err != nil {
		return nil, err
	}

	// Bound the handshake so a daemon that dies between EnsureDaemon's probe and
	// this connection can't hang the command forever. Cleared once the handshake
	// completes; long-lived reads afterwards run without a deadline.
	_ = c.conn.SetDeadline(time.Now().Add(daemonHandshakeTimeout))

	if err := c.Handshake(); err != nil {
		c.Close()
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		c.Close()
		return nil, err
	}

	if resp.Type == "handshake_err" {
		var hsErr protocol.HandshakeErrMsg

		_ = protocol.DecodePayload(resp, &hsErr)
		if serverProtocol, ok := OlderServerProtocolFromHandshakeError(hsErr.Reason); ok && autoUpgrade && version.Version != "dev" {
			return c.restartAcrossProtocolBoundary(ctx, paths, cfg, configFile, serverProtocol)
		}

		c.Close()

		return nil, fmt.Errorf("handshake rejected: %s", hsErr.Reason)
	}

	if resp.Type != "handshake_ok" {
		c.Close()
		return nil, fmt.Errorf("unexpected handshake response: %s", resp.Type)
	}

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(resp, &hsOk); err != nil {
		c.Close()
		return nil, fmt.Errorf("invalid handshake_ok payload: %w", err)
	}

	// Handshake done — clear the deadline so the long-lived connection can block
	// on long-lived reads (attach and subscriptions) without timing out.
	_ = c.conn.SetDeadline(time.Time{})

	if autoUpgrade && version.Version != "dev" {
		if hsOk.DaemonVersion != "" && hsOk.DaemonVersion != version.Version && version.IsNewer(version.Version, hsOk.DaemonVersion) {
			if !protocol.VersionCompatible(hsOk.Version) {
				return c.restartAcrossProtocolBoundary(ctx, paths, cfg, configFile, hsOk.Version)
			}

			fmt.Fprintf(os.Stderr, "Daemon version mismatch (daemon=%s, cli=%s), upgrading daemon...\n", hsOk.DaemonVersion, version.Version)

			// Capture the daemon's pre-upgrade instance ID so readiness can prove
			// the NEW generation is serving, not the inherited listener (#1319).
			priorInstanceID := hsOk.DaemonInstanceID

			upgradeRequested, managedUpgrade, upgradeErr := requestUpgradeForClient(ctx, c)
			if upgradeErr != nil {
				c.Close()

				return nil, fmt.Errorf("prepare daemon exec upgrade: %w", upgradeErr)
			}

			if upgradeRequested {
				c.Close()

				if waitForNewDaemonGeneration(paths.SocketPath, paths, version.Version, priorInstanceID) {
					return connect(ctx, cfg, paths, configFile, false)
				}

				if managedUpgrade {
					return nil, errors.New("managed daemon exec upgrade did not produce a new generation; the existing daemon and its sessions were left running")
				}

				fmt.Fprintf(os.Stderr, "Exec upgrade did not produce a new daemon generation, falling back to clean restart...\n")
			} else {
				c.Close()
			}

			stopped, stopErr := stopDaemonByPID(paths.PIDFile)
			if stopErr != nil {
				return nil, stopErr
			}

			if stopped {
				waitForSocketGone(paths.SocketPath)
			}

			return connect(ctx, cfg, paths, configFile, false)
		}
	}

	if !protocol.VersionCompatible(hsOk.Version) {
		c.Close()
		return nil, fmt.Errorf("protocol version mismatch: server=%s, client=%s; try: gr daemon restart", hsOk.Version, protocol.Version)
	}

	return c, nil
}

// OlderServerProtocolFromHandshakeError recognizes the exact rejection emitted
// by older graith daemons. Protocol 1 rejects a protocol-2 client before it can
// report handshake_ok, so this is the only safe point at which the new client
// can choose a clean, non-preserving transition. Only an older numeric major is
// accepted; arbitrary handshake failures never trigger process lifecycle work.
// Exported so the `gr daemon restart` path (internal/cli) can recognize the same
// boundary and fall through to a clean stop/start instead of aborting.
func OlderServerProtocolFromHandshakeError(reason string) (string, bool) {
	marker := "protocol version mismatch: client=" + protocol.Version + ", server="
	if !strings.HasPrefix(reason, marker) {
		return "", false
	}

	server := reason[len(marker):]
	if end := strings.IndexAny(server, ";, \t\r\n"); end >= 0 {
		server = server[:end]
	}

	serverMajor, serverMinor, ok := strings.Cut(server, ".")
	if !ok {
		return "", false
	}

	currentMajor, _, ok := strings.Cut(protocol.Version, ".")
	if !ok {
		return "", false
	}

	serverNumber, err := strconv.Atoi(serverMajor)
	if err != nil || serverNumber <= 0 {
		return "", false
	}

	minorNumber, err := strconv.Atoi(serverMinor)
	if err != nil || minorNumber < 0 {
		return "", false
	}

	currentNumber, err := strconv.Atoi(currentMajor)
	if err != nil || serverNumber >= currentNumber {
		return "", false
	}

	return server, true
}

// restartAcrossProtocolBoundary performs a clean security-boundary restart.
// Protocol 1 cannot provide the all-process, identity-bearing adoption manifest,
// so it must retain ownership long enough for graceful StopAll to terminate both
// PTY and headless agents. No new daemon is started unless the exact old Unix
// peer identity and its socket are both proved gone.
func (c *Client) restartAcrossProtocolBoundary(ctx context.Context, paths config.Paths, cfg *config.Config, configFile, serverProtocol string) (*Client, error) {
	oldPID, err := c.DaemonPID()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("protocol-breaking daemon restart cannot identify old socket peer: %w", err)
	}

	oldStart, err := grpty.ProcessStartTime(oldPID)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("protocol-breaking daemon restart cannot verify old PID %d identity: %w", oldPID, err)
	}

	if oldStart == 0 {
		c.Close()
		return nil, fmt.Errorf("protocol-breaking daemon restart cannot verify old PID %d identity: zero process start time", oldPID)
	}

	prepareContext, cancel := context.WithTimeout(ctx, daemonStartTimeout)
	err = prepareDaemonCleanRestartForUpgrade(prepareContext, paths)

	cancel()

	if err != nil {
		c.Close()
		return nil, fmt.Errorf("protocol-breaking daemon restart could not reserve its managed service before stopping: %w", err)
	}

	c.Close()

	fmt.Fprintf(os.Stderr, "Daemon protocol changed (daemon=%s, cli=%s); stopping old daemon and its sessions before clean restart...\n", serverProtocol, protocol.Version)

	if err := stopDaemonIdentityForUpgrade(oldPID, oldStart); err != nil {
		return nil, fmt.Errorf("protocol-breaking daemon restart failed closed: %w", err)
	}

	if !waitForSocketGoneForUpgrade(paths.SocketPath) {
		return nil, fmt.Errorf("protocol-breaking daemon restart failed closed: old socket %s remained after PID %d exited", paths.SocketPath, oldPID)
	}

	if reconnectAfterCleanUpgrade != nil {
		return reconnectAfterCleanUpgrade(cfg, paths, configFile)
	}

	return connect(ctx, cfg, paths, configFile, false)
}

var (
	requestUpgradeForClient             = requestUpgrade
	resolveUpgradeCandidateForClient    = daemonservice.ResolveUpgradeCandidateContext
	stopDaemonIdentityForUpgrade        = stopDaemonIdentity
	waitForSocketGoneForUpgrade         = waitForSocketGone
	reconnectAfterCleanUpgrade          func(*config.Config, config.Paths, string) (*Client, error)
	prepareDaemonCleanRestartForUpgrade = PrepareDaemonCleanRestart
)

// probeDaemonIdentity handshakes the daemon at sockPath and returns its reported
// version and per-process instance ID. Empty strings mean the daemon was
// unreachable, didn't reply, or (for the instance ID) predates that field. The
// dial and handshake use their own policies, each capped at the remaining
// aggregate readiness budget so a stalled socket can't overrun it (#1319).
func probeDaemonIdentity(sockPath string, paths config.Paths, aggregateDeadline time.Time) (daemonVersion, instanceID string) {
	conn, err := dialLocalDaemonBefore("unix", sockPath, daemonDialTimeout, aggregateDeadline)
	if err != nil {
		return "", ""
	}
	defer func() { _ = conn.Close() }()

	// Bound the handshake so a stale/foreign socket that accepts but never
	// replies can't hang the auto-upgrade version probe forever (issue #260),
	// capped at the remaining startup budget (issue #1319).
	handshakeDeadline, ok := localOperationDeadline(aggregateDeadline, daemonHandshakeTimeout)
	if !ok {
		return "", ""
	}

	if err := conn.SetDeadline(handshakeDeadline); err != nil {
		return "", ""
	}

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := BuildHandshake(paths, 0, 0, "")
	hs.ClientID = fmt.Sprintf("upgrade-check-%d", os.Getpid())

	token := resolveClientToken(paths)

	var hsData []byte
	if token != "" {
		hsData, _ = protocol.EncodeControlWithToken("handshake", hs, token)
	} else {
		hsData, _ = protocol.EncodeControl("handshake", hs)
	}

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

// waitForNewDaemonGeneration polls until the daemon reports the wanted version
// AND a boot instance ID different from priorInstanceID (the one observed before
// the upgrade was requested). An exec upgrade preserves the inherited listening
// socket and can keep the same version string (a same-version rebuild), so a
// bare dial — or even a version match — cannot distinguish the old daemon from
// the new one; only a changed instance ID proves the replacement generation is
// actually serving (issue #1319). Bounded by the effective start policy.
func waitForNewDaemonGeneration(sockPath string, paths config.Paths, wantVersion, priorInstanceID string) bool {
	budget := maxDuration(daemonStartTimeout, upgradeReadinessFloor)

	return pollDaemonReadyWithin(budget, func(deadline time.Time) bool {
		v, id := probeDaemonIdentity(sockPath, paths, deadline)

		return v == wantVersion && id != "" && id != priorInstanceID
	})
}

func upgradeMessageForClient(ctx context.Context) (protocol.UpgradeMsg, bool, error) {
	execPath, err := os.Executable()
	if err != nil {
		return protocol.UpgradeMsg{}, false, err
	}

	execPath, managed, err := resolveUpgradeCandidateForClient(ctx, execPath, version.Version, version.CommitSHA, os.Getuid())
	if err != nil {
		return protocol.UpgradeMsg{}, false, err
	}

	return protocol.UpgradeMsg{
		ExecPath:      execPath,
		ClientVersion: version.Version,
	}, managed, nil
}

func requestUpgrade(ctx context.Context, c *Client) (bool, bool, error) {
	return requestUpgradeWithGuard(ctx, c, testprocess.RefuseDaemonLifecycleMutation)
}

func requestUpgradeWithGuard(ctx context.Context, c *Client, guard func(string) error) (bool, bool, error) {
	if err := guard("initiate preserved daemon restart"); err != nil {
		return false, false, err
	}

	msg, managed, err := upgradeMessageForClient(ctx)
	if err != nil {
		return false, managed, err
	}

	negotiationTimeout := maxDuration(daemonHandshakeTimeout, upgradeNegotiationFloor)
	if err := c.conn.SetDeadline(time.Now().Add(negotiationTimeout)); err != nil {
		return false, managed, errors.New("set upgrade negotiation deadline")
	}

	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	if err := c.SendControl("upgrade_preflight", msg); err != nil {
		return false, managed, errors.New("send upgrade preflight")
	}

	preflight, err := c.ReadControlResponse()
	if err != nil {
		return false, managed, errors.New("read upgrade preflight response")
	}

	if preflight.Type != "upgrade_preflight_ok" {
		message := "daemon rejected upgrade preflight"

		if preflight.Type == "error" {
			var preflightErr protocol.ErrorMsg
			if protocol.DecodePayload(preflight, &preflightErr) == nil && preflightErr.Message != "" {
				message += ": " + preflightErr.Message
			}
		}

		return false, managed, errors.New(message)
	}

	if err := c.SendControl("upgrade", msg); err != nil {
		return false, managed, err
	}

	// Connection drop is expected — the daemon exec'd itself.
	response, err := c.ReadControlResponse()
	if err != nil {
		return true, managed, nil
	}

	if response.Type == "error" {
		var upgradeErr protocol.ErrorMsg

		_ = protocol.DecodePayload(response, &upgradeErr)

		if upgradeErr.Message == "" {
			upgradeErr.Message = "daemon rejected exec upgrade"
		}

		return false, managed, errors.New(upgradeErr.Message)
	}

	if response.Type != "upgrading" {
		return false, managed, fmt.Errorf("unexpected daemon upgrade response %q", response.Type)
	}

	return true, managed, nil
}

// pollDaemonReady polls ready at daemonStartPollInterval until it returns true
// or the daemonStartTimeout budget elapses, checking once before the first sleep
// so an already-ready daemon returns immediately. It shares the effective
// [connection] start policy (start_timeout / start_poll_interval) with
// EnsureDaemon so post-exec readiness and stop/socket-disappearance lifecycle
// waits honour a configured startup allowance instead of a fixed retry count
// (issue #1319). The absolute aggregate deadline is passed into ready so each
// dial and handshake can cap its distinct operation policy at the remaining
// startup budget.
func pollDaemonReady(ready func(deadline time.Time) bool) bool {
	return pollDaemonReadyBefore(time.Now().Add(daemonStartTimeout), ready)
}

func pollDaemonReadyWithin(timeout time.Duration, ready func(deadline time.Time) bool) bool {
	return pollDaemonReadyBefore(time.Now().Add(timeout), ready)
}

func pollDaemonReadyBefore(deadline time.Time, ready func(deadline time.Time) bool) bool {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}

		if ready(deadline) {
			return true
		}

		remaining = time.Until(deadline)
		if remaining <= 0 {
			return false
		}

		// Cap the sleep to the remaining budget so a poll interval larger than the
		// start timeout can't overshoot the aggregate deadline (#1319 review).
		sleep := daemonStartPollInterval
		if sleep > remaining {
			sleep = remaining
		}

		time.Sleep(sleep)
	}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}

	return b
}

func stopDaemonByPID(pidFile string) (bool, error) {
	if err := testprocess.RefuseDaemonLifecycleMutation("stop daemon from PID file"); err != nil {
		return false, err
	}

	return stopDaemonByPIDWith(pidFile, processidentity.IsGraithDaemon, syscall.Kill), nil
}

func stopDaemonByPIDWith(
	pidFile string,
	isDaemon func(int) bool,
	signal func(int, syscall.Signal) error,
) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return false
	}

	if !isDaemon(pid) {
		return false
	}

	_ = signal(pid, syscall.SIGTERM)

	// Wait for the process to actually exit, bounded by the same effective start
	// policy as the other lifecycle waits rather than a fixed 50×100ms retry
	// count (issue #1319).
	return pollDaemonReady(func(time.Time) bool {
		return signal(pid, 0) != nil
	})
}

// waitForSocketGone waits for a stopped daemon's socket to disappear, bounded by
// the effective start policy rather than a fixed 20×100ms retry count (issue
// #1319), and reports whether disappearance was proved.
func waitForSocketGone(sockPath string) bool {
	return pollDaemonReady(func(time.Time) bool {
		_, err := os.Stat(sockPath)

		return os.IsNotExist(err)
	})
}

// WaitForDaemonSocketGone proves that a stopped daemon no longer owns its
// filesystem rendezvous point before lifecycle state is committed.
func WaitForDaemonSocketGone(sockPath string) bool {
	return waitForSocketGone(sockPath)
}

// stopDaemonIdentity signals only the process that answered the authenticated
// Unix-socket handshake and only while its start time still matches. It waits
// for that exact identity to disappear before a new daemon may start.
func stopDaemonIdentity(pid int, startTime int64) error {
	return stopDaemonIdentityWith(
		pid,
		startTime,
		testprocess.RefuseDaemonLifecycleMutation,
		grpty.ProcessStartTime,
		syscall.Kill,
		pollDaemonReady,
	)
}

func stopDaemonIdentityWith(
	pid int,
	startTime int64,
	guard func(string) error,
	processStartTime func(int) (int64, error),
	signal func(int, syscall.Signal) error,
	wait func(func(time.Time) bool) bool,
) error {
	if err := guard("stop daemon identity"); err != nil {
		return err
	}

	if pid <= 1 || startTime == 0 {
		return fmt.Errorf("invalid daemon identity pid=%d start=%d", pid, startTime)
	}

	current, err := processStartTime(pid)
	if err != nil {
		if killErr := signal(pid, 0); errors.Is(killErr, syscall.ESRCH) {
			return nil
		}

		return fmt.Errorf("verify daemon PID %d before SIGTERM: %w", pid, err)
	}

	if current != startTime {
		return fmt.Errorf("daemon PID %d identity changed before SIGTERM (recorded=%d, current=%d)", pid, startTime, current)
	}

	if err := signal(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}

		return fmt.Errorf("SIGTERM daemon PID %d: %w", pid, err)
	}

	if wait(func(time.Time) bool {
		if err := signal(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}

		observed, err := processStartTime(pid)

		return err == nil && observed != startTime
	}) {
		return nil
	}

	return fmt.Errorf("daemon PID %d did not exit within %s", pid, daemonStartTimeout)
}

// StopDaemonIdentity signals a previously authenticated daemon process while
// guarding against PID reuse.
func StopDaemonIdentity(identity DaemonIdentity) error {
	return stopDaemonIdentity(identity.PID, identity.StartTime)
}

// ConnectFast is a fast-path connect for hooks. It dials the daemon socket
// directly with the configured local dial timeout ([connection].dial_timeout,
// installed via ConfigureConnection) and does NOT auto-start the daemon. The
// short handshake deadline set below stays independent of the dial timeout.
func ConnectFast(paths config.Paths) (*Client, error) {
	conn, err := dialLocalDaemon("unix", paths.SocketPath, daemonDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		paths:  paths,
		token:  resolveClientToken(paths),
	}

	if err := c.Handshake(); err != nil {
		c.Close()
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		c.Close()
		return nil, err
	}

	if resp.Type == "handshake_err" {
		var hsErr protocol.HandshakeErrMsg

		_ = protocol.DecodePayload(resp, &hsErr)

		c.Close()

		return nil, fmt.Errorf("handshake rejected: %s", hsErr.Reason)
	}

	if resp.Type != "handshake_ok" {
		c.Close()
		return nil, fmt.Errorf("unexpected handshake response: %s", resp.Type)
	}

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(resp, &hsOk); err != nil {
		c.Close()
		return nil, fmt.Errorf("invalid handshake_ok payload: %w", err)
	}

	if !protocol.VersionCompatible(hsOk.Version) {
		c.Close()
		return nil, fmt.Errorf("protocol version mismatch: server=%s, client=%s; try: gr daemon restart", hsOk.Version, protocol.Version)
	}

	_ = conn.SetDeadline(time.Time{})

	return c, nil
}

// ConnectForPolicy establishes a hook connection with a bounded end-to-end
// deadline. The deadline remains installed for the synchronous policy reply.
func ConnectForPolicy(paths config.Paths, timeout time.Duration) (*Client, error) {
	conn, err := dialLocalDaemon("unix", paths.SocketPath, daemonDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}

	_ = conn.SetDeadline(time.Now().Add(timeout))

	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		paths:  paths,
		token:  resolveClientToken(paths),
	}

	if err := c.Handshake(); err != nil {
		c.Close()
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		c.Close()
		return nil, err
	}

	if resp.Type == "handshake_err" {
		var hsErr protocol.HandshakeErrMsg

		_ = protocol.DecodePayload(resp, &hsErr)

		c.Close()

		return nil, fmt.Errorf("handshake rejected: %s", hsErr.Reason)
	}

	if resp.Type != "handshake_ok" {
		c.Close()
		return nil, fmt.Errorf("unexpected handshake response: %s", resp.Type)
	}

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(resp, &hsOk); err != nil {
		c.Close()
		return nil, fmt.Errorf("invalid handshake_ok payload: %w", err)
	}

	if !protocol.VersionCompatible(hsOk.Version) {
		c.Close()
		return nil, fmt.Errorf("protocol version mismatch: server=%s, client=%s; try: gr daemon restart", hsOk.Version, protocol.Version)
	}

	return c, nil
}

func readHumanToken(paths config.Paths) string {
	data, err := os.ReadFile(paths.HumanTokenFile)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

// resolveClientToken picks the credential the CLI presents to the daemon: an
// in-session agent's GRAITH_TOKEN takes precedence, and only an unset/empty env
// var falls back to the daemon-written human.token. This preserves cooperative
// agent identity while letting the human CLI authenticate transparently.
func resolveClientToken(paths config.Paths) string {
	if t := os.Getenv("GRAITH_TOKEN"); t != "" {
		return t
	}

	return readHumanToken(paths)
}

func (c *Client) Close() {
	_ = c.conn.Close()
}

// BuildHandshake constructs a HandshakeMsg with the given paths and terminal
// dimensions. All code that needs to send a handshake should use this so the
// Profile field is always populated.
func BuildHandshake(paths config.Paths, cols, rows uint16, cwd string) protocol.HandshakeMsg {
	return protocol.HandshakeMsg{
		Version:      protocol.Version,
		ClientID:     strconv.Itoa(os.Getpid()),
		TerminalSize: [2]uint16{cols, rows},
		Cwd:          cwd,
		Profile:      paths.Profile,
	}
}

func (c *Client) Handshake() error {
	cwd, _ := os.Getwd()

	cols, rows := fallbackCols, fallbackRows
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols, rows = uint16(w), uint16(h) //nolint:gosec // G115: terminal dimensions from term.GetSize are small non-negative ints
	}

	return c.SendControl("handshake", BuildHandshake(c.paths, cols, rows, cwd))
}

func (c *Client) SendControl(msgType string, payload any) error {
	var (
		data []byte
		err  error
	)
	if c.token != "" {
		data, err = protocol.EncodeControlWithToken(msgType, payload, c.token)
	} else {
		data, err = protocol.EncodeControl(msgType, payload)
	}

	if err != nil {
		return err
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()

	return c.writer.WriteFrame(protocol.ChannelControl, data)
}

func (c *Client) SendData(data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	return c.writer.WriteFrame(protocol.ChannelData, data)
}

func (c *Client) SendFrame(channel byte, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	return c.writer.WriteFrame(channel, data)
}

func (c *Client) ReadFrame() (protocol.Frame, error) {
	return c.reader.ReadFrame()
}

// SetReadDeadline sets the read deadline on the underlying connection. A zero
// time clears the deadline. Callers reading a bounded stream of frames (e.g.
// the check-inbox hook) use this so a slow or hung daemon can't block them
// indefinitely.
func (c *Client) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

// SetDeadline sets the read/write deadline on the underlying connection. A zero
// time clears it. The CLI exec-upgrade path uses this to bound its raw handshake
// + upgrade exchange (issue #1319).
func (c *Client) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

func (c *Client) ReadControlResponse() (protocol.Envelope, error) {
	frame, err := c.reader.ReadFrame()
	if err != nil {
		return protocol.Envelope{}, err
	}

	if frame.Channel != protocol.ChannelControl {
		return protocol.Envelope{}, fmt.Errorf("expected control frame, got channel %d", frame.Channel)
	}

	return protocol.DecodeControl(frame.Payload)
}

func WriteScreenRestore(snap *protocol.ScreenSnapshotResponseMsg) {
	if snap == nil || snap.Frame == "" {
		return
	}

	var buf strings.Builder
	buf.WriteString("\x1b[?2026h")
	buf.WriteString("\x1b[?25l")
	buf.WriteString("\x1b[H")
	buf.WriteString(snap.Frame)
	fmt.Fprintf(&buf, "\x1b[%d;%dH", snap.CursorY+1, snap.CursorX+1)

	if snap.CursorVisible {
		buf.WriteString("\x1b[?25h")
	}

	buf.WriteString("\x1b[?2026l")
	_, _ = os.Stdout.WriteString(buf.String())
}

func FetchScreenSnapshot(cfg *config.Config, paths config.Paths, configFile string, sessionID string) *protocol.ScreenSnapshotResponseMsg {
	c, err := Connect(cfg, paths, configFile)
	if err != nil {
		return nil
	}
	defer c.Close()

	if err := c.SendControl("screen_snapshot", protocol.ScreenSnapshotMsg{SessionID: sessionID}); err != nil {
		return nil
	}

	resp, err := c.ReadControlResponse()
	if err != nil || resp.Type != "screen_snapshot_response" {
		return nil
	}

	var snap protocol.ScreenSnapshotResponseMsg
	if err := protocol.DecodePayload(resp, &snap); err != nil {
		return nil
	}

	return &snap
}

func FetchScrollbackPreview(cfg *config.Config, paths config.Paths, configFile string, sessionID string) string {
	c, err := Connect(cfg, paths, configFile)
	if err != nil {
		return ""
	}
	defer c.Close()

	if err := c.SendControl("screen_preview", protocol.ScreenPreviewMsg{SessionID: sessionID}); err != nil {
		return ""
	}

	resp, err := c.ReadControlResponse()
	if err != nil || resp.Type != "screen_preview_response" {
		return ""
	}

	var preview protocol.ScreenPreviewResponseMsg
	if err := protocol.DecodePayload(resp, &preview); err != nil {
		return ""
	}

	return preview.Preview
}

// FetchScrollback retrieves up to `lines` lines of a session's raw scrollback
// via a one-shot connection using the same "logs" message `gr logs` uses. It
// collects the data frames until the daemon signals logs_done (or an error) and
// returns the accumulated output cleaned to plain, scroll-friendly text. It
// returns "" when the daemon is unreachable or the session has no output.
func FetchScrollback(cfg *config.Config, paths config.Paths, configFile string, sessionID string, lines int) string {
	c, err := Connect(cfg, paths, configFile)
	if err != nil {
		return ""
	}
	defer c.Close()

	if err := c.SendControl("logs", protocol.LogsMsg{SessionID: sessionID, Lines: lines, Follow: false}); err != nil {
		return ""
	}

	var buf strings.Builder

	for {
		frame, err := c.ReadFrame()
		if err != nil {
			break
		}

		switch frame.Channel {
		case protocol.ChannelData:
			buf.Write(frame.Payload)
		case protocol.ChannelControl:
			msg, _ := protocol.DecodeControl(frame.Payload)
			if msg.Type == "logs_done" || msg.Type == "error" {
				return cleanScrollback(buf.String())
			}
		}
	}

	return cleanScrollback(buf.String())
}

// FetchConversation retrieves the full direct-message conversation (both
// directions) for sessionID via a one-shot passive connection. It is safe for
// the human CLI: msg_conversation authorises with the self-or-descendant rule,
// which permits unauthenticated callers.
func FetchConversation(cfg *config.Config, paths config.Paths, configFile string, sessionID string) ([]protocol.ConversationMessage, error) {
	c, err := ConnectPassive(cfg, paths, configFile)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if err := c.SendControl("msg_conversation", protocol.MsgConversationMsg{SessionID: sessionID}); err != nil {
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return nil, fmt.Errorf("%s", e.Message)
	}

	if resp.Type != "msg_conversation_list" {
		return nil, fmt.Errorf("unexpected response %q", resp.Type)
	}

	var list protocol.MsgConversationListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}

	return list.Messages, nil
}
