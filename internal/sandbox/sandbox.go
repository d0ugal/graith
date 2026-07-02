// Package sandbox wraps agent processes in an OS-level sandbox. It supports
// pluggable backends: "safehouse" (macOS Seatbelt, the original backend) and
// "nono" (cross-platform: Landlock+seccomp on Linux, Seatbelt on macOS).
//
// The daemon resolves a merged sandbox policy and calls Wrap, which dispatches
// to the configured backend. A backend turns (command, args) plus the policy
// in WrapOpts into the actual command line to exec. Availability reports
// whether a backend can enforce on the current host, so the daemon can fail
// closed when the sandbox is enabled but cannot be enforced.
package sandbox

import "fmt"

// Backend identifiers used in config (`[sandbox] backend = "..."`).
const (
	BackendSafehouse = "safehouse"
	BackendNono      = "nono"
)

// WrapOpts is the resolved, expanded sandbox policy for a single process.
// Paths are already `~`- and glob-expanded and made absolute by the daemon
// before they reach a backend.
type WrapOpts struct {
	// Backend selects the sandbox backend. Empty is invalid at the daemon
	// layer (backend must be chosen explicitly); Wrap defaults it to safehouse
	// for backward compatibility of the low-level helper.
	Backend string

	WorktreeDir string
	ReadDirs    []string
	WriteDirs   []string
	Features    []string
	EnvKeys     []string

	// BackendCommand overrides the backend binary name/path. Empty means the
	// backend's own default ("safehouse" / "nono"). Formerly SafehouseCommand.
	BackendCommand string

	// ProfilePath, for the nono backend, is where the generated per-session
	// profile is written. The daemon points it under RuntimeDir so it is
	// readable inside the sandbox and survives for the process lifetime and
	// resume. Empty means the backend writes to an os.CreateTemp file.
	ProfilePath string
}

// Backend turns a resolved sandbox policy into a wrapped command.
type Backend interface {
	// Name is the config value that selects this backend.
	Name() string
	// Availability reports whether this backend can enforce on this host now.
	// command overrides the backend binary; empty uses the backend default.
	Availability(command string) Availability
	// Wrap returns the command and args to exec so the child runs sandboxed.
	Wrap(command string, args []string, opts WrapOpts) (string, []string, error)
}

// Availability describes whether a backend can enforce a sandbox right now,
// and if so whether enforcement is degraded (some requested controls are
// unavailable but core filesystem confinement still holds).
type Availability struct {
	// CanEnforce is false when the backend cannot enforce at all (binary
	// missing, wrong OS, kernel too old, version below the pin). The daemon
	// must fail closed in that case.
	CanEnforce bool
	// Degraded is true when enforcement works but some controls are missing
	// (e.g. Landlock present but no network-filtering ABI). Filesystem
	// confinement still holds; the daemon runs but should surface the state.
	Degraded bool
	// Detail is a human-readable explanation for logs / doctor / errors.
	Detail string
}

func backendByName(name string) (Backend, error) {
	switch name {
	case "", BackendSafehouse:
		return safehouseBackend{}, nil
	case BackendNono:
		return nonoBackend{}, nil
	default:
		return nil, fmt.Errorf("unknown sandbox backend %q (want %q or %q)", name, BackendSafehouse, BackendNono)
	}
}

// Wrap dispatches to the configured backend and returns the command+args to
// exec. An empty opts.Backend defaults to safehouse for backward compatibility.
func Wrap(command string, args []string, opts WrapOpts) (string, []string, error) {
	be, err := backendByName(opts.Backend)
	if err != nil {
		return "", nil, err
	}

	return be.Wrap(command, args, opts)
}

// CheckAvailability reports whether the named backend can enforce on this host.
// command overrides the backend binary; empty uses the backend default.
func CheckAvailability(backend, command string) (Availability, error) {
	be, err := backendByName(backend)
	if err != nil {
		return Availability{}, err
	}

	return be.Availability(command), nil
}

// Available reports whether the default (safehouse) backend can enforce.
// Retained for callers that predate pluggable backends.
func Available() bool {
	return safehouseBackend{}.Availability("").CanEnforce
}

// AvailableCommand reports whether the safehouse backend can enforce with the
// given binary name. Retained for backward compatibility.
func AvailableCommand(command string) bool {
	return safehouseBackend{}.Availability(command).CanEnforce
}
