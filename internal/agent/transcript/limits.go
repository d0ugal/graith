package transcript

import "sync/atomic"

// Scanner buffer defaults. Reading a transcript uses a bufio.Scanner whose token
// buffer is capped so a pathological (or corrupt) line can't exhaust memory. The
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

// Live scanner caps, set by Configure. Stored as atomics because Configure runs
// on the daemon's config-reload goroutine while readers run on session
// goroutines; a zero value means "unset" and the getters fall back to the
// defaults above.
var (
	liveMaxLineBytes     atomic.Int64
	liveMaxMetadataBytes atomic.Int64
)

// Configure sets the scanner buffer caps used by every subsequent transcript
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

// scanBufferStartCap is the initial bufio.Scanner buffer capacity used for
// typical short lines. It matches bufio's own historical default and is only an
// allocation hint, never a cap on its own.
const scanBufferStartCap = 64 * 1024

// newScanBuffer returns the (initial buffer, max token size) pair for a
// bufio.Scanner whose effective per-line cap must equal limit. Go treats the
// effective maximum token size as the LARGER of the Buffer max argument and the
// initial buffer capacity, so a fixed 64 KiB initial capacity silently raised
// the real limit to 64 KiB whenever a caller configured a smaller limit (a line
// up to 64 KiB fit in the initial buffer and was returned before the resize path
// ever consulted the max). Capping the initial capacity at limit keeps the
// effective cap equal to the configured value while never exceeding the 64 KiB
// start size for the common large-limit case (issue #1295).
func newScanBuffer(limit int) ([]byte, int) {
	return make([]byte, 0, min(scanBufferStartCap, limit)), limit
}
