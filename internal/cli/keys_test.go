package cli

import "testing"

func TestParseKeyByte(t *testing.T) {
	tests := []struct {
		input string
		want  byte
	}{
		{"n", 'n'},
		{"p", 'p'},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseKeyByte(tt.input)
			if got != tt.want {
				t.Errorf("parseKeyByte(%q) = %#x, want %#x", tt.input, got, tt.want)
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
