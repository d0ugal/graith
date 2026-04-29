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

	if autoUpgrade && version.Version != "dev" {
		var hsOk protocol.HandshakeOkMsg
		if err := protocol.DecodePayload(resp, &hsOk); err == nil {
			if hsOk.DaemonVersion != "" && hsOk.DaemonVersion != version.Version {
				fmt.Fprintf(os.Stderr, "Daemon version mismatch (daemon=%s, cli=%s), restarting daemon...\n", hsOk.DaemonVersion, version.Version)
				c.Close()
				stopDaemonByPID(paths.PIDFile)
				waitForSocketGone(paths.SocketPath)
				os.Remove(paths.SocketPath)
				return connect(cfg, paths, configFile, false)
			}
		}
	}

	return c, nil
}

func stopDaemonByPID(pidFile string) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
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
	}
	if err := c.Handshake(); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.ReadControlResponse(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// ConnectForApproval is like ConnectFast but with a long deadline suitable
// for blocking on approval responses (up to 15 minutes).
func ConnectForApproval(paths config.Paths) (*Client, error) {
	conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}
	conn.SetDeadline(time.Now().Add(15 * time.Minute))
	c := &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
	}
	if err := c.Handshake(); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.ReadControlResponse(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() {
	_ = c.conn.Close()
}

func (c *Client) Handshake() error {
	cwd, _ := os.Getwd()
	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols, rows = w, h
	}

	return c.SendControl("handshake", protocol.HandshakeMsg{
		Version:      protocol.Version,
		ClientID:     fmt.Sprintf("%d", os.Getpid()),
		TerminalSize: [2]uint16{uint16(cols), uint16(rows)},
		Cwd:          cwd,
	})
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
