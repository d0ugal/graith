package daemon

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const (
	waitFuzzMaxPatternBytes = 256
	waitFuzzMaxDataBytes    = 32 * 1024
	waitFuzzMaxPlanBytes    = 64
	waitFuzzMaxChunkBytes   = 512
	waitFuzzMaxBufferBytes  = 512
)

func FuzzScanForMatch(f *testing.F) {
	f.Add("bonnie", []byte("first line\r\n\x1b[32mbonnie\x1b[0m the second\r\nthird\n"))
	f.Add("^$", []byte("first\n\nthird\n"))
	f.Add("^$", []byte("first\n"))
	f.Add("ready$", []byte("wait\nready"))
	f.Add("^braw$", []byte("braw\r\ncanny\r\n"))
	f.Add("braw|canny", []byte("dreich\ncanny\nbraw\n"))
	f.Add(".*", []byte("\n"))
	f.Add("bonnie", []byte("\x1b[31mbonnie\x1b[0m\n"))
	f.Add("[unterminated", []byte("braw\n"))
	f.Add("never", []byte(strings.Repeat("a", 4096)))

	f.Fuzz(func(t *testing.T, pattern string, data []byte) {
		if len(pattern) > waitFuzzMaxPatternBytes || len(data) > waitFuzzMaxDataBytes {
			t.Skip()
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return
		}

		gotLine, gotOK := scanForMatch(re, data)

		wantLine, wantOK := referenceScanForMatch(re, data)
		if gotOK != wantOK || gotLine != wantLine {
			t.Fatalf("scanForMatch(%q, %q) = (%q, %t), want (%q, %t)", pattern, data, gotLine, gotOK, wantLine, wantOK)
		}

		if gotOK && !re.MatchString(gotLine) {
			t.Fatalf("scanForMatch returned non-matching line %q for pattern %q", gotLine, pattern)
		}
	})
}

func FuzzMatchWriterWrite(f *testing.F) {
	f.Add("ready>", []byte("waiting\nready> "), []byte{8, 3}, 32)
	f.Add("^$", []byte("first\n\nthird\n"), []byte{6, 1, 6}, 16)
	f.Add("^$", []byte("first\n"), []byte{6}, 16)
	f.Add("bonnie", []byte("\x1b[32mbonnie\x1b[0m\n"), []byte{2, 3, 5, 8}, 32)
	f.Add("^braw$", []byte("braw\r\ncanny\r\n"), []byte{2, 3, 4}, 16)
	f.Add("braw|canny", []byte("dreich\ncanny\nbraw\n"), []byte{1, 2, 3, 4}, 32)
	f.Add(".*", []byte("abc\n"), []byte{0, 4}, 8)
	f.Add("[unterminated", []byte("braw\n"), []byte{1}, 8)
	f.Add("never", []byte(strings.Repeat("a", 4096)), []byte{255, 255, 255}, 64)
	f.Add(".*", []byte(strings.Repeat("a", 4096)), []byte{255, 255, 255}, 16)

	f.Fuzz(func(t *testing.T, pattern string, data, chunkPlan []byte, maxBufSeed int) {
		if len(pattern) > waitFuzzMaxPatternBytes ||
			len(data) > waitFuzzMaxDataBytes ||
			len(chunkPlan) > waitFuzzMaxPlanBytes {
			t.Skip()
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return
		}

		chunks := waitFuzzChunks(data, chunkPlan)
		maxBuf := 1 + int(uint(maxBufSeed)%waitFuzzMaxBufferBytes)
		matchCh := make(chan string, 4)
		mw := &matchWriter{re: re, matchCh: matchCh, maxBuf: maxBuf}

		for _, chunk := range chunks {
			n, err := mw.Write(chunk)
			if err != nil {
				t.Fatalf("Write(%q) returned unexpected error: %v", chunk, err)
			}

			if n != len(chunk) {
				t.Fatalf("Write(%q) returned n=%d, want %d", chunk, n, len(chunk))
			}

			if len(mw.buf) > maxBuf {
				t.Fatalf("buffer length = %d after Write(%q), want <= %d", len(mw.buf), chunk, maxBuf)
			}

			if len(matchCh) > 1 {
				t.Fatalf("matchWriter fired %d times, want at most once", len(matchCh))
			}
		}

		gotLines := drainWaitFuzzMatches(matchCh)
		if len(gotLines) > 1 {
			t.Fatalf("matchWriter fired %d times, want at most once", len(gotLines))
		}

		wantLine, wantOK := referenceMatchWriterLine(re, chunks, maxBuf)
		if !wantOK {
			if len(gotLines) != 0 {
				t.Fatalf("matchWriter matched %q, want no match", gotLines[0])
			}

			if mw.done {
				t.Fatal("matchWriter marked done without producing a match")
			}

			return
		}

		if len(gotLines) != 1 {
			t.Fatalf("matchWriter produced %d matches, want one %q", len(gotLines), wantLine)
		}

		if gotLines[0] != wantLine {
			t.Fatalf("matchWriter matched %q, want %q", gotLines[0], wantLine)
		}

		if !mw.done {
			t.Fatal("matchWriter produced a match without marking done")
		}

		if !re.MatchString(gotLines[0]) {
			t.Fatalf("matchWriter returned non-matching line %q for pattern %q", gotLines[0], pattern)
		}
	})
}

func referenceScanForMatch(re *regexp.Regexp, data []byte) (string, bool) {
	for len(data) > 0 {
		raw, rest, foundNewline := bytes.Cut(data, []byte("\n"))
		if line, ok := referenceMatchLine(re, raw, false); ok {
			return line, true
		}

		if !foundNewline {
			return "", false
		}

		data = rest
	}

	return "", false
}

func referenceMatchWriterLine(re *regexp.Regexp, chunks [][]byte, maxBuf int) (string, bool) {
	var buf []byte

	for _, chunk := range chunks {
		buf = append(buf, chunk...)
		start := 0

		for {
			i := bytes.IndexByte(buf[start:], '\n')
			trailing := i < 0

			var raw []byte
			if trailing {
				raw = buf[start:]
			} else {
				raw = buf[start : start+i]
			}

			if line, ok := referenceMatchLine(re, raw, trailing); ok {
				return line, true
			}

			if trailing {
				break
			}

			start += i + 1
		}

		if start > 0 {
			buf = buf[start:]
		}

		if len(buf) > maxBuf {
			buf = buf[len(buf)-maxBuf:]
		}
	}

	return "", false
}

func referenceMatchLine(re *regexp.Regexp, raw []byte, trailing bool) (string, bool) {
	clean := ansi.Strip(strings.TrimRight(string(raw), "\r"))
	if trailing && clean == "" {
		return "", false
	}

	return clean, re.MatchString(clean)
}

func waitFuzzChunks(data, plan []byte) [][]byte {
	chunks := make([][]byte, 0, len(plan)+1)
	pos := 0

	for _, step := range plan {
		if len(chunks) >= waitFuzzMaxPlanBytes {
			break
		}

		if step == 0 || pos >= len(data) {
			chunks = append(chunks, nil)
			continue
		}

		n := 1 + int(step)%waitFuzzMaxChunkBytes
		if remaining := len(data) - pos; n > remaining {
			n = remaining
		}

		chunks = append(chunks, data[pos:pos+n])
		pos += n
	}

	if pos < len(data) {
		chunks = append(chunks, data[pos:])
	}

	if len(chunks) == 0 {
		chunks = append(chunks, data)
	}

	return chunks
}

func drainWaitFuzzMatches(ch <-chan string) []string {
	var matches []string

	for {
		select {
		case line := <-ch:
			matches = append(matches, line)
		default:
			return matches
		}
	}
}
