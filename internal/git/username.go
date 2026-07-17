package git

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"

	"github.com/d0ugal/graith/internal/tools"
)

// DiscoverGitHubUsername resolves the GitHub username via the gh CLI, then two
// git fallbacks (config github.user, then the origin remote URL). ctx bounds the
// WHOLE operation: previously only the gh attempt was context-aware while the
// git fallbacks used the context-less RunOutput, so a stalled configured git
// wrapper could hang create/resume/fork past git.username_timeout even after the
// deadline expired (#1238). The gh and git executables are snapshotted once so
// every attempt runs against a single tools generation (#1287) — a reload
// mid-discovery cannot mix executables.
func DiscoverGitHubUsername(ctx context.Context, repoPath string) (string, error) {
	// Snapshot both executables once (gh for the API attempt, git for the two
	// fallbacks) via the shared pinned Runner, and bound every attempt with the
	// same ctx. Previously only the gh attempt honored ctx while the git
	// fallbacks used the context-less RunOutput, so a stalled configured git
	// wrapper could hang create/resume/fork past git.username_timeout (#1238);
	// resolving per subprocess also let a reload mix executables (#1287).
	ghBin := tools.GH()
	r := NewRunner()

	if u, err := ghCLIUsername(ctx, ghBin); err == nil && u != "" {
		return u, nil
	}

	if u, err := r.RunOutputContext(ctx, repoPath, "config", "github.user"); err == nil && u != "" {
		return u, nil
	}

	if remoteURL, err := r.RunOutputContext(ctx, repoPath, "remote", "get-url", "origin"); err == nil {
		if u, ok := ParseGitHubUsername(remoteURL); ok {
			return u, nil
		}
	}

	return "", errors.New("cannot determine GitHub username; set github_username in config")
}

func ghCLIUsername(ctx context.Context, ghBin string) (string, error) {
	if _, err := exec.LookPath(ghBin); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, ghBin, "api", "user", "--jq", ".login")

	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

func ParseGitHubUsername(remoteURL string) (string, bool) {
	if strings.HasPrefix(remoteURL, "git@github.com:") {
		rest := strings.TrimPrefix(remoteURL, "git@github.com:")

		parts := strings.SplitN(rest, "/", 2)
		if len(parts) == 2 {
			return parts[0], true
		}
	}

	if u, err := url.Parse(remoteURL); err == nil && u.Host == "github.com" {
		parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
		if len(parts) == 2 {
			return parts[0], true
		}
	}

	return "", false
}
