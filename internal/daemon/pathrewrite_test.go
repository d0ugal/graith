package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIncludePathRewrites(t *testing.T) {
	home := "/hame/dougal"
	rewrites := buildIncludePathRewrites(
		home,
		"/hame/dougal/Code/devenv",
		"/data/worktrees/devenv/abc/id/devenv",
		[]IncludedRepoState{
			{RepoPath: "/hame/dougal/Code/grafana", WorktreePath: "/data/worktrees/devenv/abc/id/grafana"},
			{RepoPath: "/opt/croft/examples", WorktreePath: "/data/worktrees/devenv/abc/id/examples"},
		},
	)

	if len(rewrites) != 3 {
		t.Fatalf("want 3 rewrites, got %d", len(rewrites))
	}

	// Main repo under home: both absolute and tilde forms.
	if got := rewrites[0].sourceForms; len(got) != 2 ||
		got[0] != "/hame/dougal/Code/devenv" || got[1] != "~/Code/devenv" {
		t.Errorf("main repo forms = %v", got)
	}

	// Included repo under home: tilde form derived.
	if got := rewrites[1].sourceForms; len(got) != 2 ||
		got[1] != "~/Code/grafana" {
		t.Errorf("grafana forms = %v", got)
	}

	// Included repo NOT under home: absolute form only.
	if got := rewrites[2].sourceForms; len(got) != 1 || got[0] != "/opt/croft/examples" {
		t.Errorf("examples forms = %v (want absolute only)", got)
	}
}

func TestBuildIncludePathRewritesNoHome(t *testing.T) {
	rewrites := buildIncludePathRewrites(
		"",
		"/hame/dougal/Code/devenv",
		"/wt/devenv",
		nil,
	)

	if len(rewrites) != 1 {
		t.Fatalf("want 1 rewrite, got %d", len(rewrites))
	}

	if got := rewrites[0].sourceForms; len(got) != 1 {
		t.Errorf("want absolute form only with empty home, got %v", got)
	}
}

func TestBuildIncludePathRewritesSkipsEmpty(t *testing.T) {
	rewrites := buildIncludePathRewrites("/hame", "", "", nil)
	if len(rewrites) != 0 {
		t.Fatalf("want 0 rewrites for empty main paths, got %d", len(rewrites))
	}
}

func TestRewritePathsInContent(t *testing.T) {
	rewrites := []pathRewrite{
		{
			worktree:    "/data/wt/grafana",
			sourceForms: []string{"/hame/dougal/Code/grafana", "~/Code/grafana"},
		},
		{
			worktree:    "/data/wt/examples",
			sourceForms: []string{"/hame/dougal/Code/examples", "~/Code/examples"},
		},
	}

	tests := []struct {
		name        string
		in          string
		want        string
		wantChanged bool
	}{
		{
			name:        "env var absolute path",
			in:          "GRAFANA_PATH=/hame/dougal/Code/grafana\n",
			want:        "GRAFANA_PATH=/data/wt/grafana\n",
			wantChanged: true,
		},
		{
			name:        "tilde form",
			in:          "GRAFANA_PATH=~/Code/grafana\n",
			want:        "GRAFANA_PATH=/data/wt/grafana\n",
			wantChanged: true,
		},
		{
			name:        "docker-compose bind mount preserves suffix",
			in:          "    volumes:\n      - ~/Code/grafana/conf:/etc/grafana\n",
			want:        "    volumes:\n      - /data/wt/grafana/conf:/etc/grafana\n",
			wantChanged: true,
		},
		{
			name:        "quoted absolute path",
			in:          `path: "/hame/dougal/Code/grafana"`,
			want:        `path: "/data/wt/grafana"`,
			wantChanged: true,
		},
		{
			name:        "multiple repos on separate lines",
			in:          "A=~/Code/grafana\nB=~/Code/examples\n",
			want:        "A=/data/wt/grafana\nB=/data/wt/examples\n",
			wantChanged: true,
		},
		{
			name:        "prefix collision left untouched",
			in:          "PATH=~/Code/grafana-enterprise\n",
			want:        "PATH=~/Code/grafana-enterprise\n",
			wantChanged: false,
		},
		{
			name:        "dotted sibling left untouched",
			in:          "PATH=~/Code/grafana.bak\n",
			want:        "PATH=~/Code/grafana.bak\n",
			wantChanged: false,
		},
		{
			name:        "embedded in longer absolute path left untouched",
			in:          "PATH=/mnt/hame/dougal/Code/grafana\n",
			want:        "PATH=/mnt/hame/dougal/Code/grafana\n",
			wantChanged: false,
		},
		{
			name:        "no match",
			in:          "PATH=~/Code/loch\n",
			want:        "PATH=~/Code/loch\n",
			wantChanged: false,
		},
		{
			name:        "exact match at end of string",
			in:          "PATH=~/Code/grafana",
			want:        "PATH=/data/wt/grafana",
			wantChanged: true,
		},
		{
			name:        "same repo twice",
			in:          "A=~/Code/grafana\nB=~/Code/grafana\n",
			want:        "A=/data/wt/grafana\nB=/data/wt/grafana\n",
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := rewritePathsInContent(tt.in, rewrites)
			if got != tt.want {
				t.Errorf("content:\n got %q\nwant %q", got, tt.want)
			}

			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}

func TestRewritePathsInContentEmptyRewrites(t *testing.T) {
	got, changed := rewritePathsInContent("PATH=~/Code/grafana\n", nil)
	if changed {
		t.Errorf("want no change with empty rewrites")
	}

	if got != "PATH=~/Code/grafana\n" {
		t.Errorf("content mutated: %q", got)
	}
}

func TestRewriteIncludeConfigPaths(t *testing.T) {
	mainWt := t.TempDir()
	incWt := t.TempDir()

	rewrites := []pathRewrite{
		{worktree: "/data/wt/grafana", sourceForms: []string{"~/Code/grafana"}},
	}

	// docker-compose.override.yml in the main worktree references the sibling.
	overridePath := filepath.Join(mainWt, "docker-compose.override.yml")
	if err := os.WriteFile(overridePath, []byte("services:\n  gr:\n    volumes:\n      - ~/Code/grafana:/app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// .env.local with no matching path — should be left byte-identical.
	envPath := filepath.Join(mainWt, ".env.local")
	envBody := "UNRELATED=~/Code/loch\n"

	if err := os.WriteFile(envPath, []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// A file we don't know about — must be ignored even if it has a match.
	otherPath := filepath.Join(mainWt, "config.yaml")
	otherBody := "path: ~/Code/grafana\n"

	if err := os.WriteFile(otherPath, []byte(otherBody), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newTestSM(t)
	sm.rewriteIncludeConfigPaths([]string{mainWt, incWt}, rewrites)

	got, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(got), "/data/wt/grafana:/app") {
		t.Errorf("override not rewritten: %q", got)
	}

	// Untouched files stay byte-identical.
	if b, _ := os.ReadFile(envPath); string(b) != envBody {
		t.Errorf(".env.local changed unexpectedly: %q", b)
	}

	if b, _ := os.ReadFile(otherPath); string(b) != otherBody {
		t.Errorf("unknown file was rewritten: %q", b)
	}
}

func TestRewriteIncludeConfigPathsPreservesMode(t *testing.T) {
	wt := t.TempDir()
	path := filepath.Join(wt, ".env.local")

	if err := os.WriteFile(path, []byte("P=~/Code/grafana\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newTestSM(t)
	sm.rewriteIncludeConfigPaths(
		[]string{wt},
		[]pathRewrite{{worktree: "/wt/grafana", sourceForms: []string{"~/Code/grafana"}}},
	)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	if b, _ := os.ReadFile(path); string(b) != "P=/wt/grafana\n" {
		t.Errorf("content = %q", b)
	}
}

func TestRewriteIncludeConfigPathsNoOpCases(t *testing.T) {
	sm := newTestSM(t)

	// No rewrites: returns immediately, no panic on a nil root.
	sm.rewriteIncludeConfigPaths(nil, nil)

	// Missing files in a real dir: no error, nothing created.
	wt := t.TempDir()
	sm.rewriteIncludeConfigPaths(
		[]string{wt, ""},
		[]pathRewrite{{worktree: "/wt/x", sourceForms: []string{"~/Code/x"}}},
	)

	if entries, _ := os.ReadDir(wt); len(entries) != 0 {
		t.Errorf("expected no files created, got %d", len(entries))
	}
}
