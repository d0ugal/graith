package cli

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/version"
)

// TestUpgradeMsgPopulatesExecAndVersion verifies upgradeMsg captures the running
// executable path and the client version, which the daemon needs to exec into
// the correct binary during a preserve-restart.
func TestUpgradeMsgPopulatesExecAndVersion(t *testing.T) {
	msg := upgradeMsg()

	if msg.ExecPath == "" {
		t.Errorf("ExecPath is empty; want the running executable path")
	}

	if msg.ClientVersion != version.Version {
		t.Errorf("ClientVersion = %q, want %q", msg.ClientVersion, version.Version)
	}
}

// TestProbeDaemonVersionNoSocket verifies probeDaemonVersion returns an empty
// string (rather than blocking or panicking) when the socket does not exist.
func TestProbeDaemonVersionNoSocket(t *testing.T) {
	origPaths := paths

	t.Cleanup(func() { paths = origPaths })

	paths.SocketPath = filepath.Join(t.TempDir(), "absent.sock")

	if v := probeDaemonVersion(); v != "" {
		t.Errorf("probeDaemonVersion() = %q, want empty string", v)
	}
}

// TestDaemonStopMissingPIDFile verifies the stop command surfaces the
// daemon-not-running error when no pid file is present.
func TestDaemonStopMissingPIDFile(t *testing.T) {
	origPaths := paths
	origOut := out

	t.Cleanup(func() {
		paths = origPaths
		out = origOut
	})

	paths.PIDFile = filepath.Join(t.TempDir(), "absent.pid")
	out = output.NewWithWriter(false, io.Discard)

	err := daemonStopCmd.RunE(daemonStopCmd, nil)
	if err == nil {
		t.Fatalf("expected error stopping a daemon with no pid file")
	}
}
