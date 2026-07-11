package config

import (
	"strings"
	"testing"
)

func TestExpandTrigger(t *testing.T) {
	vars := TriggerVars{
		Name:         "canny-lint",
		Date:         "2026-07-11",
		SessionName:  "braw",
		WorktreePath: "/tmp/bothy",
		ChangedFiles: "glen/a.go, glen/b.go",
		ChangeCount:  "2",
	}
	cases := []struct {
		in   string
		want string
	}{
		{"report {name} on {date}", "report canny-lint on 2026-07-11"},
		{"{change_count} files in {session_name}", "2 files in braw"},
		{"at {worktree_path}: {changed_files}", "at /tmp/bothy: glen/a.go, glen/b.go"},
		{"no tokens here", "no tokens here"},
	}
	for _, tc := range cases {
		got, err := ExpandTrigger(tc.in, vars)
		if err != nil {
			t.Fatalf("ExpandTrigger(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ExpandTrigger(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandTrigger_UnknownVar(t *testing.T) {
	_, err := ExpandTrigger("hello {bogus}", TriggerVars{})
	if err == nil {
		t.Fatal("expected error for unknown var")
	}
	if !strings.Contains(err.Error(), "unknown trigger template variable") {
		t.Errorf("unexpected error: %v", err)
	}
}
