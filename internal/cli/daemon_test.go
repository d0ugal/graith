package cli

import (
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
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

// fakeUpgradeConn scripts the handshake + upgrade round-trip execUpgrade drives,
// recording the deadline installed on it. responses are returned from
// ReadControlResponse in order.
type fakeUpgradeConn struct {
	deadline    time.Time
	deadlineSet bool
	deadlineErr error
	responses   []protocol.Envelope
	readIdx     int
	sent        []string
	closed      bool
}

func (f *fakeUpgradeConn) SetDeadline(t time.Time) error {
	f.deadline = t
	f.deadlineSet = true

	return f.deadlineErr
}

func (f *fakeUpgradeConn) Handshake() error { return nil }

func (f *fakeUpgradeConn) SendControl(msgType string, _ any) error {
	f.sent = append(f.sent, msgType)

	return nil
}

func (f *fakeUpgradeConn) ReadControlResponse() (protocol.Envelope, error) {
	if f.readIdx >= len(f.responses) {
		return protocol.Envelope{}, io.EOF
	}

	resp := f.responses[f.readIdx]
	f.readIdx++

	return resp, nil
}

func (f *fakeUpgradeConn) Close() { f.closed = true }

// TestExecUpgradeInstallsConfiguredHandshakeDeadline proves execUpgrade bounds
// its raw handshake + upgrade exchange with the configured local handshake
// deadline before driving it, so a stale daemon that accepts but never replies
// can't wedge the handshake (issue #1242). It uses a refused upgrade to return
// before the readiness wait.
func TestExecUpgradeInstallsConfiguredHandshakeDeadline(t *testing.T) {
	origCfg, origNow, origDial := cfg, connectionNow, dialUpgradeClient

	t.Cleanup(func() { cfg, connectionNow, dialUpgradeClient = origCfg, origNow, origDial })

	cfg = &config.Config{Connection: config.ConnectionConfig{HandshakeTimeout: "3210ms"}}

	fixedNow := time.Unix(1_700_000, 0)
	connectionNow = func() time.Time { return fixedNow }

	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{
			{Type: "handshake_ok"},        // discarded post-handshake read
			{Type: "error", Payload: nil}, // upgrade refused -> early return
		},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	if err := execUpgrade("done"); err == nil {
		t.Fatal("expected an error from a refused upgrade")
	}

	if !fake.deadlineSet {
		t.Fatal("execUpgrade did not install a handshake deadline")
	}

	want := fixedNow.Add(3210 * time.Millisecond)
	if !fake.deadline.Equal(want) {
		t.Errorf("handshake deadline = %v, want %v", fake.deadline, want)
	}

	if !fake.closed {
		t.Error("execUpgrade did not close the connection")
	}
}
