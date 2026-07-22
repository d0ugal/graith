package pty

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinAMD64NativeTerminalBackendFailsClosed(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	args := []string{"list", "-tags=libghostty", "-deps", "./internal/pty"}
	command := exec.Command("go", args...)
	command.Dir = repository

	command.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=amd64", "CGO_ENABLED=1")

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, output)
	}

	packages := strings.Fields(string(output))
	assertPlatformDependency(t, packages, "go.mitchellh.com/libghostty", false)
}

func assertPlatformDependency(t *testing.T, packages []string, dependency string, want bool) {
	t.Helper()

	present := false

	for _, candidate := range packages {
		if candidate == dependency {
			present = true

			break
		}
	}

	if present != want {
		t.Fatalf("Darwin amd64 dependency %s present = %t, want %t", dependency, present, want)
	}
}
