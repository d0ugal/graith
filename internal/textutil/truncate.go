// Package textutil contains small text-boundary helpers shared across CLI and
// daemon rendering paths.
package textutil

import "unicode/utf8"

// TruncateUTF8Bytes retains at most limit bytes from s and appends suffix when
// truncation is necessary. The retained prefix is backed up to a UTF-8 rune
// boundary; suffix is deliberately outside the byte budget, matching the
// existing display-limit contracts used by inbox and approval previews.
func TruncateUTF8Bytes(s string, limit int, suffix string) string {
	if len(s) <= limit {
		return s
	}

	if limit < 1 {
		return suffix
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + suffix
}
