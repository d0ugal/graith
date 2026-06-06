package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/d0ugal/graith/internal/protocol"
	"golang.org/x/term"
)

type PassthroughResult int

const (
	ResultDetached PassthroughResult = iota
	ResultOverlay
	ResultShell
	ResultQuit
	ResultDisconnected
	ResultRestart
	ResultNextSession
	ResultPrevSession
)

// kittyCtrlSeq returns the Kitty keyboard protocol escape sequence for
// Ctrl+<letter>. For example, Ctrl+b (prefixByte=0x02) produces "\x1b[98;5u".
// Terminals like Ghostty use this encoding instead of sending raw control bytes.
func kittyCtrlSeq(prefixByte byte) []byte {
	if prefixByte < 1 || prefixByte > 26 {
		return nil
	}
	codepoint := int(prefixByte) + 96
	return []byte(fmt.Sprintf("\x1b[%d;5u", codepoint))
}

// showHelpBar renders a one-line help bar at the bottom of the screen using
// ANSI save-cursor / restore-cursor so the agent's output isn't disturbed.
func showHelpBar(w io.Writer) {
	// Save cursor, move to last line, clear it, write help, restore cursor.
	help := "\x1b[7m d detach  w sessions  n next  p prev  s shell  r restart \x1b[0m"
	_, _ = w.Write([]byte("\x1b7\x1b[999B\r\x1b[2K" + help + "\x1b8"))
}

func clearHelpBar(w io.Writer) {
	_, _ = w.Write([]byte("\x1b7\x1b[999B\r\x1b[2K\x1b8"))
}

type PassthroughKeys struct {
	Prefix      byte
	NextSession byte
	PrevSession byte
}

func (c *Client) RunPassthrough(ctx context.Context, keys PassthroughKeys) PassthroughResult {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return ResultQuit
	}
	defer func() { _ = term.Restore(fd, oldState) }()

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
					_ = c.SendControl("resize", protocol.ResizeMsg{
						Cols: uint16(w),
						Rows: uint16(h),
					})
				}
			}
		}
	}()

	return c.runPassthroughLoop(ctx, keys, os.Stdin, os.Stdout)
}

type frameDemux struct {
	dataCh    chan []byte
	controlCh chan protocol.Envelope
	errCh     chan error
	done      chan struct{}
}

func (c *Client) startDemux(ctx context.Context) *frameDemux {
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
				select {
				case d.dataCh <- frame.Payload:
				case <-ctx.Done():
					return
				}
			case protocol.ChannelControl:
				msg, _ := protocol.DecodeControl(frame.Payload)
				select {
				case d.controlCh <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return d
}

func (c *Client) stopDemux(d *frameDemux) {
	_ = c.conn.Close()
	<-d.done
}

func (c *Client) runPassthroughLoop(ctx context.Context, keys PassthroughKeys, stdin io.Reader, stdout io.Writer) PassthroughResult {
	innerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	prefixByte := keys.Prefix
	kittySeq := kittyCtrlSeq(prefixByte)

	result := ResultQuit
	var resultOnce sync.Once
	setResult := func(r PassthroughResult) {
		resultOnce.Do(func() { result = r })
	}

	demux := c.startDemux(innerCtx)

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

			// Replace Kitty keyboard protocol sequences with raw prefix byte
			// so the scanning loop below can handle both traditional and
			// modern terminal encodings uniformly.
			input := buf[:n]
			if kittySeq != nil && bytes.Contains(input, kittySeq) {
				input = bytes.ReplaceAll(input, kittySeq, []byte{prefixByte})
				n = len(input)
			}

			sendStart := 0
			for i := 0; i < n; i++ {
				if prefixSeen {
					prefixSeen = false
					clearHelpBar(stdout)
					switch input[i] {
					case prefixByte:
						_ = c.SendData([]byte{prefixByte})
					case 'd':
						setResult(ResultDetached)
						return
					case 'w', 0:
						setResult(ResultOverlay)
						return
					case 's':
						setResult(ResultShell)
						return
					case keys.NextSession:
						setResult(ResultNextSession)
						return
					case keys.PrevSession:
						setResult(ResultPrevSession)
						return
					case 'r':
						setResult(ResultRestart)
						return
					default:
						_ = c.SendData([]byte{prefixByte, input[i]})
					}
					sendStart = i + 1
					continue
				}
				if input[i] == prefixByte {
					if i > sendStart {
						_ = c.SendData(input[sendStart:i])
					}
					prefixSeen = true
					showHelpBar(stdout)
					sendStart = i + 1
					continue
				}
			}
			if sendStart < n && !prefixSeen {
				_ = c.SendData(input[sendStart:n])
			}
		}
	}()

	<-innerCtx.Done()
	c.stopDemux(demux)

	return result
}
