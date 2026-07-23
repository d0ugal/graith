package daemon

import (
	"fmt"
	"io"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/headless"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// sessionLifecycle is the daemon's consumer-owned lifecycle contract. It does
// not describe how a driver transports input or renders output, and therefore
// can be implemented by both PTY and headless sessions without PTY-shaped
// methods.
type sessionLifecycle interface { //nolint:interfacebloat // lifecycle is the consumer-owned process contract.
	ProcessPID() int
	Pgid() int
	Done() <-chan struct{}
	Exited() bool
	ExitCode() int
	ExitSignal() syscall.Signal
	PeakRSSBytes() int64
	RecentlyAdopted(grace time.Duration) bool
	BytesRead() int64
	WasAdopted() bool
	CreatedAt() time.Time
	Kill() error
	ForceKill() error
	Close()
}

// sessionInput contains input operations issued by daemon lifecycle and
// control consumers.
type sessionInput interface {
	WriteInput(data []byte) error
	WriteInputAndSubmit(data []byte) error
	Interrupt(count int, delay time.Duration) error
}

// sessionOutput contains the shared output and scrollback surface. Scrollback
// is intentionally shared by PTY and headless drivers so logs and previews do
// not depend on the transport.
type sessionOutput interface {
	Attach(w io.Writer)
	Detach()
	DetachWriter(w io.Writer)
	ScreenPreview() string
	ScreenSnapshot() grpty.ScreenCapture
	ScrollbackFile() *grpty.Scrollback
	LastOutputAt() time.Time
}

// sessionDriver is the minimal contract needed by the daemon's common
// lifecycle, input, and output consumers.
type sessionDriver interface {
	sessionLifecycle
	sessionInput
	sessionOutput
}

// interactiveDriver is optional. Only a PTY-backed driver should implement it;
// callers must check for this capability before requesting terminal-specific
// behavior instead of treating a headless no-op as success.
type interactiveDriver interface {
	Fd() uintptr
	Resize(rows, cols uint16) error
	Poke()
	NotifyUserInput()
	WaitForUserIdle(idleTimeout, maxWait time.Duration) bool
}

// SessionDriver is the temporary compatibility contract for call sites that
// have not yet migrated to capability-specific interfaces. New consumers
// should depend on sessionDriver, sessionOutput, or interactiveDriver instead.
// Keeping this seam for one migration slice lets PTY and headless call sites be
// converted independently without changing runtime behavior.
type SessionDriver interface {
	sessionDriver
	interactiveDriver
}

// inputCapability returns the input surface without exposing the legacy
// aggregate contract to a consumer.
func inputCapability(driver any) (sessionInput, bool) {
	input, ok := driver.(sessionInput)

	return input, ok
}

// outputCapability returns the output surface without exposing the legacy
// aggregate contract to a consumer. It is intentionally a type assertion so a
// future driver can omit output when it only supports lifecycle operations.
func outputCapability(driver any) (sessionOutput, bool) {
	output, ok := driver.(sessionOutput)

	return output, ok
}

// interactiveCapability reports whether a driver supports terminal-only
// operations. Unsupported operations must be handled by the caller rather than
// represented as successful no-ops.
func interactiveCapability(driver any) (interactiveDriver, bool) {
	interactive, ok := driver.(interactiveDriver)

	return interactive, ok
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
// Headless requires the experimental gate and an explicitly capable agent. It
// uses the same optional Graith sandbox setting as PTY sessions.
func resolveDriverKind(explicit bool, agent config.Agent, hc config.HeadlessConfig, _ bool) (string, error) {
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
	default:
		return DriverHeadless, nil
	}
}

// headlessArgs builds the argv for a v1 one-shot headless launch with the stdin
// control channel (issue #1136). The control-channel prefix comes from the
// agent's headless_args config (issue #1236); for Claude it is:
//
//	claude -p --output-format stream-json --input-format stream-json \
//	       --verbose <agentArgs...>
//
// --input-format stream-json turns stdin into the message/control channel:
// graith delivers the prompt as an initial user message (not a positional arg),
// issues `interrupt` control requests. A bundled non_interactive_args prefix
// normally prevents native prompts; if one arrives, the headless driver must
// deny it because there is no TUI in which a human can respond. The CLI still
// runs one turn to a terminal result; graith closes stdin on that result so the
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

// Compile-time assertions keep provider capabilities explicit. Headless is a
// core driver but deliberately does not satisfy interactiveDriver.
var _ sessionDriver = (*headless.Session)(nil)
var _ SessionDriver = (*grpty.Session)(nil)
var _ interactiveDriver = (*grpty.Session)(nil)
