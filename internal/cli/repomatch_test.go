package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestMatchesRepo(t *testing.T) {
	session := protocol.SessionInfo{
		RepoName: "croft",
		RepoPath: "/Users/braw/Code/croft",
	}

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"full path", "/Users/braw/Code/croft", true},
		{"suffix segment", "croft", true},
		{"repo name", "croft", true},
		{"multi-segment suffix", "Code/croft", true},
		{"no match", "thrawn", false},
		{"partial name is not a segment suffix", "oft", false},
		{"empty value", "", false},
		{"path prefix without trailing segment", "/Users/braw/Code", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesRepo(session, tt.value); got != tt.want {
				t.Errorf("matchesRepo(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// TestMatchesRepoNameWithoutPath covers the RepoName branch in isolation: a
// session whose RepoName does not appear anywhere in its RepoPath still matches
// on the name.
func TestMatchesRepoNameWithoutPath(t *testing.T) {
	session := protocol.SessionInfo{
		RepoName: "bothy",
		RepoPath: "/Users/canny/src/some-other-dir",
	}

	if !matchesRepo(session, "bothy") {
		t.Errorf("expected match on repo name %q", "bothy")
	}

	if matchesRepo(session, "wynd") {
		t.Errorf("did not expect a match for an unrelated value")
	}
}

// TestFilterSessionsRepoMatchesListMatching locks issue #202: batch --repo must
// select the same sessions as `gr list --repo`, i.e. match on full path, path
// suffix, and repo name — not just strict RepoName equality.
func TestFilterSessionsRepoMatching(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "1", Name: "braw", RepoName: "graith", RepoPath: "/Users/dev/Code/graith", Status: "running"},
		{ID: "2", Name: "canny", RepoName: "graith", RepoPath: "/Users/dev/Code/graith", Status: "running"},
		{ID: "3", Name: "thrawn", RepoName: "croft", RepoPath: "/Users/dev/Code/croft", Status: "running"},
	}

	tests := []struct {
		name    string
		repo    string
		wantIDs []string
	}{
		{"by short name", "graith", []string{"1", "2"}},
		{"by full path", "/Users/dev/Code/graith", []string{"1", "2"}},
		{"by path suffix", "Code/graith", []string{"1", "2"}},
		{"no match", "haar", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := filterSessions(sessions, &batchFlags{repo: tt.repo})
			if err != nil {
				t.Fatalf("filterSessions error: %v", err)
			}

			var gotIDs []string
			for _, s := range got {
				gotIDs = append(gotIDs, s.ID)
			}

			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got IDs %v, want %v", gotIDs, tt.wantIDs)
			}

			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("got IDs %v, want %v", gotIDs, tt.wantIDs)
				}
			}
		})
	}
}
