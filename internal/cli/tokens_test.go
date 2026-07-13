package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestWithCommas(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}

	for _, c := range cases {
		if got := withCommas(c.n); got != c.want {
			t.Errorf("withCommas(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestNewTokenRow(t *testing.T) {
	// Supported + known.
	r := newTokenRow(protocol.SessionInfo{
		Name: "braw", ID: "id1", Agent: "claude",
		Tokens: &protocol.TokenInfo{Input: 10, Output: 20, Total: 30, Degraded: true},
	})
	if !r.Supported || !r.Known || r.Total != 30 || !r.Degraded {
		t.Errorf("row = %+v, want supported+known, total 30, degraded", r)
	}

	// Supported agent, no counts yet.
	r = newTokenRow(protocol.SessionInfo{Name: "canny", Agent: "codex"})
	if !r.Supported || r.Known {
		t.Errorf("row = %+v, want supported but not known", r)
	}

	// Unsupported agent.
	r = newTokenRow(protocol.SessionInfo{Name: "dreich", Agent: "cursor"})
	if r.Supported {
		t.Errorf("row = %+v, want unsupported", r)
	}
}

func TestTokenTableRow(t *testing.T) {
	unsupported := tokenTableRow(tokenRow{Name: "dreich", Agent: "cursor", Supported: false})
	if !strings.Contains(unsupported, "(unsupported)") {
		t.Errorf("unsupported row = %q, want (unsupported)", unsupported)
	}

	unknown := tokenTableRow(tokenRow{Name: "canny", Agent: "codex", Supported: true, Known: false})
	if !strings.Contains(unknown, "(unknown)") {
		t.Errorf("unknown row = %q, want (unknown)", unknown)
	}

	known := tokenTableRow(tokenRow{
		Name: "braw", Agent: "claude", Supported: true, Known: true,
		Input: 1234, Output: 56, Total: 1290, Degraded: true,
	})
	if !strings.Contains(known, "1,234") || !strings.HasSuffix(known, "1,290~") {
		t.Errorf("known row = %q, want commas and trailing ~", known)
	}
}

func TestPrintTokenTable(t *testing.T) {
	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	rows := []tokenRow{
		{Name: "braw", Agent: "claude", Supported: true, Known: true, Input: 100, Output: 200, Total: 300},
		{Name: "canny", Agent: "codex", Supported: true, Known: true, Input: 10, Output: 20, Total: 30},
	}

	printTokenTable(cmd, rows)

	out := buf.String()
	if !strings.Contains(out, "SESSION") || !strings.Contains(out, "TOTAL") {
		t.Errorf("missing header: %q", out)
	}

	// Totals row present for >1 session: 300+30 = 330.
	if !strings.Contains(out, "330") {
		t.Errorf("missing totals row (330): %q", out)
	}
}
