package headless

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
)

// maxLineBytes bounds a single stream-json line. A default bufio.Scanner caps
// tokens at 64KB; stream-json lines carrying large tool outputs or base64
// images exceed that, so we raise the limit (matching the transcript reader's
// 16MiB ceiling in internal/agent/transcript/claude.go).
const maxLineBytes = 16 * 1024 * 1024

// controlTimeout bounds how long a synchronous control request waits for its
// matching control_response before failing (never blocks forever).
const controlTimeout = 30 * time.Second

// Opts configures a headless session launch. It mirrors the fields
// pty.SessionOpts needs, plus the approval callback.
type Opts struct {
	ID         string
	Command    string
	Args       []string
	Dir        string
	Env        map[string]string
	LogPath    string
	MaxLogSize int64

	// OnPermission is invoked for each inbound can_use_tool request. If nil,
	// every tool request is denied (fail-closed — a headless session must not
	// block on a human that will never answer). The callback must not block
	// indefinitely; it runs on the read loop's goroutine via a worker.
	OnPermission func(PermissionRequest) PermissionDecision
}

// Session is a headless stream-json agent process. It satisfies the daemon's
// SessionDriver surface so the session manager can hold it interchangeably with
// a *pty.Session.
type Session struct {
	id         string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	scrollback *grpty.Scrollback

	onPermission func(PermissionRequest) PermissionDecision

	writeMu sync.Mutex // serialises all writes to stdin (NDJSON lines)

	mu           sync.RWMutex
	status       Status
	toolName     string
	result       *ResultEnvelope
	degraded     bool
	lastOutputAt time.Time
	writers      []io.Writer

	exitMu     sync.RWMutex
	exited     bool
	exitCode   int
	exitSignal syscall.Signal

	reqMu   sync.Mutex
	reqSeq  int
	pending map[string]chan json.RawMessage // request_id -> response body

	done     chan struct{}
	readDone chan struct{}
}

// New launches a headless session: starts the process with piped
// stdin/stdout/stderr, performs the initialize handshake, and starts the read
// loop. It returns an error if the process fails to start or initialize.
func New(opts Opts) (*Session, error) {
	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = buildEnv(opts.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	sb, err := grpty.NewScrollback(opts.LogPath, opts.MaxLogSize)
	if err != nil {
		return nil, fmt.Errorf("scrollback: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = sb.Close()

		return nil, fmt.Errorf("start headless process: %w", err)
	}

	s := &Session{
		id:           opts.ID,
		cmd:          cmd,
		stdin:        stdin,
		scrollback:   sb,
		onPermission: opts.OnPermission,
		status:       StatusActive,
		pending:      make(map[string]chan json.RawMessage),
		done:         make(chan struct{}),
		readDone:     make(chan struct{}),
	}

	go s.drainStderr(stderr)
	go s.readLoop(stdout)
	go s.waitLoop()

	return s, nil
}

// readLoop consumes the stream-json output line by line until stdout closes.
func (s *Session) readLoop(stdout io.Reader) {
	defer close(s.readDone)

	r := bufio.NewReaderSize(stdout, 64*1024)

	var (
		line []byte
		err  error
	)

	for {
		line, err = readLine(r, maxLineBytes)
		if len(line) > 0 {
			s.handleLine(line)
		}

		if err != nil {
			return
		}
	}
}

// handleLine decodes and dispatches a single stream-json line. A non-JSON line
// is written to scrollback verbatim (surfaces early crash banners); a malformed
// control frame marks the session degraded.
func (s *Session) handleLine(line []byte) {
	s.appendScrollback(line)
	s.touch()

	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return // non-JSON banner already written to scrollback verbatim
	}

	if st, ok := statusForEvent(ev); ok {
		s.setStatus(st, toolNameOf(ev))
	}

	switch ev.Type {
	case "result":
		s.setResult(ev)
	case "control_response":
		s.deliverResponse(ev.RequestID, ev.Response)
	case "control_request":
		if controlSubtypeOf(ev.Request) == "can_use_tool" {
			go s.handlePermission(ev)
		}
	}
}

// handlePermission answers an inbound can_use_tool request via the approval
// callback (fail-closed deny if none), writing a control_response on stdin.
func (s *Session) handlePermission(ev event) {
	decision := PermissionDecision{Allow: false, Reason: "headless: no approval backend"}
	if s.onPermission != nil {
		decision = s.onPermission(PermissionRequest{
			RequestID: ev.RequestID,
			ToolName:  controlToolName(ev.Request),
			Input:     ev.Request,
		})
	}

	behavior := "deny"
	if decision.Allow {
		behavior = "allow"
	}

	resp := map[string]any{
		"type":       "control_response",
		"request_id": ev.RequestID,
		"response": map[string]any{
			"subtype":  "can_use_tool",
			"behavior": behavior,
			"message":  decision.Reason,
		},
	}
	if err := s.writeJSON(resp); err != nil {
		s.markDegraded()
	}
}

// --- control protocol -------------------------------------------------------

// Interrupt sends an `interrupt` control request. The count/delay arguments
// exist for SessionDriver compatibility; headless needs neither (the control
// request is acknowledged), so it sends exactly one.
func (s *Session) Interrupt(_ int, _ time.Duration) error {
	_, err := s.control(controlSubtype{Subtype: "interrupt"})

	return err
}

// ContextUsage issues a get_context_usage control request and returns the raw
// response body.
func (s *Session) ContextUsage() (json.RawMessage, error) {
	return s.control(controlSubtype{Subtype: "get_context_usage"})
}

// control sends a control request and waits for its matching response.
func (s *Session) control(request any) (json.RawMessage, error) {
	if s.Exited() {
		return nil, fmt.Errorf("headless session has exited")
	}

	s.reqMu.Lock()
	s.reqSeq++
	id := "req-" + strconv.Itoa(s.reqSeq)
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.reqMu.Unlock()

	defer func() {
		s.reqMu.Lock()
		delete(s.pending, id)
		s.reqMu.Unlock()
	}()

	if err := s.writeJSON(controlRequest{Type: "control_request", RequestID: id, Request: request}); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-s.done:
		return nil, fmt.Errorf("headless session exited before control response")
	case <-time.After(controlTimeout):
		return nil, fmt.Errorf("timeout waiting for control response %q", id)
	}
}

// deliverResponse routes a control_response to the waiter registered for its id.
func (s *Session) deliverResponse(id string, body json.RawMessage) {
	if id == "" {
		s.markDegraded()

		return
	}

	s.reqMu.Lock()
	ch, ok := s.pending[id]
	s.reqMu.Unlock()

	if !ok {
		return // unmatched/duplicate id — tolerated, not fatal
	}

	select {
	case ch <- body:
	default:
	}
}

// --- input ------------------------------------------------------------------

// WriteInput sends the bytes as a stream-json user message (a new turn).
func (s *Session) WriteInput(data []byte) error {
	return s.writeJSON(userMessage{
		Type: "user",
		Message: map[string]any{
			"role":    "user",
			"content": string(data),
		},
	})
}

// WriteInputAndSubmit is identical to WriteInput for headless: a user message
// is a complete, submitted turn (there is no separate submit key).
func (s *Session) WriteInputAndSubmit(data []byte) error {
	return s.WriteInput(data)
}

// writeJSON marshals v and writes it as one NDJSON line under the write mutex.
func (s *Session) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	b = append(b, '\n')

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.Exited() {
		return fmt.Errorf("headless session has exited")
	}

	_, err = s.stdin.Write(b)

	return err
}

// --- SessionDriver: lifecycle ----------------------------------------------

func (s *Session) ProcessPID() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}

	return 0
}

// Fd has no meaning for a pipe-backed session (there is no ptmx). It returns 0;
// the daemon's upgrade FD-handoff skips sessions it can't hand off.
func (s *Session) Fd() uintptr { return 0 }

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Exited() bool {
	s.exitMu.RLock()
	defer s.exitMu.RUnlock()

	return s.exited
}

func (s *Session) ExitCode() int {
	s.exitMu.RLock()
	defer s.exitMu.RUnlock()

	return s.exitCode
}

func (s *Session) ExitSignal() syscall.Signal {
	s.exitMu.RLock()
	defer s.exitMu.RUnlock()

	return s.exitSignal
}

// PeakRSSBytes is not tracked for headless sessions.
func (s *Session) PeakRSSBytes() int64 { return 0 }

func (s *Session) LastOutputAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.lastOutputAt
}

// RecentlyAdopted is always false: headless sessions are not adopted across
// daemon restart (their stdout pipe can't be re-read); the daemon resumes them.
func (s *Session) RecentlyAdopted(time.Duration) bool { return false }

func (s *Session) Kill() error {
	pid := s.ProcessPID()
	if pid == 0 {
		return nil
	}

	return syscall.Kill(-pid, syscall.SIGTERM)
}

func (s *Session) ForceKill() error {
	pid := s.ProcessPID()
	if pid == 0 {
		return nil
	}

	return syscall.Kill(-pid, syscall.SIGKILL)
}

func (s *Session) Close() {
	_ = s.stdin.Close()
	<-s.readDone
	_ = s.scrollback.Close()
}

// --- SessionDriver: PTY-shaped no-ops ---------------------------------------

// Resize is a no-op: a headless session has no terminal to resize.
func (s *Session) Resize(uint16, uint16) error { return nil }

// Poke is a no-op: there is no TUI to nudge.
func (s *Session) Poke() {}

// NotifyUserInput is a no-op: headless sessions have no attached human typing.
func (s *Session) NotifyUserInput() {}

// WaitForUserIdle returns immediately true: no interactive user to wait on.
func (s *Session) WaitForUserIdle(time.Duration, time.Duration) bool { return true }

// --- SessionDriver: output surfaces -----------------------------------------

func (s *Session) Attach(w io.Writer) {
	s.mu.Lock()
	s.writers = append(s.writers, w)
	s.mu.Unlock()
}

func (s *Session) Detach() {
	s.mu.Lock()
	s.writers = nil
	s.mu.Unlock()
}

func (s *Session) DetachWriter(w io.Writer) {
	s.mu.Lock()
	for i, wr := range s.writers {
		if wr == w {
			s.writers = append(s.writers[:i], s.writers[i+1:]...)

			break
		}
	}
	s.mu.Unlock()
}

func (s *Session) ScrollbackFile() *grpty.Scrollback { return s.scrollback }

// --- structured extras ------------------------------------------------------

// Snapshot returns the current structured status + last result envelope.
func (s *Session) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return Snapshot{
		Status:   s.status,
		ToolName: s.toolName,
		Result:   s.result,
		Degraded: s.degraded,
	}
}

// --- internal helpers -------------------------------------------------------

func (s *Session) setStatus(st Status, tool string) {
	s.mu.Lock()
	s.status = st
	if tool != "" {
		s.toolName = tool
	}
	s.mu.Unlock()
}

func (s *Session) setResult(ev event) {
	res := &ResultEnvelope{
		NumTurns: intOr(ev.NumTurns),
		Usage:    ev.Usage,
		Text:     ev.ResultText,
		At:       time.Now(),
	}
	if ev.IsError != nil {
		res.IsError = *ev.IsError
	}
	if ev.TotalCost != nil {
		res.TotalCost = *ev.TotalCost
	}
	if ev.DurationMS != nil {
		res.DurationMS = *ev.DurationMS
	}
	if ev.DurationAPI != nil {
		res.DurationAPI = *ev.DurationAPI
	}

	s.mu.Lock()
	s.result = res
	s.mu.Unlock()
}

func (s *Session) markDegraded() {
	s.mu.Lock()
	s.degraded = true
	s.mu.Unlock()
}

func (s *Session) touch() {
	s.mu.Lock()
	s.lastOutputAt = time.Now()
	s.mu.Unlock()
}

// appendScrollback renders a line to the scrollback file and fans it to any
// attached read-only writers.
func (s *Session) appendScrollback(line []byte) {
	rendered := renderLine(line)
	_, _ = s.scrollback.Write(rendered)

	s.mu.RLock()
	writers := make([]io.Writer, len(s.writers))
	copy(writers, s.writers)
	s.mu.RUnlock()

	for _, w := range writers {
		if w != nil {
			_, _ = w.Write(rendered)
		}
	}
}

func (s *Session) drainStderr(stderr io.Reader) {
	r := bufio.NewReader(stderr)
	for {
		line, err := readLine(r, maxLineBytes)
		if len(line) > 0 {
			out := append([]byte("[stderr] "), line...)
			out = append(out, '\n')
			_, _ = s.scrollback.Write(out)
		}

		if err != nil {
			return
		}
	}
}

func (s *Session) waitLoop() {
	err := s.cmd.Wait()
	<-s.readDone

	s.exitMu.Lock()
	s.exited = true
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			s.exitCode = exitErr.ExitCode()
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				s.exitSignal = ws.Signal()
			}
		} else {
			s.exitCode = -1
		}
	}
	s.exitMu.Unlock()

	close(s.done)
}

// buildEnv mirrors pty.buildEnv: overlay the extra vars on the parent env.
func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}

	return env
}

func intOr(p *int) int {
	if p == nil {
		return 0
	}

	return *p
}
