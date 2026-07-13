package client

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestCompactCount(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1k"},
		{1200, "1.2k"},
		{3400, "3.4k"},
		{9999, "9.9k"},
		{10000, "10k"},
		{847000, "847k"},
		{1_200_000, "1.2M"},
		{12_000_000, "12M"},
		{1_361_526, "1.3M"},
		{5_000_000_000, "5B"},
		{-5, "0"},
	}

	for _, c := range cases {
		if got := CompactCount(c.n); got != c.want {
			t.Errorf("CompactCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestCliTokens(t *testing.T) {
	cases := []struct {
		name string
		in   protocol.SessionInfo
		want string
	}{
		{"unknown when nil", protocol.SessionInfo{}, ""},
		{"compact total", protocol.SessionInfo{Tokens: &protocol.TokenInfo{Total: 1_361_526}}, "1.3M"},
		{"degraded marked", protocol.SessionInfo{Tokens: &protocol.TokenInfo{Total: 3400, Degraded: true}}, "3.4k~"},
		{"known zero", protocol.SessionInfo{Tokens: &protocol.TokenInfo{Total: 0}}, "0"},
	}

	for _, c := range cases {
		if got := cliTokens(c.in); got != c.want {
			t.Errorf("%s: cliTokens = %q, want %q", c.name, got, c.want)
		}
	}
}
