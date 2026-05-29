package git

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

func ListMaintenanceRepos(ctx context.Context) ([]string, error) {
	stdout, _, err := RunContext(ctx, "", "config", "--global", "--get-all", "maintenance.repo")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	if stdout == "" {
		return nil, nil
	}
	lines := strings.Split(stdout, "\n")
	var repos []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			repos = append(repos, line)
		}
	}
	return repos, nil
}
