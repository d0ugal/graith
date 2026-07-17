package transcript

import (
	"strings"
	"testing"
)

// bigStr returns a string of n 'z' bytes, used to inflate a JSONL record past a
// scanner cap.
func bigStr(n int) string {
	return strings.Repeat("z", n)
}

// --- boundedLineReader core contract -------------------------------------------------

func TestBoundedLineReaderEnforcesExactCap(t *testing.T) {
	// A record of exactly max bytes is kept; max+1 is dropped. This is the
	// property bufio.Scanner lost by flooring the effective cap at the 64 KiB
	// initial buffer (issue #1295).
	const capBytes = 8

	r := newBoundedLineReader(strings.NewReader("12345678\n123456789\nshort\n"), capBytes)

	var got []string

	for r.scan() {
		got = append(got, string(r.bytes()))
	}

	if r.err != nil {
		t.Fatalf("unexpected err: %v", r.err)
	}

	want := []string{"12345678", "short"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("records = %v, want %v", got, want)
	}

	if r.oversized != 1 {
		t.Fatalf("oversized = %d, want 1 (the 9-byte record)", r.oversized)
	}
}

func TestBoundedLineReaderContinuesPastOversizedMiddle(t *testing.T) {
	// The defect the issue's continuation requirement targets: an over-cap record
	// in the MIDDLE of the stream must not terminate iteration. Every later valid
	// record is still delivered.
	in := "afore\n" + bigStr(100) + "\nefter\n" + bigStr(100) + "\nsyne\n"

	r := newBoundedLineReader(strings.NewReader(in), 8)

	var got []string

	for r.scan() {
		got = append(got, string(r.bytes()))
	}

	want := []string{"afore", "efter", "syne"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("records = %v, want %v", got, want)
	}

	if r.oversized != 2 {
		t.Fatalf("oversized = %d, want 2", r.oversized)
	}
}

func TestBoundedLineReaderOversizedAtEOFNoTrailingNewline(t *testing.T) {
	// A final over-cap record without a trailing newline is counted once and does
	// not yield a phantom record.
	r := newBoundedLineReader(strings.NewReader("ok\n"+bigStr(50)), 8)

	var got []string

	for r.scan() {
		got = append(got, string(r.bytes()))
	}

	if len(got) != 1 || got[0] != "ok" {
		t.Fatalf("records = %v, want [ok]", got)
	}

	if r.oversized != 1 {
		t.Fatalf("oversized = %d, want 1", r.oversized)
	}
}

func TestBoundedLineReaderRecordSpanningReadBuffer(t *testing.T) {
	// A valid record larger than the internal working buffer (so ReadSlice returns
	// ErrBufferFull mid-record) is reassembled intact when the cap allows it.
	big := bigStr(scanReadBufferSize * 3)

	r := newBoundedLineReader(strings.NewReader("a\n"+big+"\nb\n"), scanReadBufferSize*4)

	var got []string

	for r.scan() {
		got = append(got, string(r.bytes()))
	}

	if r.oversized != 0 {
		t.Fatalf("oversized = %d, want 0", r.oversized)
	}

	if len(got) != 3 || got[1] != big {
		t.Fatalf("middle record not reassembled intact (len %d)", len(got[1]))
	}
}

func TestBoundedLineReaderLimitRecordsCountsSkipped(t *testing.T) {
	// A drained over-cap record still counts toward the physical-record window, so
	// an oversized blob cannot push a bounded scan past its limit.
	in := bigStr(50) + "\n" + bigStr(50) + "\nthird\nfourth\n"

	r := newBoundedLineReader(strings.NewReader(in), 8).limitRecords(3)

	var got []string

	for r.scan() {
		got = append(got, string(r.bytes()))
	}

	if len(got) != 1 || got[0] != "third" {
		t.Fatalf("records = %v, want [third] (records 1-2 drained, limit reached before 'fourth')", got)
	}

	if r.oversized != 2 {
		t.Fatalf("oversized = %d, want 2", r.oversized)
	}
}

// --- reader-level buffer-floor regressions (issue #1295) -----------------------------

// assertReadSubFloorCap is the buffer-floor regression body: a valid record
// between the configured 1 KiB cap and the old 64 KiB initial-buffer floor was
// silently accepted before the fix. It must now be dropped under the small cap.
func assertReadSubFloorCap(t *testing.T, read func(string) ([]Turn, int, error), line string) {
	t.Helper()
	resetScanLimits(t)

	path := writeLines(t, []string{line})

	turns, dropped, err := read(path)
	if err != nil {
		t.Fatalf("read (default cap): %v", err)
	}

	if len(turns) != 1 || dropped != 0 {
		t.Fatalf("default cap: got %d turns, %d dropped; want 1 turn, 0 dropped", len(turns), dropped)
	}

	Configure(1024, 0)

	turns, dropped, err = read(path)
	if err != nil {
		t.Fatalf("read (1 KiB cap): %v", err)
	}

	if len(turns) != 0 || dropped != 1 {
		t.Fatalf("1 KiB cap: got %d turns, %d dropped; want 0 turns, 1 dropped", len(turns), dropped)
	}
}

func TestClaudeReadEnforcesSubFloorCap(t *testing.T) {
	// ~4 KiB line: above a 1 KiB cap, below the 64 KiB floor that used to hide it.
	line := `{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"` + bigStr(4*1024) + `"}}`
	assertReadSubFloorCap(t, claudeReader{}.read, line)
}

func TestClaudeUsageEnforcesSubFloorCap(t *testing.T) {
	resetScanLimits(t)

	line := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","id":"m1","usage":{"input_tokens":7,"output_tokens":3},"content":"` + bigStr(4*1024) + `"}}`
	path := writeLines(t, []string{line})

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage (default cap): %v", err)
	}

	if u.Total() != 10 || u.Dropped != 0 {
		t.Fatalf("default cap: total=%d dropped=%d; want 10, 0", u.Total(), u.Dropped)
	}

	Configure(1024, 0)

	u, err = claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage (1 KiB cap): %v", err)
	}

	if u.Total() != 0 || u.Dropped != 1 {
		t.Fatalf("1 KiB cap: total=%d dropped=%d; want 0, 1", u.Total(), u.Dropped)
	}
}

func TestCodexReadEnforcesSubFloorCap(t *testing.T) {
	line := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + bigStr(4*1024) + `"}]}}`
	assertReadSubFloorCap(t, codexReader{}.read, line)
}

func TestCodexUsageEnforcesSubFloorCap(t *testing.T) {
	resetScanLimits(t)

	// A single token_count line inflated past 1 KiB via a large ignored payload
	// field, so the whole record trips the cap.
	line := `{"type":"event_msg","payload":{"type":"token_count","pad":"` + bigStr(4*1024) + `","info":{"total_token_usage":{"input_tokens":40,"cached_input_tokens":0,"output_tokens":0,"total_tokens":40}}}}`
	path := writeLines(t, []string{line})

	u, err := codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage (default cap): %v", err)
	}

	if u.Total() != 40 || u.Dropped != 0 {
		t.Fatalf("default cap: total=%d dropped=%d; want 40, 0", u.Total(), u.Dropped)
	}

	Configure(1024, 0)

	u, err = codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage (1 KiB cap): %v", err)
	}

	if u.Found || u.Dropped != 1 {
		t.Fatalf("1 KiB cap: found=%v dropped=%d; want found=false, dropped=1", u.Found, u.Dropped)
	}
}

func TestCodexMetadataEnforcesSubFloorCap(t *testing.T) {
	resetScanLimits(t)

	// session_meta line inflated past 1 KiB via a long cwd.
	pad := "/glen/" + bigStr(4*1024)
	line := `{"type":"session_meta","payload":{"id":"sess-1","cwd":"` + pad + `"}}`
	path := writeLines(t, []string{line})

	if cwd, ok := codexRolloutCwd(path); !ok || cwd != pad {
		t.Fatalf("default cap: cwd=%q ok=%v; want the padded cwd", cwd, ok)
	}

	if id, ok := CodexRolloutID(path); !ok || id != "sess-1" {
		t.Fatalf("default cap: id=%q ok=%v; want sess-1", id, ok)
	}

	Configure(0, 1024) // tighten the metadata cap only

	if cwd, ok := codexRolloutCwd(path); ok {
		t.Fatalf("1 KiB metadata cap: cwd=%q ok=%v; want not found (line over cap)", cwd, ok)
	}

	if id, ok := CodexRolloutID(path); ok {
		t.Fatalf("1 KiB metadata cap: id=%q ok=%v; want not found (line over cap)", id, ok)
	}
}

// TestCodexMetadataDrainsWithinWindow proves drain/continue is allowed inside the
// first-5-physical-record window: an oversized leading blob is skipped and the
// session_meta on the next physical line is still found.
func TestCodexMetadataDrainsWithinWindow(t *testing.T) {
	resetScanLimits(t)
	Configure(0, 1024)

	lines := []string{
		`{"type":"event_msg","pad":"` + bigStr(4*1024) + `"}`, // oversized under the 1 KiB metadata cap
		`{"type":"session_meta","payload":{"id":"sess-2","cwd":"/bothy"}}`,
	}
	path := writeLines(t, lines)

	if cwd, ok := codexRolloutCwd(path); !ok || cwd != "/bothy" {
		t.Fatalf("cwd=%q ok=%v; want /bothy (found after draining the oversized leader)", cwd, ok)
	}

	if id, ok := CodexRolloutID(path); !ok || id != "sess-2" {
		t.Fatalf("id=%q ok=%v; want sess-2 (found after draining the oversized leader)", id, ok)
	}
}

// TestCodexMetadataStopsAtPhysicalWindow proves the physical-record bound is
// exactly 5: session_meta behind 4 oversized records (physical position 5) is
// found, but behind 5 (position 6) it is not — an oversized blob must not let the
// scan drain an entire rollout looking for metadata past the top-of-file window.
func TestCodexMetadataStopsAtPhysicalWindow(t *testing.T) {
	resetScanLimits(t)
	Configure(0, 1024)

	oversized := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = `{"type":"event_msg","pad":"` + bigStr(4*1024) + `"}`
		}

		return out
	}

	meta := `{"type":"session_meta","payload":{"id":"deep","cwd":"/strath"}}`

	// Position 5 (four oversized leaders): within the window → found.
	within := writeLines(t, append(oversized(4), meta))
	if cwd, ok := codexRolloutCwd(within); !ok || cwd != "/strath" {
		t.Fatalf("position 5: cwd=%q ok=%v; want /strath (within the 5-record window)", cwd, ok)
	}

	// Position 6 (five oversized leaders): past the window → not found.
	beyond := writeLines(t, append(oversized(5), meta))
	if cwd, ok := codexRolloutCwd(beyond); ok {
		t.Fatalf("position 6: cwd=%q ok=%v; want not found (past the 5-record window)", cwd, ok)
	}

	if id, ok := CodexRolloutID(beyond); ok {
		t.Fatalf("position 6: id=%q ok=%v; want not found (past the 5-record window)", id, ok)
	}
}

// --- reader-level mid-stream continuation regressions --------------------------------
//
// Each uses an oversized record that exceeds BOTH the configured cap and the old
// 64 KiB initial-buffer floor, so the pre-fix bufio.Scanner returned ErrTooLong
// and lost every later record. The fix drains the over-cap record and continues.

// assertReadOversizedMiddleKeepsLater is the continuation regression body: an
// over-cap record in the middle of the stream (exceeding both the 1 KiB cap and
// the old 64 KiB floor) must be drained, not fatal, so the later valid records
// still land as turns "afore" then "efter".
func assertReadOversizedMiddleKeepsLater(t *testing.T, read func(string) ([]Turn, int, error), lines []string) {
	t.Helper()
	resetScanLimits(t)
	Configure(1024, 0)

	path := writeLines(t, lines)

	turns, dropped, err := read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (the oversized record)", dropped)
	}

	if len(turns) != 2 || turns[0].Text != "afore" || turns[1].Text != "efter" {
		t.Fatalf("turns = %+v, want [afore, efter]", turns)
	}
}

func TestClaudeReadOversizedMiddleKeepsLaterRecords(t *testing.T) {
	// The active leaf is u2 (last user/assistant in file order); its chain reaches
	// back to u1. Pre-fix, u2 was never read (scan stopped at the oversized line)
	// so the leaf was u1 and "efter" was lost.
	lines := []string{
		`{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"afore"}}`,
		`{"type":"assistant","uuid":"big","parentUuid":"u1","message":{"role":"assistant","content":"` + bigStr(128*1024) + `"}}`,
		`{"type":"user","uuid":"u2","parentUuid":"u1","message":{"role":"user","content":"efter"}}`,
	}
	assertReadOversizedMiddleKeepsLater(t, claudeReader{}.read, lines)
}

func TestClaudeUsageOversizedMiddleKeepsLaterRecords(t *testing.T) {
	resetScanLimits(t)
	Configure(1024, 0)

	lines := []string{
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","id":"m1","usage":{"input_tokens":10,"output_tokens":0}}}`,
		`{"type":"assistant","uuid":"big","message":{"role":"assistant","id":"m2","usage":{"input_tokens":1,"output_tokens":0},"content":"` + bigStr(128*1024) + `"}}`,
		`{"type":"assistant","uuid":"a3","message":{"role":"assistant","id":"m3","usage":{"input_tokens":20,"output_tokens":0}}}`,
	}
	path := writeLines(t, lines)

	u, err := claudeReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	// Pre-fix, scanning stopped at the oversized m2 line and m3 was lost (total
	// would be 10). The fix drains m2 and counts m1 + m3.
	if u.Input != 30 {
		t.Fatalf("Input = %d, want 30 (m1 + m3; later record must survive)", u.Input)
	}

	if u.Dropped != 1 {
		t.Fatalf("Dropped = %d, want 1 (the oversized record)", u.Dropped)
	}
}

func TestCodexReadOversizedMiddleKeepsLaterRecords(t *testing.T) {
	lines := []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"afore"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + bigStr(128*1024) + `"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"efter"}]}}`,
	}
	assertReadOversizedMiddleKeepsLater(t, codexReader{}.read, lines)
}

func TestCodexUsageOversizedMiddleNoStaleSnapshot(t *testing.T) {
	resetScanLimits(t)
	Configure(1024, 0)

	mk := func(total int64) string {
		return `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
			`{"input_tokens":` + i64(total) + `,"cached_input_tokens":0,"output_tokens":0,"total_tokens":` + i64(total) + `}}}}`
	}
	lines := []string{
		mk(100),
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + bigStr(128*1024) + `"}]}}`,
		mk(900), // newer cumulative snapshot AFTER the oversized record
	}
	path := writeLines(t, lines)

	u, err := codexReader{}.usage(path)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	// Pre-fix, the oversized middle record halted scanning and the reader cached
	// the stale 100 snapshot. The fix drains it and the final 900 wins.
	if u.Total() != 900 {
		t.Fatalf("Total = %d, want 900 (final cumulative, not the stale pre-oversized 100)", u.Total())
	}

	if u.Dropped != 1 {
		t.Fatalf("Dropped = %d, want 1 (the oversized record)", u.Dropped)
	}
}
