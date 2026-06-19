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
}

func New(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	conn, err := EnsureDaemon(paths.SocketPath, configFile)
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		cfg:    cfg,
		paths:  paths,
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

	if autoUpgrade && version.Version != "dev" {
		if hsOk.DaemonVersion != "" && hsOk.DaemonVersion != version.Version && version.IsNewer(version.Version, hsOk.DaemonVersion) {
			fmt.Fprintf(os.Stderr, "Daemon version mismatch (daemon=%s, cli=%s), upgrading daemon...\n", hsOk.DaemonVersion, version.Version)
			if requestUpgrade(c) {
				c.Close()
				if waitForDaemon(paths.SocketPath) {
					if v := probeDaemonVersion(paths.SocketPath, paths); v == version.Version {
						return connect(cfg, paths, configFile, false)
					}
					fmt.Fprintf(os.Stderr, "Exec upgrade did not produce correct version, falling back to clean restart...\n")
				}
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

func probeDaemonVersion(sockPath string, paths config.Paths) string {
	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := BuildHandshake(paths, 0, 0, "")
	hs.ClientID = fmt.Sprintf("upgrade-check-%d", os.Getpid())
	hsData, _ := protocol.EncodeControl("handshake", hs)
	_ = writer.WriteFrame(protocol.ChannelControl, hsData)

	frame, err := reader.ReadFrame()
	if err != nil || frame.Channel != protocol.ChannelControl {
		return ""
	}
	env, _ := protocol.DecodeControl(frame.Payload)
	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(env, &hsOk); err != nil {
		return ""
	}
	return hsOk.DaemonVersion
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
	c.ReadControlResponse()
	return true
}

func waitForDaemon(sockPath string) bool {
	for range 20 {
		time.Sleep(250 * time.Millisecond)
		if conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond); err == nil {
			conn.Close()
			return true
		}
	}
	return false
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
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func waitForSocketGone(sockPath string) {
	for range 20 {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ConnectFast is a fast-path connect for hooks. It dials the daemon socket
// directly with a short timeout and does NOT auto-start the daemon.
func ConnectFast(paths config.Paths) (*Client, error) {
	conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		paths:  paths,
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
	conn.SetDeadline(time.Time{})
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
	conn.SetDeadline(time.Now().Add(approvalDeadline(approvalTimeout)))
	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		paths:  paths,
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
	conn.SetDeadline(time.Time{})
	return c, nil
}

func approvalDeadline(approvalTimeout time.Duration) time.Duration {
	d := approvalTimeout + time.Minute
	if d < time.Minute {
		d = time.Minute
	}
	return d
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
		ClientID:     fmt.Sprintf("%d", os.Getpid()),
		TerminalSize: [2]uint16{cols, rows},
		Cwd:          cwd,
		Profile:      paths.Profile,
	}
}

func (c *Client) Handshake() error {
	cwd, _ := os.Getwd()
	cols, rows := uint16(80), uint16(24)
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols, rows = uint16(w), uint16(h)
	}

	return c.SendControl("handshake", BuildHandshake(c.paths, cols, rows, cwd))
}

func (c *Client) SendControl(msgType string, payload any) error {
	data, err := protocol.EncodeControl(msgType, payload)
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
	os.Stdout.WriteString(buf.String())
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
