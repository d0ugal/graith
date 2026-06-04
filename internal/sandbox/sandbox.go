package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type WrapOpts struct {
	WorktreeDir      string
	ReadDirs         []string
	WriteDirs        []string
	Features         []string
	EnvKeys          []string
	SafehouseCommand string
}

func Wrap(command string, args []string, opts WrapOpts) (string, []string, error) {
	safehouse := opts.SafehouseCommand
	if safehouse == "" {
		safehouse = "safehouse"
	}

	if err := validatePaths(opts.WorktreeDir); err != nil {
		return "", nil, fmt.Errorf("workdir: %w", err)
	}
	if err := validatePaths(opts.ReadDirs...); err != nil {
		return "", nil, fmt.Errorf("read dirs: %w", err)
	}
	if err := validatePaths(opts.WriteDirs...); err != nil {
		return "", nil, fmt.Errorf("write dirs: %w", err)
	}

	var wrapped []string

	wrapped = append(wrapped, "--workdir", opts.WorktreeDir)

	if len(opts.ReadDirs) > 0 {
		wrapped = append(wrapped, "--add-dirs-ro", strings.Join(opts.ReadDirs, ":"))
	}

	if len(opts.WriteDirs) > 0 {
		wrapped = append(wrapped, "--add-dirs", strings.Join(opts.WriteDirs, ":"))
	}

	if len(opts.Features) > 0 {
		wrapped = append(wrapped, "--enable", strings.Join(opts.Features, ","))
	}

	if len(opts.EnvKeys) > 0 {
		wrapped = append(wrapped, "--env-pass", strings.Join(opts.EnvKeys, ","))
	}

	wrapped = append(wrapped, "--", command)
	wrapped = append(wrapped, args...)

	return safehouse, wrapped, nil
}

func validatePaths(paths ...string) error {
	for _, p := range paths {
		if strings.Contains(p, ":") {
			return fmt.Errorf("path %q contains a colon, which conflicts with safehouse's colon-separated path format", p)
		}
	}
	return nil
}

func Available() bool {
	return AvailableCommand("safehouse")
}

func AvailableCommand(command string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil
}
