package transcript

import "testing"

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
