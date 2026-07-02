package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// MinNonoVersion is the minimum nono version graith requires. nono is pre-1.0
// and its profile schema / CLI can shift between releases, so graith pins a
// floor and refuses to run below it rather than risk emitting a mis-shaped
// profile. Facts in this backend are pinned to nono v0.66.0.
const MinNonoVersion = "0.66.0"

// nonoBackend runs the agent under nono (`nono run --profile <file> -- ...`).
// nono's Linux backend is Landlock (filesystem enforcement) + seccomp
// (supervisor-notify layer); its macOS backend is Seatbelt. graith generates a
// per-session JSON profile (JSONC-compatible) rather than ad-hoc flags, because
// nono can only restrict the environment via a profile (`environment.allow_vars`),
// and ad-hoc flags inherit *all* env — a credential-leak regression versus
// safehouse's `--env-pass` allowlist.
type nonoBackend struct{}

func (nonoBackend) Name() string { return BackendNono }

func (nonoBackend) Availability(command string) Availability {
	return nonoAvailability(command, lookNono, nonoVersionOutput, landlockState)
}

// nonoProfile is the subset of nono's profile schema graith generates. Fields
// map onto nono v0.66.0's documented profile format. Empty slices/maps are
// omitted so the emitted profile stays minimal.
type nonoProfile struct {
	Extends     string             `json:"extends"`
	Meta        nonoProfileMeta    `json:"meta"`
	Workdir     nonoProfileWorkdir `json:"workdir"`
	Filesystem  nonoProfileFS      `json:"filesystem"`
	Environment *nonoProfileEnv    `json:"environment,omitempty"`
}

type nonoProfileMeta struct {
	Name string `json:"name"`
}

type nonoProfileWorkdir struct {
	Access string `json:"access"`
}

type nonoProfileFS struct {
	Allow []string `json:"allow,omitempty"`
	Read  []string `json:"read,omitempty"`
	// Write is deliberately unused: nono's filesystem.write is WRITE-ONLY (no
	// read-back, no delete), which breaks agents that read files they wrote.
	// graith maps both write_dirs and the worktree to Allow (read+write).
	Write      []string `json:"write,omitempty"`
	UnixSocket []string `json:"unix_socket,omitempty"`
	// Deny re-denies read-only paths that fall under a nono default-writable
	// prefix (/tmp, $TMPDIR), so a "read-only" read_dir there isn't silently
	// writable via nono's system_write_linux group.
	Deny []string `json:"deny,omitempty"`
}

type nonoProfileEnv struct {
	// AllowVars is nono's env allowlist. It has NO omitempty and buildNonoProfile
	// always populates it (empty slice, not nil) so the marshalled profile always
	// carries "allow_vars": []. An absent block makes nono inherit the daemon's
	// entire environment (fail-open credential leak); an explicit empty allowlist
	// scrubs all env (fail-closed).
	AllowVars []string `json:"allow_vars"`
}

// defaultWritablePrefixes are paths nono grants write to by default on Linux
// via its system_write_linux group (/tmp, /dev/null, /proc/self/fd) plus
// $TMPDIR. A read-only grant under one of these is silently writable, so
// buildNonoProfile re-denies such paths.
func defaultWritablePrefixes() []string {
	prefixes := []string{"/tmp"}
	if td := os.Getenv("TMPDIR"); td != "" {
		prefixes = append(prefixes, filepath.Clean(td))
	}

	return prefixes
}

// underDefaultWritable reports whether path is at or under a nono
// default-writable prefix (/tmp or $TMPDIR).
func underDefaultWritable(path string) bool {
	return isWithinAny(path, defaultWritablePrefixes())
}

// isWithin reports whether path is prefix itself or nested under it.
func isWithin(path, prefix string) bool {
	if prefix == "" {
		return false
	}

	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)

	return path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator))
}

func isWithinAny(path string, prefixes []string) bool {
	for _, pre := range prefixes {
		if isWithin(path, pre) {
			return true
		}
	}

	return false
}

// buildNonoProfile compiles a resolved graith policy into a nono profile.
//
// Mapping (Phase 1):
//   - worktree            -> filesystem.allow (read+write recursive) + workdir rw
//   - write_dirs          -> filesystem.allow (read+write recursive)
//   - read_dirs           -> filesystem.read  (read-only recursive)
//   - EnvKeys             -> environment.allow_vars (env allowlist; the env-leak
//     fix). Always emitted, even when EnvKeys is empty: an absent block makes
//     nono inherit ALL of the daemon's env (fail-open), so an empty allowlist
//     (scrub everything) is the fail-closed default.
//   - feature "ssh"       -> filesystem.unix_socket [$SSH_AUTH_SOCK] (agent socket only)
//   - feature "process-control" -> no-op under nono (default signal_mode already
//     permits same-sandbox signals; see design doc §C5)
//   - any other feature   -> warned (returned in warnings), not silently dropped
//
// "extends": "default" pulls in nono's audited deny groups (deny_credentials,
// deny_shell_history, ...) and base system paths, so graith need not enumerate
// them. sshAuthSock is $SSH_AUTH_SOCK resolved by the caller ("" if unset).
func buildNonoProfile(name string, opts WrapOpts, sshAuthSock string) (nonoProfile, []string) {
	var warnings []string

	p := nonoProfile{
		Extends: "default",
		Meta:    nonoProfileMeta{Name: name},
		Workdir: nonoProfileWorkdir{Access: "readwrite"},
	}

	if opts.WorktreeDir != "" {
		p.Filesystem.Allow = append(p.Filesystem.Allow, opts.WorktreeDir)
	}

	// write_dirs (and the worktree above) map to Allow = read+write. NEVER to
	// filesystem.write, which is write-only under nono.
	p.Filesystem.Allow = append(p.Filesystem.Allow, opts.WriteDirs...)

	// read_dirs map to read-only. But nono grants write to /tmp and $TMPDIR by
	// default (system_write_linux), so a read-only path located there would be
	// silently writable. Re-deny those to preserve the read-only guarantee.
	for _, rd := range opts.ReadDirs {
		p.Filesystem.Read = append(p.Filesystem.Read, rd)

		if underDefaultWritable(rd) && !isWithinAny(rd, opts.WriteDirs) && !isWithin(rd, opts.WorktreeDir) {
			p.Filesystem.Deny = append(p.Filesystem.Deny, rd)
			warnings = append(warnings, fmt.Sprintf("read-only path %q is under a nono default-writable dir (/tmp or $TMPDIR); re-denied to keep it read-only", rd))
		}
	}

	for _, f := range opts.Features {
		switch f {
		case "ssh":
			if sshAuthSock == "" {
				warnings = append(warnings, "feature \"ssh\" requested but SSH_AUTH_SOCK is unset; no agent socket granted")

				continue
			}

			p.Filesystem.UnixSocket = append(p.Filesystem.UnixSocket, sshAuthSock)
		case "process-control":
			// No-op under nono: its default signal_mode already permits
			// same-sandbox signals. This gates under safehouse but not nono
			// (documented cross-backend divergence, design doc §C5).
		default:
			warnings = append(warnings, fmt.Sprintf("feature %q has no nono mapping and was ignored", f))
		}
	}

	// Always emit environment.allow_vars for the nono backend, even when the
	// allowlist is empty. Under nono an ABSENT environment block means the
	// sandboxed process inherits the daemon's ENTIRE environment (a
	// credential-leak fail-open). An empty allow_vars scrubs all env instead —
	// the safe, fail-closed direction. The security boundary must not depend on
	// a caller always populating EnvKeys.
	allowVars := opts.EnvKeys
	if allowVars == nil {
		allowVars = []string{}
	}

	p.Environment = &nonoProfileEnv{AllowVars: allowVars}

	return p, warnings
}

func (nonoBackend) Wrap(command string, args []string, opts WrapOpts) (string, []string, error) {
	nono := opts.BackendCommand
	if nono == "" {
		nono = BackendNono
	}

	name := opts.profileName()

	profile, warnings := buildNonoProfile(name, opts, os.Getenv("SSH_AUTH_SOCK"))
	for _, w := range warnings {
		// Best-effort: the daemon has structured logging, but Wrap has no
		// logger; surface via stderr so warnings aren't silently lost.
		fmt.Fprintln(os.Stderr, "graith: nono sandbox:", w)
	}

	profilePath, err := writeNonoProfile(profile, opts.ProfilePath)
	if err != nil {
		return "", nil, fmt.Errorf("write nono profile: %w", err)
	}

	wrapped := []string{"run", "--profile", profilePath, "--", command}
	wrapped = append(wrapped, args...)

	return nono, wrapped, nil
}

// profileName derives a stable, filesystem-safe profile name from the worktree.
func (opts WrapOpts) profileName() string {
	base := filepath.Base(opts.WorktreeDir)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "session"
	}

	return "graith-" + base
}

// writeNonoProfile marshals the profile to JSON and writes it. When path is
// non-empty the daemon has chosen a stable location (under RuntimeDir) that is
// readable inside the sandbox and lives for the process lifetime; the parent
// directory is created. Otherwise a temp file is used.
func writeNonoProfile(p nonoProfile, path string) (string, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}

	if path == "" {
		f, err := os.CreateTemp("", "graith-nono-*.json")
		if err != nil {
			return "", err
		}

		defer func() { _ = f.Close() }()

		if _, err := f.Write(data); err != nil {
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

// --- availability -----------------------------------------------------------

// lookNono / nonoVersionOutput / landlockState are seams so nonoAvailability is
// unit-testable without nono installed.

func lookNono(command string) (string, error) {
	if command == "" {
		command = BackendNono
	}

	return exec.LookPath(command)
}

func nonoVersionOutput(command string) (string, error) {
	if command == "" {
		command = BackendNono
	}

	out, err := exec.Command(command, "--version").Output()
	if err != nil {
		return "", err
	}

	return string(out), nil
}

// landlockKind classifies the host's Landlock enforcement capability.
type landlockKind int

const (
	// landlockNotApplicable: not Linux (e.g. macOS Seatbelt handles enforcement).
	landlockNotApplicable landlockKind = iota
	// landlockNotEnforced: Linux but Landlock unavailable (kernel too old / disabled).
	landlockNotEnforced
	// landlockPartial: FS enforcement works but ABI predates network filtering (v4/6.7).
	landlockPartial
	// landlockFull: FS + network-filtering ABI available.
	landlockFull
)

type landlockInfo struct {
	kind   landlockKind
	detail string
}

// landlockState reports the host's Landlock enforcement capability.
func landlockState() landlockInfo {
	if runtime.GOOS != "linux" {
		return landlockInfo{kind: landlockNotApplicable, detail: runtime.GOOS + " uses Seatbelt, not Landlock"}
	}

	rel, err := kernelRelease()
	if err != nil {
		return landlockInfo{kind: landlockNotEnforced, detail: "could not read kernel release: " + err.Error()}
	}

	return classifyLandlock(rel)
}

// classifyLandlock maps a kernel release string to a Landlock capability.
//   - < 5.13         : Landlock absent          -> NotEnforced
//   - 5.13 .. < 6.7  : FS control, no TCP filter -> Partial
//   - >= 6.7         : FS + TCP filtering (ABI v4) -> Full
func classifyLandlock(release string) landlockInfo {
	major, minor, ok := parseKernelVersion(release)
	if !ok {
		return landlockInfo{kind: landlockNotEnforced, detail: "unrecognised kernel release " + strconv.Quote(release)}
	}

	switch {
	case major < 5 || (major == 5 && minor < 13):
		return landlockInfo{
			kind:   landlockNotEnforced,
			detail: fmt.Sprintf("kernel %d.%d has no Landlock (needs 5.13+)", major, minor),
		}
	case major == 5 || (major == 6 && minor < 7):
		return landlockInfo{
			kind:   landlockPartial,
			detail: fmt.Sprintf("kernel %d.%d: Landlock filesystem enforcement, no network filtering (needs 6.7+ for ABI v4)", major, minor),
		}
	default:
		return landlockInfo{
			kind:   landlockFull,
			detail: fmt.Sprintf("kernel %d.%d: Landlock with network filtering", major, minor),
		}
	}
}

var kernelVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)`)

func parseKernelVersion(release string) (major, minor int, ok bool) {
	m := kernelVersionRe.FindStringSubmatch(release)
	if m == nil {
		return 0, 0, false
	}

	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, false
	}

	minor, err = strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, false
	}

	return major, minor, true
}

var nonoVersionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parseNonoVersion extracts a semver triple from `nono --version` output
// (typically "nono 0.66.0").
func parseNonoVersion(out string) (major, minor, patch int, ok bool) {
	m := nonoVersionRe.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, 0, false
	}

	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])

	return major, minor, patch, true
}

// versionAtLeast reports whether (aMaj.aMin.aPat) >= min ("x.y.z").
func versionAtLeast(aMaj, aMin, aPat int, minVer string) bool {
	m := nonoVersionRe.FindStringSubmatch(minVer)
	if m == nil {
		return true // no pin to enforce
	}

	minMaj, _ := strconv.Atoi(m[1])
	minMin, _ := strconv.Atoi(m[2])
	minPat, _ := strconv.Atoi(m[3])

	switch {
	case aMaj != minMaj:
		return aMaj > minMaj
	case aMin != minMin:
		return aMin > minMin
	default:
		return aPat >= minPat
	}
}

// nonoAvailability applies the design doc's fail-closed matrix (§B2):
//   - binary absent / version below pin / Landlock NotEnforced -> CanEnforce=false
//   - Landlock Partial (FS but no net ABI) -> CanEnforce=true, Degraded=true
//     (v1 emits no network policy, so FS confinement is sufficient)
//   - macOS / Landlock Full -> CanEnforce=true
func nonoAvailability(
	command string,
	look func(string) (string, error),
	versionOut func(string) (string, error),
	llState func() landlockInfo,
) Availability {
	if _, err := look(command); err != nil {
		bin := command
		if bin == "" {
			bin = BackendNono
		}

		return Availability{CanEnforce: false, Detail: fmt.Sprintf("nono binary %q not found in PATH", bin)}
	}

	out, err := versionOut(command)
	if err != nil {
		return Availability{CanEnforce: false, Detail: "could not run `nono --version`: " + err.Error()}
	}

	maj, minr, pat, ok := parseNonoVersion(out)
	if !ok {
		return Availability{CanEnforce: false, Detail: "could not parse nono version from " + strconv.Quote(strings.TrimSpace(out))}
	}

	if !versionAtLeast(maj, minr, pat, MinNonoVersion) {
		return Availability{
			CanEnforce: false,
			Detail:     fmt.Sprintf("nono %d.%d.%d is below the required minimum %s", maj, minr, pat, MinNonoVersion),
		}
	}

	ll := llState()
	switch ll.kind {
	case landlockNotEnforced:
		return Availability{CanEnforce: false, Detail: ll.detail}
	case landlockPartial:
		return Availability{CanEnforce: true, Degraded: true, Detail: ll.detail}
	case landlockFull, landlockNotApplicable:
		return Availability{CanEnforce: true, Detail: ll.detail}
	default:
		return Availability{CanEnforce: false, Detail: "unknown Landlock state"}
	}
}
