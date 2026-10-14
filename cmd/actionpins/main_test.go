package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testWorkflow = `steps:
  - uses: actions/checkout@%s
  - uses: actions/upload-artifact@%s
  - uses: actions/download-artifact@%s
`

func writeRepo(t *testing.T, sha string) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	content := strings.ReplaceAll(testWorkflow, "%s", strings.Repeat(sha, 40))
	if err := os.WriteFile(filepath.Join(dir, "regen.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "internal", "ciworkflow", "testdata"), 0o750); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestRunRewritesIsIdempotentAndChecks(t *testing.T) {
	root := writeRepo(t, "a")
	pinFile := filepath.Join(root, "internal", "ciworkflow", "testdata", "action_pins.json")

	var out bytes.Buffer

	if err := run([]string{"-repo", root, "-check"}, &out); err == nil {
		t.Fatal("-check passed with a missing pin file")
	}

	if err := run([]string{"-repo", root}, &out); err != nil {
		t.Fatal(err)
	}

	first, err := os.ReadFile(pinFile)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(first), strings.Repeat("a", 40)) {
		t.Fatalf("pin file missing sha:\n%s", first)
	}

	if err := run([]string{"-repo", root, "-check"}, &out); err != nil {
		t.Fatalf("-check failed after generate: %v", err)
	}

	if err := run([]string{"-repo", root}, &out); err != nil {
		t.Fatal(err)
	}

	second, _ := os.ReadFile(pinFile)
	if !bytes.Equal(first, second) {
		t.Fatal("second run changed the file")
	}

	// A Renovate-style bump makes the committed pins stale, then regenerates.
	bumped := writeRepo(t, "b")
	if err := os.Rename(filepath.Join(bumped, ".github", "workflows", "regen.yml"), filepath.Join(root, ".github", "workflows", "regen.yml")); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"-repo", root, "-check"}, &out); err == nil {
		t.Fatal("-check passed with stale pins")
	}

	if err := run([]string{"-repo", root}, &out); err != nil {
		t.Fatal(err)
	}

	third, _ := os.ReadFile(pinFile)
	if !strings.Contains(string(third), strings.Repeat("b", 40)) || strings.Contains(string(third), strings.Repeat("a", 40)) {
		t.Fatalf("pins not rewritten:\n%s", third)
	}
}
