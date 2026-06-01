package client

import (
	"errors"
	"os"
	"os/exec"
)

// RunShellInWorktree spawns an interactive shell with its working directory
// set to the given worktree path, and GRAITH_WORKTREE exported in the env.
// A non-zero exit status from the shell is not treated as an error since
// interactive shells commonly inherit the status of their last command.
func RunShellInWorktree(worktreePath string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Dir = worktreePath
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"GRAITH_WORKTREE="+worktreePath,
		"GRAITH_ATTACHED=1",
	)

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}
