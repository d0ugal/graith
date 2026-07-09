package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	// read_files/write_files behave consistently across backends.
	readPaths := append(append([]string{}, opts.ReadDirs...), opts.ReadFiles...)
	writePaths := append(append([]string{}, opts.WriteDirs...), opts.WriteFiles...)

	var wrapped []string

	wrapped = append(wrapped, "--workdir", opts.WorktreeDir)

	if len(readPaths) > 0 {
		wrapped = append(wrapped, "--add-dirs-ro", strings.Join(readPaths, ":"))
	}

	if len(writePaths) > 0 {
		wrapped = append(wrapped, "--add-dirs", strings.Join(writePaths, ":"))
	}

	// Unix sockets need a CONNECT grant, which Seatbelt classifies as
	// network-outbound — NOT file-read/write. safehouse's default profile only
	// allows network-outbound to IPs and the DNS socket, so connecting to any
	// other Unix socket (e.g. the graith daemon socket) is deny-by-default and
	// no --add-dirs/--add-dirs-ro grant can change that. Emit a Seatbelt
	// fragment with an explicit `(allow network-outbound (remote unix-socket …))`
	// per socket and append it via --append-profile (last-match-wins, so it
	// overrides the default network posture). safehouse also auto-denies writes
	// to the appended fragment, so the sandboxed process can't tamper with it.
	if len(opts.UnixSockets) > 0 {
		fragPath, err := writeSafehouseSocketFragment(opts.UnixSockets, opts.SafehouseFragmentPath)
		if err != nil {
			return "", nil, fmt.Errorf("unix sockets: %w", err)
		}

		wrapped = append(wrapped, "--append-profile", fragPath)
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

// writeSafehouseSocketFragment writes a Seatbelt policy fragment granting
// network-outbound connect access to each socket, for use with safehouse's
// --append-profile. It returns the fragment path. An empty path writes a temp
// file (matching writeNonoProfile); the daemon normally supplies a per-session
// path under RuntimeDir so the fragment survives resume and is cleaned up on
// session delete.
func writeSafehouseSocketFragment(sockets []string, path string) (string, error) {
	var b strings.Builder

	b.WriteString(";; graith: allow connect() to the daemon socket(s).\n")
	b.WriteString(";; Unix-socket connect is network-outbound under Seatbelt, not file access.\n")

	for _, s := range sockets {
		fmt.Fprintf(&b, "(allow network-outbound (remote unix-socket (path-literal %s)))\n", seatbeltString(s))
	}

	data := []byte(b.String())

	if path == "" {
		f, err := os.CreateTemp("", "graith-safehouse-*.sb")
		if err != nil {
			return "", err
		}

		defer func() { _ = f.Close() }()

		if _, err := f.Write(data); err != nil {
			_ = os.Remove(f.Name())
			return "", err
		}

		return f.Name(), nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}

	return path, nil
}

// seatbeltString renders a Go string as a Seatbelt/TinyScheme string literal,
// escaping backslashes and double quotes so a path can't break out of the
// literal or inject policy. Absolute socket paths won't normally contain these,
// but escaping keeps the emitted policy well-formed regardless.
func seatbeltString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)

	return `"` + r.Replace(s) + `"`
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
