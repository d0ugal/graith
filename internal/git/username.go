package git

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"
)

func DiscoverGitHubUsername(ctx context.Context, repoPath string) (string, error) {
	if u, err := ghCLIUsername(ctx); err == nil && u != "" {
		return u, nil
	}

	if u, err := RunOutput(repoPath, "config", "github.user"); err == nil && u != "" {
		return u, nil
	}

	if remoteURL, err := RunOutput(repoPath, "remote", "get-url", "origin"); err == nil {
		if u, ok := ParseGitHubUsername(remoteURL); ok {
			return u, nil
		}
	}

	return "", errors.New("cannot determine GitHub username; set github_username in config")
}

func ghCLIUsername(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login")

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
