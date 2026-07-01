package cli

import "strings"

func parseKeyByte(s string) byte {
	if len(s) == 0 {
		return 0
	}

	return s[0]
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
