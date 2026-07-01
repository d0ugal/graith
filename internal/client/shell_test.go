package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunShellInWorktree_InvalidShell(t *testing.T) {
	t.Setenv("SHELL", "/nonexistent/shell")

	err := RunShellInWorktree(t.TempDir())
	if err == nil {
		t.Fatal("expected error for invalid shell, got nil")
	}
}

func TestRunShellInWorktree_InvalidWorktree(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")

	err := RunShellInWorktree("/nonexistent/worktree/path")
	if err == nil {
		t.Fatal("expected error for invalid worktree path, got nil")
	}
}

func TestRunShellInWorktree_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fakeshell")

	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
		t.Fatalf("write fakeshell: %v", err)
	}

	t.Setenv("SHELL", script)

	err := RunShellInWorktree(dir)
	if err != nil {
		t.Fatalf("non-zero exit should not be an error, got: %v", err)
	}
}

func TestRunShellInWorktree_Success(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fakeshell")

	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: script/binary must be executable
		t.Fatalf("write fakeshell: %v", err)
	}

	t.Setenv("SHELL", script)

	err := RunShellInWorktree(dir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
