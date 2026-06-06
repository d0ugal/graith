package client

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
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

	go func() {
		ctx2, cancel := context.WithCancel(ctx)
		defer cancel()
		for {
			select {
			case <-ctx2.Done():
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

	return c.runPassthroughLoop(ctx, prefixByte, os.Stdin, os.Stdout)
}

// frameDemux reads frames from the connection and dispatches them to
// channels. It provides exclusive read access — no other goroutine
// should call ReadFrame while a demux is running.
type frameDemux struct {
	dataCh    chan []byte
	controlCh chan protocol.Envelope
	errCh     chan error
	done      chan struct{}
}

func (c *Client) startDemux() *frameDemux {
	d := &frameDemux{
		dataCh:    make(chan []byte, 64),
		controlCh: make(chan protocol.Envelope, 4),
		errCh:     make(chan error, 1),
		done:      make(chan struct{}),
	}
	go func() {
		defer close(d.done)
		for {
			frame, err := c.ReadFrame()
			if err != nil {
				select {
				case d.errCh <- err:
				default:
				}
				return
			}
			switch frame.Channel {
			case protocol.ChannelData:
				d.dataCh <- frame.Payload
			case protocol.ChannelControl:
				msg, _ := protocol.DecodeControl(frame.Payload)
				d.controlCh <- msg
			}
		}
	}()
	return d
}

// drain stops the demux by setting a read deadline on the connection,
// waits for the reader goroutine to exit, then clears the deadline.
func (c *Client) drainDemux(d *frameDemux) {
	c.conn.SetReadDeadline(shortDeadline())
	<-d.done
	c.conn.SetReadDeadline(noDeadline())
}

func (c *Client) runPassthroughLoop(ctx context.Context, prefixByte byte, stdin io.Reader, stdout io.Writer) PassthroughResult {
	innerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	result := ResultQuit
	var resultOnce sync.Once
	setResult := func(r PassthroughResult) {
		resultOnce.Do(func() { result = r })
	}

	demux := c.startDemux()

	// Read from daemon via demux, write to stdout
	go func() {
		defer cancel()
		for {
			select {
			case <-innerCtx.Done():
				return
			case data := <-demux.dataCh:
				stdout.Write(data)
			case msg := <-demux.controlCh:
				if msg.Type == "detached" {
					setResult(ResultDetached)
					return
				}
			case <-demux.errCh:
				setResult(ResultDisconnected)
				return
			}
		}
	}()

	// Read from stdin, write to daemon (with prefix key interception)
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		prefixSeen := false
		for {
			n, err := stdin.Read(buf)
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
					switch buf[i] {
					case prefixByte:
						c.SendData([]byte{prefixByte})
					case 'd':
						setResult(ResultDetached)
						return
					case 'w', 0:
						setResult(ResultOverlay)
						return
					case 's':
						setResult(ResultShell)
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
	c.drainDemux(demux)

	return result
}
