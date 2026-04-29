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

func TestIsPIDAlive(t *testing.T) {
	if !isPIDAlive(os.Getpid()) {
		t.Error("own PID should be alive")
	}
	if isPIDAlive(999999) {
		t.Error("non-existent PID should not be alive")
	}
}

func TestIsPIDAlivePID1(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: running as root won't get EPERM")
	}
	if !isPIDAlive(1) {
		t.Error("PID 1 should be alive (EPERM means alive)")
	}
}

func TestIsGraithDaemon(t *testing.T) {
	t.Run("pid 0 rejected", func(t *testing.T) {
		if IsGraithDaemon(0) {
			t.Error("pid 0 should be rejected")
		}
	})

	t.Run("pid 1 rejected", func(t *testing.T) {
		if IsGraithDaemon(1) {
			t.Error("pid 1 (init) should be rejected")
		}
	})

	t.Run("negative pid rejected", func(t *testing.T) {
		if IsGraithDaemon(-1) {
			t.Error("negative pid should be rejected")
		}
	})

	t.Run("non-existent pid rejected", func(t *testing.T) {
		if IsGraithDaemon(999999) {
			t.Error("non-existent pid should be rejected")
		}
	})

	t.Run("non-graith process rejected", func(t *testing.T) {
		if IsGraithDaemon(os.Getpid()) {
			t.Error("current process (go test) should not be identified as graith")
		}
	})
}
