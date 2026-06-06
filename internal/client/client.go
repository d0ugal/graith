package client

import (
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
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
// handshake response. On failure the connection is closed automatically.
func Connect(cfg *config.Config, paths config.Paths, configFile string) (*Client, error) {
	c, err := New(cfg, paths, configFile)
	if err != nil {
		return nil, err
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
	c.conn.Close()
}

func (c *Client) Handshake() error {
	cwd, _ := os.Getwd()
	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols, rows = w, h
	}

	return c.SendControl("handshake", protocol.HandshakeMsg{
		Version:      "1.0",
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
