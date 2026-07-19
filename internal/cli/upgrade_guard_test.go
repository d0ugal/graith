package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/d0ugal/graith/internal/daemon"
	grpty "github.com/d0ugal/graith/internal/pty"
)

func TestUpgradeGuardCleansProcessWhenConfigFailsBeforeDaemonRun(t *testing.T) {
	originalSessionID, hadSessionID := os.LookupEnv("GRAITH_SESSION_ID")

	if err := os.Unsetenv("GRAITH_SESSION_ID"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if hadSessionID {
			_ = os.Setenv("GRAITH_SESSION_ID", originalSessionID)
		} else {
			_ = os.Unsetenv("GRAITH_SESSION_ID")
		}
	})

	cmd := exec.Command("sh", "-c", "exec sleep 30")

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	pid := cmd.Process.Pid
	done := make(chan struct{})

	go func() {
		_ = cmd.Wait()

		close(done)
	}()

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)

		<-done
	})

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	dir := t.TempDir()

	manifestPath, err := daemon.WriteManifest(dir, &daemon.UpgradeManifest{
		Sessions: []daemon.UpgradeSession{{
			ID: "dreich-headless", Fd: -1, PID: pid, PIDStartTime: start,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	badConfig := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(badConfig, []byte("this is not = toml"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = executeWithArgs([]string{"daemon", "start", "--adopt-from", manifestPath, "--config", badConfig})
	if err == nil || !strings.Contains(err.Error(), "loading config") {
		t.Fatalf("executeWithArgs error = %v, want pre-Run config failure", err)
	}

	<-done

	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("inherited process remains after pre-Run config failure: %v", err)
	}
}

func TestAdoptFromArgument(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"daemon", "start", "--adopt-from", "/bothy/manifest.json"}, want: "/bothy/manifest.json"},
		{args: []string{"daemon", "start", "--adopt-from=/croft/manifest.json"}, want: "/croft/manifest.json"},
		{args: []string{"daemon", "start"}, want: ""},
		{args: []string{"list", "--adopt-from=/croft/manifest.json"}, want: ""},
		{args: []string{"msg", "send", "bothy", "daemon", "start", "--adopt-from", "/croft/manifest.json"}, want: ""},
	} {
		if got := adoptFromArgument(tc.args); got != tc.want {
			t.Errorf("adoptFromArgument(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
