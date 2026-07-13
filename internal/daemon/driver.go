package daemon

import (
	"fmt"
	"io"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
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
type SessionDriver interface { //nolint:interfacebloat // deliberately the full current *pty.Session call surface, so introducing the interface is a no-behaviour-change refactor; the capability split lands with the headless-driver phase.
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

// Driver-kind identifiers persisted on SessionState.DriverKind (issue #1075).
const (
	DriverPTY      = "pty"
	DriverHeadless = "headless"
)

// resolveDriverKind decides a session's transport at creation (issue #1075).
// It never silently downgrades an *explicit* --headless request: if headless is
// asked for but can't be honoured, it returns an error. A headless preference
// coming only from [headless] default yields gracefully to the same
// constraints (returns DriverPTY, no error), because a global default is a soft
// preference, not a demand.
//
// v1 constraints: headless requires the experimental gate, an agent flagged
// headless_capable, and a non-sandboxed session (headless + sandbox is not
// supported yet — see the design doc).
func resolveDriverKind(explicit bool, agent config.Agent, hc config.HeadlessConfig, sandboxed bool) (string, error) {
	if !explicit && !hc.Default {
		return DriverPTY, nil
	}

	reject := func(reason string) (string, error) {
		if explicit {
			return "", fmt.Errorf("cannot create headless session: %s", reason)
		}

		return DriverPTY, nil
	}

	switch {
	case !hc.Experimental:
		return reject("[headless] experimental is not enabled")
	case !agent.HeadlessCapableEnabled():
		return reject("agent is not headless_capable")
	case sandboxed:
		return reject("headless is not supported with the sandbox in v1")
	default:
		return DriverHeadless, nil
	}
}

// headlessArgs builds the argv for a v1 one-shot headless launch:
//
//	claude -p <prompt> --output-format stream-json --verbose <agentArgs...>
//
// This is the empirically-validated one-shot form (positional prompt, no
// --input-format / control protocol): Claude runs the prompt to a terminal
// result and exits, emitting the typed event stream graith parses. agentArgs
// carries the agent's own template-expanded args (e.g. --session-id <id>).
func headlessArgs(agentArgs []string, prompt string) []string {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}

	return append(args, agentArgs...)
}

// Compile-time assertion that the PTY session satisfies the driver interface.
var _ SessionDriver = (*grpty.Session)(nil)
