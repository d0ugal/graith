package transcript

import (
	"bufio"
	"io"
)

// scanReadBufferSize is the bufio.Reader working buffer used by boundedLineReader.
// It bounds the syscall granularity, not the record cap: records longer than this
// are read across several ReadSlice fragments.
const scanReadBufferSize = 64 * 1024

// boundedLineReader reads newline-delimited records from an io.Reader, bounding
// each record at max bytes. It replaces the plain bufio.Scanner the transcript
// readers used to share, which had two behaviours that broke small configured
// caps (issue #1295):
//
//  1. Scanner's effective token cap is the LARGER of the configured max and the
//     initial buffer capacity. Every reader seeded a 64 KiB initial buffer, so a
//     configured cap below 64 KiB was silently floored to 64 KiB and never
//     enforced.
//  2. A single over-cap record makes Scanner.Scan return ErrTooLong and stop
//     permanently, so every later record — including newer cumulative token
//     snapshots — was lost when an oversized line appeared mid-stream.
//
// boundedLineReader enforces max exactly (a record of max bytes is kept; max+1 is
// dropped) and, when a record exceeds max, drains the remainder of that record
// through its terminating newline, counts it once in oversized, and continues
// with the following record.
type boundedLineReader struct {
	r          *bufio.Reader
	maxBytes   int
	maxRecords int // 0 = unlimited; else stop after this many physical records
	buf        []byte
	line       []byte
	consumed   int   // physical records consumed (in-cap or drained-oversized)
	oversized  int   // count of records skipped for exceeding maxBytes
	err        error // first non-EOF read error, if any
	done       bool
}

// newBoundedLineReader bounds each record at maxBytes. A non-positive maxBytes is
// clamped to 1 so the reader always has a usable, enforceable cap.
func newBoundedLineReader(r io.Reader, maxBytes int) *boundedLineReader {
	if maxBytes < 1 {
		maxBytes = 1
	}

	return &boundedLineReader{r: bufio.NewReaderSize(r, scanReadBufferSize), maxBytes: maxBytes}
}

// limitRecords bounds scanning to the first n physical records, where an over-cap
// record that is drained-and-skipped still counts as one. This preserves the
// metadata scans' "session_meta is near the top" contract: drain/continue is
// allowed within the window, but an oversized blob can neither push the search
// past the window nor cause an entire huge rollout to be drained. A non-positive
// n leaves the reader unlimited. Returns the receiver for call chaining.
func (b *boundedLineReader) limitRecords(n int) *boundedLineReader {
	if n > 0 {
		b.maxRecords = n
	}

	return b
}

// scan advances to the next in-cap record, retrievable via bytes. It returns
// false once the input is exhausted, a non-EOF read error occurs, or the
// physical-record limit (if any) is reached. Over-cap records are skipped (and
// counted in oversized) rather than ending iteration, so valid records after an
// oversized one are still delivered.
func (b *boundedLineReader) scan() bool {
	for {
		if b.done {
			return false
		}

		if b.maxRecords > 0 && b.consumed >= b.maxRecords {
			b.done = true

			return false
		}

		line, tooLong, atEOF := b.readRecord()
		b.done = atEOF

		// A terminal empty chunk at EOF (e.g. the newline after the final record)
		// is not a record; every other outcome consumes one physical record.
		if tooLong || len(line) > 0 || !atEOF {
			b.consumed++
		}

		if tooLong {
			b.oversized++

			if b.done {
				return false
			}

			continue
		}

		if len(line) == 0 && b.done {
			return false
		}

		b.line = line

		return true
	}
}

// bytes returns the current record's content, without its trailing newline. The
// slice is owned by the reader and is only valid until the next scan call.
func (b *boundedLineReader) bytes() []byte {
	return b.line
}

// readRecord reads one newline-delimited record. When the record fits within
// maxBytes it returns the content (newline stripped) with tooLong=false; when it exceeds
// maxBytes it drains the rest of the record and returns tooLong=true with a nil
// slice. atEOF is true once the underlying reader is exhausted.
func (b *boundedLineReader) readRecord() (line []byte, tooLong bool, atEOF bool) {
	b.buf = b.buf[:0]

	for {
		frag, err := b.r.ReadSlice('\n')

		// A newline terminates the fragment only when ReadSlice returns no error.
		content := frag
		if err == nil && len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}

		// Accumulate while within cap; once over, stop keeping bytes and just
		// drain the rest of the record.
		if !tooLong {
			if len(b.buf)+len(content) <= b.maxBytes {
				b.buf = append(b.buf, content...)
			} else {
				tooLong = true
				b.buf = b.buf[:0]
			}
		}

		if err == bufio.ErrBufferFull {
			continue // more of this record remains
		}

		if err != nil {
			atEOF = true

			if err != io.EOF {
				// A hard read error mid-record: record it and discard the partial
				// record rather than yielding a truncated line. The caller flags
				// the read via err; matching bufio.Scanner, the partial is dropped.
				b.err = err

				return nil, tooLong, true
			}
		}

		if tooLong {
			return nil, true, atEOF
		}

		return b.buf, false, atEOF
	}
}
