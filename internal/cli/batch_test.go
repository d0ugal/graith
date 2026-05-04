package cli

import (
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
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
	_, err := parseStaleDuration("garbage")
	if err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestFilterSessions(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)

	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "running-graith", RepoName: "graith", Status: "running", LastAttachedAt: recent},
		{ID: "2", Name: "stopped-graith", RepoName: "graith", Status: "stopped", LastAttachedAt: old},
		{ID: "3", Name: "running-other", RepoName: "other", Status: "running", LastAttachedAt: old},
		{ID: "4", Name: "errored-graith", RepoName: "graith", Status: "errored", LastAttachedAt: old},
		{ID: "5", Name: "never-attached", RepoName: "graith", Status: "stopped", LastAttachedAt: "", CreatedAt: old},
		{ID: "6", Name: "never-attached-recent", RepoName: "graith", Status: "running", LastAttachedAt: "", CreatedAt: recent},
	}

	tests := []struct {
		name    string
		flags   batchFlags
		wantIDs []string
	}{
		{
			name:    "repo filter",
			flags:   batchFlags{repo: "graith"},
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
			flags:   batchFlags{repo: "graith", stopped: true},
			wantIDs: []string{"2", "4", "5"},
		},
		{
			name:    "repo + stopped + stale",
			flags:   batchFlags{repo: "graith", stopped: true, stale: "6h"},
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
