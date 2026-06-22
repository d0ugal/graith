package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestMatchSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{Name: "braw", WorktreePath: "/tmp/graith/braw"},
		{Name: "bonnie", WorktreePath: "/tmp/graith/bonnie"},
		{Name: "glen", WorktreePath: "/tmp/graith/braw/glen"},
		{Name: "wynd", WorktreePath: "/tmp/graith/wynd/"},
	}

	tests := []struct {
		name    string
		cwd     string
		want    string
		wantNil bool
	}{
		{
			name: "exact match",
			cwd:  "/tmp/graith/braw",
			want: "braw",
		},
		{
			name: "subdirectory match",
			cwd:  "/tmp/graith/braw/src",
			want: "braw",
		},
		{
			name: "prefix false positive rejected",
			cwd:  "/tmp/graith/bonnie",
			want: "bonnie",
		},
		{
			name: "bonnie subdirectory does not match braw",
			cwd:  "/tmp/graith/bonnie/src",
			want: "bonnie",
		},
		{
			name: "most specific match wins",
			cwd:  "/tmp/graith/braw/glen/deep",
			want: "glen",
		},
		{
			name: "exact match on glen",
			cwd:  "/tmp/graith/braw/glen",
			want: "glen",
		},
		{
			name:    "no match",
			cwd:     "/tmp/other",
			wantNil: true,
		},
		{
			name:    "partial name no match",
			cwd:     "/tmp/graith/br",
			wantNil: true,
		},
		{
			name: "trailing slash on worktree",
			cwd:  "/tmp/graith/braw/src",
			want: "braw",
		},
		{
			name: "trailing slash on cwd",
			cwd:  "/tmp/graith/braw/",
			want: "braw",
		},
		{
			name: "trailing slash on worktree path",
			cwd:  "/tmp/graith/wynd/sub",
			want: "wynd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchSession(tt.cwd, sessions)
			if tt.wantNil {
				if got != nil {
					t.Errorf("matchSession(%q) = %q, want nil", tt.cwd, got.Name)
				}
				return
			}
			if got == nil {
				t.Fatalf("matchSession(%q) = nil, want %q", tt.cwd, tt.want)
			}
			if got.Name != tt.want {
				t.Errorf("matchSession(%q) = %q, want %q", tt.cwd, got.Name, tt.want)
			}
		})
	}
}
