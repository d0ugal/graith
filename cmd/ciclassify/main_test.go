package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/cipolicy"
)

func TestRunWritesGitHubOutputs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer

	output := filepath.Join(t.TempDir(), "github-output")

	err := run(
		[]string{"-mode", "libghostty", "-github-output", output},
		strings.NewReader("libghostty-native.lock.json\n"),
		&stdout,
	)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := string(data), "dependency-unit=true\nnative=true\n"; got != want {
		t.Fatalf("github output = %q, want %q", got, want)
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunWritesShellOutputs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer

	err := run(
		[]string{"-mode", "coverage"},
		strings.NewReader("internal/client/passthrough.go\n"),
		&stdout,
	)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := stdout.String(), "gui=false\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunWritesJSON(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer

	err := run(
		[]string{"-json"},
		strings.NewReader("gui/shared/Sources/CGhosttyVT/include/ghostty.h\n"),
		&stdout,
	)
	if err != nil {
		t.Fatal(err)
	}

	var got cipolicy.WorkflowClassifications
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if !got.CIMacOS || !got.CoverageGUI || !got.LibghosttyNative {
		t.Fatalf("json classification = %#v, want ci/gui/native true", got)
	}
}

func TestRunReadsChangedFilesPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	changedFiles := filepath.Join(dir, "braw-files.txt")
	if err := os.WriteFile(changedFiles, []byte("internal/sandbox/nono.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer

	err := run([]string{"-mode", "sandbox", "-changed-files", changedFiles}, strings.NewReader(""), &stdout)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := stdout.String(), "macos=true\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunRejectsInvalidInputBeforeOutput(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer

	output := filepath.Join(t.TempDir(), "github-output")

	err := run(
		[]string{"-mode", "ci", "-github-output", output},
		strings.NewReader(" internal/pty/session.go\n"),
		&stdout,
	)
	if err == nil {
		t.Fatal("run succeeded, want invalid path error")
	}

	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("github output stat error = %v, want file not created", statErr)
	}
}

func TestRunRequiresModeForGitHubOutput(t *testing.T) {
	t.Parallel()

	err := run(
		[]string{"-json", "-github-output", filepath.Join(t.TempDir(), "github-output")},
		strings.NewReader("go.mod\n"),
		&bytes.Buffer{},
	)
	if err == nil || !strings.Contains(err.Error(), "-github-output requires -mode") {
		t.Fatalf("run error = %v, want mode requirement", err)
	}
}
