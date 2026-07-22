//go:build libghostty && cgo && ((darwin && arm64) || linux)

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
	ghosttyModule          = "go.mitchellh.com/libghostty"
)

// TestTerminalBackendBuildMetadata guards the terminal backend's production
// packaging boundary, not merely backend selection at runtime. The production
// native tag must omit the pure-Go parser packages from both the package
// dependency graph and the linked PTY probe binary's module metadata. The
// default build retains them until every supported platform has a native path
// or an explicit support decision.
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
		wantGhostty bool
	}{
		{name: "production_native", tags: "libghostty", wantGhostty: true},
		{
			name:        "production_without_cgo",
			tags:        "libghostty",
			environment: []string{"CGO_ENABLED=0"},
			wantCharm:   false,
		},
		{name: "default", wantCharm: true},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			listArgs := taggedGoArgs("list", variant.tags, "-deps", "./internal/pty")
			packages := runGoCommandWithEnvironment(t, repository, variant.environment, listArgs...)
			assertCharmDependencies(t, strings.Fields(packages), variant.wantCharm, "go list")
			assertDependency(
				t,
				strings.Fields(packages),
				ghosttyModule,
				variant.wantGhostty,
				"PTY go list",
			)

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

			modules := readBuildInfoModules(t, binary)
			assertCharmDependencies(t, modules, variant.wantCharm, "binary metadata")
			assertDependency(t, modules, ghosttyModule, variant.wantGhostty, "PTY binary metadata")

			commandPackages := runGoCommandWithEnvironment(
				t,
				repository,
				variant.environment,
				taggedGoArgs("list", variant.tags, "-deps", "./cmd/graith")...,
			)
			assertDependency(
				t,
				strings.Fields(commandPackages),
				charmVTModule,
				variant.wantCharm,
				"command go list (Ultraviolet remains UI-only via Bubble Tea/Lip Gloss)",
			)
			assertDependency(
				t,
				strings.Fields(commandPackages),
				ghosttyModule,
				variant.wantGhostty,
				"command go list",
			)

			commandBinary := filepath.Join(t.TempDir(), "graith-build-probe")
			runGoCommandWithEnvironment(
				t,
				repository,
				variant.environment,
				taggedGoArgs("build", variant.tags, "-trimpath", "-o", commandBinary, "./cmd/graith")...,
			)
			commandModules := readBuildInfoModules(t, commandBinary)
			assertDependency(
				t,
				commandModules,
				charmVTModule,
				variant.wantCharm,
				"command binary metadata (Ultraviolet remains UI-only via Bubble Tea/Lip Gloss)",
			)
			assertDependency(
				t,
				commandModules,
				ghosttyModule,
				variant.wantGhostty,
				"command binary metadata",
			)
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
}

func readBuildInfoModules(t *testing.T, binary string) []string {
	t.Helper()

	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatalf("read binary build metadata: %v", err)
	}

	modules := make([]string, 0, len(info.Deps))
	for _, dependency := range info.Deps {
		modules = append(modules, dependency.Path)
	}

	return modules
}

func taggedGoArgs(command, tags string, args ...string) []string {
	result := []string{command}
	if tags != "" {
		result = append(result, "-tags="+tags)
	}

	return append(result, args...)
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
