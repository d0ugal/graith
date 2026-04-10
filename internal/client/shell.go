package client

import (
	"os"
	"os/exec"
)

// RunShellInWorktree spawns an interactive shell with its working directory
// set to the given worktree path, and GRAITH_WORKTREE exported in the env.
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
	cmd.Env = append(os.Environ(), "GRAITH_WORKTREE="+worktreePath)

	return cmd.Run()
}
