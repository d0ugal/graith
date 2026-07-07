package daemon

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// waitScrollbackLines bounds how much existing scrollback is scanned for an
// already-present match before a contains wait starts following live output.
const waitScrollbackLines = 500

// handleWait blocks until the session named by w satisfies its condition, then
// sends one of "wait_matched", "wait_timeout", or "error". It returns true when
// it has taken ownership of the reader (a goroutine drains frames to detect
// client detach), matching the contract used by handleMsgStreamRead.
func (sm *SessionManager) handleWait(
	ctx context.Context,
	sendControl func(string, any),
	reader *protocol.FrameReader,
	w protocol.WaitMsg,
) bool {
	switch w.Mode {
	case "contains":
		return sm.handleWaitContains(ctx, sendControl, reader, w)
	case "status", "idle":
		return sm.handleWaitStatus(ctx, sendControl, reader, w)
	default:
		sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("invalid wait mode %q", w.Mode)})
		return false
	}
}

// timeoutChan returns a channel that fires after the message's timeout, or nil
// (blocks forever) when no timeout was requested. The stop func must be called
// to release the timer.
func waitTimeoutChan(timeoutMs int) (<-chan time.Time, func()) {
	if timeoutMs <= 0 {
		return nil, func() {}
	}

	t := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)

	return t.C, func() { t.Stop() }
}

// readForDetach drains control frames from the client while a wait blocks,
// closing detachCh when the client sends "detach" or disconnects. This lets a
// client cancel (e.g. on Ctrl-C) without leaving the daemon goroutine wedged.
func readForDetach(reader *protocol.FrameReader) <-chan struct{} {
	detachCh := make(chan struct{})

	go func() {
		for {
			f, err := reader.ReadFrame()
			if err != nil {
				close(detachCh)
				return
			}

			if f.Channel == protocol.ChannelControl {
				ctrl, _ := protocol.DecodeControl(f.Payload)
				if ctrl.Type == "detach" {
					close(detachCh)
					return
				}
			}
		}
	}()

	return detachCh
}

func (sm *SessionManager) handleWaitStatus(
	ctx context.Context,
	sendControl func(string, any),
	reader *protocol.FrameReader,
	w protocol.WaitMsg,
) bool {
	if _, ok := sm.Get(w.SessionID); !ok {
		sendControl("error", protocol.ErrorMsg{Message: "session not found"})
		return false
	}

	// Subscribe before the first check so no status change is missed in the
	// gap between checking and blocking.
	sub, unsub := sm.messages.Subscribe("_system.status")
	defer unsub()

	if status, matched, _ := sm.waitStatusMet(w); matched {
		sendControl("wait_matched", protocol.WaitMatchedMsg{Status: status})
		return false
	}

	sendControl("wait_watching", struct{}{})

	detachCh := readForDetach(reader)

	timeoutCh, stopTimeout := waitTimeoutChan(w.TimeoutMs)
	defer stopTimeout()

	// The subscription is the primary, event-driven wake source, but
	// _system.status does not carry every lifecycle transition (create/resume
	// to running and delete publish nothing), so a short backstop ticker
	// re-checks authoritative in-memory state. That bounds detection latency
	// well under a typical timeout instead of relying on an event that may
	// never come. The re-check is a cheap map read under the state lock and
	// only runs while a wait is pending.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	check := func() (done bool) {
		status, matched, exists := sm.waitStatusMet(w)

		switch {
		case matched:
			sendControl("wait_matched", protocol.WaitMatchedMsg{Status: status})
		case !exists:
			// The session was deleted after we started waiting; the condition
			// can never be met, so fail rather than block until timeout.
			sendControl("error", protocol.ErrorMsg{Message: "session deleted before condition was met"})
		default:
			return false
		}

		return true
	}

	for {
		select {
		case <-sub:
			if check() {
				return true
			}
		case <-ticker.C:
			if check() {
				return true
			}
		case <-timeoutCh:
			sendControl("wait_timeout", struct{}{})
			return true
		case <-detachCh:
			return true
		case <-ctx.Done():
			return true
		}
	}
}

// waitStatusMet reports whether the session currently satisfies a status/idle
// wait. It returns the observed status (for the reply), whether the condition
// is met, and whether the session still exists — the caller distinguishes a
// deleted session from an unmet condition.
func (sm *SessionManager) waitStatusMet(w protocol.WaitMsg) (status string, matched, exists bool) {
	sess, ok := sm.Get(w.SessionID)
	if !ok {
		return "", false, false
	}

	if w.Mode == "idle" {
		return sess.AgentStatus, isIdleAgentStatus(sess.AgentStatus), true
	}

	return string(sess.Status), string(sess.Status) == w.Status, true
}

// isIdleAgentStatus reports whether an agent status counts as idle: the agent
// is at rest ("ready") or its state cannot be determined ("unknown"/unset),
// as opposed to actively working or awaiting approval.
func isIdleAgentStatus(status string) bool {
	switch status {
	case "ready", "unknown", "":
		return true
	default:
		return false
	}
}

func (sm *SessionManager) handleWaitContains(
	ctx context.Context,
	sendControl func(string, any),
	reader *protocol.FrameReader,
	w protocol.WaitMsg,
) bool {
	re, err := regexp.Compile(w.Pattern)
	if err != nil {
		sendControl("error", protocol.ErrorMsg{Message: fmt.Sprintf("invalid pattern: %v", err)})
		return false
	}

	ptySess, ok := sm.GetPTY(w.SessionID)
	if !ok {
		// No live PTY: the session cannot produce new output. Scan any
		// on-disk scrollback for an already-present match, otherwise report
		// that there is nothing to wait on.
		sess, exists := sm.Get(w.SessionID)
		if !exists {
			sendControl("error", protocol.ErrorMsg{Message: "session not found"})
			return false
		}

		if tail, terr := grpty.TailFile(sm.scrollbackLogPath(w.SessionID), waitScrollbackLines); terr == nil {
			if line, matched := scanForMatch(re, tail); matched {
				sendControl("wait_matched", protocol.WaitMatchedMsg{MatchedLine: line})
				return false
			}
		}

		sendControl("error", protocol.ErrorMsg{
			Message: fmt.Sprintf("session %s is not running (%s); no live output to wait on", sessionLabel(sess), describeSessionExit(sess)),
		})

		return false
	}

	matchCh := make(chan string, 1)
	mw := &matchWriter{re: re, matchCh: matchCh}

	// Attach the live matcher BEFORE scanning existing scrollback. Output
	// produced in the gap between a scrollback snapshot and attaching is
	// written to scrollback but not to a not-yet-attached writer, so scanning
	// first would silently lose a match landing in that window. Attaching
	// first means such output reaches matchWriter; the subsequent scrollback
	// scan then covers anything already on screen. matchWriter fires once, so
	// the overlap is harmless.
	ptySess.Attach(mw)
	defer ptySess.DetachWriter(mw)

	// Catch a pattern already visible in scrollback before following.
	if tail, terr := ptySess.Scrollback.Tail(waitScrollbackLines); terr == nil {
		if line, matched := scanForMatch(re, tail); matched {
			sendControl("wait_matched", protocol.WaitMatchedMsg{MatchedLine: line})
			return false
		}
	}

	sendControl("wait_following", struct{}{})

	detachCh := readForDetach(reader)

	timeoutCh, stopTimeout := waitTimeoutChan(w.TimeoutMs)
	defer stopTimeout()

	select {
	case line := <-matchCh:
		sendControl("wait_matched", protocol.WaitMatchedMsg{MatchedLine: line})
	case <-timeoutCh:
		sendControl("wait_timeout", struct{}{})
	case <-ptySess.Done():
		sendControl("error", protocol.ErrorMsg{Message: "session ended before the pattern matched"})
	case <-detachCh:
	case <-ctx.Done():
	}

	return true
}

// scanForMatch reports the first line in data (ANSI stripped, CR trimmed) that
// matches re. Blank lines are matched (so patterns like `^$` work), but the
// empty final element that bytes.Split yields for data ending in a newline is
// skipped so it cannot spuriously satisfy a zero-width pattern.
func scanForMatch(re *regexp.Regexp, data []byte) (string, bool) {
	lines := bytes.Split(data, []byte("\n"))
	for i, raw := range lines {
		if i == len(lines)-1 && len(raw) == 0 {
			continue
		}

		clean := ansi.Strip(strings.TrimRight(string(raw), "\r"))
		if re.MatchString(clean) {
			return clean, true
		}
	}

	return "", false
}

// matchWriter is an io.Writer attached to a live PTY that fires matchCh (once)
// with the first output line matching re. It tolerates ANSI escape sequences
// and matches the trailing partial line too, so a pattern that appears in a
// prompt (with no trailing newline) is still caught.
//
// Write runs synchronously on the PTY read loop (pty.Session.readLoop fans each
// chunk out to attached writers inline), so matching shares that goroutine. Go's
// regexp is RE2 (linear time), so a pathological pattern cannot hang it; the
// per-write cost is bounded by the 64 KiB partial-line cap below.
type matchWriter struct {
	re      *regexp.Regexp
	matchCh chan string

	mu   sync.Mutex
	buf  []byte
	done bool
}

// matchWriterMaxBuf bounds the retained partial line so a long stream without a
// newline cannot grow the buffer without limit.
const matchWriterMaxBuf = 64 * 1024

func (m *matchWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.done {
		return len(p), nil
	}

	m.buf = append(m.buf, p...)

	start := 0

	for {
		var segment []byte

		i := bytes.IndexByte(m.buf[start:], '\n')
		trailing := i < 0

		if trailing {
			segment = m.buf[start:] // trailing partial line
		} else {
			segment = m.buf[start : start+i]
		}

		clean := ansi.Strip(strings.TrimRight(string(segment), "\r"))

		// Match completed lines even when blank (so `^$` works), but skip an
		// empty trailing partial so a zero-width/`.*` pattern can't fire on the
		// empty tail that sits between writes.
		if (!trailing || clean != "") && m.re.MatchString(clean) {
			m.done = true

			select {
			case m.matchCh <- clean:
			default:
			}

			return len(p), nil
		}

		if trailing {
			break
		}

		start += i + 1
	}

	// Drop the consumed complete lines, keeping the trailing partial.
	if start > 0 {
		m.buf = m.buf[start:]
	}

	if len(m.buf) > matchWriterMaxBuf {
		m.buf = m.buf[len(m.buf)-matchWriterMaxBuf:]
	}

	return len(p), nil
}
