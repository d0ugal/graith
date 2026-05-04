package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

func Run(dir string, args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func RunOutput(dir string, args ...string) (string, error) {
	stdout, stderr, err := Run(dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
	return stdout, nil
}

func RunCheck(dir string, args ...string) bool {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func IsInsideGitRepo(dir string) bool {
	return RunCheck(dir, "rev-parse", "--is-inside-work-tree")
}

func RefExists(dir string, ref string) bool {
	return RunCheck(dir, "rev-parse", "--verify", ref)
}

func HasRemote(dir string, name string) bool {
	out, err := RunOutput(dir, "remote")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

func HasUncommittedChanges(dir string) (bool, error) {
	out, err := RunOutput(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(out) > 0, nil
}

func UnpushedCommitCount(worktreePath, baseBranch string) (int, error) {
	out, err := RunOutput(worktreePath, "rev-list", "--count", "origin/"+baseBranch+"..HEAD")
	if err != nil {
		return 0, err
	}
	var n int
	fmt.Sscanf(out, "%d", &n)
	return n, nil
}

func DirtyFiles(dir string) ([]string, error) {
	out, err := RunOutput(dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func UnpushedCommitSummaries(worktreePath, baseBranch string) ([]string, error) {
	baseRef := "origin/" + baseBranch
	if !RefExists(worktreePath, baseRef) {
		baseRef = baseBranch
	}
	out, err := RunOutput(worktreePath, "log", "--oneline", baseRef+"..HEAD")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func RepoRootPath(dir string) (string, error) {
	return RunOutput(dir, "rev-parse", "--show-toplevel")
}
