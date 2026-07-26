package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/cibaseline"
)

func TestGenerateAndValidateRemainStaticInventoryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dreich-inventory.json")

	if err := run([]string{"-repo", "../..", "-output", path, "generate"}); err != nil {
		t.Fatal(err)
	}

	inventory, err := readInventory(path)
	if err != nil {
		t.Fatal(err)
	}

	if inventory.SchemaVersion != cibaseline.SchemaVersion {
		t.Fatalf("generated schema = %d, want %d", inventory.SchemaVersion, cibaseline.SchemaVersion)
	}

	if err := inventory.Validate(); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"-repo", "../..", "-inventory", path, "validate"}); err != nil {
		t.Fatal(err)
	}

	inventory.Workflows = inventory.Workflows[1:]
	if err := writeJSON(path, inventory); err != nil {
		t.Fatal(err)
	}

	err = run([]string{"-repo", "../..", "-inventory", path, "validate"})
	if err == nil || !strings.Contains(err.Error(), "missing workflows") {
		t.Fatalf("run(validate stale inventory) error = %v, want missing workflow rejection", err)
	}
}

func TestWriteJSONCreatesPrivateInventoryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dreich-inventory.json")

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

func TestHistoryEvidenceAndAcceptanceModesStayRemoved(t *testing.T) {
	tests := map[string]string{
		"acceptance": "unknown command",
		"collect":    "unknown command",
		"fetch":      "unknown command",
		"replay":     "unknown command",
	}

	for command, want := range tests {
		t.Run(command, func(t *testing.T) {
			err := run([]string{command})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("run(%q) error = %v, want %q", command, err, want)
			}
		})
	}
}

func TestHistoryEvidenceAndAcceptanceFlagsStayRemoved(t *testing.T) {
	tests := map[string][]string{
		"acceptance allowance": {"-allow-incomplete-acceptance", "validate"},
		"collection elapsed":   {"-max-elapsed", "1m", "validate"},
		"collection requests":  {"-max-requests", "1", "validate"},
		"collection retries":   {"-max-retries", "1", "validate"},
		"evidence input":       {"-input", "braw.json", "validate"},
		"github repository":    {"-repository", "d0ugal/graith", "validate"},
		"maturation delay":     {"-maturation-delay", "1h", "validate"},
		"window since":         {"-since", "2026-07-25T06:05:00Z", "validate"},
		"window until":         {"-until", "2026-07-25T12:05:00Z", "validate"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			err := run(args)
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("run(%v) error = %v, want removed flag rejection", args, err)
			}
		})
	}
}
