package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/tools"
)

// TestRunUsesConfiguredExecutable proves the git call sites honour the
// tools-package executable override instead of the literal "git" (issue #1238).
// A fake git stub echoes a sentinel so the test is independent of a real git.
func TestRunUsesConfiguredExecutable(t *testing.T) {
	t.Cleanup(tools.Reset)

	dir := t.TempDir()
	fakeGit := filepath.Join(dir, "braw-git")

	script := "#!/bin/sh\necho \"canny-marker $@\"\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for exec
		t.Fatalf("write fake git: %v", err)
	}

	tools.Configure(tools.Config{Git: fakeGit})

	out, err := RunOutput(dir, "status")
	if err != nil {
		t.Fatalf("RunOutput = %v", err)
	}

	if out != "canny-marker status" {
		t.Errorf("RunOutput = %q, want the fake git's output", out)
	}
}
