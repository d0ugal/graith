package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// --- safehouse backend (behaviour unchanged from before the refactor) -------

func TestWrapBasic(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: "/hame/user/bothy",
		EnvKeys:     []string{"GRAITH_SESSION_ID", "TERM"},
	}

	cmd, args, err := Wrap("claude", []string{"--session-id", "braw"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}

	want := []string{
		"--workdir", "/hame/user/bothy",
		"--env-pass", "GRAITH_SESSION_ID,TERM",
		"--", "claude", "--session-id", "braw",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}

	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestWrapDefaultsToSafehouse(t *testing.T) {
	cmd, _, err := Wrap("claude", nil, WrapOpts{WorktreeDir: "/tmp/bothy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}
}

func TestWrapUnknownBackend(t *testing.T) {
	_, _, err := Wrap("claude", nil, WrapOpts{Backend: "thrawn", WorktreeDir: "/tmp/bothy"})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestWrapWithFeatures(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: "/tmp/bothy",
		Features:    []string{"ssh", "process-control"},
		EnvKeys:     []string{"TERM"},
	}

	cmd, args, err := Wrap("codex", []string{}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}

	found := false

	for i, a := range args {
		if a == "--enable" && i+1 < len(args) {
			if args[i+1] != "ssh,process-control" {
				t.Errorf("--enable value = %q, want %q", args[i+1], "ssh,process-control")
			}

			found = true

			break
		}
	}

	if !found {
		t.Errorf("--enable not found in args: %v", args)
	}
}

func TestWrapWithReadDirs(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/hame/user/glen", "/opt/wynd"},
		EnvKeys:     []string{"TERM"},
	}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false

	for i, a := range args {
		if a == "--add-dirs-ro" && i+1 < len(args) {
			if args[i+1] != "/hame/user/glen:/opt/wynd" {
				t.Errorf("--add-dirs-ro value = %q, want %q", args[i+1], "/hame/user/glen:/opt/wynd")
			}

			found = true

			break
		}
	}

	if !found {
		t.Errorf("--add-dirs-ro not found in args: %v", args)
	}
}

func TestWrapWithWriteDirs(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: "/tmp/bothy",
		WriteDirs:   []string{"/tmp/croft"},
		EnvKeys:     []string{"TERM"},
	}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false

	for i, a := range args {
		if a == "--add-dirs" && i+1 < len(args) {
			if args[i+1] != "/tmp/croft" {
				t.Errorf("--add-dirs value = %q, want %q", args[i+1], "/tmp/croft")
			}

			found = true

			break
		}
	}

	if !found {
		t.Errorf("--add-dirs not found in args: %v", args)
	}
}

func TestWrapNoEnvKeys(t *testing.T) {
	opts := WrapOpts{Backend: BackendSafehouse, WorktreeDir: "/tmp/bothy"}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, a := range args {
		if a == "--env-pass" {
			t.Error("--env-pass should not be present when EnvKeys is empty")
		}
	}
}

func TestWrapCommandAndArgsAfterSeparator(t *testing.T) {
	opts := WrapOpts{Backend: BackendSafehouse, WorktreeDir: "/tmp/bothy", EnvKeys: []string{"TERM"}}

	_, args, err := Wrap("codex", []string{"resume", "--last"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tail := argsAfterSeparator(t, args)
	if len(tail) != 3 || tail[0] != "codex" || tail[1] != "resume" || tail[2] != "--last" {
		t.Errorf("args after -- = %v, want [codex resume --last]", tail)
	}
}

func TestWrapCustomCommand(t *testing.T) {
	opts := WrapOpts{
		Backend:        BackendSafehouse,
		WorktreeDir:    "/tmp/bothy",
		BackendCommand: "/usr/local/bin/safehouse",
		EnvKeys:        []string{"TERM"},
	}

	cmd, _, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "/usr/local/bin/safehouse" {
		t.Fatalf("cmd = %q, want /usr/local/bin/safehouse", cmd)
	}
}

func TestWrapRejectsColonInWorkdir(t *testing.T) {
	opts := WrapOpts{Backend: BackendSafehouse, WorktreeDir: "/tmp/thrawn:bothy:colons", EnvKeys: []string{"TERM"}}

	_, _, err := Wrap("claude", nil, opts)
	if err == nil {
		t.Fatal("expected error for colon in workdir path")
	}
}

func TestWrapRejectsColonInReadDirs(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/bonnie/glen", "/thrawn:glen"},
		EnvKeys:     []string{"TERM"},
	}

	_, _, err := Wrap("claude", nil, opts)
	if err == nil {
		t.Fatal("expected error for colon in read dir path")
	}
}

func TestWrapAcceptsPathsWithoutColons(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/hame/user/glen", "/opt/wynd"},
		WriteDirs:   []string{"/tmp/croft"},
		EnvKeys:     []string{"TERM"},
	}

	_, _, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error for valid paths: %v", err)
	}
}

func TestAvailableOnlyOnDarwin(t *testing.T) {
	result := Available()
	if runtime.GOOS != "darwin" && result {
		t.Error("Available() should be false on non-darwin")
	}
}

func TestAvailableCommandCustomBinary(t *testing.T) {
	if AvailableCommand("this-binary-does-not-exist-anywhere") {
		t.Error("AvailableCommand should be false for nonexistent binary")
	}
}

// --- nono backend: profile generation ---------------------------------------

func TestBuildNonoProfileFilesystemMapping(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/hame/user/bothy",
		ReadDirs:    []string{"/hame/user/glen"},
		WriteDirs:   []string{"/tmp/croft"},
		EnvKeys:     []string{"PATH", "HOME"},
	}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	if p.Extends != "default" {
		t.Errorf("Extends = %q, want default (inherits nono deny groups)", p.Extends)
	}

	if p.Workdir.Access != "readwrite" {
		t.Errorf("Workdir.Access = %q, want readwrite", p.Workdir.Access)
	}

	if !slices.Contains(p.Filesystem.Allow, "/hame/user/bothy") {
		t.Errorf("filesystem.allow missing worktree: %v", p.Filesystem.Allow)
	}

	if !slices.Contains(p.Filesystem.Allow, "/tmp/croft") {
		t.Errorf("filesystem.allow missing write dir: %v", p.Filesystem.Allow)
	}

	if !slices.Contains(p.Filesystem.Read, "/hame/user/glen") {
		t.Errorf("filesystem.read missing read dir: %v", p.Filesystem.Read)
	}
}

func TestBuildNonoProfileEnvAllowlist(t *testing.T) {
	opts := WrapOpts{
		Backend: BackendNono,
		EnvKeys: []string{"GRAITH_SESSION_ID", "TERM"},
	}

	p, _ := buildNonoProfile("graith-braw", opts, "")
	if p.Environment == nil {
		t.Fatal("environment section missing; env would leak (inherit-all)")
	}

	if !slices.Equal(p.Environment.AllowVars, []string{"GRAITH_SESSION_ID", "TERM"}) {
		t.Errorf("allow_vars = %v, want [GRAITH_SESSION_ID TERM]", p.Environment.AllowVars)
	}
}

func TestBuildNonoProfileNoEnvKeysScrubsEnv(t *testing.T) {
	// With no EnvKeys the nono backend must still emit environment.allow_vars,
	// as an EMPTY allowlist. Omitting the block would make nono inherit the
	// daemon's entire environment (fail-open credential leak); an empty
	// allowlist scrubs all env instead (fail-closed). See issue #694.
	p, _ := buildNonoProfile("graith-neep", WrapOpts{Backend: BackendNono}, "")
	if p.Environment == nil {
		t.Fatal("environment section missing with empty EnvKeys; env would leak (inherit-all)")
	}

	if len(p.Environment.AllowVars) != 0 {
		t.Errorf("allow_vars = %v, want empty allowlist (scrub all env)", p.Environment.AllowVars)
	}
}

func TestBuildNonoProfileEmptyEnvScrubsNotInherits(t *testing.T) {
	// Prove the emitted profile scrubs env rather than inheriting it: the
	// marshalled JSON must carry an explicit "allow_vars": [] so nono clears
	// the environment. A missing allow_vars (null / absent) would inherit all.
	p, _ := buildNonoProfile("graith-neep", WrapOpts{Backend: BackendNono}, "")

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}

	if !strings.Contains(string(data), `"allow_vars":[]`) {
		t.Errorf("profile JSON should contain empty allow_vars (scrub all env), got: %s", data)
	}

	if strings.Contains(string(data), `"allow_vars":null`) {
		t.Errorf("allow_vars must not be null (would inherit all env): %s", data)
	}
}

func TestBuildNonoProfileSSHFeature(t *testing.T) {
	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", Features: []string{"ssh"}}

	p, warnings := buildNonoProfile("graith-bothy", opts, "/run/user/1000/ssh-agent.sock")
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings for ssh with socket set: %v", warnings)
	}

	if !slices.Contains(p.Filesystem.UnixSocket, "/run/user/1000/ssh-agent.sock") {
		t.Errorf("ssh feature did not grant the agent socket: %v", p.Filesystem.UnixSocket)
	}
}

func TestBuildNonoProfileSSHWithoutSocketWarns(t *testing.T) {
	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", Features: []string{"ssh"}}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")
	if len(p.Filesystem.UnixSocket) != 0 {
		t.Errorf("no socket should be granted when SSH_AUTH_SOCK unset: %v", p.Filesystem.UnixSocket)
	}

	if len(warnings) == 0 {
		t.Error("expected a warning when ssh requested but SSH_AUTH_SOCK unset")
	}
}

func TestBuildNonoProfileProcessControlWarnsWithoutSignalMode(t *testing.T) {
	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", Features: []string{"process-control"}}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")
	// process-control alone is a no-op under nono, but graith surfaces a hint
	// (don't silently drop) telling the user to set signal_mode = "isolated".
	if len(warnings) != 1 || !strings.Contains(warnings[0], "signal_mode") {
		t.Errorf("process-control without signal_mode should warn to set signal_mode, got %v", warnings)
	}

	if p.Security != nil {
		t.Errorf("process-control without signal_mode should not emit security section, got %+v", p.Security)
	}

	if len(p.Filesystem.UnixSocket) != 0 {
		t.Errorf("process-control should add no grants, got sockets %v", p.Filesystem.UnixSocket)
	}
}

func TestBuildNonoProfileUnmappedFeatureWarns(t *testing.T) {
	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", Features: []string{"clipboard", "haar"}}

	_, warnings := buildNonoProfile("graith-bothy", opts, "")
	if len(warnings) != 2 {
		t.Fatalf("want 2 warnings for [clipboard haar], got %v", warnings)
	}

	if !strings.Contains(strings.Join(warnings, " "), "clipboard") {
		t.Errorf("warning should mention clipboard: %v", warnings)
	}
}

func TestNonoWrapArgvShapeAndAdversarialPaths(t *testing.T) {
	tmp := t.TempDir()
	profilePath := filepath.Join(tmp, "kirk.json")

	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"--wynd", "/glen:ben"},
		EnvKeys:     []string{"TERM"},
		ProfilePath: profilePath,
	}

	cmd, args, err := Wrap("claude", []string{"resume", "--last"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != BackendNono {
		t.Fatalf("cmd = %q, want nono", cmd)
	}

	want := []string{"run", "--profile", profilePath, "--", "claude", "resume", "--last"}
	if !slices.Equal(args, want) {
		t.Fatalf("argv = %v, want %v", args, want)
	}

	for _, a := range args {
		if a == "--wynd" || a == "/glen:ben" {
			t.Errorf("adversarial path leaked onto argv: %q", a)
		}
	}

	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}

	var got nonoProfile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("profile is not valid JSON: %v", err)
	}

	if !slices.Contains(got.Filesystem.Read, "--wynd") || !slices.Contains(got.Filesystem.Read, "/glen:ben") {
		t.Errorf("adversarial paths not preserved in profile read list: %v", got.Filesystem.Read)
	}
}

func TestNonoWrapWritesReadableProfile(t *testing.T) {
	tmp := t.TempDir()
	profilePath := filepath.Join(tmp, "nested", "braw.json")

	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", EnvKeys: []string{"PATH"}, ProfilePath: profilePath}

	_, _, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("profile not written at %s: %v", profilePath, err)
	}
}

// --- nono backend: availability matrix (design doc §B2) ---------------------

func TestNonoAvailabilityBinaryAbsent(t *testing.T) {
	look := func(string) (string, error) { return "", os.ErrNotExist }

	av := nonoAvailability("", Requirements{}, look, nil, nil)
	if av.CanEnforce {
		t.Error("CanEnforce should be false when nono binary is absent")
	}
}

func TestNonoAvailabilityBelowVersionPin(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono 0.1.0", nil }

	av := nonoAvailability("", Requirements{}, look, ver, func() landlockInfo { return landlockInfo{kind: landlockFull} })
	if av.CanEnforce {
		t.Errorf("CanEnforce should be false below the version pin; detail=%q", av.Detail)
	}
}

func TestNonoAvailabilityNotEnforcedIsHardFail(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono " + MinNonoVersion, nil }
	ll := func() landlockInfo {
		return landlockInfo{kind: landlockNotEnforced, detail: "kernel 5.4 has no Landlock"}
	}

	av := nonoAvailability("", Requirements{}, look, ver, ll)
	if av.CanEnforce {
		t.Error("Landlock NotEnforced with sandbox enabled must be a hard fail (fail-open regression otherwise)")
	}
}

func TestNonoAvailabilityPartialRunsDegraded(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono " + MinNonoVersion, nil }
	ll := func() landlockInfo { return landlockInfo{kind: landlockPartial, detail: "no net filtering"} }

	av := nonoAvailability("", Requirements{}, look, ver, ll)
	if !av.CanEnforce {
		t.Error("Partial Landlock (FS but no net ABI) should still enforce for FS-only v1")
	}

	if !av.Degraded {
		t.Error("Partial Landlock should be reported as degraded")
	}
}

func TestNonoAvailabilityFullEnforces(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono 1.2.3", nil }
	ll := func() landlockInfo { return landlockInfo{kind: landlockFull} }

	av := nonoAvailability("", Requirements{}, look, ver, ll)
	if !av.CanEnforce || av.Degraded {
		t.Errorf("Full Landlock should enforce and not be degraded, got %+v", av)
	}
}

// --- kernel classification ---------------------------------------------------

func TestClassifyLandlock(t *testing.T) {
	cases := []struct {
		release string
		want    landlockKind
	}{
		{"5.4.0-generic", landlockNotEnforced},
		{"5.13.0", landlockPartial},
		{"6.1.0-31-amd64", landlockPartial},
		{"6.7.0", landlockFull},
		{"6.12.94+deb13-amd64", landlockFull},
		{"garbage", landlockNotEnforced},
	}

	for _, c := range cases {
		got := classifyLandlock(c.release).kind
		if got != c.want {
			t.Errorf("classifyLandlock(%q).kind = %d, want %d", c.release, got, c.want)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		out  string
		min  string
		want bool
	}{
		{"nono 0.66.0", "0.66.0", true},
		{"nono 0.66.1", "0.66.0", true},
		{"nono 0.67.0", "0.66.0", true},
		{"nono 1.0.0", "0.66.0", true},
		{"nono 0.65.9", "0.66.0", false},
		{"nono 0.66.0", "0.67.0", false},
	}

	for _, c := range cases {
		maj, minr, pat, ok := parseNonoVersion(c.out)
		if !ok {
			t.Fatalf("parseNonoVersion(%q) failed", c.out)
		}

		if got := versionAtLeast(maj, minr, pat, c.min); got != c.want {
			t.Errorf("versionAtLeast(%s, %s) = %v, want %v", c.out, c.min, got, c.want)
		}
	}
}

func argsAfterSeparator(t *testing.T, args []string) []string {
	t.Helper()

	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}

	t.Fatal("separator -- not found in args")

	return nil
}

// TestBuildNonoProfileWriteDirsUseAllowNotWrite: write_dirs must map to
// filesystem.allow (read+write), never filesystem.write (write-only under nono,
// which would break read-back and deletes).
func TestBuildNonoProfileWriteDirsUseAllowNotWrite(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/hame/user/bothy",
		WriteDirs:   []string{"/hame/user/croft"},
	}

	p, _ := buildNonoProfile("graith-bothy", opts, "")

	if len(p.Filesystem.Write) != 0 {
		t.Errorf("filesystem.write must stay empty (it is write-only under nono); got %v", p.Filesystem.Write)
	}

	if !slices.Contains(p.Filesystem.Allow, "/hame/user/croft") {
		t.Errorf("write dir should be in filesystem.allow (read+write): %v", p.Filesystem.Allow)
	}
}

// TestBuildNonoProfileReadDirUnderTmpIsReDenied: a read-only path under /tmp is
// silently writable via nono's system_write_linux group, so it must be
// re-denied to keep the read-only guarantee.
func TestBuildNonoProfileReadDirUnderTmpIsReDenied(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/hame/user/bothy",
		ReadDirs:    []string{"/tmp/dreich-readonly", "/hame/user/glen"},
	}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")

	if !slices.Contains(p.Filesystem.Deny, "/tmp/dreich-readonly") {
		t.Errorf("read-only path under /tmp should be re-denied: deny=%v", p.Filesystem.Deny)
	}

	// A read-only path NOT under /tmp is left alone.
	if slices.Contains(p.Filesystem.Deny, "/hame/user/glen") {
		t.Errorf("non-/tmp read dir should not be denied: %v", p.Filesystem.Deny)
	}

	if len(warnings) == 0 {
		t.Error("expected a warning about the /tmp read-only leak")
	}
}

// TestBuildNonoProfileWorktreeUnderTmpNotDenied: the worktree is meant to be
// writable, so a worktree under /tmp is NOT re-denied even though it's under a
// default-writable prefix.
func TestBuildNonoProfileWorktreeUnderTmpNotDenied(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/tmp/bothy/sub"}, // under the (writable) worktree
	}

	p, _ := buildNonoProfile("graith-bothy", opts, "")

	if slices.Contains(p.Filesystem.Deny, "/tmp/bothy/sub") {
		t.Errorf("read path within the writable worktree should not be denied: %v", p.Filesystem.Deny)
	}
}

func TestIsWithin(t *testing.T) {
	if !isWithin("/tmp/bothy/sub", "/tmp/bothy") {
		t.Error("/tmp/bothy/sub should be within /tmp/bothy")
	}

	if isWithin("/tmp/bothy-sibling", "/tmp/bothy") {
		t.Error("/tmp/bothy-sibling must NOT be within /tmp/bothy (prefix-without-separator)")
	}

	if isWithin("/hame/glen", "") {
		t.Error("nothing is within an empty prefix")
	}
}

// --- Phase 2: signal_mode (process-control tightening) -----------------------

func TestBuildNonoProfileSignalModeIsolated(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/tmp/bothy",
		Features:    []string{"process-control"},
		SignalMode:  "isolated",
	}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")
	if p.Security == nil || p.Security.SignalMode != "isolated" {
		t.Fatalf("signal_mode should be emitted as security.signal_mode=isolated, got %+v", p.Security)
	}

	// With signal_mode set, process-control is meaningful, so no hint fires.
	for _, w := range warnings {
		if strings.Contains(w, "process-control") {
			t.Errorf("process-control should not warn when signal_mode is set: %q", w)
		}
	}
}

func TestBuildNonoProfileNoSignalModeOmitsSecurity(t *testing.T) {
	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy"}

	p, _ := buildNonoProfile("graith-canny", opts, "")
	if p.Security != nil {
		t.Errorf("no signal_mode should omit the security section, got %+v", p.Security)
	}
}

func TestBuildNonoProfileSignalModeSerialisesInSecurity(t *testing.T) {
	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", SignalMode: "isolated"}

	p, _ := buildNonoProfile("graith-kirk", opts, "")

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	sec, ok := got["security"].(map[string]any)
	if !ok {
		t.Fatalf("security section missing from JSON: %s", data)
	}

	if sec["signal_mode"] != "isolated" {
		t.Errorf("security.signal_mode = %v, want isolated", sec["signal_mode"])
	}
}

// --- Phase 2: network egress policy ------------------------------------------

func TestBuildNonoProfileNetworkBlock(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/tmp/bothy",
		Network:     &NetworkPolicy{Block: true},
	}

	p, _ := buildNonoProfile("graith-thrawn", opts, "")
	if p.Network == nil || !p.Network.Block {
		t.Fatalf("network.block should be true, got %+v", p.Network)
	}

	if len(p.Network.AllowDomain) != 0 {
		t.Errorf("no allow_domain expected, got %v", p.Network.AllowDomain)
	}
}

func TestBuildNonoProfileNetworkAllowDomains(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/tmp/bothy",
		Network:     &NetworkPolicy{AllowDomains: []string{"kirk.example", "https://glen.example/**"}},
	}

	p, _ := buildNonoProfile("graith-glen", opts, "")
	if p.Network == nil {
		t.Fatal("network section should be emitted")
	}

	if !slices.Equal(p.Network.AllowDomain, []string{"kirk.example", "https://glen.example/**"}) {
		t.Errorf("allow_domain mismatch, got %v", p.Network.AllowDomain)
	}
}

func TestBuildNonoProfileNoNetworkOmitsSection(t *testing.T) {
	// A nil / empty network policy must leave nono's allow-by-default posture
	// untouched (no network section emitted) — matches pre-Phase-2 behaviour.
	for _, np := range []*NetworkPolicy{nil, {}, {AllowDomains: nil}} {
		opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", Network: np}

		p, _ := buildNonoProfile("graith-neep", opts, "")
		if p.Network != nil {
			t.Errorf("empty network policy %+v should omit the network section, got %+v", np, p.Network)
		}
	}
}

func TestBuildNonoProfileNetworkSerialises(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/tmp/bothy",
		Network:     &NetworkPolicy{Block: true, AllowDomains: []string{"kirk.example"}},
	}

	p, _ := buildNonoProfile("graith-brig", opts, "")

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	net, ok := got["network"].(map[string]any)
	if !ok {
		t.Fatalf("network section missing from JSON: %s", data)
	}

	if net["block"] != true {
		t.Errorf("network.block = %v, want true", net["block"])
	}

	dom, ok := net["allow_domain"].([]any)
	if !ok || len(dom) != 1 || dom[0] != "kirk.example" {
		t.Errorf("network.allow_domain = %v, want [kirk.example]", net["allow_domain"])
	}
}

// --- Phase 2: ABI-v4 network fail-closed (§B2) -------------------------------

func TestNonoAvailabilityPartialWithNetworkFailsClosed(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono " + MinNonoVersion, nil }
	ll := func() landlockInfo {
		return landlockInfo{kind: landlockPartial, detail: "kernel 5.15: no network filtering"}
	}

	// FS-only request on a partial kernel: runs degraded.
	if av := nonoAvailability("", Requirements{Network: false}, look, ver, ll); !av.CanEnforce || !av.Degraded {
		t.Errorf("partial + no network should run degraded, got %+v", av)
	}

	// Network request on the same partial kernel: must fail closed (ABI v4).
	av := nonoAvailability("", Requirements{Network: true}, look, ver, ll)
	if av.CanEnforce {
		t.Errorf("partial Landlock + network policy must fail closed (needs ABI v4), got %+v", av)
	}

	if !strings.Contains(av.Detail, "ABI v4") {
		t.Errorf("fail-closed detail should mention ABI v4, got %q", av.Detail)
	}
}

func TestNonoAvailabilityFullWithNetworkEnforces(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono " + MinNonoVersion, nil }
	ll := func() landlockInfo { return landlockInfo{kind: landlockFull} }

	av := nonoAvailability("", Requirements{Network: true}, look, ver, ll)
	if !av.CanEnforce || av.Degraded {
		t.Errorf("full Landlock should enforce a network policy without degradation, got %+v", av)
	}
}

func TestNonoAvailabilityMacOSWithNetworkEnforces(t *testing.T) {
	look := func(string) (string, error) { return "/usr/local/bin/nono", nil }
	ver := func(string) (string, error) { return "nono " + MinNonoVersion, nil }
	// landlockNotApplicable is the macOS (Seatbelt) case; network filtering is
	// handled by nono's proxy there, so a network policy is enforceable.
	ll := func() landlockInfo { return landlockInfo{kind: landlockNotApplicable} }

	av := nonoAvailability("", Requirements{Network: true}, look, ver, ll)
	if !av.CanEnforce {
		t.Errorf("macOS nono should enforce a network policy, got %+v", av)
	}
}

// --- Phase 2: safehouse rejects a network policy (fail-closed) ---------------

func TestSafehouseFailsClosedOnNetworkPolicy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("safehouse only enforces on darwin; network gate tested via nono path elsewhere")
	}

	av := safehouseBackend{}.Availability("", Requirements{Network: true})
	if av.CanEnforce {
		t.Errorf("safehouse has no network primitive; a network policy must fail closed, got %+v", av)
	}
}

func TestNetworkPolicyIsSet(t *testing.T) {
	cases := []struct {
		name string
		np   *NetworkPolicy
		want bool
	}{
		{"nil", nil, false},
		{"empty", &NetworkPolicy{}, false},
		{"block", &NetworkPolicy{Block: true}, true},
		{"domains", &NetworkPolicy{AllowDomains: []string{"kirk.example"}}, true},
	}
	for _, tc := range cases {
		if got := tc.np.IsSet(); got != tc.want {
			t.Errorf("%s: IsSet()=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestBuildNonoProfileFileGrants: read_files map to filesystem.read_file and
// write_files map to filesystem.allow_file (read+write single file), so a login
// file like ~/.claude.json can be granted without exposing all of $HOME.
func TestBuildNonoProfileFileGrants(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/hame/user/bothy",
		ReadFiles:   []string{"/hame/user/.gitconfig"},
		WriteFiles:  []string{"/hame/user/.claude.json", "/hame/user/.claude.json.lock"},
	}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	if !slices.Contains(p.Filesystem.ReadFile, "/hame/user/.gitconfig") {
		t.Errorf("read_files should map to filesystem.read_file: %v", p.Filesystem.ReadFile)
	}

	for _, want := range []string{"/hame/user/.claude.json", "/hame/user/.claude.json.lock"} {
		if !slices.Contains(p.Filesystem.AllowFile, want) {
			t.Errorf("write_files should map to filesystem.allow_file (read+write); missing %q in %v", want, p.Filesystem.AllowFile)
		}
	}

	// write_files must never become nono's write-only write_file.
	if len(p.Filesystem.Write) != 0 {
		t.Errorf("filesystem.write must stay empty; got %v", p.Filesystem.Write)
	}
}

// TestBuildNonoProfileReadFileUnderTmpIsReDenied: like read_dirs, a read-only
// file under /tmp is silently writable via nono's system_write_linux group, so
// it must be re-denied to keep the read-only guarantee.
func TestBuildNonoProfileReadFileUnderTmpIsReDenied(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/hame/user/bothy",
		ReadFiles:   []string{"/tmp/dreich.conf", "/hame/user/.gitconfig"},
	}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")

	if !slices.Contains(p.Filesystem.Deny, "/tmp/dreich.conf") {
		t.Errorf("read-only file under /tmp should be re-denied: deny=%v", p.Filesystem.Deny)
	}

	if slices.Contains(p.Filesystem.Deny, "/hame/user/.gitconfig") {
		t.Errorf("non-/tmp read file should not be denied: %v", p.Filesystem.Deny)
	}

	if len(warnings) == 0 {
		t.Error("expected a warning about the /tmp read-only file leak")
	}
}

// TestWrapWithFileGrants: the safehouse backend folds read_files/write_files
// into its read-only / read-write path lists (Seatbelt path rules cover files).
func TestWrapWithFileGrants(t *testing.T) {
	opts := WrapOpts{
		Backend:     BackendSafehouse,
		WorktreeDir: "/tmp/bothy",
		ReadDirs:    []string{"/hame/user/glen"},
		ReadFiles:   []string{"/hame/user/.gitconfig"},
		WriteDirs:   []string{"/tmp/croft"},
		WriteFiles:  []string{"/hame/user/.claude.json"},
		EnvKeys:     []string{"TERM"},
	}

	_, args, err := Wrap("claude", nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	valueAfter := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}

		return ""
	}

	if ro := valueAfter("--add-dirs-ro"); !strings.Contains(ro, "/hame/user/.gitconfig") || !strings.Contains(ro, "/hame/user/glen") {
		t.Errorf("--add-dirs-ro = %q, want it to include both the read dir and read file", ro)
	}

	if rw := valueAfter("--add-dirs"); !strings.Contains(rw, "/hame/user/.claude.json") || !strings.Contains(rw, "/tmp/croft") {
		t.Errorf("--add-dirs = %q, want it to include both the write dir and write file", rw)
	}
}

// TestBuildNonoProfileSingleFileInDirGrants: a read_dirs/write_dirs entry that
// points at a single file (e.g. ~/.claude.json) must be routed to nono's
// file-grant form (read_file/allow_file) rather than the directory-grant list
// (read/allow), which nono rejects at profile parse — a fail-closed abort of
// the whole session. Directory entries stay in the directory lists. Regression
// test for issue #714.
func TestBuildNonoProfileSingleFileInDirGrants(t *testing.T) {
	tmp := t.TempDir()

	readDir := filepath.Join(tmp, "glen")
	writeDir := filepath.Join(tmp, "croft")
	for _, d := range []string{readDir, writeDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	readFile := filepath.Join(tmp, ".gitconfig")
	writeFile := filepath.Join(tmp, ".claude.json")
	for _, f := range []string{readFile, writeFile} {
		if err := os.WriteFile(f, []byte("skelf"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: filepath.Join(tmp, "bothy"),
		ReadDirs:    []string{readDir, readFile},
		WriteDirs:   []string{writeDir, writeFile},
	}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	// The single-file entries are routed to the file-grant lists.
	if !slices.Contains(p.Filesystem.ReadFile, readFile) {
		t.Errorf("a read_dirs file should map to filesystem.read_file: %v", p.Filesystem.ReadFile)
	}

	if !slices.Contains(p.Filesystem.AllowFile, writeFile) {
		t.Errorf("a write_dirs file should map to filesystem.allow_file: %v", p.Filesystem.AllowFile)
	}

	// ...and must NOT leak into the directory-grant lists (what nono rejects).
	if slices.Contains(p.Filesystem.Read, readFile) {
		t.Errorf("a read_dirs file must not stay in filesystem.read: %v", p.Filesystem.Read)
	}

	if slices.Contains(p.Filesystem.Allow, writeFile) {
		t.Errorf("a write_dirs file must not stay in filesystem.allow: %v", p.Filesystem.Allow)
	}

	// Directory entries stay in the directory lists.
	if !slices.Contains(p.Filesystem.Read, readDir) {
		t.Errorf("a read_dirs directory should map to filesystem.read: %v", p.Filesystem.Read)
	}

	if !slices.Contains(p.Filesystem.Allow, writeDir) {
		t.Errorf("a write_dirs directory should map to filesystem.allow: %v", p.Filesystem.Allow)
	}
}