package cli

import (
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
)

func parseKeyByte(s string) client.PassthroughBinding {
	if len(s) != 1 || s[0] < 0x20 || s[0] >= 0x7f {
		return client.PassthroughBinding{}
	}

	return client.NewPassthroughBinding(s[0])
}

// parsePrefixKey decodes the configured attach-prefix key through the canonical
// config parser so the CLI honours a printable literal prefix byte-for-byte —
// including uppercase "A" and the space byte — instead of trimming/lowercasing
// the whole value (issue #1233). An unparseable value (already rejected by
// config validation at load) falls back to the historical ctrl+b default.
func parsePrefixKey(s string) byte {
	if b, ok := config.ParsePrefixByte(s); ok {
		return b
	}

	return 0x02 // default ctrl+b
}
