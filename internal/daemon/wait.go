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

	if status, ok := sm.waitStatusMet(w); ok {
		sendControl("wait_matched", protocol.WaitMatchedMsg{Status: status})
		return false
	}

	sendControl("wait_watching", struct{}{})

	detachCh := readForDetach(reader)

	timeoutCh, stopTimeout := waitTimeoutChan(w.TimeoutMs)
	defer stopTimeout()

	// A slow backstop ticker re-checks authoritative state. The subscription
	// above is the primary, sub-second wake source (the daemon republishes
	// status transitions there); the ticker only covers transitions that emit
	// no event, so the wait can never wedge forever short of its timeout.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sub:
			if status, ok := sm.waitStatusMet(w); ok {
				sendControl("wait_matched", protocol.WaitMatchedMsg{Status: status})
				return true
			}
		case <-ticker.C:
			if status, ok := sm.waitStatusMet(w); ok {
				sendControl("wait_matched", protocol.WaitMatchedMsg{Status: status})
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
// wait, returning the observed status for the reply. The session is assumed to
// exist; a session that has vanished is treated as unmet.
func (sm *SessionManager) waitStatusMet(w protocol.WaitMsg) (string, bool) {
	sess, ok := sm.Get(w.SessionID)
	if !ok {
		return "", false
	}

	if w.Mode == "idle" {
		return sess.AgentStatus, isIdleAgentStatus(sess.AgentStatus)
	}

	return string(sess.Status), string(sess.Status) == w.Status
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

	// Catch a pattern already visible in scrollback before following.
	if tail, terr := ptySess.Scrollback.Tail(waitScrollbackLines); terr == nil {
		if line, matched := scanForMatch(re, tail); matched {
			sendControl("wait_matched", protocol.WaitMatchedMsg{MatchedLine: line})
			return false
		}
	}

	matchCh := make(chan string, 1)
	mw := &matchWriter{re: re, matchCh: matchCh}

	ptySess.Attach(mw)
	defer ptySess.DetachWriter(mw)

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
// matches re.
func scanForMatch(re *regexp.Regexp, data []byte) (string, bool) {
	for _, raw := range bytes.Split(data, []byte("\n")) {
		clean := ansi.Strip(strings.TrimRight(string(raw), "\r"))
		if clean != "" && re.MatchString(clean) {
			return clean, true
		}
	}

	return "", false
}

// matchWriter is an io.Writer attached to a live PTY that fires matchCh (once)
// with the first output line matching re. It tolerates ANSI escape sequences
// and matches the trailing partial line too, so a pattern that appears in a
// prompt (with no trailing newline) is still caught.
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
		if i < 0 {
			segment = m.buf[start:] // trailing partial line
		} else {
			segment = m.buf[start : start+i]
		}

		clean := ansi.Strip(strings.TrimRight(string(segment), "\r"))
		if clean != "" && m.re.MatchString(clean) {
			m.done = true

			select {
			case m.matchCh <- clean:
			default:
			}

			return len(p), nil
		}

		if i < 0 {
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
