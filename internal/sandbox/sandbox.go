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
	// ReadFiles / WriteFiles grant single files rather than whole directories,
	// for paths that can't be a directory grant without over-sharing (notably
	// single files directly in $HOME, e.g. an agent's ~/.claude.json). Paths are
	// already ~/glob-expanded and absolute. ReadFiles is read-only (nono
	// filesystem.read_file); WriteFiles is read+write (nono filesystem.allow_file,
	// like WriteDirs → allow, not the write-only filesystem.write). The safehouse
	// backend folds them into its read-only / read-write dir lists.
	ReadFiles  []string
	WriteFiles []string
	// UnixSockets grants connect access to existing Unix domain sockets (e.g.
	// the graith daemon socket, so a sandboxed agent can reach the daemon for
	// `gr msg`, `gr status`, etc.). A read-only path grant is NOT enough to
	// connect() to a socket: safehouse folds these into its read/write dir list
	// (a read/write grant is what lets Seatbelt permit the connect, as proven by
	// docker.sock/podman.sock), and nono maps them to filesystem.unix_socket
	// (the same field the "ssh" feature uses for $SSH_AUTH_SOCK). Paths are
	// already absolute.
	UnixSockets []string
	Features    []string
	EnvKeys     []string

	// SignalMode maps to nono's security.signal_mode ("isolated",
	// "allow_same_sandbox", "allow_all"). Empty inherits nono's default. The
	// safehouse backend ignores it.
	SignalMode string

	// Profile (nono only) is the base profile the generated profile extends
	// (nono's "extends" field). Empty means "default" (nono's audited deny
	// groups + base system paths). A maintained registry profile such as
	// "always-further/claude" inherits that agent's upstream file grants. nono
	// MERGES the base with graith's generated profile — collection fields
	// (filesystem.allow/read, environment.allow_vars, …) are unioned, so
	// graith's grants are always present but its env allowlist can only widen
	// the base profile's, never narrow it. A custom base profile is only as
	// tight as the operator has audited it. The safehouse backend ignores it.
	Profile string

	// Network is the optional egress policy. Nil means no network restriction
	// (nono is allow-by-default). When set, the nono backend emits a
	// network.block / network.allow_domain section; the safehouse backend has
	// no network primitive and warns that it cannot enforce it.
	Network *NetworkPolicy

	// BackendCommand overrides the backend binary name/path. Empty means the
	// backend's own default ("safehouse" / "nono"). Formerly SafehouseCommand.
	BackendCommand string

	// ProfilePath, for the nono backend, is where the generated per-session
	// profile is written. The daemon points it under RuntimeDir so it is
	// readable inside the sandbox and survives for the process lifetime and
	// resume. Empty means the backend writes to an os.CreateTemp file.
	ProfilePath string
}

// NetworkPolicy is the resolved egress policy passed to a backend. It mirrors
// config.SandboxNetworkConfig. Nil ⇒ no restriction. It maps onto nono
// v0.66.0's profile network section (network.block / network.allow_domain).
type NetworkPolicy struct {
	// Block denies all outbound network (nono network.block = true).
	Block bool
	// AllowDomains is the L7 proxy allowlist (nono network.allow_domain).
	AllowDomains []string
}

// IsSet reports whether any egress restriction is requested.
func (n *NetworkPolicy) IsSet() bool {
	return n != nil && (n.Block || len(n.AllowDomains) > 0)
}

// Requirements describes the enforcement controls a resolved policy needs, so
// Availability can fail closed when the host cannot enforce one of them. Today
// only network filtering raises the floor beyond base filesystem enforcement
// (it needs Landlock ABI v4 / kernel 6.7+ on Linux).
type Requirements struct {
	// Network is true when a network policy is requested. On Linux this
	// requires Landlock ABI v4; a host that only supports filesystem
	// enforcement must fail closed rather than pretend to block egress.
	Network bool
}

// Backend turns a resolved sandbox policy into a wrapped command.
type Backend interface {
	// Name is the config value that selects this backend.
	Name() string
	// Availability reports whether this backend can enforce on this host now,
	// given the controls the policy requires. command overrides the backend
	// binary; empty uses the backend default.
	Availability(command string, req Requirements) Availability
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

// CheckAvailability reports whether the named backend can enforce on this host,
// given the controls req requires (e.g. network filtering). command overrides
// the backend binary; empty uses the backend default.
func CheckAvailability(backend, command string, req Requirements) (Availability, error) {
	be, err := backendByName(backend)
	if err != nil {
		return Availability{}, err
	}

	return be.Availability(command, req), nil
}

// Available reports whether the default (safehouse) backend can enforce.
// Retained for callers that predate pluggable backends.
func Available() bool {
	return safehouseBackend{}.Availability("", Requirements{}).CanEnforce
}

// AvailableCommand reports whether the safehouse backend can enforce with the
// given binary name. Retained for backward compatibility.
func AvailableCommand(command string) bool {
	return safehouseBackend{}.Availability(command, Requirements{}).CanEnforce
}
