package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

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
		{"`", '`'},
		{"", 0x02},
		{"nonsense", 0x02},
		// Printable literals must survive byte-for-byte: uppercase "A" stays 0x41
		// (not lowercased to 0x61) and the space byte stays 0x20 (not trimmed to
		// empty and silently restored to ctrl+b) — issue #1233.
		{"A", 'A'},
		{"Z", 'Z'},
		{" ", 0x20},
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

// TestParsePrefixKeyMatchesCanonical locks that the CLI prefix parser delegates
// to the shared config.ParsePrefixByte parser (issue #1233): for any valid value
// the two must agree byte-for-byte, so the CLI never re-diverges into its own
// trim/lowercase normalization.
func TestParsePrefixKeyMatchesCanonical(t *testing.T) {
	for _, in := range []string{"ctrl+b", "CTRL+A", "ctrl+z", "A", "Z", "a", " ", "`", "~", "!", ""} {
		want, ok := config.ParsePrefixByte(in)
		if !ok {
			continue // invalid values fall back to the default, covered above
		}

		if got := parsePrefixKey(in); got != want {
			t.Errorf("parsePrefixKey(%q) = %#x, want canonical %#x", in, got, want)
		}
	}
}
