package pty

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinAMD64TerminalBackendSelection(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	variants := []struct {
		name        string
		tags        string
		cgo         string
		wantCharm   bool
		wantGhostty bool
	}{
		{name: "default", cgo: "0", wantCharm: true},
		{name: "explicit_native", tags: "libghostty", cgo: "1"},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			args := []string{"list"}
			if variant.tags != "" {
				args = append(args, "-tags="+variant.tags)
			}

			args = append(args, "-deps", "./internal/pty")
			command := exec.Command("go", args...)
			command.Dir = repository

			command.Env = append(os.Environ(),
				"GOOS=darwin",
				"GOARCH=amd64",
				"CGO_ENABLED="+variant.cgo,
			)

			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, output)
			}

			packages := strings.Fields(string(output))
			assertPlatformDependency(t, packages, "github.com/charmbracelet/x/vt", variant.wantCharm)
			assertPlatformDependency(t, packages, "go.mitchellh.com/libghostty", variant.wantGhostty)
		})
	}
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
