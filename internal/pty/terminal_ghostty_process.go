//go:build libghostty && cgo && (darwin || linux)

package pty

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	ghosttyHelperArg       = "--graith-internal-libghostty-helper"
	ghosttyHelperEnv       = "GRAITH_INTERNAL_LIBGHOSTTY_HELPER"
	ghosttyHelperFD        = 3
	ghosttyProtocolVersion = 1
	ghosttyRPCTimeout      = 5 * time.Second
	ghosttyMaxRequestBytes = 16 * 1024 * 1024
	ghosttyMaxReplyBytes   = 128 * 1024 * 1024

	ghosttyOpCreate   = 1
	ghosttyOpWrite    = 2
	ghosttyOpResize   = 3
	ghosttyOpSnapshot = 4
	ghosttyOpClose    = 5

	ghosttyStatusOK       = 0
	ghosttyStatusInvalid  = 1
	ghosttyStatusNative   = 2
	ghosttyStatusProtocol = 3
)

var (
	errGhosttyHelperClosed   = errors.New("libghostty helper is closed")
	errGhosttyHelperIO       = errors.New("libghostty helper communication failed")
	errGhosttyHelperProtocol = errors.New("libghostty helper protocol violation")
	errGhosttyHelperTimeout  = errors.New("libghostty helper operation timed out")
)

var ghosttyRequestMagic = [4]byte{'G', 'V', 'T', 'Q'}
var ghosttyReplyMagic = [4]byte{'G', 'V', 'T', 'R'}

// init supplies the helper entry point to every libghostty-enabled Graith or Go
// test binary. It activates only for the exact private argument and marker set
// by newGhosttyProcessTerminal; ordinary package import has no side effects.
func init() {
	if len(os.Args) != 2 || os.Args[1] != ghosttyHelperArg || os.Getenv(ghosttyHelperEnv) != "1" {
		return
	}

	code := 0
	if err := serveGhosttyHelperFD(); err != nil {
		code = 70
	}

	os.Exit(code)
}

// ghosttyProcessTerminal owns no native terminal state. The pinned C/Zig
// adapter lives in a child process so an abort, segmentation fault, or safety
// trap loses only a reconstructable screen model rather than the daemon.
type ghosttyProcessTerminal struct {
	cmd      *exec.Cmd
	conn     net.Conn
	waitDone chan error

	cols int
	rows int

	cache    TerminalSnapshot
	dirty    bool
	fatalErr error

	closeOnce sync.Once
	closeErr  error
}

var _ Terminal = (*ghosttyProcessTerminal)(nil)
var _ terminalSnapshotter = (*ghosttyProcessTerminal)(nil)

func newGhosttyProcessTerminal(cols, rows int) (*ghosttyProcessTerminal, error) {
	cols, rows, err := validateGhosttySize(cols, rows)
	if err != nil {
		return nil, err
	}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("create libghostty helper socket: %w", err)
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])

	parentFile := os.NewFile(uintptr(fds[0]), "libghostty-parent")
	childFile := os.NewFile(uintptr(fds[1]), "libghostty-child")
	if parentFile == nil || childFile == nil {
		if parentFile != nil {
			_ = parentFile.Close()
		}
		if childFile != nil {
			_ = childFile.Close()
		}

		return nil, errors.New("create libghostty helper socket files")
	}

	conn, err := net.FileConn(parentFile)
	_ = parentFile.Close()
	if err != nil {
		_ = childFile.Close()

		return nil, fmt.Errorf("open libghostty helper socket: %w", err)
	}

	executable, err := os.Executable()
	if err != nil {
		_ = childFile.Close()
		_ = conn.Close()

		return nil, fmt.Errorf("resolve libghostty helper executable: %w", err)
	}

	cmd := exec.Command(executable, ghosttyHelperArg)
	cmd.Env = ghosttyChildEnvironment()
	cmd.ExtraFiles = []*os.File{childFile}
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = childFile.Close()
		_ = conn.Close()

		return nil, fmt.Errorf("start libghostty helper: %w", err)
	}
	_ = childFile.Close()

	terminal := &ghosttyProcessTerminal{
		cmd:      cmd,
		conn:     conn,
		waitDone: make(chan error, 1),
		cols:     cols,
		rows:     rows,
		dirty:    true,
	}
	go func() {
		terminal.waitDone <- cmd.Wait()
		close(terminal.waitDone)
	}()

	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], uint16(cols))
	binary.BigEndian.PutUint16(payload[2:4], uint16(rows))
	if _, err := terminal.exchange(ghosttyOpCreate, payload); err != nil {
		terminal.stop()

		return nil, fmt.Errorf("initialize libghostty helper: %w", err)
	}

	return terminal, nil
}

func ghosttyChildEnvironment() []string {
	env := []string{ghosttyHelperEnv + "=1"}
	// Preserve sanitizer/race configuration for dedicated validation runs but
	// do not copy credentials or unrelated daemon environment into the helper.
	for _, key := range []string{"ASAN_OPTIONS", "GORACE", "LSAN_OPTIONS", "TSAN_OPTIONS", "UBSAN_OPTIONS"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}

	return env
}

func (gt *ghosttyProcessTerminal) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, gt.currentError()
	}
	if len(p) > ghosttyMaxRequestBytes {
		return 0, fmt.Errorf("libghostty write exceeds %d-byte helper limit", ghosttyMaxRequestBytes)
	}
	if _, err := gt.exchange(ghosttyOpWrite, p); err != nil {
		return 0, err
	}

	gt.dirty = true

	return len(p), nil
}

func (gt *ghosttyProcessTerminal) Resize(cols, rows int) error {
	cols, rows, err := validateGhosttySize(cols, rows)
	if err != nil {
		return err
	}

	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], uint16(cols))
	binary.BigEndian.PutUint16(payload[2:4], uint16(rows))
	if _, err := gt.exchange(ghosttyOpResize, payload); err != nil {
		return err
	}

	gt.cols = cols
	gt.rows = rows
	gt.dirty = true

	return nil
}

func (gt *ghosttyProcessTerminal) Size() (int, int) {
	return gt.cols, gt.rows
}

func (gt *ghosttyProcessTerminal) Cursor() (int, int, bool) {
	snapshot, err := gt.Snapshot()
	if err != nil {
		return 0, 0, false
	}

	return snapshot.CursorX, snapshot.CursorY, snapshot.CursorVisible
}

func (gt *ghosttyProcessTerminal) Cell(x, y int) Cell {
	if x < 0 || x >= gt.cols || y < 0 || y >= gt.rows {
		return Cell{Content: " "}
	}

	snapshot, err := gt.Snapshot()
	if err != nil {
		return Cell{Content: " "}
	}

	return snapshot.Cells[y*snapshot.Cols+x]
}

func (gt *ghosttyProcessTerminal) Snapshot() (TerminalSnapshot, error) {
	if err := gt.currentError(); err != nil {
		return TerminalSnapshot{}, err
	}
	if !gt.dirty {
		return gt.cache, nil
	}

	payload, err := gt.exchange(ghosttyOpSnapshot, nil)
	if err != nil {
		return TerminalSnapshot{}, err
	}

	snapshot, err := decodeGhosttySnapshot(payload)
	if err != nil {
		gt.fail(err)

		return TerminalSnapshot{}, err
	}
	if snapshot.Cols != gt.cols || snapshot.Rows != gt.rows {
		err := fmt.Errorf("%w: helper returned geometry %dx%d, expected %dx%d",
			errGhosttyHelperProtocol, snapshot.Cols, snapshot.Rows, gt.cols, gt.rows)
		gt.fail(err)

		return TerminalSnapshot{}, err
	}

	gt.cache = snapshot
	gt.dirty = false

	return snapshot, nil
}

func (gt *ghosttyProcessTerminal) Close() error {
	gt.closeOnce.Do(func() {
		if gt.conn != nil && gt.fatalErr == nil {
			_, gt.closeErr = gt.exchange(ghosttyOpClose, nil)
		}
		gt.stop()
	})

	return gt.closeErr
}

func (gt *ghosttyProcessTerminal) PeakRSSBytes() int64 {
	if gt.cmd == nil {
		return 0
	}

	return extractPeakRSS(gt.cmd.ProcessState)
}

func (gt *ghosttyProcessTerminal) currentError() error {
	if gt.fatalErr != nil {
		return gt.fatalErr
	}
	if gt.conn == nil {
		return errGhosttyHelperClosed
	}

	return nil
}

func (gt *ghosttyProcessTerminal) exchange(op byte, payload []byte) ([]byte, error) {
	if err := gt.currentError(); err != nil {
		return nil, err
	}
	if len(payload) > ghosttyMaxRequestBytes {
		return nil, errGhosttyHelperProtocol
	}

	if err := gt.conn.SetDeadline(time.Now().Add(ghosttyRPCTimeout)); err != nil {
		gt.fail(errGhosttyHelperIO)

		return nil, errGhosttyHelperIO
	}

	header := make([]byte, 12)
	copy(header[0:4], ghosttyRequestMagic[:])
	header[4] = ghosttyProtocolVersion
	header[5] = op
	binary.BigEndian.PutUint32(header[8:12], uint32(len(payload)))
	if err := writeAll(gt.conn, header); err != nil {
		return nil, gt.exchangeIOError(err)
	}
	if err := writeAll(gt.conn, payload); err != nil {
		return nil, gt.exchangeIOError(err)
	}

	if _, err := io.ReadFull(gt.conn, header); err != nil {
		return nil, gt.exchangeIOError(err)
	}
	if !bytes.Equal(header[0:4], ghosttyReplyMagic[:]) ||
		header[4] != ghosttyProtocolVersion || header[5] != op || header[7] != 0 {
		gt.fail(errGhosttyHelperProtocol)

		return nil, errGhosttyHelperProtocol
	}

	length := int(binary.BigEndian.Uint32(header[8:12]))
	if length > ghosttyMaxReplyBytes {
		gt.fail(errGhosttyHelperProtocol)

		return nil, errGhosttyHelperProtocol
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(gt.conn, payload); err != nil {
		return nil, gt.exchangeIOError(err)
	}
	_ = gt.conn.SetDeadline(time.Time{})

	status := header[6]
	if status > ghosttyStatusProtocol || (status != ghosttyStatusOK && len(payload) != 0) {
		gt.fail(errGhosttyHelperProtocol)

		return nil, errGhosttyHelperProtocol
	}
	if status != ghosttyStatusOK {
		return nil, fmt.Errorf("libghostty helper rejected operation %d (status %d)", op, status)
	}

	return payload, nil
}

func (gt *ghosttyProcessTerminal) exchangeIOError(err error) error {
	result := errGhosttyHelperIO
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		result = errGhosttyHelperTimeout
	}
	gt.fail(result)

	return result
}

func (gt *ghosttyProcessTerminal) fail(err error) {
	if gt.fatalErr == nil {
		gt.fatalErr = err
	}
	if gt.conn != nil {
		_ = gt.conn.Close()
		gt.conn = nil
	}
	if gt.cmd != nil && gt.cmd.Process != nil {
		_ = gt.cmd.Process.Kill()
	}
}

func (gt *ghosttyProcessTerminal) stop() {
	if gt.conn != nil {
		_ = gt.conn.Close()
		gt.conn = nil
	}

	if gt.waitDone == nil {
		return
	}

	select {
	case <-gt.waitDone:
		return
	case <-time.After(ghosttyRPCTimeout):
		if gt.cmd != nil && gt.cmd.Process != nil {
			_ = gt.cmd.Process.Kill()
		}
	}

	select {
	case <-gt.waitDone:
	case <-time.After(time.Second):
	}
}

func serveGhosttyHelperFD() error {
	file := os.NewFile(ghosttyHelperFD, "libghostty-helper")
	if file == nil {
		return errGhosttyHelperProtocol
	}
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	defer conn.Close()

	return serveGhosttyHelper(conn)
}

func serveGhosttyHelper(conn net.Conn) error {
	var terminal *ghosttyTerminal
	defer func() {
		if terminal != nil {
			_ = terminal.Close()
		}
	}()

	for {
		op, payload, err := readGhosttyRequest(conn)
		if err != nil {
			return err
		}

		status := byte(ghosttyStatusOK)
		var reply []byte
		switch op {
		case ghosttyOpCreate:
			if terminal != nil || len(payload) != 4 {
				status = ghosttyStatusInvalid
				break
			}
			cols := int(binary.BigEndian.Uint16(payload[0:2]))
			rows := int(binary.BigEndian.Uint16(payload[2:4]))
			terminal, err = newGhosttyTerminal(cols, rows)
			if err != nil {
				status = ghosttyStatusNative
			}
		case ghosttyOpWrite:
			if terminal == nil {
				status = ghosttyStatusInvalid
				break
			}
			if _, err = terminal.Write(payload); err != nil {
				status = ghosttyStatusNative
			}
		case ghosttyOpResize:
			if terminal == nil || len(payload) != 4 {
				status = ghosttyStatusInvalid
				break
			}
			cols := int(binary.BigEndian.Uint16(payload[0:2]))
			rows := int(binary.BigEndian.Uint16(payload[2:4]))
			if err = terminal.Resize(cols, rows); err != nil {
				status = ghosttyStatusNative
			}
		case ghosttyOpSnapshot:
			if terminal == nil || len(payload) != 0 {
				status = ghosttyStatusInvalid
				break
			}
			snapshot, snapshotErr := terminal.Snapshot()
			if snapshotErr != nil {
				status = ghosttyStatusNative
			} else {
				reply, err = encodeGhosttySnapshot(snapshot)
				if err != nil {
					status = ghosttyStatusProtocol
				}
			}
		case ghosttyOpClose:
			if terminal == nil || len(payload) != 0 {
				status = ghosttyStatusInvalid
				break
			}
			err = terminal.Close()
			terminal = nil
			if err != nil {
				status = ghosttyStatusNative
			}
		default:
			status = ghosttyStatusInvalid
		}

		if err := writeGhosttyReply(conn, op, status, reply); err != nil {
			return err
		}
		if op == ghosttyOpClose && status == ghosttyStatusOK {
			return nil
		}
	}
}

func readGhosttyRequest(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	if !bytes.Equal(header[0:4], ghosttyRequestMagic[:]) ||
		header[4] != ghosttyProtocolVersion || header[6] != 0 || header[7] != 0 {
		return 0, nil, errGhosttyHelperProtocol
	}

	length := int(binary.BigEndian.Uint32(header[8:12]))
	if length > ghosttyMaxRequestBytes {
		return 0, nil, errGhosttyHelperProtocol
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}

	return header[5], payload, nil
}

func writeGhosttyReply(w io.Writer, op, status byte, payload []byte) error {
	if len(payload) > ghosttyMaxReplyBytes {
		return errGhosttyHelperProtocol
	}
	header := make([]byte, 12)
	copy(header[0:4], ghosttyReplyMagic[:])
	header[4] = ghosttyProtocolVersion
	header[5] = op
	header[6] = status
	binary.BigEndian.PutUint32(header[8:12], uint32(len(payload)))
	if err := writeAll(w, header); err != nil {
		return err
	}

	return writeAll(w, payload)
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}

	return nil
}

func encodeGhosttySnapshot(snapshot TerminalSnapshot) ([]byte, error) {
	if snapshot.Cols < 1 || snapshot.Rows < 1 ||
		snapshot.Cols > int(^uint16(0)) || snapshot.Rows > int(^uint16(0)) ||
		snapshot.CursorX < 0 || snapshot.CursorX > int(^uint16(0)) ||
		snapshot.CursorY < 0 || snapshot.CursorY > int(^uint16(0)) ||
		len(snapshot.Cells) != snapshot.Cols*snapshot.Rows || len(snapshot.Cells) > maxGhosttyCells {
		return nil, errGhosttyHelperProtocol
	}

	var buf bytes.Buffer
	buf.Grow(13 + len(snapshot.Cells)*16)
	fixed := make([]byte, 13)
	binary.BigEndian.PutUint16(fixed[0:2], uint16(snapshot.Cols))
	binary.BigEndian.PutUint16(fixed[2:4], uint16(snapshot.Rows))
	binary.BigEndian.PutUint16(fixed[4:6], uint16(snapshot.CursorX))
	binary.BigEndian.PutUint16(fixed[6:8], uint16(snapshot.CursorY))
	if snapshot.CursorVisible {
		fixed[8] = 1
	}
	binary.BigEndian.PutUint32(fixed[9:13], uint32(len(snapshot.Cells)))
	_, _ = buf.Write(fixed)

	for _, cell := range snapshot.Cells {
		if len(cell.Content) > ghosttyMaxReplyBytes {
			return nil, errGhosttyHelperProtocol
		}
		record := make([]byte, 16)
		binary.BigEndian.PutUint32(record[0:4], uint32(len(cell.Content)))
		record[4] = byte(cell.Style.FG.Kind)
		record[5] = byte(cell.Style.BG.Kind)
		binary.BigEndian.PutUint16(record[6:8], encodeGhosttyStyleFlags(cell.Style))
		binary.BigEndian.PutUint32(record[8:12], cell.Style.FG.Value)
		binary.BigEndian.PutUint32(record[12:16], cell.Style.BG.Value)
		_, _ = buf.Write(record)
		_, _ = buf.WriteString(cell.Content)
		if buf.Len() > ghosttyMaxReplyBytes {
			return nil, errGhosttyHelperProtocol
		}
	}

	return buf.Bytes(), nil
}

func decodeGhosttySnapshot(payload []byte) (TerminalSnapshot, error) {
	if len(payload) < 13 {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}

	snapshot := TerminalSnapshot{
		Cols:          int(binary.BigEndian.Uint16(payload[0:2])),
		Rows:          int(binary.BigEndian.Uint16(payload[2:4])),
		CursorX:       int(binary.BigEndian.Uint16(payload[4:6])),
		CursorY:       int(binary.BigEndian.Uint16(payload[6:8])),
		CursorVisible: payload[8] != 0,
	}
	if payload[8] > 1 {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}
	count := int(binary.BigEndian.Uint32(payload[9:13]))
	if snapshot.Cols < 1 || snapshot.Rows < 1 || count != snapshot.Cols*snapshot.Rows ||
		count > maxGhosttyCells || count > (len(payload)-13)/16 {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}

	payload = payload[13:]
	snapshot.Cells = make([]Cell, count)
	for i := range snapshot.Cells {
		if len(payload) < 16 {
			return TerminalSnapshot{}, errGhosttyHelperProtocol
		}
		contentLen := int(binary.BigEndian.Uint32(payload[0:4]))
		fgKind := ColorKind(payload[4])
		bgKind := ColorKind(payload[5])
		flags := binary.BigEndian.Uint16(payload[6:8])
		if fgKind > ColorRGB || bgKind > ColorRGB || flags&^uint16(0x7f) != 0 ||
			contentLen > len(payload)-16 || !utf8.Valid(payload[16:16+contentLen]) {
			return TerminalSnapshot{}, errGhosttyHelperProtocol
		}
		snapshot.Cells[i] = Cell{
			Content: string(payload[16 : 16+contentLen]),
			Style: CellStyle{
				FG: Color{Kind: fgKind, Value: binary.BigEndian.Uint32(payload[8:12])},
				BG: Color{Kind: bgKind, Value: binary.BigEndian.Uint32(payload[12:16])},
			},
		}
		decodeGhosttyStyleFlags(&snapshot.Cells[i].Style, flags)
		payload = payload[16+contentLen:]
	}
	if len(payload) != 0 {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}

	return snapshot, nil
}

func encodeGhosttyStyleFlags(style CellStyle) uint16 {
	var flags uint16
	values := [...]bool{
		style.Bold, style.Faint, style.Italic, style.Underline,
		style.Blink, style.Reverse, style.Strikethrough,
	}
	for bit, value := range values {
		if value {
			flags |= 1 << bit
		}
	}

	return flags
}

func decodeGhosttyStyleFlags(style *CellStyle, flags uint16) {
	style.Bold = flags&1 != 0
	style.Faint = flags&(1<<1) != 0
	style.Italic = flags&(1<<2) != 0
	style.Underline = flags&(1<<3) != 0
	style.Blink = flags&(1<<4) != 0
	style.Reverse = flags&(1<<5) != 0
	style.Strikethrough = flags&(1<<6) != 0
}
