package cli

import (
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
)

func parseKeyByte(s string) client.PassthroughKey {
	b, err := config.ParseKeybindingActionByte(s)
	if err != nil {
		return client.PassthroughKey{}
	}

	return client.NewPassthroughKey(b)
}

func parsePrefixKey(s string) byte {
	b, err := config.ParseKeybindingPrefixByte(s)
	if err != nil {
		return config.DefaultPrefixByte
	}

	return b
}
