package pty

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAttachOutputFilterDropsDaemonOwnedQueries(t *testing.T) {
	tests := map[string]struct {
		chunks []string
		want   string
	}{
		"device status and attributes": {
			chunks: []string{"braw\x1b[6n", " \x1b[c", " \x1b[>c", " \x1b[=c", " croft"},
			want:   "braw    croft",
		},
		"decrqm and xtversion": {
			chunks: []string{"braw\x1b[?25$p", "\x1b[>q", "croft"},
			want:   "brawcroft",
		},
		"window size reports": {
			chunks: []string{"braw\x1b[18t", "croft"},
			want:   "brawcroft",
		},
		"fragmented csi": {
			chunks: []string{"braw\x1b[", "6n", "croft"},
			want:   "brawcroft",
		},
		"decid": {
			chunks: []string{"braw\x1bZcroft"},
			want:   "brawcroft",
		},
		"kitty keyboard query": {
			chunks: []string{"braw\x1b[?ucroft"},
			want:   "brawcroft",
		},
		"sixel geometry query": {
			chunks: []string{"braw\x1b[?1;1Scroft"},
			want:   "brawcroft",
		},
		"non-query csi preserved": {
			chunks: []string{"braw\x1b[31m", "croft\x1b[0m"},
			want:   "braw\x1b[31mcroft\x1b[0m",
		},
		"decfra preserved": {
			chunks: []string{"braw\x1b[32;1;1;5;5$xcroft"},
			want:   "braw\x1b[32;1;1;5;5$xcroft",
		},
		"c1 csi query": {
			chunks: []string{"braw\x9b6n", "croft"},
			want:   "brawcroft",
		},
		"enquiry": {
			chunks: []string{"braw\x05croft"},
			want:   "brawcroft",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var filter AttachOutputFilter

			var got bytes.Buffer
			for _, chunk := range test.chunks {
				got.Write(filter.Filter([]byte(chunk)))
			}

			got.Write(filter.Finish())

			if got.String() != test.want {
				t.Fatalf("filtered output = %q, want %q", got.String(), test.want)
			}
		})
	}
}

func TestAttachOutputFilterPreservesUTF8Text(t *testing.T) {
	input := []byte("emoji 🎉 curly “quotes” ‘and’ box ▛ hyphen ‐\n")

	if got := FilterAttachOutput(input); !bytes.Equal(got, input) {
		t.Fatalf("filtered UTF-8 output = %q, want %q", got, input)
	}
}

func TestAttachOutputFilterPreservesSplitUTF8Text(t *testing.T) {
	chunks := [][]byte{
		[]byte("emoji \xf0"),
		[]byte("\x9f\x8e"),
		[]byte("\x89 curly \xe2"),
		[]byte("\x80\x9cquotes\xe2\x80"),
		[]byte("\x9d box \xe2\x96"),
		[]byte("\x9b done\n"),
	}
	want := []byte("emoji 🎉 curly “quotes” box ▛ done\n")

	var filter AttachOutputFilter

	var got bytes.Buffer
	for _, chunk := range chunks {
		got.Write(filter.Filter(chunk))
	}

	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("filtered split UTF-8 output = %q, want %q", got.Bytes(), want)
	}
}

func TestAttachOutputFilterDropsOSCSideEffectsAndImageProtocols(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"window title": {
			input: "braw\x1b]0;dreich title\x07croft",
			want:  "brawcroft",
		},
		"clipboard": {
			input: "braw\x1b]52;c;Ym9ubmll\x1b\\croft",
			want:  "brawcroft",
		},
		"hyperlink keeps visible text": {
			input: "\x1b]8;;https://example.invalid\x1b\\braw link\x1b]8;;\x1b\\",
			want:  "braw link",
		},
		"notification": {
			input: "braw\x1b]777;notify;graith;done\x1b\\croft",
			want:  "brawcroft",
		},
		"kitty apc image": {
			input: "braw\x1b_Ga=T,f=100;AAAA\x1b\\croft",
			want:  "brawcroft",
		},
		"sixel dcs image": {
			input: "braw\x1bPq~~~~\x1b\\croft",
			want:  "brawcroft",
		},
		"incomplete osc fails closed": {
			input: "braw\x1b]52;c;Ym9ubmll",
			want:  "braw",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := string(FilterAttachOutput([]byte(test.input))); got != test.want {
				t.Fatalf("filtered output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadLoopWritesTerminalRepliesToPTY(t *testing.T) {
	master, child := newSocketpairFiles(t)

	childConn, err := net.FileConn(child)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = childConn.Close() })

	_ = child.Close()

	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "braw.log"), 1024)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = scrollback.Close() })

	screen := &replyingTestTerminal{cols: 80, rows: 24, reply: []byte("\x1b[?1;0c")}
	session := &Session{
		ID:                 "braw-replies",
		Ptmx:               master,
		Scrollback:         scrollback,
		screen:             screen,
		readDone:           make(chan struct{}),
		done:               make(chan struct{}),
		log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		userInputCond:      sync.NewCond(&sync.Mutex{}),
		screenRecoveryNow:  time.Now,
		terminalPtyReplies: make(chan []byte, terminalPtyReplyQueueSize),
	}

	go session.terminalPtyReplyLoop()
	go session.readLoop()

	t.Cleanup(func() {
		_ = childConn.Close()
		session.Close()
	})

	if _, err := childConn.Write([]byte("\x1b[c")); err != nil {
		t.Fatal(err)
	}

	_ = childConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)

	n, err := childConn.Read(buf)
	if err != nil {
		t.Fatalf("read terminal reply: %v", err)
	}

	if got := string(buf[:n]); got != "\x1b[?1;0c" {
		t.Fatalf("terminal reply = %q, want DA response", got)
	}

	tail, err := scrollback.TailBytes(64)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(tail, []byte("\x1b[c")) {
		t.Fatalf("scrollback = %q, want only child output query", tail)
	}
}

func TestReadLoopDoesNotHoldSessionLockWhilePolling(t *testing.T) {
	pollEntered := make(chan struct{})
	releasePoll := make(chan struct{})

	var enterOnce sync.Once

	ptmx, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = writer.Close() })
	t.Cleanup(func() { _ = ptmx.Close() })

	session := &Session{
		ID:       "braw-poll",
		Ptmx:     ptmx,
		screen:   &replyingTestTerminal{cols: 80, rows: 24},
		readDone: make(chan struct{}),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		pollInput: func(_ []unix.PollFd, _ int) (int, error) {
			enterOnce.Do(func() { close(pollEntered) })
			<-releasePoll

			return 0, nil
		},
	}

	go session.readLoop()

	select {
	case <-pollEntered:
	case <-time.After(time.Second):
		t.Fatal("read loop did not enter poll")
	}

	snapshotDone := make(chan struct{})

	go func() {
		_ = session.ScreenSnapshot()

		close(snapshotDone)
	}()

	select {
	case <-snapshotDone:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("screen snapshot blocked behind read-loop poll")
	}

	session.mu.Lock()
	session.closed = true
	session.mu.Unlock()

	close(releasePoll)

	select {
	case <-session.readDone:
	case <-time.After(time.Second):
		t.Fatal("read loop did not exit")
	}
}

func TestReplayWritesDiscardTerminalReplies(t *testing.T) {
	term := &replyingTestTerminal{cols: 80, rows: 24, reply: []byte("dreich")}

	if err := writeTerminalChunks(term, []byte("braw")); err != nil {
		t.Fatal(err)
	}

	if got := term.DrainPtyReplies(); len(got) != 0 {
		t.Fatalf("replay replies = %q, want discarded", got)
	}
}

func newSocketpairFiles(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}

	return os.NewFile(uintptr(fds[0]), "master"), os.NewFile(uintptr(fds[1]), "child")
}

type replyingTestTerminal struct {
	cols    int
	rows    int
	reply   []byte
	pending []byte
}

func (t *replyingTestTerminal) Write(p []byte) (int, error) {
	t.pending = append(t.pending, t.reply...)

	return len(p), nil
}

func (t *replyingTestTerminal) DrainPtyReplies() []byte {
	out := append([]byte(nil), t.pending...)
	t.pending = nil

	return out
}

func (t *replyingTestTerminal) Resize(cols, rows int) error {
	t.cols = cols
	t.rows = rows

	return nil
}

func (t *replyingTestTerminal) Size() (int, int) { return t.cols, t.rows }
func (t *replyingTestTerminal) Cursor() (int, int, bool) {
	return 0, 0, true
}
func (t *replyingTestTerminal) Cell(int, int) Cell { return Cell{Content: " "} }
func (t *replyingTestTerminal) Close() error       { return nil }
