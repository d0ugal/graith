package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	grpty "github.com/d0ugal/graith/internal/pty"
)

type upgradeHeadlessDriver struct {
	*wedgeDriver

	pid int
}

func (d *upgradeHeadlessDriver) ProcessPID() int { return d.pid }

func TestWriteAndReadManifest(t *testing.T) {
	dir := t.TempDir()

	original := &UpgradeManifest{
		ListenerFd: 5,
		ConfigFile: "/home/user/.config/graith/config.toml",
		Sessions: []UpgradeSession{
			{ID: "abc123", Fd: 10, HasPTY: true, PID: 1234, PIDStartTime: 111},
			{ID: "def456", Fd: -1, PID: 5678, PIDStartTime: 222},
		},
	}

	path, err := WriteManifest(dir, original)
	if err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	wantPath := filepath.Join(dir, "upgrade-manifest.json")
	if path != wantPath {
		t.Errorf("WriteManifest() path = %q, want %q", path, wantPath)
	}

	loaded, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}

	if loaded.ListenerFd != original.ListenerFd {
		t.Errorf("ListenerFd = %d, want %d", loaded.ListenerFd, original.ListenerFd)
	}

	if loaded.ConfigFile != original.ConfigFile {
		t.Errorf("ConfigFile = %q, want %q", loaded.ConfigFile, original.ConfigFile)
	}

	if len(loaded.Sessions) != len(original.Sessions) {
		t.Fatalf("Sessions len = %d, want %d", len(loaded.Sessions), len(original.Sessions))
	}

	for i, s := range loaded.Sessions {
		orig := original.Sessions[i]
		if s.ID != orig.ID || s.Fd != orig.Fd || s.HasPTY != orig.HasPTY || s.PID != orig.PID || s.PIDStartTime != orig.PIDStartTime {
			t.Errorf("Sessions[%d] = %+v, want %+v", i, s, orig)
		}
	}
}

func TestWriteManifestEmptySessions(t *testing.T) {
	dir := t.TempDir()

	original := &UpgradeManifest{
		ListenerFd: 3,
		ConfigFile: "",
		Sessions:   nil,
	}

	path, err := WriteManifest(dir, original)
	if err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	loaded, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}

	if loaded.ListenerFd != 3 {
		t.Errorf("ListenerFd = %d, want 3", loaded.ListenerFd)
	}

	if len(loaded.Sessions) != 0 {
		t.Errorf("Sessions len = %d, want 0", len(loaded.Sessions))
	}
}

func TestPrepareUpgradeRecordsNonPTYProcessIdentity(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	driver := &upgradeHeadlessDriver{wedgeDriver: newWedgeDriver(false), pid: pid}
	sm.sessions["canny-headless"] = driver
	sm.state.Sessions["canny-headless"] = &SessionState{
		ID: "canny-headless", Name: "canny-headless", Agent: "sleeper",
		Status: StatusRunning, DriverKind: DriverHeadless,
		PID: driver.ProcessPID(), PIDStartTime: start,
	}

	listener, listenerPeer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = listener.Close()
		_ = listenerPeer.Close()
	})

	manifest, err := sm.PrepareUpgrade(listener.Fd(), "")
	if err != nil {
		t.Fatalf("PrepareUpgrade: %v", err)
	}

	if len(manifest.Sessions) != 1 {
		t.Fatalf("manifest sessions = %+v, want one recorded headless process", manifest.Sessions)
	}

	got := manifest.Sessions[0]
	if got.ID != "canny-headless" || got.HasPTY || got.Fd != -1 ||
		got.PID != driver.ProcessPID() || got.PIDStartTime != start {
		t.Fatalf("headless manifest entry = %+v, want identity without PTY handoff", got)
	}
}

func TestCleanupUpgradeManifestTerminatesRecordedHeadlessProcess(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	manifest := &UpgradeManifest{Sessions: []UpgradeSession{{
		ID: "dreich-headless", Fd: -1, PID: pid, PIDStartTime: start,
	}}}
	if err := sm.cleanupUpgradeManifest(manifest); err != nil {
		t.Fatalf("cleanupUpgradeManifest: %v", err)
	}

	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("headless process group remains after replacement startup failure: %v", err)
	}
}

func TestUpgradeFailureGuardDisarmPreservesTransferredProcess(t *testing.T) {
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	manifestPath, err := WriteManifest(t.TempDir(), &UpgradeManifest{Sessions: []UpgradeSession{{
		ID: "canny-transferred", Fd: -1, PID: pid, PIDStartTime: start,
	}}})
	if err != nil {
		t.Fatal(err)
	}

	guard, err := ArmUpgradeFailureGuard(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	guard.Disarm()

	if err := guard.Cleanup(); err != nil {
		t.Fatalf("Cleanup after Disarm: %v", err)
	}

	if err := syscall.Kill(-pid, 0); err != nil {
		t.Fatalf("transferred process was terminated by disarmed startup guard: %v", err)
	}
}

func TestReadManifestNonExistent(t *testing.T) {
	_, err := ReadManifest("/nonexistent/manifest.json")
	if err == nil {
		t.Fatal("expected error for nonexistent manifest file")
	}
}

func TestStopDaemonNonExistentPidFile(t *testing.T) {
	err := StopDaemon(filepath.Join(t.TempDir(), "nonexistent.pid"))
	if err == nil {
		t.Fatal("expected error for nonexistent pid file")
	}

	want := "daemon not running (no pid file)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestStopDaemonInvalidPID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"pid zero", "0", "refusing to signal invalid pid 0"},
		{"pid one", "1", "refusing to signal invalid pid 1"},
		{"pid negative", "-1", "refusing to signal invalid pid -1"},
		{"not a number", "notapid", "invalid pid file"},
		{"empty file", "", "invalid pid file"},
		{"trailing garbage", "123abc", "invalid pid file"},
		{"multiple numbers", "123 456", "invalid pid file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "daemon.pid")
			if err := os.WriteFile(pidFile, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			err := StopDaemon(pidFile)
			if err == nil {
				t.Fatal("expected error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}

			if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
				t.Error("expected pid file to be removed after invalid content")
			}
		})
	}
}
