//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	ghosttyHelperArg          = "--graith-internal-libghostty-helper"
	ghosttyHelperEnv          = "GRAITH_INTERNAL_LIBGHOSTTY_HELPER"
	ghosttyPinnedExecFD       = 3
	ghosttyHelperFD           = 4
	ghosttyProtocolVersion    = 1
	ghosttyRPCTimeout         = 5 * time.Second
	ghosttyShutdownTimeout    = 250 * time.Millisecond
	ghosttyReapTimeout        = 2 * time.Second
	ghosttyMaxRequestBytes    = 1 * 1024 * 1024
	ghosttyMaxReplyBytes      = 32 * 1024 * 1024
	ghosttyMaxWriteReplyBytes = 64 * 1024
	// A terminal cell is one grapheme, not arbitrary retained output. This
	// generous cap contains malicious combining-mark runs without affecting
	// ordinary emoji or composed scripts.
	ghosttyMaxCellContentBytes = 1024
	ghosttyMaxHelperProcesses  = 64
	ghosttyHelperFDLimit       = 64

	ghosttyOpCreate   = 1
	ghosttyOpWrite    = 2
	ghosttyOpResize   = 3
	ghosttyOpSnapshot = 4
	ghosttyOpClose    = 5
	ghosttyOpPinProbe = 6

	ghosttyStatusOK       = 0
	ghosttyStatusInvalid  = 1
	ghosttyStatusNative   = 2
	ghosttyStatusProtocol = 3

	ghosttySnapshotRequestHistory = 1
)

var (
	errGhosttyHelperClosed   = errors.New("libghostty helper is closed")
	errGhosttyHelperIO       = errors.New("libghostty helper communication failed")
	errGhosttyHelperProtocol = errors.New("libghostty helper protocol violation")
	errGhosttyHelperTimeout  = errors.New("libghostty helper operation timed out")
	errGhosttyHelperNative   = errors.New("libghostty native operation failed")
	errGhosttyHelperLimit    = errors.New("libghostty helper resource limit reached")
	errGhosttyHelperStart    = errors.New("libghostty helper could not start")
)

var ghosttyRequestMagic = [4]byte{'G', 'V', 'T', 'Q'}
var ghosttyReplyMagic = [4]byte{'G', 'V', 'T', 'R'}
var ghosttySnapshotHistoryMagic = [4]byte{'H', 'S', 'T', '1'}
var ghosttySnapshotHistoryPayload = []byte{ghosttySnapshotRequestHistory}

type ghosttyProcessLimiter struct {
	slots chan struct{}
}

func newGhosttyProcessLimiter(limit int) *ghosttyProcessLimiter {
	return &ghosttyProcessLimiter{slots: make(chan struct{}, limit)}
}

func (l *ghosttyProcessLimiter) acquire() bool {
	select {
	case l.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *ghosttyProcessLimiter) release() {
	<-l.slots
}

var ghosttyHelperLimiter = newGhosttyProcessLimiter(ghosttyMaxHelperProcesses)

// ghosttyHelperRegistry is the exec-upgrade barrier. A helper is retained from
// successful Start until the exact cmd.Wait completes, including failed and
// replaced terminals whose bounded Close has returned. Freeze first bars new
// starts, then waits for starts already between the gate and registration.
type ghosttyHelperRegistry struct {
	mu       sync.Mutex
	frozen   bool
	creating int
	helpers  map[int]int64
	changed  chan struct{}
}

func newGhosttyHelperRegistry() *ghosttyHelperRegistry {
	return &ghosttyHelperRegistry{helpers: make(map[int]int64), changed: make(chan struct{})}
}

func (r *ghosttyHelperRegistry) signalLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *ghosttyHelperRegistry) begin() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frozen {
		return errTerminalGenerationFrozen
	}

	r.creating++

	return nil
}

func (r *ghosttyHelperRegistry) finish(identity HelperProcessIdentity) {
	r.mu.Lock()
	if identity.PID > 0 && identity.StartTime > 0 {
		r.helpers[identity.PID] = identity.StartTime
	}

	r.creating--
	r.signalLocked()
	r.mu.Unlock()
}

func (r *ghosttyHelperRegistry) remove(identity HelperProcessIdentity) {
	r.mu.Lock()
	if r.helpers[identity.PID] == identity.StartTime {
		delete(r.helpers, identity.PID)
	}
	r.mu.Unlock()
}

func (r *ghosttyHelperRegistry) freeze(ctx context.Context) ([]HelperProcessIdentity, error) {
	r.mu.Lock()

	r.frozen = true
	for r.creating > 0 {
		changed := r.changed
		r.mu.Unlock()

		select {
		case <-changed:
			r.mu.Lock()
		case <-ctx.Done():
			r.mu.Lock()
			r.frozen = false
			r.signalLocked()
			r.mu.Unlock()

			return nil, ctx.Err()
		}
	}

	result := make([]HelperProcessIdentity, 0, len(r.helpers))
	for pid, startTime := range r.helpers {
		result = append(result, HelperProcessIdentity{PID: pid, StartTime: startTime})
	}
	r.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].PID < result[j].PID })

	return result, nil
}

func (r *ghosttyHelperRegistry) thaw() {
	r.mu.Lock()
	r.frozen = false
	r.signalLocked()
	r.mu.Unlock()
}

var ghosttyHelpers = newGhosttyHelperRegistry()

func FreezeTerminalHelpers(ctx context.Context) ([]HelperProcessIdentity, error) {
	return ghosttyHelpers.freeze(ctx)
}

func ThawTerminalHelpers() { ghosttyHelpers.thaw() }

type ghosttyProcessConfig struct {
	executable         string
	limiter            *ghosttyProcessLimiter
	onExecutablePinned func()
	onStart            func(*exec.Cmd)
	historyRows        int
}

type ghosttyPinnedImage struct {
	file        *os.File
	path        string
	prepare     func() error
	validate    func() error
	releasePath func() error
	cleanup     func() error
}

var (
	ghosttyPinnedMu         sync.Mutex
	ghosttyPinnedExecutable *ghosttyPinnedImage
	ghosttyPinnedFDClosed   bool
)

// init supplies the helper entry point to every libghostty-enabled Graith or Go
// test binary. It activates only for the exact private argument and marker set
// by newGhosttyProcessTerminal. Ordinary import performs no filesystem work;
// the daemon or the first lazy terminal construction pins the running image
// after upgrade ownership has been established.
//
//nolint:gochecknoinits // Helper binaries must dispatch before package tests or main run.
func init() {
	if len(os.Args) == 2 && os.Args[1] == ghosttyHelperArg && os.Getenv(ghosttyHelperEnv) == "1" {
		err := unix.Close(ghosttyPinnedExecFD)
		ghosttyPinnedFDClosed = err == nil || errors.Is(err, unix.EBADF)

		code := 0
		if err := serveGhosttyHelperFD(); err != nil {
			code = 70
		}

		os.Exit(code)
	}
}

// PreparePinnedTerminalExecutable performs the fallible running-image pin only
// after the daemon has established ownership of any transferred upgrade
// resources. Package init deliberately does no filesystem, hashing, or fsync
// work before RunAdoptBootstrap can arm its cleanup guard.
func PreparePinnedTerminalExecutable() error {
	ghosttyPinnedMu.Lock()
	defer ghosttyPinnedMu.Unlock()

	return preparePinnedTerminalExecutableLocked()
}

func preparePinnedTerminalExecutableLocked() error {
	if ghosttyPinnedExecutable != nil {
		return nil
	}

	pinned, err := pinRunningGhosttyExecutable()
	if err != nil {
		return err
	}

	ghosttyPinnedExecutable = pinned

	return nil
}

// ClosePinnedTerminalExecutable releases the daemon-lifetime executable pin.
// Run calls it only during final daemon shutdown, after new helper creation has
// stopped. A failed cleanup is deliberately ignored: it is safer to leave a
// private retained image than to make shutdown or recovery fail.
func ClosePinnedTerminalExecutable() {
	ghosttyPinnedMu.Lock()
	defer ghosttyPinnedMu.Unlock()

	if ghosttyPinnedExecutable == nil || ghosttyPinnedExecutable.cleanup == nil {
		return
	}

	if err := ghosttyPinnedExecutable.cleanup(); err == nil {
		ghosttyPinnedExecutable = nil
	}
}

// ReleasePinnedTerminalExecutablePathForExec removes platform-specific
// retained path state after helper generation has been frozen. The open image
// descriptor remains available so a failed syscall.Exec can recreate the path
// during terminal recovery. Linux executes through that descriptor directly.
func ReleasePinnedTerminalExecutablePathForExec() error {
	ghosttyPinnedMu.Lock()
	defer ghosttyPinnedMu.Unlock()

	if ghosttyPinnedExecutable == nil || ghosttyPinnedExecutable.releasePath == nil {
		return nil
	}

	return ghosttyPinnedExecutable.releasePath()
}

func RestorePinnedTerminalExecutableAfterExec() error {
	ghosttyPinnedMu.Lock()
	defer ghosttyPinnedMu.Unlock()

	if ghosttyPinnedExecutable == nil {
		return errGhosttyHelperStart
	}

	if err := ghosttyPinnedExecutable.prepare(); err != nil {
		return err
	}

	return ghosttyPinnedExecutable.validate()
}

// ghosttyProcessTerminal owns no native terminal state. The pinned C/Zig
// adapter lives in a child process so an abort, segmentation fault, or safety
// trap loses only a reconstructable screen model rather than the daemon.
type ghosttyProcessTerminal struct {
	opMu     sync.Mutex
	cmd      *exec.Cmd
	conn     net.Conn
	waitDone chan error
	limiter  *ghosttyProcessLimiter
	peakRSS  atomic.Int64

	rpcTimeout      time.Duration
	shutdownTimeout time.Duration
	reapTimeout     time.Duration

	cols int
	rows int

	cache             TerminalSnapshot
	dirty             bool
	fatalErr          error
	pendingPtyReplies []byte

	closeOnce sync.Once
	closeErr  error
	stopOnce  sync.Once
	slotOnce  sync.Once
}

var _ Terminal = (*ghosttyProcessTerminal)(nil)
var _ terminalSnapshotter = (*ghosttyProcessTerminal)(nil)
var _ terminalHistorySnapshotter = (*ghosttyProcessTerminal)(nil)

func newGhosttyProcessTerminal(cols, rows int) (*ghosttyProcessTerminal, error) {
	return newGhosttyProcessTerminalWithConfig(cols, rows, ghosttyProcessConfig{
		limiter: ghosttyHelperLimiter,
	})
}

func newGhosttyProcessTerminalWithHistory(cols, rows, historyRows int) (*ghosttyProcessTerminal, error) {
	if historyRows <= 0 {
		historyRows = defaultTerminalHistoryRows
	}

	return newGhosttyProcessTerminalWithConfig(cols, rows, ghosttyProcessConfig{
		limiter:     ghosttyHelperLimiter,
		historyRows: historyRows,
	})
}

func newGhosttyProcessTerminalWithConfig(
	cols, rows int,
	config ghosttyProcessConfig,
) (*ghosttyProcessTerminal, error) {
	cols, rows, cols16, rows16, err := validateGhosttySize16(cols, rows)
	if err != nil {
		return nil, err
	}

	if err := ghosttyHelpers.begin(); err != nil {
		return nil, err
	}

	generationActive := true
	defer func() {
		if generationActive {
			ghosttyHelpers.finish(HelperProcessIdentity{})
		}
	}()

	if config.limiter == nil || !config.limiter.acquire() {
		return nil, errGhosttyHelperLimit
	}

	slotHeld := true
	defer func() {
		if slotHeld {
			config.limiter.release()
		}
	}()

	fds, err := ghosttySocketpair()
	if err != nil {
		return nil, fmt.Errorf("create libghostty helper socket: %w", err)
	}

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

	globalPinLocked := false

	if config.executable == "" {
		// Registry begin above makes an in-progress launch visible to the exec
		// freeze. Hold the owner lock until the child has completed bootstrap so
		// path release or final shutdown cannot close the pin beneath Start.
		ghosttyPinnedMu.Lock()

		globalPinLocked = true
		defer func() {
			if globalPinLocked {
				ghosttyPinnedMu.Unlock()
			}
		}()

		if err := preparePinnedTerminalExecutableLocked(); err != nil {
			_ = childFile.Close()
			_ = conn.Close()

			return nil, errGhosttyHelperStart
		}
	}

	var pinned *ghosttyPinnedImage
	if globalPinLocked {
		pinned = ghosttyPinnedExecutable
	}

	closePinned := false
	customCleanupPending := false

	if config.executable != "" {
		pinned, err = pinGhosttyExecutable(config.executable)
		if err != nil {
			_ = childFile.Close()
			_ = conn.Close()

			return nil, errGhosttyHelperStart
		}

		closePinned = true

		customCleanupPending = true
		defer func() {
			if customCleanupPending {
				_ = pinned.cleanup()
			}
		}()
	}

	if pinned == nil || pinned.file == nil || pinned.prepare == nil {
		_ = childFile.Close()
		_ = conn.Close()

		return nil, errGhosttyHelperStart
	}

	if err := pinned.prepare(); err != nil {
		_ = childFile.Close()
		_ = conn.Close()

		return nil, errGhosttyHelperStart
	}

	if config.onExecutablePinned != nil {
		config.onExecutablePinned()
	}

	if pinned.path == "" || pinned.validate == nil {
		_ = childFile.Close()
		_ = conn.Close()

		return nil, errGhosttyHelperStart
	}

	if err := pinned.validate(); err != nil {
		_ = childFile.Close()
		_ = conn.Close()

		return nil, errGhosttyHelperStart
	}

	cmd := exec.Command(pinned.path, ghosttyHelperArg)
	cmd.Env = ghosttyChildEnvironment()
	cmd.ExtraFiles = []*os.File{pinned.file, childFile}
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = childFile.Close()
		_ = conn.Close()

		return nil, errGhosttyHelperStart
	}

	if globalPinLocked {
		ghosttyPinnedMu.Unlock()

		globalPinLocked = false
	}

	if config.onStart != nil {
		config.onStart(cmd)
	}

	_ = childFile.Close()

	helperStartTime, startErr := ProcessStartTime(cmd.Process.Pid)
	if startErr != nil || helperStartTime == 0 {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()

		return nil, errGhosttyHelperStart
	}

	helperIdentity := HelperProcessIdentity{PID: cmd.Process.Pid, StartTime: helperStartTime}
	ghosttyHelpers.finish(helperIdentity)

	generationActive = false

	terminal := &ghosttyProcessTerminal{
		cmd:      cmd,
		conn:     conn,
		waitDone: make(chan error, 1),
		limiter:  config.limiter,

		rpcTimeout:      ghosttyRPCTimeout,
		shutdownTimeout: ghosttyShutdownTimeout,
		reapTimeout:     ghosttyReapTimeout,

		cols:  cols,
		rows:  rows,
		dirty: true,
	}
	slotHeld = false

	go func() {
		waitErr := cmd.Wait()
		terminal.peakRSS.Store(extractPeakRSS(cmd.ProcessState))
		ghosttyHelpers.remove(helperIdentity)
		terminal.releaseSlot()

		terminal.waitDone <- waitErr

		close(terminal.waitDone)
	}()

	if _, err := terminal.exchange(ghosttyOpCreate, ghosttyCreatePayload(cols16, rows16, cols, config.historyRows)); err != nil {
		terminal.stop(true)

		return nil, fmt.Errorf("initialize libghostty helper: %w", err)
	}

	if closePinned {
		_ = pinned.cleanup()
		customCleanupPending = false
	}

	return terminal, nil
}

func ghosttyCreatePayload(cols16, rows16 uint16, cols, historyRows int) []byte {
	payloadLen := 4
	if historyRows > 0 {
		payloadLen = 6
	}

	payload := make([]byte, payloadLen)
	binary.BigEndian.PutUint16(payload[0:2], cols16)
	binary.BigEndian.PutUint16(payload[2:4], rows16)

	if historyRows > 0 {
		binary.BigEndian.PutUint16(payload[4:6], uint16(resolveTerminalHistoryRows(historyRows, cols))) //nolint:gosec // G115: resolver clamps below uint16.
	}

	return payload
}

func ghosttySocketpair() ([2]int, error) {
	// Darwin has no SOCK_CLOEXEC flag. Pair socket creation and FD_CLOEXEC
	// under the same fork lock used by os/exec so a concurrent process launch
	// cannot inherit either private endpoint in the small setup window.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return [2]int{}, err
	}

	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])

	return fds, nil
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
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	if len(p) == 0 {
		return 0, gt.currentError()
	}

	if len(p) > ghosttyMaxRequestBytes {
		return 0, errGhosttyHelperLimit
	}

	reply, err := gt.exchangeLocked(ghosttyOpWrite, p)
	if err != nil {
		return 0, err
	}

	gt.pendingPtyReplies = append(gt.pendingPtyReplies, reply...)
	gt.dirty = true
	gt.cache = TerminalSnapshot{}

	return len(p), nil
}

func (gt *ghosttyProcessTerminal) DrainPtyReplies() []byte {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	out := append([]byte(nil), gt.pendingPtyReplies...)
	gt.pendingPtyReplies = nil

	return out
}

func (gt *ghosttyProcessTerminal) Resize(cols, rows int) error {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	cols, rows, cols16, rows16, err := validateGhosttySize16(cols, rows)
	if err != nil {
		return err
	}

	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], cols16)
	binary.BigEndian.PutUint16(payload[2:4], rows16)

	if _, err := gt.exchangeLocked(ghosttyOpResize, payload); err != nil {
		return err
	}

	gt.cols = cols
	gt.rows = rows
	gt.dirty = true
	gt.cache = TerminalSnapshot{}

	return nil
}

func (gt *ghosttyProcessTerminal) Size() (int, int) {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	return gt.cols, gt.rows
}

func (gt *ghosttyProcessTerminal) Cursor() (int, int, bool) {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	snapshot, err := gt.snapshotLocked(false)
	if err != nil {
		return 0, 0, false
	}

	return snapshot.CursorX, snapshot.CursorY, snapshot.CursorVisible
}

func (gt *ghosttyProcessTerminal) Cell(x, y int) Cell {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	if x < 0 || x >= gt.cols || y < 0 || y >= gt.rows {
		return Cell{Content: " "}
	}

	snapshot, err := gt.snapshotLocked(false)
	if err != nil {
		return Cell{Content: " "}
	}

	return snapshot.Cells[y*snapshot.Cols+x]
}

func (gt *ghosttyProcessTerminal) Snapshot() (TerminalSnapshot, error) {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	return gt.snapshotLocked(false)
}

func (gt *ghosttyProcessTerminal) SnapshotWithHistory() (TerminalSnapshot, error) {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	return gt.snapshotLocked(true)
}

func (gt *ghosttyProcessTerminal) snapshotLocked(includeHistory bool) (TerminalSnapshot, error) {
	if err := gt.currentError(); err != nil {
		return TerminalSnapshot{}, err
	}

	if !includeHistory && !gt.dirty {
		return gt.cache, nil
	}

	var requestPayload []byte
	if includeHistory {
		requestPayload = ghosttySnapshotHistoryPayload
	}

	payload, err := gt.exchangeLocked(ghosttyOpSnapshot, requestPayload)
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
	gt.cache.History = TerminalHistory{}
	gt.dirty = false

	return snapshot, nil
}

func (gt *ghosttyProcessTerminal) Close() error {
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	gt.closeOnce.Do(func() {
		if gt.conn != nil && gt.fatalErr == nil {
			_, gt.closeErr = gt.exchangeLocked(ghosttyOpClose, nil)
		}

		gt.stop(false)
	})

	return gt.closeErr
}

func (gt *ghosttyProcessTerminal) PeakRSSBytes() int64 {
	return gt.peakRSS.Load()
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
	gt.opMu.Lock()
	defer gt.opMu.Unlock()

	return gt.exchangeLocked(op, payload)
}

func (gt *ghosttyProcessTerminal) exchangeLocked(op byte, payload []byte) ([]byte, error) {
	if err := gt.currentError(); err != nil {
		return nil, err
	}

	if err := validateGhosttyRequest(op, len(payload)); err != nil {
		return nil, err
	}

	if err := gt.conn.SetDeadline(time.Now().Add(gt.rpcDeadline())); err != nil {
		gt.fail(errGhosttyHelperIO)

		return nil, errGhosttyHelperIO
	}

	header := make([]byte, 12)
	copy(header[0:4], ghosttyRequestMagic[:])
	header[4] = ghosttyProtocolVersion
	header[5] = op
	binary.BigEndian.PutUint32(header[8:12], uint32(len(payload))) //nolint:gosec // G115: validateGhosttyRequest caps payload at 1 MiB.

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

	status := header[6]
	if err := validateGhosttyReply(op, status, length, gt.cols, gt.rows); err != nil {
		gt.fail(errGhosttyHelperProtocol)

		return nil, errGhosttyHelperProtocol
	}

	payload = make([]byte, length)
	if _, err := io.ReadFull(gt.conn, payload); err != nil {
		return nil, gt.exchangeIOError(err)
	}

	_ = gt.conn.SetDeadline(time.Time{})

	switch status {
	case ghosttyStatusOK:
		return payload, nil
	case ghosttyStatusNative:
		gt.fail(errGhosttyHelperNative)

		return nil, errGhosttyHelperNative
	default:
		// The parent only emits valid operations, so Invalid and Protocol are
		// evidence that the two sides disagree. Poison the connection rather
		// than continuing on a potentially desynchronized native state.
		gt.fail(errGhosttyHelperProtocol)

		return nil, errGhosttyHelperProtocol
	}
}

func (gt *ghosttyProcessTerminal) rpcDeadline() time.Duration {
	if gt.rpcTimeout > 0 {
		return gt.rpcTimeout
	}

	return ghosttyRPCTimeout
}

func (gt *ghosttyProcessTerminal) exchangeIOError(err error) error {
	result := errGhosttyHelperIO

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		result = errGhosttyHelperTimeout
	}

	gt.fail(result)

	return result
}

func (gt *ghosttyProcessTerminal) fail(err error) {
	if gt.fatalErr == nil {
		gt.fatalErr = err
	}

	gt.stop(true)
}

func (gt *ghosttyProcessTerminal) stop(force bool) {
	gt.stopOnce.Do(func() {
		if gt.conn != nil {
			_ = gt.conn.Close()
			gt.conn = nil
		}

		if force && gt.cmd != nil && gt.cmd.Process != nil {
			_ = gt.cmd.Process.Kill()
		}

		reaped := gt.waitDone == nil
		if gt.waitDone != nil {
			reaped = gt.waitForExit(gt.shutdownDeadline())
			if !reaped {
				if gt.cmd != nil && gt.cmd.Process != nil {
					_ = gt.cmd.Process.Kill()
				}

				reaped = gt.waitForExit(gt.reapDeadline())
			}
		}

		gt.cache = TerminalSnapshot{}
		if reaped {
			gt.releaseSlot()
		}
	})
}

func (gt *ghosttyProcessTerminal) releaseSlot() {
	gt.slotOnce.Do(func() {
		if gt.limiter != nil {
			gt.limiter.release()
		}
	})
}

func (gt *ghosttyProcessTerminal) waitForExit(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-gt.waitDone:
		return true
	case <-timer.C:
		return false
	}
}

func (gt *ghosttyProcessTerminal) shutdownDeadline() time.Duration {
	if gt.shutdownTimeout > 0 {
		return gt.shutdownTimeout
	}

	return ghosttyShutdownTimeout
}

func (gt *ghosttyProcessTerminal) reapDeadline() time.Duration {
	if gt.reapTimeout > 0 {
		return gt.reapTimeout
	}

	return ghosttyReapTimeout
}

func validateGhosttyRequest(op byte, length int) error {
	if length < 0 || length > ghosttyMaxRequestBytes {
		return errGhosttyHelperLimit
	}

	switch op {
	case ghosttyOpCreate:
		if length != 4 && length != 6 {
			return errGhosttyHelperProtocol
		}
	case ghosttyOpResize:
		if length != 4 {
			return errGhosttyHelperProtocol
		}
	case ghosttyOpWrite:
		// Zero-length writes are harmless and keep the wire grammar simple.
	case ghosttyOpSnapshot:
		if length != 0 && length != 1 {
			return errGhosttyHelperProtocol
		}
	case ghosttyOpClose, ghosttyOpPinProbe:
		if length != 0 {
			return errGhosttyHelperProtocol
		}
	default:
		return errGhosttyHelperProtocol
	}

	return nil
}

func validateGhosttyReply(op, status byte, length, cols, rows int) error {
	if status > ghosttyStatusProtocol || length < 0 || length > ghosttyMaxReplyBytes {
		return errGhosttyHelperProtocol
	}

	if status != ghosttyStatusOK {
		if length != 0 {
			return errGhosttyHelperProtocol
		}

		return nil
	}

	switch op {
	case ghosttyOpSnapshot:
		if cols < 1 || rows < 1 || cols > maxGhosttyCells/rows {
			return errGhosttyHelperProtocol
		}

		cells := cols * rows
		minimum := 13 + cells*16

		if length < minimum {
			return errGhosttyHelperProtocol
		}
	case ghosttyOpPinProbe:
		if length != 1 {
			return errGhosttyHelperProtocol
		}
	case ghosttyOpWrite:
		if length > ghosttyMaxWriteReplyBytes {
			return errGhosttyHelperProtocol
		}
	case ghosttyOpCreate, ghosttyOpResize, ghosttyOpClose:
		if length != 0 {
			return errGhosttyHelperProtocol
		}
	default:
		return errGhosttyHelperProtocol
	}

	return nil
}

func serveGhosttyHelperFD() error {
	if err := hardenGhosttyHelperResources(); err != nil {
		return errGhosttyHelperLimit
	}

	file := os.NewFile(ghosttyHelperFD, "libghostty-helper")
	if file == nil {
		return errGhosttyHelperProtocol
	}

	conn, err := net.FileConn(file)
	_ = file.Close()

	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	return serveGhosttyHelper(conn)
}

func serveGhosttyHelper(conn net.Conn) error {
	var terminal, historyTerminal *ghosttyTerminal
	defer func() {
		if terminal != nil {
			_ = terminal.Close()
		}

		if historyTerminal != nil {
			_ = historyTerminal.Close()
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
			if terminal != nil || (len(payload) != 4 && len(payload) != 6) {
				status = ghosttyStatusInvalid
				break
			}

			cols := int(binary.BigEndian.Uint16(payload[0:2]))
			rows := int(binary.BigEndian.Uint16(payload[2:4]))

			historyRows := 0
			if len(payload) == 6 {
				historyRows = int(binary.BigEndian.Uint16(payload[4:6]))
			}

			terminal, err = newGhosttyTerminal(cols, rows)
			if err == nil && historyRows > 0 {
				historyTerminal, _ = newGhosttyTerminalWithHistory(cols, rows, historyRows)
			}

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
			} else {
				reply = terminal.DrainPtyReplies()

				if historyTerminal != nil {
					if _, historyErr := historyTerminal.Write(payload); historyErr != nil {
						_ = historyTerminal.Close()
						historyTerminal = nil
					} else {
						_ = historyTerminal.DrainPtyReplies()
					}
				}
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
			} else if historyTerminal != nil {
				historyRows := resolveTerminalHistoryRows(historyTerminal.historyRows, cols)
				if historyRows != historyTerminal.historyRows {
					_ = historyTerminal.Close()
					historyTerminal, _ = newGhosttyTerminalWithHistory(cols, rows, historyRows)
				} else if historyErr := historyTerminal.Resize(cols, rows); historyErr != nil {
					_ = historyTerminal.Close()
					historyTerminal = nil
				}
			}
		case ghosttyOpSnapshot:
			if terminal == nil || len(payload) > 1 {
				status = ghosttyStatusInvalid
				break
			}

			includeHistory := false

			if len(payload) == 1 {
				if payload[0] != ghosttySnapshotRequestHistory {
					status = ghosttyStatusInvalid
					break
				}

				includeHistory = true
			}

			snapshot, snapshotErr := terminal.Snapshot()
			if snapshotErr == nil && includeHistory && historyTerminal != nil {
				snapshot.History, snapshotErr = historyTerminal.HistorySnapshot(historyTerminal.historyRows)
			}

			if snapshotErr != nil {
				status = ghosttyStatusNative
			} else {
				reply, err = encodeGhosttySnapshotForReply(snapshot)
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

			if historyTerminal != nil {
				_ = historyTerminal.Close()
				historyTerminal = nil
			}

			if err != nil {
				status = ghosttyStatusNative
			}
		case ghosttyOpPinProbe:
			if len(payload) != 0 {
				status = ghosttyStatusInvalid
				break
			}

			reply = []byte{0}
			if ghosttyPinnedFDClosed {
				reply[0] = 1
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

func hardenGhosttyHelperResources() error {
	// Core dumps can retain terminal contents and native heap state. Disable
	// them before constructing any terminal, and irreversibly cap descriptors
	// because this helper needs only stdio plus its private socket.
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return err
	}

	var files unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &files); err != nil {
		return err
	}

	limit := files.Cur
	if uint64(ghosttyHelperFDLimit) < limit {
		limit = ghosttyHelperFDLimit
	}

	if files.Max < limit {
		limit = files.Max
	}

	return unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: limit, Max: limit})
}

func readGhosttyRequest(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, errGhosttyHelperProtocol
	}

	if !bytes.Equal(header[0:4], ghosttyRequestMagic[:]) ||
		header[4] != ghosttyProtocolVersion || header[6] != 0 || header[7] != 0 {
		return 0, nil, errGhosttyHelperProtocol
	}

	length := int(binary.BigEndian.Uint32(header[8:12]))
	if err := validateGhosttyRequest(header[5], length); err != nil {
		return 0, nil, err
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, errGhosttyHelperProtocol
	}

	return header[5], payload, nil
}

func writeGhosttyReply(w io.Writer, op, status byte, payload []byte) error {
	if status > ghosttyStatusProtocol || len(payload) > ghosttyMaxReplyBytes ||
		(status != ghosttyStatusOK && len(payload) != 0) {
		return errGhosttyHelperProtocol
	}

	if status == ghosttyStatusOK {
		switch op {
		case ghosttyOpSnapshot:
			if len(payload) == 0 {
				return errGhosttyHelperProtocol
			}
		case ghosttyOpPinProbe:
			if len(payload) != 1 {
				return errGhosttyHelperProtocol
			}
		case ghosttyOpWrite:
			if len(payload) > ghosttyMaxWriteReplyBytes {
				return errGhosttyHelperProtocol
			}
		default:
			if len(payload) != 0 {
				return errGhosttyHelperProtocol
			}
		}
	}

	header := make([]byte, 12)
	copy(header[0:4], ghosttyReplyMagic[:])
	header[4] = ghosttyProtocolVersion
	header[5] = op
	header[6] = status
	binary.BigEndian.PutUint32(header[8:12], uint32(len(payload))) //nolint:gosec // G115: payload length is capped by ghosttyMaxReplyBytes above.

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

func encodeGhosttySnapshotForReply(snapshot TerminalSnapshot) ([]byte, error) {
	trimmed, err := trimGhosttySnapshotHistoryForReply(snapshot)
	if err != nil {
		return nil, err
	}

	return encodeGhosttySnapshot(trimmed)
}

func trimGhosttySnapshotHistoryForReply(snapshot TerminalSnapshot) (TerminalSnapshot, error) {
	history, present, err := validatedGhosttySnapshotHistory(snapshot.History)
	if err != nil || !present {
		return snapshot, err
	}

	base := snapshot
	base.History = TerminalHistory{}

	basePayload, err := encodeGhosttySnapshot(base)
	if err != nil {
		return snapshot, err
	}

	if len(basePayload) > ghosttyMaxReplyBytes-14 {
		snapshot.History = TerminalHistory{}

		return snapshot, nil
	}

	budget := ghosttyMaxReplyBytes - len(basePayload)

	historyBytes := 14
	for _, line := range history.Lines {
		historyBytes += 7 + len(line.Frame)
	}

	for historyBytes > budget && len(history.Lines) > 0 {
		historyBytes -= 7 + len(history.Lines[0].Frame)
		history.Lines = history.Lines[1:]
		history.Truncated = true
	}

	if historyBytes > budget {
		snapshot.History = TerminalHistory{}

		return snapshot, nil
	}

	snapshot.History = history

	return snapshot, nil
}

func encodeGhosttySnapshot(snapshot TerminalSnapshot) ([]byte, error) {
	if snapshot.Cols < 1 || snapshot.Rows < 1 ||
		snapshot.Cols > int(^uint16(0)) || snapshot.Rows > int(^uint16(0)) ||
		snapshot.CursorX < 0 || snapshot.CursorX >= snapshot.Cols ||
		snapshot.CursorY < 0 || snapshot.CursorY >= snapshot.Rows ||
		len(snapshot.Cells) != snapshot.Cols*snapshot.Rows || len(snapshot.Cells) > maxGhosttyCells {
		return nil, errGhosttyHelperProtocol
	}

	total := 13 + ghosttySnapshotInputModeBytes
	for _, cell := range snapshot.Cells {
		if len(cell.Content) > ghosttyMaxCellContentBytes || !utf8.ValidString(cell.Content) ||
			!validGhosttyColor(cell.Style.FG) || !validGhosttyColor(cell.Style.BG) ||
			total > ghosttyMaxReplyBytes-16-len(cell.Content) {
			return nil, errGhosttyHelperProtocol
		}

		total += 16 + len(cell.Content)
	}

	history, historyPresent, err := validatedGhosttySnapshotHistory(snapshot.History)
	if err != nil {
		return nil, err
	}

	if historyPresent {
		if total > ghosttyMaxReplyBytes-14 {
			return nil, errGhosttyHelperProtocol
		}

		total += 14
		for _, line := range history.Lines {
			if total > ghosttyMaxReplyBytes-7-len(line.Frame) {
				return nil, errGhosttyHelperProtocol
			}

			total += 7 + len(line.Frame)
		}
	}

	var buf bytes.Buffer
	buf.Grow(total)

	fixed := make([]byte, 13)
	binary.BigEndian.PutUint16(fixed[0:2], uint16(snapshot.Cols))
	binary.BigEndian.PutUint16(fixed[2:4], uint16(snapshot.Rows))
	binary.BigEndian.PutUint16(fixed[4:6], uint16(snapshot.CursorX)) //nolint:gosec // G115: cursor bounds are validated above.
	binary.BigEndian.PutUint16(fixed[6:8], uint16(snapshot.CursorY)) //nolint:gosec // G115: cursor bounds are validated above.

	if snapshot.CursorVisible {
		fixed[8] = 1
	}

	binary.BigEndian.PutUint32(fixed[9:13], uint32(len(snapshot.Cells))) //nolint:gosec // G115: maxGhosttyCells caps cell count well below uint32.
	_, _ = buf.Write(fixed)

	for _, cell := range snapshot.Cells {
		record := make([]byte, 16)
		binary.BigEndian.PutUint32(record[0:4], uint32(len(cell.Content))) //nolint:gosec // G115: cell content is capped by ghosttyMaxCellContentBytes above.
		record[4] = byte(cell.Style.FG.Kind)
		record[5] = byte(cell.Style.BG.Kind)
		binary.BigEndian.PutUint16(record[6:8], encodeGhosttyStyleFlags(cell.Style))
		binary.BigEndian.PutUint32(record[8:12], cell.Style.FG.Value)
		binary.BigEndian.PutUint32(record[12:16], cell.Style.BG.Value)
		_, _ = buf.Write(record)
		_, _ = buf.WriteString(cell.Content)
	}

	modeBits := make([]byte, ghosttySnapshotInputModeBytes)
	binary.BigEndian.PutUint32(modeBits, encodeGhosttySnapshotInputModes(snapshot.InputModes))
	_, _ = buf.Write(modeBits)

	if historyPresent {
		_, _ = buf.Write(ghosttySnapshotHistoryMagic[:])

		fixed := make([]byte, 10)

		fixed[0] = encodeGhosttyActiveScreen(history.ActiveScreen)
		if history.Truncated {
			fixed[1] = 1
		}

		binary.BigEndian.PutUint32(fixed[2:6], uint32(history.MaxLines))    //nolint:gosec // G115: history validation caps the value.
		binary.BigEndian.PutUint32(fixed[6:10], uint32(len(history.Lines))) //nolint:gosec // G115: history validation caps the value.
		_, _ = buf.Write(fixed)

		for _, line := range history.Lines {
			record := make([]byte, 7)
			binary.BigEndian.PutUint16(record[0:2], uint16(line.Width)) //nolint:gosec // G115: history validation caps the value.

			if line.Wrapped {
				record[2] |= 1
			}

			if line.WrapContinuation {
				record[2] |= 2
			}

			binary.BigEndian.PutUint32(record[3:7], uint32(len(line.Frame))) //nolint:gosec // G115: frame size is bounded by reply cap.
			_, _ = buf.Write(record)
			_, _ = buf.WriteString(line.Frame)
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
		count > maxGhosttyCells || count > (len(payload)-13)/16 ||
		snapshot.CursorX >= snapshot.Cols || snapshot.CursorY >= snapshot.Rows {
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
		fg := Color{Kind: fgKind, Value: binary.BigEndian.Uint32(payload[8:12])}

		bg := Color{Kind: bgKind, Value: binary.BigEndian.Uint32(payload[12:16])}
		if flags&^uint16(0x7f) != 0 || contentLen > ghosttyMaxCellContentBytes ||
			contentLen > len(payload)-16 || !utf8.Valid(payload[16:16+contentLen]) {
			return TerminalSnapshot{}, errGhosttyHelperProtocol
		}

		if !validGhosttyColor(fg) || !validGhosttyColor(bg) {
			return TerminalSnapshot{}, errGhosttyHelperProtocol
		}

		snapshot.Cells[i] = Cell{
			Content: string(payload[16 : 16+contentLen]),
			Style: CellStyle{
				FG: fg,
				BG: bg,
			},
		}
		decodeGhosttyStyleFlags(&snapshot.Cells[i].Style, flags)

		payload = payload[16+contentLen:]
	}

	switch len(payload) {
	case 0:
		snapshot.InputModes = TerminalInputModes{
			MouseTracking: TerminalMouseTrackingNone,
			MouseFormat:   TerminalMouseFormatX10,
		}
	default:
		if len(payload) < ghosttySnapshotInputModeBytes {
			return TerminalSnapshot{}, errGhosttyHelperProtocol
		}

		snapshot.InputModes = decodeGhosttySnapshotInputModes(binary.BigEndian.Uint32(payload[:ghosttySnapshotInputModeBytes]))
		payload = payload[ghosttySnapshotInputModeBytes:]
	}

	if len(payload) == 0 {
		return snapshot, nil
	}

	if len(payload) < 14 || !bytes.Equal(payload[:4], ghosttySnapshotHistoryMagic[:]) {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}

	activeScreen, ok := decodeGhosttyActiveScreen(payload[4])
	if !ok || payload[5] > 1 {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}

	truncated := payload[5] == 1
	maxLines := int(binary.BigEndian.Uint32(payload[6:10]))

	historyCount := int(binary.BigEndian.Uint32(payload[10:14]))
	if maxLines > maxTerminalHistoryRows ||
		historyCount > maxTerminalHistoryRows ||
		(maxLines > 0 && historyCount > maxLines) ||
		historyCount > (len(payload)-14)/7 {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}

	payload = payload[14:]

	history := TerminalHistory{
		Lines:        make([]TerminalHistoryLine, historyCount),
		MaxLines:     maxLines,
		Truncated:    truncated,
		ActiveScreen: activeScreen,
	}

	for i := range history.Lines {
		if len(payload) < 7 {
			return TerminalSnapshot{}, errGhosttyHelperProtocol
		}

		width := int(binary.BigEndian.Uint16(payload[0:2]))
		flags := payload[2]

		frameLen := int(binary.BigEndian.Uint32(payload[3:7]))
		if flags&^byte(0x03) != 0 || frameLen > len(payload)-7 ||
			frameLen > ghosttyMaxReplyBytes || !utf8.Valid(payload[7:7+frameLen]) {
			return TerminalSnapshot{}, errGhosttyHelperProtocol
		}

		history.Lines[i] = TerminalHistoryLine{
			Frame:            string(payload[7 : 7+frameLen]),
			Width:            width,
			Wrapped:          flags&1 != 0,
			WrapContinuation: flags&2 != 0,
		}

		payload = payload[7+frameLen:]
	}

	if len(payload) != 0 {
		return TerminalSnapshot{}, errGhosttyHelperProtocol
	}

	snapshot.History = history

	return snapshot, nil
}

const (
	ghosttySnapshotInputModeBytes = 4

	ghosttySnapshotMouseTrackingMask = 0x7
	ghosttySnapshotMouseFormatShift  = 3
	ghosttySnapshotMouseFormatMask   = 0x7 << ghosttySnapshotMouseFormatShift

	ghosttySnapshotFocusBit          = 1 << 6
	ghosttySnapshotPasteBit          = 1 << 7
	ghosttySnapshotKeyboardLockedBit = 1 << 8
	ghosttySnapshotCursorKeysBit     = 1 << 9
	ghosttySnapshotKeypadBit         = 1 << 10
	ghosttySnapshotAltScreenBit      = 1 << 11
	ghosttySnapshotAltScrollBit      = 1 << 12
)

func encodeGhosttySnapshotInputModes(m TerminalInputModes) uint32 {
	m = m.normalized()

	var bits uint32

	switch m.MouseTracking {
	case TerminalMouseTrackingX10:
		bits |= 1
	case TerminalMouseTrackingNormal:
		bits |= 2
	case TerminalMouseTrackingButton:
		bits |= 3
	case TerminalMouseTrackingAny:
		bits |= 4
	}

	var format uint32

	switch m.MouseFormat {
	case TerminalMouseFormatUTF8:
		format = 1
	case TerminalMouseFormatSGR:
		format = 2
	case TerminalMouseFormatURxvt:
		format = 3
	case TerminalMouseFormatSGRPixels:
		format = 4
	}

	bits |= format << ghosttySnapshotMouseFormatShift

	if m.Focus {
		bits |= ghosttySnapshotFocusBit
	}

	if m.BracketedPaste {
		bits |= ghosttySnapshotPasteBit
	}

	if m.KeyboardLocked {
		bits |= ghosttySnapshotKeyboardLockedBit
	}

	if m.ApplicationCursorKeys {
		bits |= ghosttySnapshotCursorKeysBit
	}

	if m.ApplicationKeypad {
		bits |= ghosttySnapshotKeypadBit
	}

	if m.AlternateScreen {
		bits |= ghosttySnapshotAltScreenBit
	}

	if m.AlternateScroll {
		bits |= ghosttySnapshotAltScrollBit
	}

	return bits
}

func decodeGhosttySnapshotInputModes(bits uint32) TerminalInputModes {
	modes := TerminalInputModes{
		MouseTracking: TerminalMouseTrackingNone,
		MouseFormat:   TerminalMouseFormatX10,
	}

	switch bits & ghosttySnapshotMouseTrackingMask {
	case 1:
		modes.MouseTracking = TerminalMouseTrackingX10
	case 2:
		modes.MouseTracking = TerminalMouseTrackingNormal
	case 3:
		modes.MouseTracking = TerminalMouseTrackingButton
	case 4:
		modes.MouseTracking = TerminalMouseTrackingAny
	}

	switch (bits & ghosttySnapshotMouseFormatMask) >> ghosttySnapshotMouseFormatShift {
	case 1:
		modes.MouseFormat = TerminalMouseFormatUTF8
	case 2:
		modes.MouseFormat = TerminalMouseFormatSGR
	case 3:
		modes.MouseFormat = TerminalMouseFormatURxvt
	case 4:
		modes.MouseFormat = TerminalMouseFormatSGRPixels
	}

	modes.Focus = bits&ghosttySnapshotFocusBit != 0
	modes.BracketedPaste = bits&ghosttySnapshotPasteBit != 0
	modes.KeyboardLocked = bits&ghosttySnapshotKeyboardLockedBit != 0
	modes.ApplicationCursorKeys = bits&ghosttySnapshotCursorKeysBit != 0
	modes.ApplicationKeypad = bits&ghosttySnapshotKeypadBit != 0
	modes.AlternateScreen = bits&ghosttySnapshotAltScreenBit != 0
	modes.AlternateScroll = bits&ghosttySnapshotAltScrollBit != 0

	return modes
}

func validatedGhosttySnapshotHistory(history TerminalHistory) (TerminalHistory, bool, error) {
	present := history.ActiveScreen != "" || history.MaxLines != 0 || history.Truncated || len(history.Lines) != 0
	if !present {
		return TerminalHistory{}, false, nil
	}

	if history.ActiveScreen == "" {
		history.ActiveScreen = TerminalScreenPrimary
	}

	if encodeGhosttyActiveScreen(history.ActiveScreen) == 0 ||
		history.MaxLines < 0 || history.MaxLines > maxTerminalHistoryRows ||
		len(history.Lines) > maxTerminalHistoryRows ||
		(history.MaxLines > 0 && len(history.Lines) > history.MaxLines) {
		return TerminalHistory{}, false, errGhosttyHelperProtocol
	}

	for _, line := range history.Lines {
		if line.Width < 0 || line.Width > int(^uint16(0)) ||
			len(line.Frame) > ghosttyMaxReplyBytes || !utf8.ValidString(line.Frame) {
			return TerminalHistory{}, false, errGhosttyHelperProtocol
		}
	}

	return history, true, nil
}

func encodeGhosttyActiveScreen(screen string) byte {
	switch screen {
	case TerminalScreenPrimary:
		return 1
	case TerminalScreenAlternate:
		return 2
	default:
		return 0
	}
}

func decodeGhosttyActiveScreen(screen byte) (string, bool) {
	switch screen {
	case 1:
		return TerminalScreenPrimary, true
	case 2:
		return TerminalScreenAlternate, true
	default:
		return "", false
	}
}

func validGhosttyColor(color Color) bool {
	switch color.Kind {
	case ColorDefault:
		return color.Value == 0
	case ColorIndexed:
		return color.Value <= 255
	case ColorRGB:
		return color.Value <= 0xFFFFFF
	default:
		return false
	}
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
