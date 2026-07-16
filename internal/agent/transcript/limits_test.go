package transcript

import (
	"strings"
	"testing"
)

// resetScanLimits clears the process-global scanner caps so a test starts from
// the built-in defaults and does not leak its Configure() into sibling tests.
func resetScanLimits(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		liveMaxLineBytes.Store(0)
		liveMaxMetadataBytes.Store(0)
	})

	liveMaxLineBytes.Store(0)
	liveMaxMetadataBytes.Store(0)
}

func TestScanBufferGettersFallBackToDefaults(t *testing.T) {
	resetScanLimits(t)

	if got := maxLineBytes(); got != DefaultMaxLineBytes {
		t.Errorf("maxLineBytes() = %d, want default %d", got, DefaultMaxLineBytes)
	}

	if got := maxMetadataLineBytes(); got != DefaultMaxMetadataLineBytes {
		t.Errorf("maxMetadataLineBytes() = %d, want default %d", got, DefaultMaxMetadataLineBytes)
	}
}

func TestConfigureSetsScanBuffers(t *testing.T) {
	resetScanLimits(t)

	Configure(123456, 7890)

	if got := maxLineBytes(); got != 123456 {
		t.Errorf("maxLineBytes() = %d, want 123456", got)
	}

	if got := maxMetadataLineBytes(); got != 7890 {
		t.Errorf("maxMetadataLineBytes() = %d, want 7890", got)
	}
}

func TestConfigureIgnoresNonPositive(t *testing.T) {
	resetScanLimits(t)

	Configure(555, 666)
	// A zero/negative value must not clobber a previously-set cap; it means
	// "keep the current setting" (the daemon passes accessor-resolved values, so
	// this only bites a direct caller passing 0).
	Configure(0, -1)

	if got := maxLineBytes(); got != 555 {
		t.Errorf("maxLineBytes() = %d, want 555 (non-positive must not clobber)", got)
	}

	if got := maxMetadataLineBytes(); got != 666 {
		t.Errorf("maxMetadataLineBytes() = %d, want 666 (non-positive must not clobber)", got)
	}
}

// TestConfiguredLineCapGovernsReader is the behavioural regression: a transcript
// line longer than the configured cap is skipped by the reader (counted as a
// dropped line), while the same line reads intact under the default cap. This
// exercises that the scanner buffer is actually driven by Configure (issue
// #1250), not the old fixed 16 MiB literal.
func TestConfiguredLineCapGovernsReader(t *testing.T) {
	resetScanLimits(t)

	// One valid user record whose content is ~200 KiB — well under the default
	// cap, well over a 1 KiB cap.
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = 'z'
	}

	line := `{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"` + string(big) + `"}}`
	path := writeLines(t, []string{line})

	// Default cap: the line fits, one turn, nothing dropped.
	turns, dropped, err := claudeReader{}.read(path)
	if err != nil {
		t.Fatalf("read (default cap): %v", err)
	}

	if len(turns) != 1 || dropped != 0 {
		t.Fatalf("default cap: got %d turns, %d dropped; want 1 turn, 0 dropped", len(turns), dropped)
	}

	// Tiny cap: the line exceeds the scanner buffer and is skipped.
	Configure(1024, 0)

	turns, dropped, err = claudeReader{}.read(path)
	if err != nil {
		t.Fatalf("read (tiny cap): %v", err)
	}

	if len(turns) != 0 || dropped != 1 {
		t.Fatalf("tiny cap: got %d turns, %d dropped; want 0 turns, 1 dropped", len(turns), dropped)
	}
}

// TestScannerCapBelow64KiBIsEnforced is the regression for issue #1295. Every
// transcript scanner previously got a fixed 64 KiB initial buffer, and Go treats
// the effective maximum token size as the LARGER of the Buffer max argument and
// the initial capacity. So a record larger than a configured sub-64-KiB cap but
// smaller than 64 KiB slipped through unchecked — the pre-existing small-cap test
// missed this because its record was 200 KiB (over both limits). With the fix the
// initial buffer is capped at the configured maximum, so a ~4 KiB record (between
// 1 KiB and 64 KiB) is rejected under a 1 KiB cap on every affected path (Claude
// read/usage, Codex read/usage, and the Codex metadata cwd/id scans), while the
// same record reads intact under the defaults.
func TestScannerCapBelow64KiBIsEnforced(t *testing.T) {
	// Padding that puts each record in the 1 KiB–64 KiB window the old fixed
	// initial buffer let through. ASCII, so it needs no JSON escaping.
	pad := strings.Repeat("z", 4*1024)

	// assertReadCapEnforced checks that a single valid record reads as one turn
	// under the default cap but is dropped under a 1 KiB cap, for either reader.
	assertReadCapEnforced := func(t *testing.T, line string, read func(string) ([]Turn, int, error)) {
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
			t.Fatalf("1 KiB cap: got %d turns, %d dropped; want 0 turns, 1 dropped (record between 1 KiB and 64 KiB must be rejected)", len(turns), dropped)
		}
	}

	t.Run("claude read", func(t *testing.T) {
		line := `{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"` + pad + `"}}`
		assertReadCapEnforced(t, line, claudeReader{}.read)
	})

	t.Run("claude usage", func(t *testing.T) {
		resetScanLimits(t)

		// An assistant record carrying usage, padded past 1 KiB via its text block.
		line := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","id":"msg-1","usage":{"input_tokens":5,"output_tokens":7},"content":[{"type":"text","text":"` + pad + `"}]}}`
		path := writeLines(t, []string{line})

		u, err := claudeReader{}.usage(path)
		if err != nil {
			t.Fatalf("usage (default cap): %v", err)
		}

		if !u.Found || u.Input != 5 || u.Output != 7 {
			t.Fatalf("default cap: usage = %+v; want found with input 5, output 7", u)
		}

		Configure(1024, 0)

		u, err = claudeReader{}.usage(path)
		if err != nil {
			t.Fatalf("usage (1 KiB cap): %v", err)
		}

		if u.Found || u.Dropped != 1 {
			t.Fatalf("1 KiB cap: usage = %+v; want not found, 1 dropped", u)
		}
	})

	t.Run("codex read", func(t *testing.T) {
		line := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + pad + `"}]}}`
		assertReadCapEnforced(t, line, codexReader{}.read)
	})

	t.Run("codex usage", func(t *testing.T) {
		resetScanLimits(t)

		// A token_count line padded past 1 KiB with a top-level field the reader
		// ignores, leaving the payload a valid cumulative snapshot.
		line := `{"type":"token_count","pad":"` + pad + `","payload":{"info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":4,"total_tokens":14}}}}`
		path := writeLines(t, []string{line})

		u, err := codexReader{}.usage(path)
		if err != nil {
			t.Fatalf("usage (default cap): %v", err)
		}

		if !u.Found || u.Input != 10 || u.Output != 4 {
			t.Fatalf("default cap: usage = %+v; want found with input 10, output 4", u)
		}

		Configure(1024, 0)

		u, err = codexReader{}.usage(path)
		if err != nil {
			t.Fatalf("usage (1 KiB cap): %v", err)
		}

		if u.Found {
			t.Fatalf("1 KiB cap: usage = %+v; want not found (oversized token_count line skipped)", u)
		}
	})

	t.Run("codex metadata cwd and id", func(t *testing.T) {
		resetScanLimits(t)

		// session_meta padded past 1 KiB with an ignored top-level field.
		line := `{"type":"session_meta","pad":"` + pad + `","payload":{"id":"sess-big","cwd":"/glen/bothy"}}`
		path := writeLines(t, []string{line})

		if cwd, ok := codexRolloutCwd(path); !ok || cwd != "/glen/bothy" {
			t.Fatalf("default metadata cap: cwd = %q, %v; want /glen/bothy, true", cwd, ok)
		}

		if id, ok := CodexRolloutID(path); !ok || id != "sess-big" {
			t.Fatalf("default metadata cap: id = %q, %v; want sess-big, true", id, ok)
		}

		// Tighten only the metadata cap; the oversized header line is now skipped,
		// so neither field resolves (before the fix the 64 KiB initial buffer
		// returned the line despite the 1 KiB cap).
		Configure(0, 1024)

		if cwd, ok := codexRolloutCwd(path); ok {
			t.Fatalf("1 KiB metadata cap: cwd = %q resolved; want skipped", cwd)
		}

		if id, ok := CodexRolloutID(path); ok {
			t.Fatalf("1 KiB metadata cap: id = %q resolved; want skipped", id)
		}
	})
}
