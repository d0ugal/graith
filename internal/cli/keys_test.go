package cli

import "testing"

func TestParseKeyByte(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		want        byte
		wantEnabled bool
	}{
		{"single ASCII", "n", 'n', true},
		{"empty disables", "", 0, false},
		{"multi-character disables", "dd", 0, false},
		{"multibyte disables", "é", 0, false},
		{"NUL disables", "\x00", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, enabled := parseKeyByte(tt.input).Byte()
			if got != tt.want || enabled != tt.wantEnabled {
				t.Errorf("parseKeyByte(%q) = (%#x, %v), want (%#x, %v)", tt.input, got, enabled, tt.want, tt.wantEnabled)
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
