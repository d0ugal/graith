package client

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
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

func New(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	conn, err := EnsureDaemon(paths, configFile)
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

// Connect creates a new client, performs the handshake, and reads the
// handshake response. If the daemon is running a different version, it
// automatically triggers a restart and reconnects. On failure the
// connection is closed automatically.
func Connect(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	return connect(cfg, paths, configFile, true)
}

// ConnectPassive creates a new client and performs the handshake, but never
// triggers daemon auto-upgrade on version mismatch. Use this for long-lived
// helper processes (e.g. MCP proxies) that may outlive a binary upgrade and
// should not race with the user's explicit daemon restart.
func ConnectPassive(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	return connect(cfg, paths, configFile, false)
}

func connect(cfg *config.Config, paths config.Paths, configFile string, autoUpgrade bool) (*Client, error) {
	c, err := New(cfg, paths, configFile)
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
	// on reads (attach, subscribe, approval waits) without timing out.
	_ = c.conn.SetDeadline(time.Time{})

	if autoUpgrade && version.Version != "dev" {
		if hsOk.DaemonVersion != "" && hsOk.DaemonVersion != version.Version && version.IsNewer(version.Version, hsOk.DaemonVersion) {
			fmt.Fprintf(os.Stderr, "Daemon version mismatch (daemon=%s, cli=%s), upgrading daemon...\n", hsOk.DaemonVersion, version.Version)

			// Capture the daemon's pre-upgrade instance ID so readiness can prove
			// the NEW generation is serving, not the inherited listener (#1319).
			priorInstanceID := hsOk.DaemonInstanceID

			if requestUpgrade(c) {
				c.Close()

				if waitForNewDaemonGeneration(paths.SocketPath, paths, version.Version, priorInstanceID) {
					return connect(cfg, paths, configFile, false)
				}

				fmt.Fprintf(os.Stderr, "Exec upgrade did not produce a new daemon generation, falling back to clean restart...\n")
			} else {
				c.Close()
			}

			if stopDaemonByPID(paths.PIDFile) {
				waitForSocketGone(paths.SocketPath)
			}

			return connect(cfg, paths, configFile, false)
		}
	}

	if !protocol.VersionCompatible(hsOk.Version) {
		c.Close()
		return nil, fmt.Errorf("protocol version mismatch: server=%s, client=%s; try: gr daemon restart", hsOk.Version, protocol.Version)
	}

	return c, nil
}

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
	return pollDaemonReady(func(deadline time.Time) bool {
		v, id := probeDaemonIdentity(sockPath, paths, deadline)

		return v == wantVersion && id != "" && id != priorInstanceID
	})
}

func requestUpgrade(c *Client) bool {
	execPath, _ := os.Executable()

	msg := protocol.UpgradeMsg{
		ExecPath:      execPath,
		ClientVersion: version.Version,
	}
	if err := c.SendControl("upgrade", msg); err != nil {
		return false
	}
	// Connection drop is expected — the daemon exec'd itself.
	_, _ = c.ReadControlResponse()

	return true
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
	deadline := time.Now().Add(daemonStartTimeout)

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

func stopDaemonByPID(pidFile string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return false
	}

	if !daemon.IsGraithDaemon(pid) {
		return false
	}

	_ = syscall.Kill(pid, syscall.SIGTERM)

	// Wait for the process to actually exit, bounded by the same effective start
	// policy as the other lifecycle waits rather than a fixed 50×100ms retry
	// count (issue #1319).
	return pollDaemonReady(func(time.Time) bool {
		return syscall.Kill(pid, 0) != nil
	})
}

// waitForSocketGone waits for a stopped daemon's socket to disappear, bounded by
// the effective start policy rather than a fixed 20×100ms retry count (issue
// #1319). It is best-effort: it returns once the socket is gone or the budget
// elapses.
func waitForSocketGone(sockPath string) {
	_ = pollDaemonReady(func(time.Time) bool {
		_, err := os.Stat(sockPath)

		return os.IsNotExist(err)
	})
}

// ConnectFast is a fast-path connect for hooks. It dials the daemon socket
// directly with a short timeout and does NOT auto-start the daemon.
func ConnectFast(paths config.Paths) (*Client, error) {
	conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
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

// ConnectForApproval is like ConnectFast but with a long deadline suitable
// for blocking on approval responses. The socket deadline is set to
// approvalTimeout plus a one-minute grace period (minimum one minute total)
// so the connection outlives the daemon's approval timer.
func ConnectForApproval(paths config.Paths, approvalTimeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}

	_ = conn.SetDeadline(time.Now().Add(approvalDeadline(approvalTimeout)))

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

func approvalDeadline(approvalTimeout time.Duration) time.Duration {
	d := approvalTimeout + time.Minute
	if d < time.Minute {
		d = time.Minute
	}

	return d
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
