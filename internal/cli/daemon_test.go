package cli

import (
	"errors"
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

// TestProbeDaemonIdentityNoSocket verifies probeDaemonIdentity returns empty
// strings (rather than blocking or panicking) when the socket does not exist.
func TestProbeDaemonIdentityNoSocket(t *testing.T) {
	origPaths := paths

	t.Cleanup(func() { paths = origPaths })

	paths.SocketPath = filepath.Join(t.TempDir(), "absent.sock")

	if v, id := probeDaemonIdentity(); v != "" || id != "" {
		t.Errorf("probeDaemonIdentity() = (%q, %q), want empty strings", v, id)
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
	sendErr     error
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

	return f.sendErr
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
			payloadEnv("handshake_ok", protocol.HandshakeOkMsg{Version: version.Version, DaemonVersion: version.Version, DaemonInstanceID: "old-gen"}),
			errEnv("upgrade refused"), // upgrade refused -> early return
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

// TestExecUpgradeWaitsForNewGenerationDespiteInheritedListener is the #1319
// round-4 scenario: after the upgrade is requested the inherited old listener
// keeps answering (right version, SAME instance ID) during a deliberately
// delayed exec. execUpgrade must keep polling and only report success once a
// DIFFERENT instance ID appears — never on the inherited listener.
func TestExecUpgradeWaitsForNewGenerationDespiteInheritedListener(t *testing.T) {
	origCfg, origNow, origSleep := cfg, connectionNow, connectionSleep
	origDial, origProbe, origOut := dialUpgradeClient, probeDaemonIdentityFn, out

	t.Cleanup(func() {
		cfg, connectionNow, connectionSleep = origCfg, origNow, origSleep
		dialUpgradeClient, probeDaemonIdentityFn, out = origDial, origProbe, origOut
	})

	cfg = &config.Config{Connection: config.ConnectionConfig{
		HandshakeTimeout:  "1s",
		StartTimeout:      "1s",
		StartPollInterval: "10ms",
	}}
	out = output.NewWithWriter(false, io.Discard)

	clk := &fakeConnClock{now: time.Unix(1_700_000, 0)}
	connectionNow = clk.Now
	connectionSleep = clk.Sleep

	// The daemon reports instance "old-gen" during the handshake before the upgrade.
	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{
			payloadEnv("handshake_ok", protocol.HandshakeOkMsg{Version: version.Version, DaemonVersion: version.Version, DaemonInstanceID: "old-gen"}),
			// upgrade reply: connection drops (daemon exec'ing) -> EOF read.
		},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	// The inherited listener answers with the SAME instance twice (delayed exec),
	// then the exec'd replacement finally reports a fresh instance.
	probes := 0
	probeDaemonIdentityFn = func() (string, string) {
		probes++
		if probes < 3 {
			return version.Version, "old-gen"
		}

		return version.Version, "new-gen"
	}

	if err := execUpgrade("upgraded"); err != nil {
		t.Fatalf("execUpgrade should succeed once the new generation appears: %v", err)
	}

	if probes < 3 {
		t.Fatalf("execUpgrade probed %d times, want it to keep polling past the inherited old-gen listener", probes)
	}
}

// TestExecUpgradeFailsWhenOnlyInheritedListenerResponds proves execUpgrade does
// NOT report success when the inherited listener keeps answering with the
// pre-upgrade instance ID (the exec never took), so the caller falls back to a
// clean restart instead of losing sessions on a false success (issue #1319).
func TestExecUpgradeFailsWhenOnlyInheritedListenerResponds(t *testing.T) {
	origCfg, origNow, origSleep := cfg, connectionNow, connectionSleep
	origDial, origProbe, origOut := dialUpgradeClient, probeDaemonIdentityFn, out

	t.Cleanup(func() {
		cfg, connectionNow, connectionSleep = origCfg, origNow, origSleep
		dialUpgradeClient, probeDaemonIdentityFn, out = origDial, origProbe, origOut
	})

	cfg = &config.Config{Connection: config.ConnectionConfig{
		HandshakeTimeout:  "1s",
		StartTimeout:      "50ms",
		StartPollInterval: "10ms",
	}}
	out = output.NewWithWriter(false, io.Discard)

	clk := &fakeConnClock{now: time.Unix(1_700_000, 0)}
	connectionNow = clk.Now
	connectionSleep = clk.Sleep

	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{
			payloadEnv("handshake_ok", protocol.HandshakeOkMsg{Version: version.Version, DaemonVersion: version.Version, DaemonInstanceID: "old-gen"}),
		},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	// Inherited listener only: right version, unchanged instance ID forever.
	probeDaemonIdentityFn = func() (string, string) { return version.Version, "old-gen" }

	if err := execUpgrade("upgraded"); err == nil {
		t.Fatal("execUpgrade must fail when only the inherited listener responds (no new generation)")
	}
}

// TestExecUpgradeFailsWhenHandshakeCaptureFails is the #1319 false-readiness
// regression: if the pre-upgrade handshake_ok can't be read/typed/decoded, the
// pre-upgrade instance ID is unknown. execUpgrade must FAIL the exchange rather
// than proceed with an empty prior ID — otherwise the unchanged old daemon (any
// non-empty instance ID) would satisfy "id != prior" and be falsely accepted as
// the new generation. The readiness probe must never run in that case.
func TestExecUpgradeFailsWhenHandshakeCaptureFails(t *testing.T) {
	cases := []struct {
		name      string
		responses []protocol.Envelope // first read is the pre-upgrade handshake_ok
	}{
		{
			// Read error: no handshake_ok frame at all (empty script -> EOF).
			name:      "read error",
			responses: nil,
		},
		{
			// Wrong reply type where handshake_ok was expected.
			name:      "wrong type",
			responses: []protocol.Envelope{{Type: "pong"}},
		},
		{
			// handshake_ok present but its payload doesn't decode.
			name:      "undecodable payload",
			responses: []protocol.Envelope{{Type: "handshake_ok", Payload: []byte("{not json")}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origCfg, origNow, origDial, origProbe := cfg, connectionNow, dialUpgradeClient, probeDaemonIdentityFn

			t.Cleanup(func() {
				cfg, connectionNow, dialUpgradeClient, probeDaemonIdentityFn = origCfg, origNow, origDial, origProbe
			})

			cfg = &config.Config{Connection: config.ConnectionConfig{HandshakeTimeout: "1s"}}
			connectionNow = func() time.Time { return time.Unix(1_700_000, 0) }

			fake := &fakeUpgradeConn{responses: tc.responses}
			dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

			probed := false
			probeDaemonIdentityFn = func() (string, string) {
				probed = true
				// If readiness ever runs, the old daemon would look "new".
				return version.Version, "old-gen-would-be-accepted"
			}

			if err := execUpgrade("done"); err == nil {
				t.Fatal("expected execUpgrade to fail when the pre-upgrade handshake can't be captured")
			}

			if probed {
				t.Error("readiness probe ran despite a failed handshake capture — false-readiness risk")
			}

			if len(fake.sent) != 0 {
				t.Errorf("no upgrade request should be sent when the capture fails, got %v", fake.sent)
			}
		})
	}
}

// TestExecUpgradeFailsWhenSendUpgradeFails proves a failed upgrade-request send
// is propagated rather than proceeding to wait for readiness of an upgrade that
// was never requested (issue #1319).
func TestExecUpgradeFailsWhenSendUpgradeFails(t *testing.T) {
	origCfg, origNow, origDial, origProbe := cfg, connectionNow, dialUpgradeClient, probeDaemonIdentityFn

	t.Cleanup(func() {
		cfg, connectionNow, dialUpgradeClient, probeDaemonIdentityFn = origCfg, origNow, origDial, origProbe
	})

	cfg = &config.Config{Connection: config.ConnectionConfig{HandshakeTimeout: "1s"}}
	connectionNow = func() time.Time { return time.Unix(1_700_000, 0) }

	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{
			payloadEnv("handshake_ok", protocol.HandshakeOkMsg{Version: version.Version, DaemonVersion: version.Version, DaemonInstanceID: "old-gen"}),
		},
		sendErr: errors.New("write failed"),
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	probed := false
	probeDaemonIdentityFn = func() (string, string) {
		probed = true
		return version.Version, "new-gen"
	}

	if err := execUpgrade("done"); err == nil {
		t.Fatal("expected execUpgrade to fail when the upgrade request can't be sent")
	}

	if probed {
		t.Error("readiness probe ran despite a failed upgrade-request send")
	}
}
