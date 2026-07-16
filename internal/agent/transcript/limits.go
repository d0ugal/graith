package transcript

import (
	"bufio"
	"errors"
	"io"
	"sync/atomic"
)

// Line-reader defaults. Reading a transcript uses a bounded record reader so a
// pathological (or corrupt) line can't exhaust memory. The
// full-transcript readers carry large tool outputs / base64 blobs and need a
// generous cap; the metadata-only scans (Codex rollout cwd / session-id lookup)
// read one small header line and use a tighter cap. Both are configurable via
// Configure so the daemon can raise them for unusually large transcripts (issue
// #1250); these constants are the fallback defaults used when Configure has not
// run (CLI, tests) or is passed a non-positive value.
const (
	// DefaultMaxLineBytes bounds a single transcript line while reading turns or
	// summing usage.
	DefaultMaxLineBytes = 16 * 1024 * 1024
	// DefaultMaxMetadataLineBytes bounds a single line during the small
	// metadata-only scans (Codex rollout cwd / session-id extraction).
	DefaultMaxMetadataLineBytes = 4 * 1024 * 1024
)

// Live line caps, set by Configure. Stored as atomics because Configure runs
// on the daemon's config-reload goroutine while readers run on session
// goroutines; a zero value means "unset" and the getters fall back to the
// defaults above.
var (
	liveMaxLineBytes     atomic.Int64
	liveMaxMetadataBytes atomic.Int64
)

// Configure sets the line caps used by every subsequent transcript
// read. It is process-global (mirroring tools.Configure): the daemon calls it at
// startup and on config reload. A zero or negative value keeps the built-in
// default for that cap. Safe to call concurrently with reads.
func Configure(maxLineBytes, maxMetadataLineBytes int) {
	if maxLineBytes > 0 {
		liveMaxLineBytes.Store(int64(maxLineBytes))
	}

	if maxMetadataLineBytes > 0 {
		liveMaxMetadataBytes.Store(int64(maxMetadataLineBytes))
	}
}

// maxLineBytes returns the configured full-transcript line cap, or the default.
func maxLineBytes() int {
	if v := liveMaxLineBytes.Load(); v > 0 {
		return int(v)
	}

	return DefaultMaxLineBytes
}

// maxMetadataLineBytes returns the configured metadata-scan line cap, or the
// default.
func maxMetadataLineBytes() int {
	if v := liveMaxMetadataBytes.Load(); v > 0 {
		return int(v)
	}

	return DefaultMaxMetadataLineBytes
}

// scanBufferStartCap is the initial buffered-reader capacity used for typical
// short lines. It matches bufio's historical scanner default and is only an
// allocation hint, never a cap on its own.
const scanBufferStartCap = 64 * 1024

// boundedLineScanner reads newline-delimited records while enforcing the exact
// configured byte cap on record content (the newline is not counted). Unlike a
// bufio.Scanner that hits ErrTooLong, it drains an oversized record through its
// newline, reports that record once, and remains usable for later records. This
// is essential for live JSONL transcripts: a single pathological middle record
// must not hide later turns or a newer cumulative token snapshot (#1295).
type boundedLineScanner struct {
	reader    *bufio.Reader
	limit     int
	line      []byte
	oversized bool
	err       error
	done      bool
}

func newBoundedLineScanner(r io.Reader, limit int) *boundedLineScanner {
	if limit < 1 {
		limit = 1
	}

	return &boundedLineScanner{
		reader: bufio.NewReaderSize(r, min(scanBufferStartCap, limit)),
		limit:  limit,
	}
}

func (s *boundedLineScanner) Scan() bool {
	if s.done {
		return false
	}

	s.line = s.line[:0]
	s.oversized = false
	sawData := false

	for {
		fragment, err := s.reader.ReadSlice('\n')
		if len(fragment) > 0 {
			sawData = true
		}

		if len(fragment) > 0 && fragment[len(fragment)-1] == '\n' {
			fragment = fragment[:len(fragment)-1]
		}

		if !s.oversized {
			if len(fragment) > s.limit-len(s.line) {
				s.oversized = true
				s.line = s.line[:0]
			} else {
				s.line = append(s.line, fragment...)
			}
		}

		switch {
		case err == nil:
			return true
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			s.done = true

			return sawData
		default:
			s.err = err
			s.done = true

			return false
		}
	}
}

func (s *boundedLineScanner) Bytes() []byte { return s.line }

func (s *boundedLineScanner) Oversized() bool { return s.oversized }

func (s *boundedLineScanner) Err() error { return s.err }
