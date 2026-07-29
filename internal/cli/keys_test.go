package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/client"
)

func TestParseKeyByte(t *testing.T) {
	tests := []struct {
		input string
		want  client.PassthroughKey
	}{
		{"n", client.NewPassthroughKey('n')},
		{"p", client.NewPassthroughKey('p')},
		{"", client.PassthroughKey{}},
		{"dd", client.PassthroughKey{}},
		{"é", client.PassthroughKey{}},
		{string([]byte{0}), client.PassthroughKey{}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseKeyByte(tt.input)
			if got != tt.want {
				t.Errorf("parseKeyByte(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParsePrefixKey(t *testing.T) {
	tests := []struct {
		input string
		want  byte
	}{
		{"ctrl+b", 0x02},
		{"ctrl+a", 0x01},
		{"ctrl+z", 0x1a},
		{"Ctrl+B", 0x02},
		{"CTRL+A", 0x01},
		{" ctrl+b ", 0x02},
		{"`", '`'},
		{"A", 'A'},
		{" ", ' '},
		{"", 0x02},
		{"nonsense", 0x02},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parsePrefixKey(tt.input)
			if got != tt.want {
				t.Errorf("parsePrefixKey(%q) = %#x, want %#x", tt.input, got, tt.want)
			}
		})
	}
}
