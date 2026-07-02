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

func TestBuildNonoProfileProcessControlIsNoOp(t *testing.T) {
	opts := WrapOpts{Backend: BackendNono, WorktreeDir: "/tmp/bothy", Features: []string{"process-control"}}

	p, warnings := buildNonoProfile("graith-bothy", opts, "")
	if len(warnings) != 0 {
		t.Errorf("process-control should not warn (it is an accepted no-op): %v", warnings)
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

	av := nonoAvailability("", look, nil, nil)
	if av.CanEnforce {
		t.Error("CanEnforce should be false when nono binary is absent")
	}
}

func TestNonoAvailabilityBelowVersionPin(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono 0.1.0", nil }

	av := nonoAvailability("", look, ver, func() landlockInfo { return landlockInfo{kind: landlockFull} })
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

	av := nonoAvailability("", look, ver, ll)
	if av.CanEnforce {
		t.Error("Landlock NotEnforced with sandbox enabled must be a hard fail (fail-open regression otherwise)")
	}
}

func TestNonoAvailabilityPartialRunsDegraded(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/nono", nil }
	ver := func(string) (string, error) { return "nono " + MinNonoVersion, nil }
	ll := func() landlockInfo { return landlockInfo{kind: landlockPartial, detail: "no net filtering"} }

	av := nonoAvailability("", look, ver, ll)
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

	av := nonoAvailability("", look, ver, ll)
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
