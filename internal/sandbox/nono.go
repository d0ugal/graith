package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
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

func (nonoBackend) Availability(command string, req Requirements) Availability {
	return nonoAvailability(command, req, lookNono, nonoVersionOutput, landlockState)
}

// nonoProfile is the subset of nono's profile schema graith generates. Fields
// map onto nono v0.66.0's documented profile format. Empty slices/maps are
// omitted so the emitted profile stays minimal.
type nonoProfile struct {
	Extends     string              `json:"extends"`
	Meta        nonoProfileMeta     `json:"meta"`
	Workdir     nonoProfileWorkdir  `json:"workdir"`
	Filesystem  nonoProfileFS       `json:"filesystem"`
	Environment *nonoProfileEnv     `json:"environment,omitempty"`
	Security    *nonoProfileSec     `json:"security,omitempty"`
	Network     *nonoProfileNetwork `json:"network,omitempty"`
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
	Write []string `json:"write,omitempty"`
	// ReadFile / AllowFile grant single files (not directories). ReadFile is
	// read-only; AllowFile is read+write (like Allow, NOT the write-only Write).
	// graith maps read_files -> ReadFile and write_files -> AllowFile. These let
	// a caller grant e.g. ~/.claude.json without exposing all of $HOME.
	ReadFile   []string `json:"read_file,omitempty"`
	AllowFile  []string `json:"allow_file,omitempty"`
	UnixSocket []string `json:"unix_socket,omitempty"`
}

type nonoProfileEnv struct {
	// AllowVars is nono's env allowlist. It has NO omitempty and buildNonoProfile
	// always populates it (empty slice, not nil) so the marshalled profile always
	// carries "allow_vars": []. An absent block makes nono inherit the daemon's
	// entire environment (fail-open credential leak); an explicit empty allowlist
	// scrubs all env (fail-closed).
	AllowVars []string `json:"allow_vars"`
}

// nonoProfileSec maps to nono's profile "security" section. Only signal_mode is
// emitted by graith today (verified against nono v0.66.0's profile schema:
// security.signal_mode ∈ {isolated, allow_same_sandbox, allow_all}).
type nonoProfileSec struct {
	SignalMode string `json:"signal_mode,omitempty"`
}

// nonoProfileNetwork maps to nono's profile "network" section. Verified against
// nono v0.66.0's profile schema: network.block (bool) and network.allow_domain
// ([]string). Empty allow_domain is omitted so an unrestricted block still
// validates.
type nonoProfileNetwork struct {
	Block       bool     `json:"block,omitempty"`
	AllowDomain []string `json:"allow_domain,omitempty"`
}

// defaultWritablePrefixes are paths nono grants write to by default on Linux
// via its system_write_linux group (/tmp, /dev/null, /proc/self/fd) plus
// $TMPDIR. nono cannot make a subpath of one of these read-only: Landlock has
// no deny-under-an-allowed-parent semantics (it is a hard validation error) and
// macOS Seatbelt deny removes read as well as write. buildNonoProfile therefore
// rejects a read-only grant under one of these prefixes rather than emit a
// profile that silently fails to enforce read-only.
func defaultWritablePrefixes() []string {
	prefixes := []string{"/tmp"}
	if td := os.Getenv("TMPDIR"); td != "" {
		prefixes = append(prefixes, filepath.Clean(td))
	}

	return prefixes
}

// underDefaultWritable reports whether path is at or under a nono
// default-writable prefix (/tmp or $TMPDIR).
//
// Known limitations (both pre-date this guard and are lexical, not resolved):
//   - Symlink aliases are not resolved. On macOS /tmp is a symlink to
//     /private/tmp; a grant spelled as the resolved target (or a config path that
//     is itself a symlink into /tmp) is not caught here. Grant paths reach this
//     code via config.ExpandPath (Abs+Clean, no EvalSymlinks).
//   - $TMPDIR is read from the daemon's environment, not the (possibly
//     agent/MCP-overridden) TMPDIR handed to the nono child. Normal sessions are
//     unaffected because GRAITH_TMPDIR is added to write_dirs (so reads there are
//     intentionally writable and exempt); only a custom TMPDIR in agent/MCP env
//     could slip a read-only grant past this check.
//
// Resolving these needs symlink evaluation and threading the effective child
// environment through WrapOpts; tracked as follow-up beyond issue #789.
func underDefaultWritable(path string) bool {
	return isWithinAny(path, defaultWritablePrefixes())
}

// isRegularFile reports whether path exists and is a regular file (not a
// directory). nono's directory-grant lists (filesystem.read/allow) reject a
// non-directory path at profile parse, so a read_dirs/write_dirs entry that
// points at a single file must be routed to the file-grant form instead. A
// path that doesn't exist (or can't be stat'd) is treated as a directory,
// preserving the prior behaviour; non-existent paths are filtered upstream.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return info.Mode().IsRegular()
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
// Mapping:
//   - worktree            -> filesystem.allow (read+write recursive) + workdir rw
//   - write_dirs          -> filesystem.allow (read+write recursive); a single-file
//     entry is routed to filesystem.allow_file (nono rejects a file in a dir grant)
//   - read_dirs           -> filesystem.read  (read-only recursive); a single-file
//     entry is routed to filesystem.read_file (nono rejects a file in a dir grant)
//   - write_files         -> filesystem.allow_file (read+write single file)
//   - read_files          -> filesystem.read_file  (read-only single file)
//   - EnvKeys             -> environment.allow_vars (env allowlist; the env-leak
//     fix). Always emitted, even when EnvKeys is empty: an absent block makes
//     nono inherit ALL of the daemon's env (fail-open), so an empty allowlist
//     (scrub everything) is the fail-closed default.
//   - feature "ssh"       -> filesystem.unix_socket [$SSH_AUTH_SOCK] (agent socket only)
//   - feature "process-control" -> no-op unless SignalMode is set; see below
//   - SignalMode          -> security.signal_mode (Phase 2: opt-in isolation)
//   - Network             -> network.block / network.allow_domain (Phase 2 egress)
//   - any other feature   -> warned (returned in warnings), not silently dropped
//
// process-control / SignalMode (design doc §C5, Phase 2): nono's default
// signal_mode (allow_same_sandbox) already permits same-sandbox signals, so
// "process-control" alone is a no-op under nono. Setting SignalMode =
// "isolated" makes it meaningful: the sandboxed process can no longer signal
// any other process. graith emits security.signal_mode only when SignalMode is
// non-empty, leaving nono's base default otherwise.
//
// "extends" defaults to "default", which pulls in nono's audited deny groups
// (deny_credentials, deny_shell_history, ...) and base system paths, so graith
// need not enumerate them. opts.Profile overrides that base with a maintained
// registry profile (e.g. "always-further/claude") so a known agent's upstream
// file grants are inherited instead of hand-listed; graith's own
// filesystem.allow/read and environment.allow_vars are still layered on top and
// win. sshAuthSock is $SSH_AUTH_SOCK resolved by the caller ("" if unset).
//
// A read-only read_dirs/read_files entry that falls under a nono
// default-writable prefix (/tmp, $TMPDIR) is rejected with an error: nono
// cannot enforce read-only there (see readOnlyUnderWritableErr), so graith fails
// closed with a clear config error rather than emit a profile that pretends to.
func buildNonoProfile(name string, opts WrapOpts, sshAuthSock string) (nonoProfile, []string, error) {
	var warnings []string

	extends := opts.Profile
	if extends == "" {
		extends = "default"
	}

	p := nonoProfile{
		Extends: extends,
		Meta:    nonoProfileMeta{Name: name},
		Workdir: nonoProfileWorkdir{Access: "readwrite"},
	}

	if opts.WorktreeDir != "" {
		p.Filesystem.Allow = append(p.Filesystem.Allow, opts.WorktreeDir)
	}

	// write_dirs (and the worktree above) map to Allow = read+write. NEVER to
	// filesystem.write, which is write-only under nono. A write_dirs entry that
	// is actually a single file is routed to allow_file instead, because nono's
	// directory-grant list rejects a non-directory path at profile parse (and
	// the sandbox is fail-closed, so that aborts the whole session).
	for _, wd := range opts.WriteDirs {
		if isRegularFile(wd) {
			p.Filesystem.AllowFile = append(p.Filesystem.AllowFile, wd)
		} else {
			p.Filesystem.Allow = append(p.Filesystem.Allow, wd)
		}
	}

	// read_dirs map to read-only. But nono grants write to /tmp and $TMPDIR by
	// default (system_write_linux), and nono cannot make a subpath of a writable
	// prefix read-only (Landlock has no deny-under-allowed-parent; macOS deny
	// removes read too). So a read-only grant there cannot be honoured — reject it
	// with a clear config error rather than emit a profile that lies. A read_dirs
	// entry that is actually a single file is routed to read_file, for the same
	// reason write_dirs files are routed to allow_file above.
	for _, rd := range opts.ReadDirs {
		if underDefaultWritable(rd) && !isWithinAny(rd, opts.WriteDirs) && !isWithin(rd, opts.WorktreeDir) {
			return nonoProfile{}, warnings, readOnlyUnderWritableErr("read-only path", rd)
		}

		if isRegularFile(rd) {
			p.Filesystem.ReadFile = append(p.Filesystem.ReadFile, rd)
		} else {
			p.Filesystem.Read = append(p.Filesystem.Read, rd)
		}
	}

	// write_files map to allow_file = read+write single file. Like write_dirs,
	// NEVER to nono's filesystem.write_file (write-only). An explicit allow_file
	// also punches through the "extends: default" deny_credentials group, which
	// is exactly what a login file like ~/.claude.json needs.
	p.Filesystem.AllowFile = append(p.Filesystem.AllowFile, opts.WriteFiles...)

	// read_files map to read_file = read-only single file, with the same
	// /tmp/$TMPDIR rejection guard as read_dirs (nono cannot make a file under a
	// default-writable prefix read-only, so fail closed rather than emit a lie).
	// The exemptions mirror read_dirs (worktree, write_dirs) plus an exact
	// write_files match: a file granted writable on purpose is fine, and config
	// merge can only append/dedup, so an agent-level write_files entry cannot
	// otherwise override a global read_files entry for the same file.
	for _, rf := range opts.ReadFiles {
		exempt := isWithinAny(rf, opts.WriteDirs) || isWithin(rf, opts.WorktreeDir) || slices.Contains(opts.WriteFiles, rf)
		if underDefaultWritable(rf) && !exempt {
			return nonoProfile{}, warnings, readOnlyUnderWritableErr("read-only file", rf)
		}

		p.Filesystem.ReadFile = append(p.Filesystem.ReadFile, rf)
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
			// No-op on its own under nono: the default signal_mode already
			// permits same-sandbox signals. To actually gate signalling, set
			// [sandbox] signal_mode = "isolated" (handled below). This still
			// gates under safehouse. Documented cross-backend divergence
			// (design doc §C5).
			if opts.SignalMode == "" {
				warnings = append(warnings, "feature \"process-control\" is a no-op under nono without [sandbox] signal_mode = \"isolated\"; set it to gate signalling")
			}
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

	// security.signal_mode: opt-in process-control tightening (Phase 2). Only
	// emitted when set, so nono's base default (allow_same_sandbox) applies
	// otherwise.
	if opts.SignalMode != "" {
		p.Security = &nonoProfileSec{SignalMode: opts.SignalMode}
	}

	// network.block / network.allow_domain (Phase 2 egress). Only emitted when
	// a policy is requested; otherwise nono's allow-by-default holds (matching
	// pre-Phase-2 behaviour). The daemon fail-closes when a network policy is
	// requested on a host whose Landlock ABI cannot filter network (§B2), so by
	// the time we reach here the host can enforce it.
	if opts.Network.IsSet() {
		p.Network = &nonoProfileNetwork{
			Block:       opts.Network.Block,
			AllowDomain: opts.Network.AllowDomains,
		}
	}

	return p, warnings, nil
}

// readOnlyUnderWritableErr builds the config error for a read-only grant that
// falls under a nono default-writable prefix (/tmp, $TMPDIR). nono cannot honour
// a read-only grant there: on Linux, Landlock has no deny-under-an-allowed-parent
// (a deny overlapping the inherited /tmp allow is a hard validation error); on
// macOS, Seatbelt deny removes read as well as write, making the path unreadable.
// Either way the grant is not read-only, so graith fails closed with this error
// rather than emit a profile that claims a guarantee it can't keep.
func readOnlyUnderWritableErr(kind, path string) error {
	return fmt.Errorf(
		"%s %q is under a nono default-writable prefix (/tmp or $TMPDIR); nono cannot "+
			"grant it read-only there (Linux Landlock has no deny-under-allowed-parent and "+
			"macOS deny would make it unreadable). Move it outside /tmp and $TMPDIR, or grant "+
			"it as a writable path",
		kind, path)
}

func (nonoBackend) Wrap(command string, args []string, opts WrapOpts) (string, []string, error) {
	nono := opts.BackendCommand
	if nono == "" {
		nono = BackendNono
	}

	name := opts.profileName()

	profile, warnings, err := buildNonoProfile(name, opts, os.Getenv("SSH_AUTH_SOCK"))
	for _, w := range warnings {
		// Best-effort: the daemon has structured logging, but Wrap has no
		// logger; surface via stderr so warnings aren't silently lost.
		fmt.Fprintln(os.Stderr, "graith: nono sandbox:", w)
	}

	if err != nil {
		// A read-only grant under a default-writable prefix (or any other build
		// error) is a config error nono can't enforce; fail closed.
		return "", nil, err
	}

	profilePath, err := writeNonoProfile(profile, opts.ProfilePath)
	if err != nil {
		return "", nil, fmt.Errorf("write nono profile: %w", err)
	}

	wrapped := []string{"run", "--profile", profilePath}

	// Emit --workdir so nono resolves its workdir from opts.WorktreeDir rather
	// than the process cwd. This matters for --share-worktree sessions: the PTY
	// is spawned with its cwd set to the read-only source worktree, but the
	// read-write workdir is the scratch dir (opts.WorktreeDir). Without an
	// explicit --workdir, nono would resolve the workdir from the cwd (the
	// source worktree) and apply profile.workdir.access = "readwrite" to it,
	// making the source writable and breaking the read-only guarantee. safehouse
	// already passes --workdir for the same reason.
	if opts.WorktreeDir != "" {
		wrapped = append(wrapped, "--workdir", opts.WorktreeDir)
	}

	wrapped = append(wrapped, "--", command)
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
//
// In both cases the profile must outlive this call — the sandboxed process
// reads it for its whole lifetime — so writeNonoProfile never removes the file
// it returns on success. The caller owns removal: the daemon deletes the stable
// profile on session teardown (e.g. SessionManager.Delete; see nonoProfilePath
// for all the sites), and temp-file callers
// that receive the path back (e.g. `gr sandbox why`) must remove it themselves.
// Note that callers reaching writeNonoProfile via sandbox.Wrap with an empty
// ProfilePath (e.g. sandboxed MCP-server processes) do not get the temp path
// back — it lives only inside the generated argv — and so cannot clean it up;
// give those callers a stable ProfilePath if the temp file must be reclaimed.
//
// On the temp-file branch a failed write removes the partial file before
// returning the error, so a caller that only ever sees a returned path never
// leaks one it wasn't told about.
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
//   - Landlock Partial (FS but no net-filtering ABI):
//   - no network policy requested -> CanEnforce=true, Degraded=true
//     (FS confinement is sufficient)
//   - network policy requested   -> CanEnforce=false (ABI-v4 fail-closed:
//     don't pretend to block egress on a kernel that can't filter network)
//   - macOS / Landlock Full -> CanEnforce=true
func nonoAvailability(
	command string,
	req Requirements,
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
		// FS enforced but no network-filtering ABI (Landlock < v4, kernel
		// < 6.7). If a network policy is requested we must fail closed rather
		// than silently not block egress (§B2 ABI-v4 rule). Otherwise FS
		// confinement is sufficient; run degraded.
		if req.Network {
			return Availability{
				CanEnforce: false,
				Detail:     ll.detail + "; a network policy needs Landlock ABI v4 (kernel 6.7+)",
			}
		}

		return Availability{CanEnforce: true, Degraded: true, Detail: ll.detail}
	case landlockFull, landlockNotApplicable:
		return Availability{CanEnforce: true, Detail: ll.detail}
	default:
		return Availability{CanEnforce: false, Detail: "unknown Landlock state"}
	}
}
