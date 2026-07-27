package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishCommandSkipsForkBeforeManifestOrNetwork(t *testing.T) {
	eventPath := filepath.Join(t.TempDir(), "braw-event.json")
	if err := os.WriteFile(eventPath, []byte(`{
		"repository": {"full_name": "clachan/croft"},
		"pull_request": {
			"number": 42,
			"head": {"repo": {"full_name": "thrawn/croft"}},
			"base": {"ref": "main", "sha": "base-sha"}
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer

	err := run(
		[]string{
			"publish",
			"-event", eventPath,
			"-repository", "clachan/croft",
			"-manifest", filepath.Join(t.TempDir(), "missing.json"),
			"-sha", "abcdef123456",
			"-run-id", "81",
			"-api-url", "http://127.0.0.1:1",
		},
		func(string) string { return "" },
		func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) },
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}

	if got := output.String(); !strings.Contains(got, "Fork PR") {
		t.Fatalf("output = %q, want fork no-op log", got)
	}
}
