package cli

import (
	"io"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestParseStaleDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"6h", 6 * time.Hour},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"1d6h", 30 * time.Hour},
		{"30m", 30 * time.Minute},
		{"2d12h30m", 60*time.Hour + 30*time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseStaleDuration(tt.input)
			if err != nil {
				t.Fatalf("parseStaleDuration(%q) error: %v", tt.input, err)
			}

			if got != tt.want {
				t.Errorf("parseStaleDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseStaleDurationErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"garbage", "garbage"},
		{"atoi overflow", "99999999999999999999d"},
		{"day multiplication overflow wraps small", "768614336404564651d"},
		{"zero days", "0d"},
		{"negative days", "-1d"},
		{"negative hours", "-6h"},
		{"zero hours", "0h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseStaleDuration(tt.input); err == nil {
				t.Errorf("parseStaleDuration(%q) expected error, got nil", tt.input)
			}
		})
	}
}

func TestFilterSessions(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)

	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "running-croft", RepoName: "croft", Status: "running", LastAttachedAt: recent},
		{ID: "2", Name: "stopped-croft", RepoName: "croft", Status: "stopped", LastAttachedAt: old},
		{ID: "3", Name: "running-thrawn", RepoName: "thrawn", Status: "running", LastAttachedAt: old},
		{ID: "4", Name: "errored-croft", RepoName: "croft", Status: "errored", LastAttachedAt: old},
		{ID: "5", Name: "never-attached", RepoName: "croft", Status: "stopped", LastAttachedAt: "", CreatedAt: old},
		{ID: "6", Name: "never-attached-recent", RepoName: "croft", Status: "running", LastAttachedAt: "", CreatedAt: recent},
	}

	tests := []struct {
		name    string
		flags   batchFlags
		wantIDs []string
	}{
		{
			name:    "repo filter",
			flags:   batchFlags{repo: "croft"},
			wantIDs: []string{"1", "2", "4", "5", "6"},
		},
		{
			name:    "stopped filter",
			flags:   batchFlags{stopped: true},
			wantIDs: []string{"2", "4", "5"},
		},
		{
			name:    "stale filter includes never-attached",
			flags:   batchFlags{stale: "6h"},
			wantIDs: []string{"2", "3", "4", "5"},
		},
		{
			name:    "repo + stopped",
			flags:   batchFlags{repo: "croft", stopped: true},
			wantIDs: []string{"2", "4", "5"},
		},
		{
			name:    "repo + stopped + stale",
			flags:   batchFlags{repo: "croft", stopped: true, stale: "6h"},
			wantIDs: []string{"2", "4", "5"},
		},
		{
			name:    "stale with no matches",
			flags:   batchFlags{stale: "7d"},
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := filterSessions(sessions, &tt.flags)
			if err != nil {
				t.Fatalf("filterSessions error: %v", err)
			}

			gotIDs := make([]string, len(got))
			for i, s := range got {
				gotIDs[i] = s.ID
			}

			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, tt.wantIDs)
			}

			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Errorf("got[%d] = %s, want %s", i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}

// setDiscardOutForBatch swaps the package out writer for a discarding one in the
// requested JSON mode, restoring the original on cleanup.
func setDiscardOutForBatch(t *testing.T, jsonMode bool) {
	t.Helper()

	orig := out

	t.Cleanup(func() { out = orig })

	out = output.NewWithWriter(jsonMode, io.Discard)
}

// TestBatchFlagsActive2 verifies active() reports true when any single filter is
// set and false only when all are empty.
func TestBatchFlagsActive2(t *testing.T) {
	tests := []struct {
		name string
		bf   batchFlags
		want bool
	}{
		{name: "empty is inactive", bf: batchFlags{}, want: false},
		{name: "repo activates", bf: batchFlags{repo: "croft"}, want: true},
		{name: "stopped activates", bf: batchFlags{stopped: true}, want: true},
		{name: "stale activates", bf: batchFlags{stale: "7d"}, want: true},
		{name: "force alone does not activate", bf: batchFlags{force: true}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bf.active(); got != tt.want {
				t.Errorf("active() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAddBatchFlags2 verifies addBatchFlags binds each flag onto the command and
// that parsing populates the backing struct.
func TestAddBatchFlags2(t *testing.T) {
	var bf batchFlags

	cmd := &cobra.Command{Use: "kirk"}
	addBatchFlags(cmd, &bf)

	for _, name := range []string{"repo", "stopped", "stale", "force"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("flag %q not registered", name)
		}
	}

	if err := cmd.Flags().Parse([]string{"--repo", "croft", "--stopped", "--stale", "3d", "--force"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if bf.repo != "croft" || !bf.stopped || bf.stale != "3d" || !bf.force {
		t.Errorf("parsed flags = %+v, want repo=croft stopped stale=3d force", bf)
	}
}

// TestConfirmBatchJSONModeErrors verifies confirmBatch refuses interactive
// confirmation in JSON mode and reports how many sessions would be affected.
func TestConfirmBatchJSONModeErrors(t *testing.T) {
	setDiscardOutForBatch(t, true)

	sessions := []protocol.SessionInfo{
		{Name: "braw"},
		{Name: "canny"},
	}

	confirmed, err := confirmBatch(&cobra.Command{}, "delete", "deleted", sessions)
	if err == nil {
		t.Fatalf("expected error in JSON mode")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestConfirmBatchNonTerminalErrors covers the no-TTY branch: without an
// interactive terminal (the test environment) confirmBatch cannot prompt and
// must error.
func TestConfirmBatchNonTerminalErrors(t *testing.T) {
	setDiscardOutForBatch(t, false)

	sessions := []protocol.SessionInfo{{Name: "dreich"}}

	confirmed, err := confirmBatch(&cobra.Command{}, "stop", "stopped", sessions)
	if err == nil {
		t.Fatalf("expected error with no TTY")
	}

	if confirmed {
		t.Fatalf("expected confirmed=false, got true")
	}
}

// TestFilterSessions2 covers timestamp edge cases in the stale filter that the
// round-1 table did not: unparseable timestamps and empty timestamps are
// skipped, and CreatedAt is used as a fallback for LastAttachedAt.
func TestFilterSessions2(t *testing.T) {
	old := "2020-01-01T00:00:00Z"

	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "garbage-ts", Status: "running", LastAttachedAt: "not-a-time"},
		{ID: "2", Name: "no-ts", Status: "running", LastAttachedAt: "", CreatedAt: ""},
		{ID: "3", Name: "created-fallback", Status: "running", LastAttachedAt: "", CreatedAt: old},
	}

	got, err := filterSessions(sessions, &batchFlags{stale: "1d"})
	if err != nil {
		t.Fatalf("filterSessions error: %v", err)
	}

	// The garbage timestamp and the no-timestamp session are skipped; only the
	// CreatedAt-fallback session (very old) matches the stale filter.
	if len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("got %+v, want only session 3", got)
	}
}

// TestFilterSessions2InvalidStalePropagates verifies an unparseable stale
// duration surfaces as an error rather than silently matching everything.
func TestFilterSessions2InvalidStalePropagates(t *testing.T) {
	if _, err := filterSessions(nil, &batchFlags{stale: "haar"}); err == nil {
		t.Fatalf("expected error for invalid stale duration")
	}
}

// TestParseStaleDuration2 adds combined day+hour forms not covered by round 1.
func TestParseStaleDuration2(t *testing.T) {
	d, err := parseStaleDuration("3d1h")
	if err != nil {
		t.Fatalf("parseStaleDuration error: %v", err)
	}

	if d.Hours() != 73 {
		t.Errorf("parseStaleDuration(3d1h) = %v hours, want 73", d.Hours())
	}
}
