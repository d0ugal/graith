package client

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"
)

func FuzzParseKittyCSIu(f *testing.F) {
	seeds := [][]byte{
		[]byte("plain"),
		[]byte("\x1b[98;5:1u"),
		[]byte("\x1b[98;5:2u"),
		[]byte("\x1b[98;5:3u"),
		[]byte("\x1b[98;1u"),
		[]byte("\x1b[98;5:1"),
		[]byte("\x1b[99999999;5u"),
		[]byte("\x1b[98;99999999u"),
		[]byte("\x1b[98;5:99999999u"),
		[]byte("ab\x1b[98;5ucd"),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256 {
			t.Skip()
		}

		for pos := 0; pos < len(input); pos++ {
			cp, mods, evType, seqLen, ok := parseKittyCSIu(input, pos)
			if !ok {
				continue
			}

			if seqLen <= 0 {
				t.Fatalf("seqLen = %d, want positive", seqLen)
			}

			if seqLen > len(input)-pos {
				t.Fatalf("seqLen = %d exceeds remaining input %d at pos %d", seqLen, len(input)-pos, pos)
			}

			if cp < 0 || mods < 0 || evType < 0 {
				t.Fatalf("parsed negative field values: cp=%d mods=%d evType=%d", cp, mods, evType)
			}

			if input[pos+seqLen-1] != 'u' {
				t.Fatalf("parsed sequence terminates with %q, want 'u'", input[pos+seqLen-1])
			}

			if hasOverlongDigitRun(input[pos : pos+seqLen]) {
				t.Fatalf("parsed overlong numeric field in %q", input[pos:pos+seqLen])
			}
		}
	})
}

func FuzzProcessKittyPrefix(f *testing.F) {
	seeds := []struct {
		input  []byte
		prefix byte
	}{
		{[]byte("plain"), 0x02},
		{[]byte("\x1b[98;5u"), 0x02},
		{[]byte("\x1b[98;5:1u"), 0x02},
		{[]byte("\x1b[98;5:2u"), 0x02},
		{[]byte("\x1b[98;5:3u"), 0x02},
		{[]byte("\x1b[98;5:9u"), 0x02},
		{[]byte("\x1b[98;1u"), 0x02},
		{[]byte("\x1b[122;5u"), 0x02},
		{[]byte("\x1b[98;5"), 0x02},
		{[]byte("\x1b[99999999;5u"), 0x02},
		{[]byte("ab\x1b[98;5ucd\x1b[98;5:3u"), 0x02},
		{[]byte("\x1b[98;5u"), 'b'},
	}
	for _, seed := range seeds {
		f.Add(seed.input, seed.prefix)
	}

	f.Fuzz(func(t *testing.T, input []byte, prefix byte) {
		if len(input) > 256 {
			t.Skip()
		}

		got := processKittyPrefix(input, prefix)

		want := referenceKittyPrefix(input, prefix)
		if !bytes.Equal(got, want) {
			t.Fatalf("processKittyPrefix(%q, 0x%02x) = %q, want %q", input, prefix, got, want)
		}
	})
}

func FuzzParseSGRMouse(f *testing.F) {
	seeds := [][]byte{
		[]byte("plain"),
		[]byte("\x1b[<0;10;5M"),
		[]byte("\x1b[<32;12;8M"),
		[]byte("\x1b[<0;3;4m"),
		[]byte("\x1b[<64;1;1M"),
		[]byte("\x1b[<4;10;5M"),
		[]byte("\x1b[<36;10;9M"),
		[]byte("\x1b[<0;1;1"),
		[]byte("\x1b[<0:1;1M"),
		[]byte("\x1b[<0;1M"),
		[]byte("\x1b[<0;99999999;1M"),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256 {
			t.Skip()
		}

		for pos := 0; pos < len(input); pos++ {
			ev, seqLen, ok := parseSGRMouse(input, pos)
			if !ok {
				continue
			}

			if seqLen <= 0 {
				t.Fatalf("seqLen = %d, want positive", seqLen)
			}

			if seqLen > len(input)-pos {
				t.Fatalf("seqLen = %d exceeds remaining input %d at pos %d", seqLen, len(input)-pos, pos)
			}

			if ev.button < 0 || ev.col < 0 || ev.row < 0 {
				t.Fatalf("parsed negative SGR mouse values: %+v", ev)
			}

			term := input[pos+seqLen-1]
			if term != 'M' && term != 'm' {
				t.Fatalf("parsed sequence terminates with %q, want M or m", term)
			}

			if hasOverlongDigitRun(input[pos : pos+seqLen]) {
				t.Fatalf("parsed overlong numeric field in %q", input[pos:pos+seqLen])
			}
		}
	})
}

func FuzzParseUint(f *testing.F) {
	seeds := []struct {
		input []byte
		pos   int
	}{
		{[]byte(""), 0},
		{[]byte("1234567"), 0},
		{[]byte("12345678"), 0},
		{[]byte("abc123"), 3},
		{[]byte("12;34"), 0},
		{[]byte(";12"), 0},
	}
	for _, seed := range seeds {
		f.Add(seed.input, seed.pos)
	}

	f.Fuzz(func(t *testing.T, input []byte, rawPos int) {
		if len(input) > 256 {
			t.Skip()
		}

		pos := fuzzIndex(rawPos, len(input)+1)
		got, next, ok := parseUint(input, pos)
		digitRun := countDigitRun(input, pos)

		switch {
		case digitRun == 0:
			if ok {
				t.Fatalf("parseUint(%q, %d) ok=true with no leading digits", input, pos)
			}

			if next != pos {
				t.Fatalf("parseUint(%q, %d) next=%d, want %d", input, pos, next, pos)
			}
		case digitRun > maxTerminalFieldDigits:
			if ok {
				t.Fatalf("parseUint(%q, %d) accepted overlong digit run of %d", input, pos, digitRun)
			}

			if next <= pos || next > len(input) {
				t.Fatalf("parseUint(%q, %d) next=%d outside input", input, pos, next)
			}
		default:
			if !ok {
				t.Fatalf("parseUint(%q, %d) ok=false for %d-digit run", input, pos, digitRun)
			}

			if next != pos+digitRun {
				t.Fatalf("parseUint(%q, %d) next=%d, want %d", input, pos, next, pos+digitRun)
			}

			want, err := strconv.Atoi(string(input[pos:next]))
			if err != nil {
				t.Fatalf("strconv.Atoi(%q): %v", input[pos:next], err)
			}

			if got != want {
				t.Fatalf("parseUint(%q, %d) value=%d, want %d", input, pos, got, want)
			}
		}
	})
}

func FuzzDragArrowState(f *testing.F) {
	seeds := [][]byte{
		{},
		{0, 10, 10, 1, 10, 14},
		{0, 10, 10, 1, 14, 10, 2, 14, 10},
		{0, 5, 5, 3, 5, 5, 1, 5, 9},
		{5, 10, 5, 6, 10, 9},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			t.Skip()
		}

		d := newDragArrowState(fuzzThreshold(data))
		for _, ev := range fuzzMouseEvents(data, 16) {
			if !d.handles(ev) {
				continue
			}

			assertOnlyArrowSequences(t, d.feed(ev))
		}
	})
}

func FuzzDragArrowProcess(f *testing.F) {
	seeds := [][]byte{
		{},
		{0, 0, 10, 10, 1, 10, 14},
		{0, 0, 10, 10, 1, 14, 10, 2, 14, 10},
		{0, 0, 5, 5, 3, 5, 5, 1, 5, 9},
		{0, 5, 10, 5, 6, 10, 9},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			t.Skip()
		}

		threshold := fuzzThreshold(data)

		eventData := data
		if len(eventData) > 0 {
			eventData = eventData[1:]
		}

		events := fuzzMouseEvents(eventData, 12)

		var (
			input []byte
			want  []byte
		)

		ref := newDragArrowState(threshold)

		for i, ev := range events {
			literal := byte('a' + i%26)
			seq := encodeSGRMouseEvent(ev)

			input = append(input, literal)
			input = append(input, seq...)
			want = append(want, literal)

			if !ref.handles(ev) {
				ref.active = false

				want = append(want, seq...)

				continue
			}

			out := ref.feed(ev)
			assertOnlyArrowSequences(t, out)
			want = append(want, out...)
		}

		input = append(input, 'z')
		want = append(want, 'z')

		got := newDragArrowState(threshold).process(input)
		if !bytes.Equal(got, want) {
			t.Fatalf("drag process = %q, want %q for input %q", got, want, input)
		}
	})
}

func referenceKittyPrefix(input []byte, prefixByte byte) []byte {
	if prefixByte < 1 || prefixByte > 26 {
		return input
	}

	prefixCP := int(prefixByte) + 96

	var out bytes.Buffer

	search, changed := 0, false

	for {
		offset := bytes.IndexByte(input[search:], '\x1b')
		if offset < 0 {
			break
		}

		pos := search + offset

		cp, mods, evType, seqLen, ok := parseKittyCSIu(input, pos)
		if !ok || cp != prefixCP || mods != 5 || !isKnownKittyPrefixEvent(evType) {
			search = pos + 1

			continue
		}

		changed = true

		if out.Len() == 0 {
			out.Grow(len(input))
		}

		out.Write(input[:pos])

		if evType != 3 {
			out.WriteByte(prefixByte)
		}

		input = input[pos+seqLen:]
		search = 0
	}

	if !changed {
		return input
	}

	out.Write(input)

	return out.Bytes()
}

func isKnownKittyPrefixEvent(evType int) bool {
	return evType >= 0 && evType <= 3
}

func hasOverlongDigitRun(input []byte) bool {
	run := 0

	for _, b := range input {
		if b >= '0' && b <= '9' {
			run++
			if run > maxTerminalFieldDigits {
				return true
			}

			continue
		}

		run = 0
	}

	return false
}

func countDigitRun(input []byte, pos int) int {
	run := 0
	for i := pos; i < len(input) && input[i] >= '0' && input[i] <= '9'; i++ {
		run++
	}

	return run
}

func fuzzIndex(raw int, slots int) int {
	if slots <= 0 {
		return 0
	}

	idx := raw % slots
	if idx < 0 {
		idx += slots
	}

	return idx
}

func fuzzThreshold(data []byte) int {
	if len(data) == 0 {
		return defaultDragArrowThreshold
	}

	return int(data[0]%5) + 1
}

var fuzzMouseButtonCases = []struct {
	button  int
	release bool
}{
	{button: 0},
	{button: mouseMotionBit},
	{button: 0, release: true},
	{button: mouseWheelBit},
	{button: mouseWheelBit | 1},
	{button: mouseShiftBit},
	{button: mouseMotionBit | mouseShiftBit},
	{button: mouseAltBit},
	{button: mouseMotionBit | mouseAltBit},
	{button: mouseCtrlBit},
	{button: mouseMotionBit | mouseCtrlBit},
	{button: 2},
	{button: mouseMotionBit | 2},
	{button: 2, release: true},
	{button: mouseMotionBit | 3},
}

func fuzzMouseEvents(data []byte, maxEvents int) []sgrMouseEvent {
	var events []sgrMouseEvent
	for i := 0; i < len(data) && len(events) < maxEvents; i += 3 {
		buttonByte := data[i]

		var colByte byte
		if i+1 < len(data) {
			colByte = data[i+1]
		}

		var rowByte byte
		if i+2 < len(data) {
			rowByte = data[i+2]
		}

		buttonCase := fuzzMouseButtonCases[int(buttonByte)%len(fuzzMouseButtonCases)]
		events = append(events, sgrMouseEvent{
			button:  buttonCase.button,
			col:     int(colByte%40) + 1,
			row:     int(rowByte%25) + 1,
			release: buttonCase.release,
		})
	}

	return events
}

func encodeSGRMouseEvent(ev sgrMouseEvent) []byte {
	term := byte('M')
	if ev.release {
		term = 'm'
	}

	return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", ev.button, ev.col, ev.row, term))
}

func assertOnlyArrowSequences(t *testing.T, out []byte) {
	t.Helper()

	for len(out) > 0 {
		switch {
		case bytes.HasPrefix(out, arrowUp):
			out = out[len(arrowUp):]
		case bytes.HasPrefix(out, arrowDown):
			out = out[len(arrowDown):]
		case bytes.HasPrefix(out, arrowRight):
			out = out[len(arrowRight):]
		case bytes.HasPrefix(out, arrowLeft):
			out = out[len(arrowLeft):]
		default:
			t.Fatalf("unexpected drag-arrow output %q", out)
		}
	}
}
