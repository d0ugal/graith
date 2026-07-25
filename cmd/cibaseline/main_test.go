package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteJSONCreatesPrivateEvidenceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dreich-evidence.json")

	if err := writeJSON(path, map[string]string{"croft": "bothy"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output mode = %#o, want 0600", got)
	}

	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // deliberately verifies that writeJSON tightens an existing broad mode
		t.Fatal(err)
	}

	if err := writeJSON(path, map[string]string{"bairn": "strath"}); err != nil {
		t.Fatal(err)
	}

	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("overwritten output mode = %#o, want 0600", got)
	}
}

func TestReadJSONRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thrawn.json")
	if err := os.WriteFile(path, []byte(`{"croft":"bothy","blether":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var target struct {
		Croft string `json:"croft"`
	}
	if err := readJSON(path, &target); err == nil {
		t.Fatal("readJSON accepted unknown field")
	}
}

func TestReadEvidenceRejectsOldSchemaBeforeOldFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "braw-evidence.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"expected_runs":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readEvidence(path); err == nil || err.Error() != "unsupported evidence schema 1" {
		t.Fatalf("readEvidence(old schema) error = %v, want version rejection", err)
	}
}

func TestReadSnapshotRejectsOldSchemaBeforeOldFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canny-snapshot.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"expected_runs":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readSnapshot(path); err == nil || err.Error() != "unsupported snapshot schema 1" {
		t.Fatalf("readSnapshot(old schema) error = %v, want version rejection", err)
	}
}

func TestFetchRejectsNegativeCollectionLimits(t *testing.T) {
	inventory := filepath.Join("..", "..", "internal", "cibaseline", "inventory.json")
	tests := map[string][]string{
		"elapsed":  {"-max-elapsed=-1s"},
		"requests": {"-max-requests=-1"},
		"retries":  {"-max-retries=-1"},
	}

	for name, limit := range tests {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"-inventory", inventory}, limit...)
			args = append(args, "fetch")

			if err := run(args); err == nil || !strings.Contains(err.Error(), "limits must not be negative") {
				t.Fatalf("run() error = %v, want negative collection limit rejection", err)
			}
		})
	}
}
