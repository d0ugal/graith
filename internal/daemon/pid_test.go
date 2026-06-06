package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquirePIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pid")

	if err := AcquirePIDFile(path); err != nil {
		t.Fatal(err)
	}
	defer ReleasePIDFile(path)

	data, _ := os.ReadFile(path)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
}

func TestAcquirePIDFileStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pid")
	os.WriteFile(path, []byte("999999\n"), 0o600)

	if err := AcquirePIDFile(path); err != nil {
		t.Fatalf("should succeed with stale PID: %v", err)
	}
	defer ReleasePIDFile(path)
}

func TestAcquirePIDFileLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pid")
	os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)

	err := AcquirePIDFile(path)
	if err == nil {
		t.Error("should fail when PID is live")
	}
}
