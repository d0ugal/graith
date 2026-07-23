package daemon

import (
	"sync"
	"testing"

	grpty "github.com/d0ugal/graith/internal/pty"
)

// daemonTestTerminal keeps daemon lifecycle tests independent of the selected
// production terminal backend. Native parser and rendering behavior is tested
// separately in internal/pty under the libghostty tag.
type daemonTestTerminal struct {
	mu     sync.RWMutex
	cols   int
	rows   int
	cursor int
	lines  [][]grpty.Cell
}

func newDaemonPTYSession(t *testing.T, opts grpty.SessionOpts) (*grpty.Session, error) {
	t.Helper()

	return newDaemonPTYSessionWithFactory(opts)
}

func newDaemonPTYSessionWithFactory(opts grpty.SessionOpts) (*grpty.Session, error) {
	return grpty.NewSessionWithTerminalFactory(opts, func(cols, rows int) (grpty.Terminal, error) {
		return newDaemonTestTerminal(cols, rows), nil
	})
}

func newDaemonTestTerminal(cols, rows int) *daemonTestTerminal {
	t := &daemonTestTerminal{cols: cols, rows: rows}

	t.lines = make([][]grpty.Cell, rows)
	for row := range t.lines {
		t.lines[row] = make([]grpty.Cell, cols)
	}

	return t
}

func (t *daemonTestTerminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, b := range p {
		switch b {
		case '\n':
			t.cursor = ((t.cursor / t.cols) + 1) * t.cols
		case '\r':
			t.cursor -= t.cursor % t.cols
		case '\b':
			if t.cursor > 0 {
				t.cursor--
			}
		default:
			if b < 0x20 || t.cursor >= t.cols*t.rows {
				continue
			}

			t.lines[t.cursor/t.cols][t.cursor%t.cols].Content = string(b)
			t.cursor++
		}
	}

	return len(p), nil
}

func (t *daemonTestTerminal) Resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cols, t.rows = cols, rows
	t.cursor = min(t.cursor, cols*rows)

	t.lines = make([][]grpty.Cell, rows)
	for row := range t.lines {
		t.lines[row] = make([]grpty.Cell, cols)
	}

	return nil
}

func (t *daemonTestTerminal) Size() (int, int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.cols, t.rows
}

func (t *daemonTestTerminal) Cursor() (int, int, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.cursor % t.cols, t.cursor / t.cols, true
}

func (t *daemonTestTerminal) Cell(x, y int) grpty.Cell {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if y < 0 || y >= len(t.lines) || x < 0 || x >= len(t.lines[y]) {
		return grpty.Cell{}
	}

	return t.lines[y][x]
}

func (*daemonTestTerminal) Close() error { return nil }
