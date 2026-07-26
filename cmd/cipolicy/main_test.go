package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateWritesPrivateValidManifestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "braw-policy.json")
	inventory := filepath.Join("..", "..", "internal", "cibaseline", "inventory.json")

	if err := run([]string{"-inventory", inventory, "-output", path, "generate"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("manifest mode = %#o, want 0600", got)
	}

	if err := run([]string{"-inventory", inventory, "-manifest", path, "validate"}); err != nil {
		t.Fatal(err)
	}
}

func TestOutputFlagIsOnlyValidForGenerate(t *testing.T) {
	err := run([]string{"-output", filepath.Join(t.TempDir(), "canny.json"), "digest"})
	if err == nil || !strings.Contains(err.Error(), "-output is only valid with generate") {
		t.Fatalf("run() error = %v, want output flag rejection", err)
	}
}
