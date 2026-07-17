package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ListMaintenanceRepos discovers the git maintenance-registered repositories.
// The Runner-method form lets the git-pull tick resolve maintenance.repo on the
// same pinned executable it then uses to pull each repo, so one tick stays on a
// single tools generation (#1287).
func (r Runner) ListMaintenanceRepos(ctx context.Context) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home dir: %w", err)
	}

	stdout, _, err := r.RunContextEnv(ctx, home, []string{"HOME=" + home}, "config", "--global", "--get-all", "maintenance.repo")
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

func ListMaintenanceRepos(ctx context.Context) ([]string, error) {
	return NewRunner().ListMaintenanceRepos(ctx)
}
