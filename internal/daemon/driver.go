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
	Pgid() int
	Fd() uintptr
	Done() <-chan struct{}
	Exited() bool
	ExitCode() int
	ExitSignal() syscall.Signal
	PeakRSSBytes() int64
	LastOutputAt() time.Time
	RecentlyAdopted(grace time.Duration) bool
	BytesRead() int64
	WasAdopted() bool
	CreatedAt() time.Time
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

// inputDelaySetter is the optional capability a driver exposes when its
// type-then-submit pause honours the live [lifecycle] input_delay policy. Only
// the PTY driver implements it; a config reload updates every live PTY's delay
// through this interface (issue #1294) and skips drivers that don't (e.g. the
// headless stream-json driver, which has no interactive submit pause). Kept off
// the core SessionDriver surface so a non-PTY driver need not no-op it.
type inputDelaySetter interface {
	SetInputDelay(delay time.Duration)
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

// headlessArgs builds the argv for a v1 one-shot headless launch with the stdin
// control channel (issue #1136). The control-channel prefix comes from the
// agent's headless_args config (issue #1236); for Claude it is:
//
//	claude -p --output-format stream-json --input-format stream-json \
//	       --verbose --permission-prompt-tool stdio <agentArgs...>
//
// --input-format stream-json turns stdin into the message/control channel:
// graith delivers the prompt as an initial user message (not a positional arg),
// issues `interrupt` control requests, and answers inbound `can_use_tool`
// permission asks routed by --permission-prompt-tool stdio. The CLI still runs
// one turn to a terminal result; graith closes stdin on that result so the
// process exits (one-shot semantics preserved). agentArgs carries the agent's
// own template-expanded args (e.g. --session-id <id>) and follows the prefix.
// The Claude defaults are pinned to what was verified against claude 2.1.211
// (see the headless design doc).
func headlessArgs(agent config.Agent, vars config.TemplateVars, agentArgs []string) ([]string, error) {
	prefix, err := config.ExpandSlice(agent.HeadlessArgs, vars)
	if err != nil {
		return nil, err
	}

	return append(prefix, agentArgs...), nil
}

// Compile-time assertion that the PTY session satisfies the driver interface.
var _ SessionDriver = (*grpty.Session)(nil)
