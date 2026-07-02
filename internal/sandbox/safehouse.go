package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// safehouseBackend wraps agent processes with `safehouse wrap` (macOS
// sandbox-exec/Seatbelt). It is the original graith sandbox backend and its
// command construction is unchanged from the pre-pluggable implementation.
type safehouseBackend struct{}

func (safehouseBackend) Name() string { return BackendSafehouse }

func (safehouseBackend) Availability(command string) Availability {
	if command == "" {
		command = BackendSafehouse
	}

	if runtime.GOOS != "darwin" {
		return Availability{
			CanEnforce: false,
			Detail:     "safehouse only supports macOS (this host is " + runtime.GOOS + ")",
		}
	}

	if _, err := exec.LookPath(command); err != nil {
		return Availability{
			CanEnforce: false,
			Detail:     fmt.Sprintf("safehouse binary %q not found in PATH", command),
		}
	}

	return Availability{CanEnforce: true, Detail: "safehouse available"}
}

func (safehouseBackend) Wrap(command string, args []string, opts WrapOpts) (string, []string, error) {
	safehouse := opts.BackendCommand
	if safehouse == "" {
		safehouse = BackendSafehouse
	}

	if err := validateSafehousePaths(opts.WorktreeDir); err != nil {
		return "", nil, fmt.Errorf("workdir: %w", err)
	}

	if err := validateSafehousePaths(opts.ReadDirs...); err != nil {
		return "", nil, fmt.Errorf("read dirs: %w", err)
	}

	if err := validateSafehousePaths(opts.WriteDirs...); err != nil {
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

// validateSafehousePaths rejects colons, which conflict with safehouse's
// colon-separated path lists. (The nono backend passes paths inside a JSON
// profile and has no such restriction.)
func validateSafehousePaths(paths ...string) error {
	for _, p := range paths {
		if strings.Contains(p, ":") {
			return fmt.Errorf("path %q contains a colon, which conflicts with safehouse's colon-separated path format", p)
		}
	}

	return nil
}
