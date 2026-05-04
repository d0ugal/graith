package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestMatchSession(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{Name: "foo", WorktreePath: "/tmp/graith/foo"},
		{Name: "foobar", WorktreePath: "/tmp/graith/foobar"},
		{Name: "nested", WorktreePath: "/tmp/graith/foo/nested"},
	}

	tests := []struct {
		name    string
		cwd     string
		want    string
		wantNil bool
	}{
		{
			name: "exact match",
			cwd:  "/tmp/graith/foo",
			want: "foo",
		},
		{
			name: "subdirectory match",
			cwd:  "/tmp/graith/foo/src",
			want: "foo",
		},
		{
			name: "prefix false positive rejected",
			cwd:  "/tmp/graith/foobar",
			want: "foobar",
		},
		{
			name: "foobar subdirectory does not match foo",
			cwd:  "/tmp/graith/foobar/src",
			want: "foobar",
		},
		{
			name: "most specific match wins",
			cwd:  "/tmp/graith/foo/nested/deep",
			want: "nested",
		},
		{
			name: "exact match on nested",
			cwd:  "/tmp/graith/foo/nested",
			want: "nested",
		},
		{
			name:    "no match",
			cwd:     "/tmp/other",
			wantNil: true,
		},
		{
			name:    "partial name no match",
			cwd:     "/tmp/graith/fo",
			wantNil: true,
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
