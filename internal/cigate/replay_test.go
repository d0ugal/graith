package cigate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileReplayStoreRejectsCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := NewFileReplayStore(path).Reserve(ReplayKey{Kind: "delivery", Value: "braw"})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("Reserve() error = %v, want decode rejection", err)
	}
}

func TestFileReplayStoreCreatesSidecarLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.json")

	if err := NewFileReplayStore(path).Reserve(ReplayKey{Kind: "delivery", Value: "braw"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("stat replay lock: %v", err)
	}
}
