package daemon

import (
	"io"
	"syscall"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
)

// SessionDriver is the transport-agnostic surface the daemon uses to drive a
// running agent process. Today the only implementation is *grpty.Session (an
// interactive PTY); the headless stream-json driver (issue #1075) will be a
// second implementation. The daemon holds sessions as SessionDriver values
// (sessions map, GetPTY) so a second driver can slot in without the lifecycle
// code caring which transport backs a session.
//
// This is deliberately the *full* current call surface of *grpty.Session, so
// introducing the interface is a pure, no-behaviour-change refactor. Some
// members (Fd, Resize, Poke, ScreenSnapshot, NotifyUserInput, WaitForUserIdle)
// are PTY-shaped; when the headless driver lands, the design splits these into
// an optional interactiveDriver so headless doesn't have to no-op them
// (see docs/design/2026-07-13-headless-stream-json-design.md). Keeping one
// interface here means zero call-site churn in this first slice.
type SessionDriver interface {
	// Lifecycle / identity.
	ProcessPID() int
	Fd() uintptr
	Done() <-chan struct{}
	Exited() bool
	ExitCode() int
	ExitSignal() syscall.Signal
	PeakRSSBytes() int64
	LastOutputAt() time.Time
	RecentlyAdopted(grace time.Duration) bool
	Kill() error
	ForceKill() error
	Close()

	// Input the daemon issues on the session's behalf.
	WriteInput(data []byte) error
	WriteInputAndSubmit(data []byte) error
	Interrupt(count int, delay time.Duration) error
	NotifyUserInput()
	WaitForUserIdle(idleTimeout, maxWait time.Duration) bool

	// PTY-shaped controls (no-ops or best-effort for non-PTY drivers).
	Resize(rows, cols uint16) error
	Poke()

	// Output surfaces: attach fan-out, scrollback, preview/snapshot.
	Attach(w io.Writer)
	Detach()
	DetachWriter(w io.Writer)
	ScreenPreview() string
	ScreenSnapshot() grpty.ScreenCapture
	ScrollbackFile() *grpty.Scrollback
}

// Compile-time assertion that the PTY session satisfies the driver interface.
var _ SessionDriver = (*grpty.Session)(nil)
