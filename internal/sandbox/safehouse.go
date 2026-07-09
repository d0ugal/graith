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

func (safehouseBackend) Availability(command string, req Requirements) Availability {
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

	// safehouse has no network-egress primitive in graith's mapping. A
	// requested network policy would silently not be enforced, which is a
	// fail-open. Fail closed instead (design doc §6/§B2): use backend "nono"
	// for network filtering.
	if req.Network {
		return Availability{
			CanEnforce: false,
			Detail:     "safehouse cannot enforce a network policy; use backend \"nono\" for network filtering",
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

	if err := validateSafehousePaths(opts.ReadFiles...); err != nil {
		return "", nil, fmt.Errorf("read files: %w", err)
	}

	if err := validateSafehousePaths(opts.WriteFiles...); err != nil {
		return "", nil, fmt.Errorf("write files: %w", err)
	}

	if err := validateSafehousePaths(opts.UnixSockets...); err != nil {
		return "", nil, fmt.Errorf("unix sockets: %w", err)
	}

	// safehouse has no file-vs-directory distinction in its flags; Seatbelt path
	// rules apply to files too. Fold file grants into the matching path list so
	// read_files/write_files behave consistently across backends. Unix sockets
	// need a read/write grant to connect() (read-only lets a process stat the
	// socket but not connect), so they join the write list alongside how
	// docker.sock/podman.sock are granted. Unlike nono's connect-only
	// filesystem.unix_socket, safehouse has no connect-only primitive, so this
	// also grants read/write on the socket inode (the minimum Seatbelt needs to
	// permit connect); the wrapped process is the user's own trusted agent.
	readPaths := append(append([]string{}, opts.ReadDirs...), opts.ReadFiles...)
	writePaths := append(append(append([]string{}, opts.WriteDirs...), opts.WriteFiles...), opts.UnixSockets...)

	var wrapped []string

	wrapped = append(wrapped, "--workdir", opts.WorktreeDir)

	if len(readPaths) > 0 {
		wrapped = append(wrapped, "--add-dirs-ro", strings.Join(readPaths, ":"))
	}

	if len(writePaths) > 0 {
		wrapped = append(wrapped, "--add-dirs", strings.Join(writePaths, ":"))
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
