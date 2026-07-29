package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

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
	ResultNewSession
	ResultForkSession
	ResultLastSession
	ResultOrchestratorSession
	ResultMessageOverlay
	ResultRenameSession
	ResultScrollMode
)

// kittyCtrlSeq returns the Kitty keyboard protocol escape sequence for
// Ctrl+<letter>. For example, Ctrl+b (prefixByte=0x02) produces "\x1b[98;5u".
// Terminals like Ghostty use this encoding instead of sending raw control bytes.
func kittyCtrlSeq(prefixByte byte) []byte {
	if prefixByte < 1 || prefixByte > 26 {
		return nil
	}

	codepoint := int(prefixByte) + 96

	return fmt.Appendf(nil, "\x1b[%d;5u", codepoint)
}

// parseKittyCSIu parses a Kitty keyboard protocol CSI u sequence at input[pos:].
// Format: ESC [ codepoint [;modifiers[:event_type]] u
// Returns codepoint, modifier value (1=none, 5=ctrl, …), event type
// (0=unspecified press, 1=press, 2=repeat, 3=release), sequence byte length,
// and whether parsing succeeded.
func parseKittyCSIu(input []byte, pos int) (int, int, int, int, bool) {
	if pos+3 >= len(input) || input[pos] != '\x1b' || input[pos+1] != '[' {
		return 0, 0, 0, 0, false
	}

	i := pos + 2

	numStart := i
	for i < len(input) && input[i] >= '0' && input[i] <= '9' {
		i++
	}

	if i == numStart || i >= len(input) {
		return 0, 0, 0, 0, false
	}

	cp := 0
	for _, b := range input[numStart:i] {
		cp = cp*10 + int(b-'0')
	}

	mods := 1
	evType := 0

	if input[i] == ';' {
		i++
		modStart := i
		mods = 0

		for i < len(input) && input[i] >= '0' && input[i] <= '9' {
			i++
		}

		if i == modStart || i >= len(input) {
			return 0, 0, 0, 0, false
		}

		for _, b := range input[modStart:i] {
			mods = mods*10 + int(b-'0')
		}

		if i < len(input) && input[i] == ':' {
			i++

			evStart := i
			for i < len(input) && input[i] >= '0' && input[i] <= '9' {
				i++
			}

			if i == evStart || i >= len(input) {
				return 0, 0, 0, 0, false
			}

			for _, b := range input[evStart:i] {
				evType = evType*10 + int(b-'0')
			}
		}
	}

	if i >= len(input) || input[i] != 'u' {
		return 0, 0, 0, 0, false
	}

	return cp, mods, evType, i - pos + 1, true
}

// processKittyPrefix scans input for Kitty CSI u sequences matching the prefix
// key (ctrl+letter). Press/repeat events are replaced with the raw prefix byte;
// release events are removed entirely. Non-matching sequences are left as-is.
func processKittyPrefix(input []byte, prefixByte byte) []byte {
	prefixCP := int(prefixByte) + 96

	var out []byte

	copied := 0

	for i := 0; i < len(input); i++ {
		if input[i] != '\x1b' {
			continue
		}

		cp, mods, evType, seqLen, ok := parseKittyCSIu(input, i)
		if !ok || cp != prefixCP || mods != 5 {
			continue
		}

		if out == nil {
			out = make([]byte, 0, len(input))
		}

		out = append(out, input[copied:i]...)
		if evType != 3 {
			out = append(out, prefixByte)
		}

		i += seqLen - 1
		copied = i + 1
	}

	if out == nil {
		return input
	}

	return append(out, input[copied:]...)
}

// keyLabel renders a keybinding byte for display in the help bar. Printable
// ASCII bytes show as themselves; anything else (unset or control) shows "?".
func keyLabel(b byte) string {
	if b >= 0x20 && b < 0x7f {
		return string(b)
	}

	return "?"
}

// showHelpBar renders a one-line help bar at the bottom of the screen using
// ANSI save-cursor / restore-cursor so the agent's output isn't disturbed. Every
// prefix-action key reflects the configured keybindings so the bar never lies
// about a remapped key (issue #1233).
func showHelpBar(w io.Writer, keys PassthroughKeys) {
	help := fmt.Sprintf(
		"\x1b[7m %s detach  %s sessions  %s messages  %s orch  %s last  %s/%s next/prev  %s new  %s fork  %s rename  %s scroll  %s shell  %s restart \x1b[0m",
		keyLabel(keys.Detach),
		keyLabel(keys.SessionList),
		keyLabel(keys.Messages),
		keyLabel(keys.OrchestratorSession),
		keyLabel(keys.LastSession),
		keyLabel(keys.NextSession),
		keyLabel(keys.PrevSession),
		keyLabel(keys.NewSession),
		keyLabel(keys.ForkSession),
		keyLabel(keys.RenameSession),
		keyLabel(keys.ScrollMode),
		keyLabel(keys.Shell),
		keyLabel(keys.RestartSession),
	)
	_, _ = w.Write([]byte("\x1b7\x1b[999B\r\x1b[2K" + help + "\x1b8"))
}

func clearHelpBar(w io.Writer) {
	_, _ = w.Write([]byte("\x1b7\x1b[999B\r\x1b[2K\x1b8"))
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (sw *syncWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	return sw.w.Write(p)
}

type PassthroughKeys struct {
	Prefix              byte
	Detach              byte
	SessionList         byte
	Shell               byte
	NextSession         byte
	PrevSession         byte
	LastSession         byte
	NewSession          byte
	ForkSession         byte
	OrchestratorSession byte
	RenameSession       byte
	ScrollMode          byte
	Messages            byte
	RestartSession      byte
}

type PassthroughOpts struct {
	Keys                 PassthroughKeys
	SessionID            string
	Info                 *protocol.SessionInfo
	StatusBar            *StatusBarCfg
	ExperimentalSeed     *protocol.ExperimentalAttachSeedMsg
	TerminalOwned        bool
	experimentalChrome   *experimentalAttachChrome
	experimentalViewport *experimentalAttachViewport
	experimentalInput    *experimentalInputRouter
	terminalRefresh      chan struct{}
	// LocalHistoryScroll is the experimental attach hook for wheel events that
	// are not routed to a child mouse-tracking or alternate-scroll mode. The
	// #1827 local-history work will install the actual history view here.
	LocalHistoryScroll func(delta int) bool
	// DragArrowKeys enables the touch/hold-and-drag gesture that translates
	// left-button mouse drags into arrow-key presses. Off by default.
	DragArrowKeys bool
	// DragArrowThreshold is the cells-per-arrow drag distance; <1 uses the default.
	DragArrowThreshold int
	// ReadOnly gates all keystroke input: the client streams PTY output but never
	// forwards typed bytes to the daemon, so an observer can watch a session
	// without risk of injecting input (issue #31). Prefix-key actions (detach,
	// session switching, overlays) still work; only data sent to the agent is
	// suppressed. A persistent indicator shows the mode.
	ReadOnly bool

	experimentalSnapshotID uint64
}

type StatusBarCfg struct {
	Position string
}

const experimentalSnapshotMinInterval = 33 * time.Millisecond

type experimentalSnapshotRequest struct {
	deltaFrom uint64
}

type experimentalSnapshotRequester struct {
	requestCh chan experimentalSnapshotRequest
	done      chan struct{}
}

func startExperimentalSnapshotRequester(ctx context.Context, c *Client, sessionID string) *experimentalSnapshotRequester {
	r := &experimentalSnapshotRequester{
		requestCh: make(chan experimentalSnapshotRequest, 1),
		done:      make(chan struct{}),
	}

	go func() {
		defer close(r.done)

		var lastSent time.Time

		for {
			select {
			case <-ctx.Done():
				return
			case req := <-r.requestCh:
				wait := time.Until(lastSent.Add(experimentalSnapshotMinInterval))
				if !waitExperimentalSnapshotInterval(ctx, wait) {
					return
				}

				_ = c.SendControl("screen_snapshot", protocol.ScreenSnapshotMsg{
					SessionID: sessionID,
					DeltaFrom: req.deltaFrom,
				})
				lastSent = time.Now()
			}
		}
	}()

	return r
}

func (r *experimentalSnapshotRequester) request(deltaFrom uint64) {
	select {
	case r.requestCh <- experimentalSnapshotRequest{deltaFrom: deltaFrom}:
	default:
	}
}

func (r *experimentalSnapshotRequester) wait() {
	<-r.done
}

func experimentalSnapshotViewportMismatch(snap *protocol.ScreenSnapshotResponseMsg, viewport *experimentalAttachViewport) bool {
	if snap == nil || snap.Cols <= 0 || snap.Rows <= 0 {
		return false
	}

	cols, rows, ok := viewport.size()
	if !ok {
		return false
	}

	return snap.Cols != cols || snap.Rows != rows
}

func (c *Client) sendExperimentalViewportResize(viewport *experimentalAttachViewport) {
	cols, rows, ok := viewport.size()
	if !ok {
		return
	}

	_ = c.SendControl("resize", protocol.ResizeMsg{
		Cols: uint16(cols), //nolint:gosec // G115: tracked terminal width from term.GetSize is a small non-negative int
		Rows: uint16(rows), //nolint:gosec // G115: tracked terminal height from term.GetSize is a small non-negative int
	})
}

func waitExperimentalSnapshotInterval(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		return true
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) RunPassthrough(ctx context.Context, opts PassthroughOpts) PassthroughResult {
	if opts.ExperimentalSeed != nil {
		return c.runExperimentalPassthrough(ctx, opts)
	}

	fd := int(os.Stdin.Fd())

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return ResultQuit
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	stdout := &syncWriter{w: os.Stdout}

	var sb *statusBarState

	// Read-only mode always shows a status bar as its persistent indicator, even
	// when the status bar is otherwise disabled (issue #31).
	if (opts.StatusBar != nil || opts.ReadOnly) && opts.Info != nil {
		w, h := int(fallbackCols), int(fallbackRows)
		if tw, th, err := term.GetSize(fd); err == nil {
			w, h = tw, th
		}

		position := "bottom"
		if opts.StatusBar != nil {
			position = opts.StatusBar.Position
		}

		sb = &statusBarState{
			sessionID: opts.SessionID,
			info:      newStatusBarInfo(*opts.Info, 0, protocol.FleetSummary{}),
			rows:      h,
			cols:      w,
			position:  position,
			readOnly:  opts.ReadOnly,
		}
		_ = c.SendControl("resize", protocol.ResizeMsg{
			Cols: uint16(w),     //nolint:gosec // G115: w is a terminal width (term.GetSize) or the small configured fallback
			Rows: uint16(h - 1), //nolint:gosec // G115: h is a terminal height (term.GetSize) or the small configured fallback
		})
	}

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
					rows := uint16(h) //nolint:gosec // G115: terminal height from term.GetSize is a small non-negative int
					if sb != nil {
						sb.updateSize(h, w)
						sb.setup(stdout)

						rows = uint16(h - 1) //nolint:gosec // G115: terminal height from term.GetSize is a small non-negative int
					}

					_ = c.SendControl("resize", protocol.ResizeMsg{
						Cols: uint16(w), //nolint:gosec // G115: terminal width from term.GetSize is a small non-negative int
						Rows: rows,
					})
				}
			}
		}
	}()

	return c.runPassthroughLoop(ctx, opts, os.Stdin, stdout, sb)
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

func (c *Client) runPassthroughLoop(ctx context.Context, opts PassthroughOpts, stdin io.Reader, stdout io.Writer, sb *statusBarState) PassthroughResult {
	keys := opts.Keys

	innerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if sb != nil {
		sb.setup(stdout)
		defer sb.teardown(stdout)
	}

	prefixByte := keys.Prefix
	hasKitty := kittyCtrlSeq(prefixByte) != nil

	// sendInput forwards keystrokes to the daemon, except in read-only mode
	// or while the child has keyboard action mode enabled. Prefix-key actions
	// (detach, overlays, session switching) bypass this; they never reach the
	// agent.
	sendInput := func(b []byte) {
		if opts.ReadOnly {
			return
		}

		if opts.experimentalInput != nil && opts.experimentalInput.keyboardLocked() {
			return
		}

		_ = c.SendData(b)
	}

	var dragArrow *dragArrowState
	if opts.DragArrowKeys {
		dragArrow = newDragArrowState(opts.DragArrowThreshold)
	}

	result := ResultQuit

	var resultOnce sync.Once

	setResult := func(r PassthroughResult) {
		resultOnce.Do(func() { result = r })
	}

	demux := c.startDemux(innerCtx)

	var snapshotRequester *experimentalSnapshotRequester
	if opts.TerminalOwned && opts.SessionID != "" {
		snapshotRequester = startExperimentalSnapshotRequester(innerCtx, c, opts.SessionID)
	}

	snapshotPending := false
	snapshotDirty := false
	snapshotForceFull := false
	snapshotBaseID := opts.experimentalSnapshotID
	requestSnapshot := func(forceFull bool) {
		if snapshotRequester == nil {
			return
		}

		if forceFull {
			snapshotForceFull = true
		}

		if snapshotPending {
			snapshotDirty = true

			return
		}

		snapshotPending = true

		deltaFrom := snapshotBaseID
		if snapshotForceFull {
			deltaFrom = 0
			snapshotForceFull = false
		}

		snapshotRequester.request(deltaFrom)
	}

	var (
		tickerCh        <-chan time.Time
		ticker          *time.Ticker
		statusSessionID string
	)
	if sb != nil {
		statusSessionID = sb.sessionID
		ticker = time.NewTicker(refreshInterval)
		tickerCh = ticker.C
	} else if opts.experimentalChrome != nil && opts.SessionID != "" {
		statusSessionID = opts.SessionID
		ticker = time.NewTicker(refreshInterval)
		tickerCh = ticker.C
	} else if opts.TerminalOwned && opts.SessionID != "" {
		ticker = time.NewTicker(refreshInterval)
		tickerCh = ticker.C
	}

	go func() {
		defer cancel()

		if ticker != nil {
			defer ticker.Stop()
		}

		for {
			select {
			case <-innerCtx.Done():
				return
			case data := <-demux.dataCh:
				if opts.TerminalOwned {
					requestSnapshot(false)
				} else {
					_, _ = stdout.Write(data)
				}
			case msg := <-demux.controlCh:
				switch msg.Type {
				case "detached":
					setResult(ResultDetached)
					return
				case "screen_snapshot_response":
					if opts.TerminalOwned {
						var snap protocol.ScreenSnapshotResponseMsg
						if protocol.DecodePayload(msg, &snap) == nil && snap.SessionID == opts.SessionID {
							if experimentalSnapshotViewportMismatch(&snap, opts.experimentalViewport) {
								snapshotBaseID = 0
								snapshotDirty = true
								snapshotForceFull = true

								c.sendExperimentalViewportResize(opts.experimentalViewport)
							} else {
								if opts.experimentalInput != nil {
									opts.experimentalInput.updateSnapshot(&snap)
								}

								writeExperimentalScreenSnapshotWithChrome(stdout, &snap, opts.experimentalChrome)

								if snap.SnapshotID != 0 {
									snapshotBaseID = snap.SnapshotID
								}
							}
						}

						snapshotPending = false

						if snapshotDirty {
							snapshotDirty = false

							requestSnapshot(false)
						}
					}
				case "error":
					if opts.TerminalOwned && snapshotPending {
						snapshotPending = false
						snapshotDirty = false
					}
				case "status_response":
					if sb != nil || opts.experimentalChrome != nil {
						var resp protocol.StatusResponseMsg
						if protocol.DecodePayload(msg, &resp) == nil {
							info := newStatusBarInfo(resp.Session, resp.UnreadCount, resp.Fleet)
							if sb != nil {
								sb.updateInfo(info)
								sb.render(stdout)
							}

							if opts.experimentalChrome != nil {
								opts.experimentalChrome.updateInfo(info)
								opts.experimentalChrome.renderTo(stdout)
							}
						}
					}
				}
			case <-tickerCh:
				if sb != nil {
					sb.render(stdout)
				}

				if opts.experimentalChrome != nil {
					opts.experimentalChrome.renderTo(stdout)
				}

				if statusSessionID != "" {
					_ = c.SendControl("status", protocol.StatusRequestMsg{
						SessionID: statusSessionID,
					})
				}

				if opts.TerminalOwned {
					requestSnapshot(true)
				}
			case <-opts.terminalRefresh:
				requestSnapshot(true)
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
		forwardedPasteActive := false

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

			// Replace Kitty keyboard protocol sequences for the prefix key
			// with the raw prefix byte. Release events (event_type=3) are
			// stripped entirely so they don't consume the prefixSeen state.
			input := buf[:n]
			if hasKitty {
				input = processKittyPrefix(input, prefixByte)
				n = len(input)
			}

			if opts.TerminalOwned && opts.experimentalChrome != nil {
				input = opts.experimentalChrome.translateMouseInput(input)
				n = len(input)
			}

			// Translate left-button drag gestures into arrow-key presses before
			// the prefix scan. Emitted arrow sequences contain no prefix byte,
			// and mouse-wheel/other events pass through untouched.
			if dragArrow != nil && (opts.experimentalInput == nil || !opts.experimentalInput.childMouseTracking()) {
				input = dragArrow.process(input)
				n = len(input)
			}

			if opts.experimentalInput != nil {
				input = opts.experimentalInput.process(input)
				n = len(input)
			}

			sendStart := 0

			for i := 0; i < n; i++ {
				if opts.experimentalInput != nil {
					if hasSequence(input, i, bracketedPasteStart) {
						forwardedPasteActive = true
						i += len(bracketedPasteStart) - 1

						continue
					}

					if hasSequence(input, i, bracketedPasteEnd) {
						forwardedPasteActive = false
						i += len(bracketedPasteEnd) - 1

						continue
					}
				}

				if prefixSeen {
					key := input[i]
					skip := 0

					if key == '\x1b' {
						if cp, _, evType, seqLen, ok := parseKittyCSIu(input, i); ok && cp > 0 && cp < 128 {
							if evType == 3 {
								i += seqLen - 1
								sendStart = i + 1

								continue
							}

							key = byte(cp)
							skip = seqLen - 1
						}
					}

					prefixSeen = false

					clearHelpBar(stdout)

					if opts.TerminalOwned {
						signalExperimentalRefresh(opts.terminalRefresh)
					}

					switch key {
					case prefixByte:
						sendInput([]byte{prefixByte})
					case keys.Detach:
						setResult(ResultDetached)
						return
					case keys.SessionList, 0:
						setResult(ResultOverlay)
						return
					case keys.Messages:
						setResult(ResultMessageOverlay)
						return
					case keys.Shell:
						setResult(ResultShell)
						return
					case keys.NextSession:
						setResult(ResultNextSession)
						return
					case keys.PrevSession:
						setResult(ResultPrevSession)
						return
					case keys.RestartSession:
						setResult(ResultRestart)
						return
					case keys.LastSession:
						setResult(ResultLastSession)
						return
					case keys.NewSession:
						setResult(ResultNewSession)
						return
					case keys.ForkSession:
						setResult(ResultForkSession)
						return
					case keys.OrchestratorSession:
						setResult(ResultOrchestratorSession)
						return
					case keys.RenameSession:
						setResult(ResultRenameSession)
						return
					case keys.ScrollMode:
						setResult(ResultScrollMode)
						return
					default:
						sendInput([]byte{prefixByte, key})
					}

					i += skip
					sendStart = i + 1

					continue
				}

				if forwardedPasteActive {
					continue
				}

				if input[i] == prefixByte {
					if i > sendStart {
						sendInput(input[sendStart:i])
					}

					prefixSeen = true

					showHelpBar(stdout, keys)

					sendStart = i + 1

					continue
				}
			}

			if sendStart < n && !prefixSeen {
				sendInput(input[sendStart:n])
			}
		}
	}()

	<-innerCtx.Done()
	c.stopDemux(demux)

	if snapshotRequester != nil {
		snapshotRequester.wait()
	}

	return result
}
