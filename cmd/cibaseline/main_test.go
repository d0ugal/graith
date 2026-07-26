package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	tests := map[string][]string{
		"elapsed":    {"-max-elapsed=-1s"},
		"requests":   {"-max-requests=-1"},
		"retries":    {"-max-retries=-1"},
		"maturation": {"-maturation-delay=-1s"},
	}

	for name, limit := range tests {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"-inventory", inventory}, limit...)
			args = append(args, "fetch")

			if err := runWithNow(args, func() time.Time { return now }); err == nil ||
				!strings.Contains(err.Error(), "limits must not be negative") {
				t.Fatalf("run() error = %v, want negative collection limit rejection", err)
			}
		})
	}
}

func TestFetchDoesNotWriteOutputForInvalidMaturedWindow(t *testing.T) {
	inventory := filepath.Join("..", "..", "internal", "cibaseline", "inventory.json")
	output := filepath.Join(t.TempDir(), "braw-evidence.json")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339)

	err := runWithNow([]string{
		"-inventory", inventory,
		"-since", future,
		"-maturation-delay", time.Hour.String(),
		"-output", output,
		"fetch",
	}, func() time.Time { return now })
	if err == nil || !strings.Contains(err.Error(), "must be after since") {
		t.Fatalf("run() error = %v, want invalid cutoff rejection", err)
	}

	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after failed fetch: %v", statErr)
	}
}

func TestFetchUntilDoesNotWriteOutputForImmatureFixedWindow(t *testing.T) {
	inventory := filepath.Join("..", "..", "internal", "cibaseline", "inventory.json")
	output := filepath.Join(t.TempDir(), "canny-evidence.json")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	err := runWithNow([]string{
		"-inventory", inventory,
		"-since", "2026-07-25T09:00:00Z",
		"--until", "2026-07-25T11:30:00Z",
		"-maturation-delay", time.Hour.String(),
		"-output", output,
		"fetch",
	}, func() time.Time { return now })
	if err == nil || !strings.Contains(err.Error(), "newer than mature cutoff") {
		t.Fatalf("run() error = %v, want explicit until maturation rejection", err)
	}

	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after failed fixed-window fetch: %v", statErr)
	}
}

func TestFetchUntilRejectsAmbiguousOrEmptyWindowBeforeOutput(t *testing.T) {
	inventory := filepath.Join("..", "..", "internal", "cibaseline", "inventory.json")
	output := filepath.Join(t.TempDir(), "bothy-evidence.json")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	err := runWithNow([]string{
		"-inventory", inventory,
		"-since", "2026-07-25T10:00:00Z",
		"-until", "2026-07-25T10:00:00Z",
		"-output", output,
		"fetch",
	}, func() time.Time { return now })
	if err == nil || !strings.Contains(err.Error(), "must be after since") {
		t.Fatalf("run() error = %v, want empty-window rejection", err)
	}

	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after empty fixed-window fetch: %v", statErr)
	}
}
