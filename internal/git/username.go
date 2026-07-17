package git

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"

	"github.com/d0ugal/graith/internal/tools"
)

// DiscoverGitHubUsernameWith runs discovery on a caller-pinned git Runner and gh
// executable, so an enclosing lifecycle operation (create/fork/resume) uses ONE
// atomic tool snapshot for the whole username lookup — the gh attempt and both
// git fallbacks share the same generation (#1287). ctx bounds every attempt so
// a stalled git wrapper can't hang past git.username_timeout (#1238).
func DiscoverGitHubUsernameWith(ctx context.Context, r Runner, ghBin, repoPath string) (string, error) {
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

// DiscoverGitHubUsername snapshots the gh and git executables itself for one-shot
// callers.
func DiscoverGitHubUsername(ctx context.Context, repoPath string) (string, error) {
	snap := tools.Snapshot()
	return DiscoverGitHubUsernameWith(ctx, NewRunnerWith(snap.Git), snap.GH, repoPath)
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
