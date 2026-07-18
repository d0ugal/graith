//go:build libghostty && cgo && (darwin || linux)

package pty

import (
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	charmVTModule          = "github.com/charmbracelet/x/vt"
	charmUltravioletModule = "github.com/charmbracelet/ultraviolet"
)

// TestTerminalBackendBuildMetadata guards the terminal backend's production
// packaging boundary, not merely backend selection at runtime. The production
// native tag must omit the rollback parser packages from both the package
// dependency graph and the linked PTY probe binary's module metadata. The
// explicit comparison tag and the default build intentionally retain them.
func TestTerminalBackendBuildMetadata(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	variants := []struct {
		name        string
		tags        string
		environment []string
		wantCharm   bool
	}{
		{name: "production_native", tags: "libghostty", wantCharm: false},
		{
			name:        "production_without_cgo",
			tags:        "libghostty",
			environment: []string{"CGO_ENABLED=0"},
			wantCharm:   false,
		},
		{
			name:      "dual_backend_comparison",
			tags:      "libghostty,libghostty_compare",
			wantCharm: true,
		},
		{name: "comparison_tag_only", tags: "libghostty_compare", wantCharm: true},
		{name: "default", wantCharm: true},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			listArgs := taggedGoArgs("list", variant.tags, "-deps", "./internal/pty")
			packages := runGoCommandWithEnvironment(t, repository, variant.environment, listArgs...)
			assertCharmDependencies(t, strings.Fields(packages), variant.wantCharm, "go list")

			binary := filepath.Join(t.TempDir(), "pty-build-probe")
			buildArgs := taggedGoArgs(
				"build",
				variant.tags,
				"-trimpath",
				"-o",
				binary,
				"./internal/pty/testdata/buildprobe",
			)
			runGoCommandWithEnvironment(t, repository, variant.environment, buildArgs...)

			info, err := buildinfo.ReadFile(binary)
			if err != nil {
				t.Fatalf("read binary build metadata: %v", err)
			}

			modules := make([]string, 0, len(info.Deps))
			for _, dependency := range info.Deps {
				modules = append(modules, dependency.Path)
			}

			assertCharmDependencies(t, modules, variant.wantCharm, "binary metadata")
		})
	}

	runGoCommandWithEnvironment(
		t,
		repository,
		[]string{"CGO_ENABLED=0"},
		taggedGoArgs(
			"test",
			"libghostty",
			"-count=1",
			"-run=^TestLibghosttyBackendRequiresCGO$",
			"./internal/pty",
		)...,
	)
	runGoCommand(
		t,
		repository,
		taggedGoArgs(
			"test",
			"libghostty_compare",
			"-count=1",
			"-run=^TestDefaultTerminalBackendIsCharm$",
			"./internal/pty",
		)...,
	)

	// Other CLI dependencies use Ultraviolet independently of the screen
	// emulator. x/vt is unique to the rollback terminal and must still disappear
	// from the complete production command graph.
	packages := runGoCommand(
		t,
		repository,
		taggedGoArgs("list", "libghostty", "-deps", "./cmd/graith")...,
	)
	assertDependency(
		t,
		strings.Fields(packages),
		charmVTModule,
		false,
		"production command go list (Ultraviolet remains UI-only via Bubble Tea/Lip Gloss)",
	)
}

func taggedGoArgs(command, tags string, args ...string) []string {
	result := []string{command}
	if tags != "" {
		result = append(result, "-tags="+tags)
	}

	return append(result, args...)
}

func runGoCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()

	return runGoCommandWithEnvironment(t, directory, nil, args...)
}

func runGoCommandWithEnvironment(
	t *testing.T,
	directory string,
	environment []string,
	args ...string,
) string {
	t.Helper()

	command := exec.Command("go", args...)
	command.Dir = directory

	command.Env = append(os.Environ(), environment...)

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, output)
	}

	return string(output)
}

func assertCharmDependencies(t *testing.T, dependencies []string, want bool, source string) {
	t.Helper()

	for _, module := range []string{charmVTModule, charmUltravioletModule} {
		assertDependency(t, dependencies, module, want, source)
	}
}

func assertDependency(t *testing.T, dependencies []string, dependency string, want bool, source string) {
	t.Helper()

	present := false

	for _, candidate := range dependencies {
		if candidate == dependency {
			present = true

			break
		}
	}

	if present != want {
		t.Errorf("%s dependency %s present = %t, want %t", source, dependency, present, want)
	}
}
