package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/tools"
)

// TestDiscoverGitHubUsernameTimeoutBoundsGitFallbacks is the #1238 regression:
// git.username_timeout must bound the WHOLE discovery, not just the gh attempt.
// With gh failing fast, discovery falls through to the git fallbacks; a stalled
// configured git wrapper must be cancelled by the ctx deadline rather than
// hanging create/resume/fork. Before the fix the fallbacks used the context-less
// RunOutput and would block for the wrapper's full sleep.
func TestDiscoverGitHubUsernameTimeoutBoundsGitFallbacks(t *testing.T) {
	dir := setupTestRepo(t)
	stubGH(t, false, "") // gh exits non-zero → discovery falls through to git.
	t.Cleanup(tools.Reset)

	binDir := t.TempDir()
	blockingGit := filepath.Join(binDir, "git")
	// `exec sleep` replaces the shell with the sleep process so CommandContext
	// kills the sleep directly on the deadline. A bare `sleep` would leave the
	// shell's child holding the inherited stdout/stderr pipes, delaying Wait
	// until the 30s sleep exits and making the regression flaky.
	script := "#!/bin/sh\nexec sleep 30\n"

	if err := os.WriteFile(blockingGit, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable
		t.Fatal(err)
	}

	tools.Configure(tools.Config{Git: blockingGit})

	const budget = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	got, err := DiscoverGitHubUsername(ctx, dir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected discovery to fail once the deadline cancels the git fallbacks, got %q", got)
	}

	// A single-process wrapper is killed promptly at the deadline, and the second
	// fallback sees an already-expired ctx, so the whole discovery finishes well
	// within a small multiple of the budget.
	if elapsed > 10*budget {
		t.Fatalf("discovery took %v; the git fallbacks were not bounded by the username_timeout deadline", elapsed)
	}
}

func TestParseGitHubUsername(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantUser string
		wantOK   bool
	}{
		// SSH URLs
		{
			name:     "SSH with .git suffix",
			url:      "git@github.com:braw/croft.git",
			wantUser: "braw",
			wantOK:   true,
		},
		{
			name:     "SSH without .git suffix",
			url:      "git@github.com:canny/glen",
			wantUser: "canny",
			wantOK:   true,
		},
		{
			name:     "SSH with nested path",
			url:      "git@github.com:bonnie-glen/auld-croft.git",
			wantUser: "bonnie-glen",
			wantOK:   true,
		},

		// HTTPS URLs
		{
			name:     "HTTPS with .git suffix",
			url:      "https://github.com/braw/croft.git",
			wantUser: "braw",
			wantOK:   true,
		},
		{
			name:     "HTTPS without .git suffix",
			url:      "https://github.com/braw/croft",
			wantUser: "braw",
			wantOK:   true,
		},
		{
			name:     "HTTPS with hyphenated user",
			url:      "https://github.com/bonnie-braw/auld-croft.git",
			wantUser: "bonnie-braw",
			wantOK:   true,
		},
		{
			name:     "HTTP (no TLS)",
			url:      "http://github.com/canny/croft.git",
			wantUser: "canny",
			wantOK:   true,
		},

		// Non-GitHub URLs — should return empty
		{
			name:     "GitLab URL",
			url:      "https://gitlab.com/braw/croft.git",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "Bitbucket URL",
			url:      "https://bitbucket.org/braw/croft.git",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "SSH non-GitHub",
			url:      "git@gitlab.com:braw/croft.git",
			wantUser: "",
			wantOK:   false,
		},

		// Malformed URLs
		{
			name:     "empty string",
			url:      "",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "just a word",
			url:      "thrawn",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "GitHub URL with no path",
			url:      "https://github.com/",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "GitHub URL with only user",
			url:      "https://github.com/braw",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "SSH with no slash in path",
			url:      "git@github.com:kenneep",
			wantUser: "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotOK := ParseGitHubUsername(tt.url)
			if gotUser != tt.wantUser || gotOK != tt.wantOK {
				t.Errorf("ParseGitHubUsername(%q) = (%q, %v), want (%q, %v)",
					tt.url, gotUser, gotOK, tt.wantUser, tt.wantOK)
			}
		})
	}
}

// stubGH puts a fake `gh` on PATH. If ok is false the stub exits non-zero so
// ghCLIUsername fails and DiscoverGitHubUsername falls through to its other
// sources; if ok is true it prints login and exits 0. This keeps the tests
// deterministic regardless of whether a real, authenticated gh is installed.
func stubGH(t *testing.T, ok bool, login string) {
	t.Helper()

	bin := t.TempDir()

	var script string
	if ok {
		script = "#!/bin/sh\necho " + login + "\n"
	} else {
		script = "#!/bin/sh\nexit 1\n"
	}

	gh := filepath.Join(bin, "gh")
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for exec.LookPath
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDiscoverGitHubUsernameFromGHCov(t *testing.T) {
	dir := setupTestRepo(t)
	stubGH(t, true, "ken-user")

	u, err := DiscoverGitHubUsername(context.Background(), dir)
	if err != nil {
		t.Fatalf("DiscoverGitHubUsername: %v", err)
	}

	if u != "ken-user" {
		t.Errorf("username = %q, want ken-user", u)
	}
}

func TestDiscoverGitHubUsernameFromConfigCov(t *testing.T) {
	dir := setupTestRepo(t)
	stubGH(t, false, "")

	if _, err := RunOutput(dir, "config", "github.user", "canny-user"); err != nil {
		t.Fatalf("set github.user: %v", err)
	}

	u, err := DiscoverGitHubUsername(context.Background(), dir)
	if err != nil {
		t.Fatalf("DiscoverGitHubUsername: %v", err)
	}

	if u != "canny-user" {
		t.Errorf("username = %q, want canny-user", u)
	}
}

func TestDiscoverGitHubUsernameFromRemoteCov(t *testing.T) {
	dir := setupTestRepo(t)
	stubGH(t, false, "")

	if _, err := RunOutput(dir, "remote", "add", "origin", "git@github.com:bonnie-braw/croft.git"); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	u, err := DiscoverGitHubUsername(context.Background(), dir)
	if err != nil {
		t.Fatalf("DiscoverGitHubUsername: %v", err)
	}

	if u != "bonnie-braw" {
		t.Errorf("username = %q, want bonnie-braw", u)
	}
}

func TestDiscoverGitHubUsernameGHBlankFallsThroughCov(t *testing.T) {
	dir := setupTestRepo(t)
	// gh exits 0 but prints only whitespace, so the empty result is rejected
	// and discovery falls through to the remote parse.
	stubGH(t, true, "   ")

	if _, err := RunOutput(dir, "remote", "add", "origin", "https://github.com/canny-glen/croft.git"); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	u, err := DiscoverGitHubUsername(context.Background(), dir)
	if err != nil {
		t.Fatalf("DiscoverGitHubUsername: %v", err)
	}

	if u != "canny-glen" {
		t.Errorf("username = %q, want canny-glen", u)
	}
}

func TestDiscoverGitHubUsernameUnresolvableCov(t *testing.T) {
	dir := setupTestRepo(t)
	stubGH(t, false, "")

	// No github.user config and no origin remote: nothing to discover.
	if _, err := DiscoverGitHubUsername(context.Background(), dir); err == nil {
		t.Error("expected error when username cannot be determined")
	}
}
