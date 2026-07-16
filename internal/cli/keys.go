package cli

import (
	"strings"

	"github.com/d0ugal/graith/internal/client"
)

func parseKeyByte(s string) client.PassthroughBinding {
	if len(s) != 1 || s[0] < 0x20 || s[0] >= 0x7f {
		return client.PassthroughBinding{}
	}

	return client.NewPassthroughBinding(s[0])
}

func parsePrefixKey(s string) byte {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasPrefix(s, "ctrl+") && len(s) == 6 {
		ch := s[5]
		if ch >= 'a' && ch <= 'z' {
			return ch - 'a' + 1
		}
	}

	if len(s) == 1 {
		return s[0]
	}

	return 0x02 // default ctrl+b
}
