package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const brawProfile = `mode: set
github.com/d0ugal/graith/internal/braw/braw.go:10.2,12.3 2 1
github.com/d0ugal/graith/internal/dreich/dreich.go:20.2,22.3 3 0
`

func writeProfile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSuccessNoBase(t *testing.T) {
	head := writeProfile(t, "cover.head.out", brawProfile)
	var out, errBuf strings.Builder
	code := run([]string{"-head", head, "-module", "github.com/d0ugal/graith"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "internal/dreich/dreich.go") {
		t.Errorf("output missing dreich row:\n%s", got)
	}
	// No base supplied -> deltas are em dashes.
	if !strings.Contains(got, "| — |") {
		t.Errorf("expected em-dash deltas without a base:\n%s", got)
	}
}

func TestRunWithBaseShowsDelta(t *testing.T) {
	head := writeProfile(t, "cover.head.out", brawProfile)
	// Base has dreich fully missed differently; braw absent -> braw is "new".
	base := writeProfile(t, "cover.base.out", `mode: set
github.com/d0ugal/graith/internal/dreich/dreich.go:20.2,22.3 3 1
`)
	var out, errBuf strings.Builder
	code := run([]string{"-head", head, "-base", base, "-module", "github.com/d0ugal/graith"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "new") {
		t.Errorf("braw (absent from base) should show 'new':\n%s", got)
	}
}

func TestRunMissingHeadFlag(t *testing.T) {
	var out, errBuf strings.Builder
	if code := run(nil, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "-head is required") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var out, errBuf strings.Builder
	if code := run([]string{"-thrawn"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2 for unknown flag", code)
	}
}

func TestRunMissingHeadFile(t *testing.T) {
	var out, errBuf strings.Builder
	code := run([]string{"-head", filepath.Join(t.TempDir(), "nae-such.out")}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for unreadable head", code)
	}
}

func TestRunMalformedHead(t *testing.T) {
	head := writeProfile(t, "cover.head.out", "mode: set\nthis is not a valid line\n")
	var out, errBuf strings.Builder
	if code := run([]string{"-head", head, "-module", "x"}, &out, &errBuf); code != 1 {
		t.Fatalf("exit = %d, want 1 for malformed head", code)
	}
}

func TestRunEmptyBaseTreatedAsUnavailable(t *testing.T) {
	head := writeProfile(t, "cover.head.out", brawProfile)
	base := writeProfile(t, "cover.base.out", "") // empty -> base unavailable, not an error
	var out, errBuf strings.Builder
	code := run([]string{"-head", head, "-base", base, "-module", "github.com/d0ugal/graith"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "| — |") {
		t.Errorf("empty base should yield em-dash deltas:\n%s", out.String())
	}
}

func TestRunMalformedBaseIsFatal(t *testing.T) {
	head := writeProfile(t, "cover.head.out", brawProfile)
	base := writeProfile(t, "cover.base.out", "mode: set\ngarbage line here\n")
	var out, errBuf strings.Builder
	if code := run([]string{"-head", head, "-base", base, "-module", "x"}, &out, &errBuf); code != 1 {
		t.Fatalf("exit = %d, want 1 for malformed base", code)
	}
}
