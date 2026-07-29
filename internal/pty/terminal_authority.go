package pty

import "bytes"

// AttachOutputFilter strips child terminal traffic that the daemon terminal
// model owns. Attached host terminals should render visible output, not answer
// terminal queries or apply OSC/DCS/APC side effects on behalf of the child.
type AttachOutputFilter struct {
	state         terminalFilterState
	seq           []byte
	kind          terminalStringKind
	utf8Remaining int
}

type terminalFilterState uint8

const (
	terminalFilterGround terminalFilterState = iota
	terminalFilterEscape
	terminalFilterCSI
	terminalFilterString
	terminalFilterStringEscape
)

type terminalStringKind uint8

const (
	terminalStringNone terminalStringKind = iota
	terminalStringOSC
	terminalStringDCS
	terminalStringAPC
	terminalStringPM
	terminalStringSOS
)

// Filter returns p with daemon-owned terminal queries and unsafe side-effect
// control strings removed. The filter is stateful so fragmented sequences are
// handled deterministically across PTY read boundaries.
func (f *AttachOutputFilter) Filter(p []byte) []byte {
	if len(p) == 0 {
		return nil
	}

	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch f.state {
		case terminalFilterGround:
			out = f.filterGround(out, b)
		case terminalFilterEscape:
			out = f.filterEscape(out, b)
		case terminalFilterCSI:
			out = f.filterCSI(out, b)
		case terminalFilterString:
			out = f.filterString(out, b)
		case terminalFilterStringEscape:
			out = f.filterStringEscape(out, b)
		}
	}

	return out
}

// Finish drops any incomplete buffered control sequence. Failing closed keeps a
// truncated query or OSC sequence from leaking to a host terminal during attach
// from a scrollback tail.
func (f *AttachOutputFilter) Finish() []byte {
	f.reset()

	return nil
}

// FilterAttachOutput applies a one-shot attach output filter and drops any
// incomplete trailing sequence.
func FilterAttachOutput(p []byte) []byte {
	var filter AttachOutputFilter

	out := filter.Filter(p)
	_ = filter.Finish()

	return out
}

func (f *AttachOutputFilter) filterGround(out []byte, b byte) []byte {
	if f.utf8Remaining > 0 {
		if isUTF8Continuation(b) {
			f.utf8Remaining--

			return append(out, b)
		}

		f.utf8Remaining = 0
	}

	if continuation := utf8ContinuationCount(b); continuation > 0 {
		f.utf8Remaining = continuation

		return append(out, b)
	}

	switch b {
	case '\x05':
		return out
	case '\x1b':
		f.seq = append(f.seq[:0], b)
		f.state = terminalFilterEscape
	case '\x90':
		f.startString(terminalStringDCS)
	case '\x98':
		f.startString(terminalStringSOS)
	case '\x9b':
		f.seq = append(f.seq[:0], b)
		f.state = terminalFilterCSI
	case '\x9d':
		f.startString(terminalStringOSC)
	case '\x9e':
		f.startString(terminalStringPM)
	case '\x9f':
		f.startString(terminalStringAPC)
	default:
		out = append(out, b)
	}

	return out
}

func (f *AttachOutputFilter) filterEscape(out []byte, b byte) []byte {
	f.seq = append(f.seq, b)

	switch b {
	case '[':
		f.state = terminalFilterCSI
	case 'Z':
		f.reset()
	case ']':
		f.startString(terminalStringOSC)
	case 'P':
		f.startString(terminalStringDCS)
	case '_':
		f.startString(terminalStringAPC)
	case '^':
		f.startString(terminalStringPM)
	case 'X':
		f.startString(terminalStringSOS)
	default:
		if b >= 0x30 && b <= 0x7e {
			out = append(out, f.seq...)
			f.reset()
		}
	}

	return out
}

func (f *AttachOutputFilter) filterCSI(out []byte, b byte) []byte {
	f.seq = append(f.seq, b)
	if b < 0x40 || b > 0x7e {
		return out
	}

	if !daemonOwnsCSI(f.seq) {
		out = append(out, f.seq...)
	}

	f.reset()

	return out
}

func (f *AttachOutputFilter) filterString(out []byte, b byte) []byte {
	switch b {
	case '\x07':
		if f.kind == terminalStringOSC {
			f.reset()
		}
	case '\x1b':
		f.state = terminalFilterStringEscape
	case '\x9c':
		f.reset()
	}

	return out
}

func (f *AttachOutputFilter) filterStringEscape(out []byte, b byte) []byte {
	if b == '\\' {
		f.reset()

		return out
	}

	if b != '\x1b' {
		f.state = terminalFilterString
	}

	return out
}

func (f *AttachOutputFilter) startString(kind terminalStringKind) {
	f.seq = f.seq[:0]
	f.kind = kind
	f.state = terminalFilterString
}

func (f *AttachOutputFilter) reset() {
	f.state = terminalFilterGround
	f.kind = terminalStringNone
	f.seq = f.seq[:0]
	f.utf8Remaining = 0
}

func daemonOwnsCSI(seq []byte) bool {
	if len(seq) == 0 {
		return false
	}

	final := seq[len(seq)-1]
	body := csiBody(seq)

	switch final {
	case 'c', 'n', 't':
		return true
	case 'S', 'u':
		return bytes.HasPrefix(body, []byte{'?'})
	case 'x':
		return !bytes.Contains(body, []byte{'$'})
	case 'p':
		return bytes.Contains(body, []byte{'$'})
	case 'q':
		return bytes.Contains(body, []byte{'>'}) && !bytes.Contains(body, []byte{' '})
	default:
		return false
	}
}

func csiBody(seq []byte) []byte {
	if len(seq) >= 2 && seq[0] == '\x1b' && seq[1] == '[' {
		return seq[2 : len(seq)-1]
	}

	if len(seq) >= 1 && seq[0] == '\x9b' {
		return seq[1 : len(seq)-1]
	}

	return nil
}

func utf8ContinuationCount(b byte) int {
	switch {
	case b >= 0xc2 && b <= 0xdf:
		return 1
	case b >= 0xe0 && b <= 0xef:
		return 2
	case b >= 0xf0 && b <= 0xf4:
		return 3
	default:
		return 0
	}
}

func isUTF8Continuation(b byte) bool {
	return b&0xc0 == 0x80
}
