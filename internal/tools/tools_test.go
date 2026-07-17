package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFakeBinary writes an executable script at dir/name and returns its path.
func writeFakeBinary(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for validation
		t.Fatalf("write fake binary: %v", err)
	}

	return path
}

func TestDefaultsPreservesHistoricalNames(t *testing.T) {
	d := Defaults()

	cases := map[string]string{
		"git":       d.Git,
		"gh":        d.GH,
		"gcx":       d.GCX,
		"sh":        d.Shell,
		"osascript": d.OSAScript,
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("default = %q, want %q", got, want)
		}
	}

	if d.PS != "/bin/ps" {
		t.Errorf("default PS = %q, want /bin/ps", d.PS)
	}

	if d.Lsof != "/usr/sbin/lsof" {
		t.Errorf("default Lsof = %q, want /usr/sbin/lsof", d.Lsof)
	}
}

func TestConfigureFillsEmptyFieldsWithDefaults(t *testing.T) {
	t.Cleanup(Reset)

	Configure(Config{Git: "/opt/nix/bin/git"})

	if got := Git(); got != "/opt/nix/bin/git" {
		t.Errorf("Git() = %q, want the override", got)
	}

	// Everything else falls back to the built-in default.
	if got := GH(); got != "gh" {
		t.Errorf("GH() = %q, want default gh", got)
	}

	if got := GCX(); got != "gcx" {
		t.Errorf("GCX() = %q, want default gcx", got)
	}

	if got := Shell(); got != "sh" {
		t.Errorf("Shell() = %q, want default sh", got)
	}

	if got := PS(); got != "/bin/ps" {
		t.Errorf("PS() = %q, want default /bin/ps", got)
	}
}

func TestResetRestoresDefaults(t *testing.T) {
	Configure(Config{Git: "custom-git", GH: "custom-gh", GCX: "custom-gcx"})
	Reset()

	if got := Git(); got != "git" {
		t.Errorf("after Reset, Git() = %q, want git", got)
	}

	if got := GH(); got != "gh" {
		t.Errorf("after Reset, GH() = %q, want gh", got)
	}

	if got := GCX(); got != "gcx" {
		t.Errorf("after Reset, GCX() = %q, want gcx", got)
	}
}

func TestValidateSkipsUnsetFields(t *testing.T) {
	// The zero Config sets nothing explicitly, so validation must pass even
	// though "osascript" is absent on Linux — defaults keep PATH-lookup
	// semantics and are only resolved when actually used.
	if err := Validate(Config{}); err != nil {
		t.Fatalf("Validate(zero) = %v, want nil", err)
	}
}

func TestValidateExplicitAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBinary(t, dir, "braw-git")

	if err := Validate(Config{Git: bin}); err != nil {
		t.Fatalf("Validate(valid path) = %v, want nil", err)
	}
}

func TestValidateRejectsMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	err := Validate(Config{Git: missing})
	if err == nil {
		t.Fatal("Validate(missing path) = nil, want error")
	}
}

func TestValidateRejectsNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bit semantics differ on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "canny")

	if err := os.WriteFile(path, []byte("not executable"), 0o644); err != nil { //nolint:gosec // G306: deliberately non-executable
		t.Fatalf("write file: %v", err)
	}

	err := Validate(Config{Shell: path})
	if err == nil {
		t.Fatal("Validate(non-executable path) = nil, want error")
	}
}

func TestValidateRejectsDirectory(t *testing.T) {
	dir := t.TempDir()

	err := Validate(Config{GH: dir})
	if err == nil {
		t.Fatal("Validate(directory) = nil, want error")
	}
}

func TestValidateBareNameOnPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "dreich-tool")

	t.Setenv("PATH", dir)

	if err := Validate(Config{Git: "dreich-tool"}); err != nil {
		t.Fatalf("Validate(bare name on PATH) = %v, want nil", err)
	}

	if err := Validate(Config{Git: "thrawn-absent"}); err == nil {
		t.Fatal("Validate(bare name not on PATH) = nil, want error")
	}
}

func TestValidateAggregatesMultipleErrors(t *testing.T) {
	missing1 := filepath.Join(t.TempDir(), "no-git")
	missing2 := filepath.Join(t.TempDir(), "no-gh")

	err := Validate(Config{Git: missing1, GH: missing2})
	if err == nil {
		t.Fatal("expected aggregated error, got nil")
	}

	msg := err.Error()
	for _, want := range []string{"tools.git", "tools.gh"} {
		if !contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}

	return false
}
