package client

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dougalmatthews/graith/internal/protocol"
	"golang.org/x/term"
)

type PassthroughResult int

const (
	ResultDetached PassthroughResult = iota
	ResultOverlay
	ResultShell
	ResultQuit
	ResultDisconnected
)

func (c *Client) RunPassthrough(ctx context.Context, prefixByte byte) PassthroughResult {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return ResultQuit
	}
	defer term.Restore(fd, oldState)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	innerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	result := ResultQuit

	// SIGWINCH handler
	go func() {
		for {
			select {
			case <-innerCtx.Done():
				return
			case <-sigCh:
				if w, h, err := term.GetSize(fd); err == nil {
					c.SendControl("resize", protocol.ResizeMsg{
						Cols: uint16(w),
						Rows: uint16(h),
					})
				}
			}
		}
	}()

	// Read from daemon, write to stdout
	go func() {
		defer cancel()
		for {
			frame, err := c.ReadFrame()
			if err != nil {
				if innerCtx.Err() == nil {
					result = ResultDisconnected
				}
				return
			}
			select {
			case <-innerCtx.Done():
				return
			default:
			}

			switch frame.Channel {
			case protocol.ChannelData:
				os.Stdout.Write(frame.Payload)
			case protocol.ChannelControl:
				msg, _ := protocol.DecodeControl(frame.Payload)
				if msg.Type == "detached" {
					result = ResultDetached
					return
				}
			}
		}
	}()

	// Read from stdin, write to daemon (with prefix key interception)
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		prefixSeen := false
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			select {
			case <-innerCtx.Done():
				return
			default:
			}

			sendStart := 0
			for i := 0; i < n; i++ {
				if prefixSeen {
					prefixSeen = false
					// Flush anything before the prefix byte
					// (already flushed when we set prefixSeen)
					switch buf[i] {
					case prefixByte:
						c.SendData([]byte{prefixByte})
					case 'd':
						result = ResultDetached
						return
					case 'w', 0:
						result = ResultOverlay
						return
					case 's':
						result = ResultShell
						return
					default:
						c.SendData([]byte{prefixByte, buf[i]})
					}
					sendStart = i + 1
					continue
				}
				if buf[i] == prefixByte {
					if i > sendStart {
						c.SendData(buf[sendStart:i])
					}
					prefixSeen = true
					sendStart = i + 1
					continue
				}
			}
			if sendStart < n && !prefixSeen {
				c.SendData(buf[sendStart:n])
			}
		}
	}()

	<-innerCtx.Done()

	return result
}
