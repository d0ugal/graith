package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	deadline     time.Time
	deadlineSet  bool
	deadlineErr  error
	daemonPID    int
	daemonPIDErr error
	sendErr      error
	sendErrAt    int
	responses    []protocol.Envelope
	readIdx      int
	sent         []string
	closed       bool
}

func (f *fakeUpgradeConn) SetDeadline(t time.Time) error {
	f.deadline = t
	f.deadlineSet = true

	return f.deadlineErr
}

func (f *fakeUpgradeConn) Handshake() error { return nil }

func (f *fakeUpgradeConn) DaemonPID() (int, error) {
	if f.daemonPIDErr != nil {
		return 0, f.daemonPIDErr
	}

	if f.daemonPID == 0 {
		return os.Getpid(), nil
	}

	return f.daemonPID, nil
}

func (f *fakeUpgradeConn) SendControl(msgType string, _ any) error {
	f.sent = append(f.sent, msgType)
	if f.sendErr != nil && (f.sendErrAt == 0 || f.sendErrAt == len(f.sent)) {
		return f.sendErr
	}

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

func setupUpgradeTest(t *testing.T) *fakeConnClock {
	t.Helper()

	origCfg, origNow, origSleep := cfg, connectionNow, connectionSleep
	origDial, origProbe, origOut, origPaths := dialUpgradeClient, probeDaemonIdentityFn, out, paths
	origNegotiationFloor, origReadinessFloor := upgradeNegotiationFloor, upgradeReadinessFloor
	t.Cleanup(func() {
		cfg, connectionNow, connectionSleep = origCfg, origNow, origSleep
		dialUpgradeClient, probeDaemonIdentityFn, out, paths = origDial, origProbe, origOut, origPaths
		upgradeNegotiationFloor, upgradeReadinessFloor = origNegotiationFloor, origReadinessFloor
	})

	upgradeNegotiationFloor = 0
	upgradeReadinessFloor = 0

	cfg = &config.Config{Connection: config.ConnectionConfig{
		HandshakeTimeout:  "1s",
		StartTimeout:      "1s",
		StartPollInterval: "10ms",
	}}
	out = output.NewWithWriter(false, io.Discard)

	clk := &fakeConnClock{now: time.Unix(1_700_000, 0)}
	connectionNow = clk.Now
	connectionSleep = clk.Sleep

	return clk
}

func upgradeHandshake(instanceID string) protocol.Envelope {
	return payloadEnv("handshake_ok", protocol.HandshakeOkMsg{
		Version:          version.Version,
		DaemonVersion:    version.Version,
		DaemonInstanceID: instanceID,
	})
}

func upgradePreflightOK() protocol.Envelope {
	return payloadEnv("upgrade_preflight_ok", struct{}{})
}

// TestExecUpgradeInstallsConfiguredHandshakeDeadline proves execUpgrade bounds
// its raw handshake + upgrade exchange with the configured local handshake
// deadline before driving it, so a stale daemon that accepts but never replies
// can't wedge the handshake (issue #1242). It uses a refused upgrade to return
// before the readiness wait.
func TestExecUpgradeInstallsConfiguredHandshakeDeadline(t *testing.T) {
	setupUpgradeTest(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{HandshakeTimeout: "3210ms"}}

	fixedNow := time.Unix(1_700_000, 0)
	connectionNow = func() time.Time { return fixedNow }

	fake := &fakeUpgradeConn{
		daemonPID: 48309,
		responses: []protocol.Envelope{
			upgradeHandshake("old-gen"),
			upgradePreflightOK(),
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

func TestExecUpgradeNegotiationFloorCoversServerAdmission(t *testing.T) {
	setupUpgradeTest(t)

	upgradeNegotiationFloor = 30 * time.Second
	cfg.Connection.HandshakeTimeout = "1s"
	fixedNow := time.Unix(1_700_000, 0)
	connectionNow = func() time.Time { return fixedNow }
	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{
			upgradeHandshake("old-gen"),
			upgradePreflightOK(),
			errEnv("upgrade refused"),
		},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	if err := execUpgrade("done"); err == nil {
		t.Fatal("expected injected refusal")
	}

	if want := fixedNow.Add(30 * time.Second); !fake.deadline.Equal(want) {
		t.Fatalf("upgrade negotiation deadline = %v, want %v", fake.deadline, want)
	}
}

// TestExecUpgradeWaitsForNewGenerationDespiteInheritedListener is the #1319
// round-4 scenario: after the upgrade is requested the inherited old listener
// keeps answering (right version, SAME instance ID) during a deliberately
// delayed exec. execUpgrade must keep polling and only report success once a
// DIFFERENT instance ID appears — never on the inherited listener.
func TestExecUpgradeWaitsForNewGenerationDespiteInheritedListener(t *testing.T) {
	setupUpgradeTest(t)

	// The daemon reports instance "old-gen" during the handshake before the upgrade.
	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{
			upgradeHandshake("old-gen"),
			upgradePreflightOK(),
			// upgrade reply: connection drops (daemon exec'ing) -> EOF read.
		},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	// The inherited listener answers with the SAME instance twice (delayed exec),
	// then the exec'd replacement finally reports a fresh instance.
	probes := 0
	probeDaemonIdentityFn = func(time.Time) (string, string) {
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

// TestRestartPreserveReconcilesGenerationAfterReadinessDeadline models the
// replacement becoming healthy immediately after the aggregate readiness wait:
// the old PID file is already gone, but the socket serves the requested version
// from a fresh generation before fallback begins. The restart must succeed from
// that authoritative identity and never enter the session-killing clean path.
func TestRestartPreserveReconcilesGenerationAfterReadinessDeadline(t *testing.T) {
	setupUpgradeTest(t)

	cfg.Connection.StartTimeout = "10ms"

	paths.PIDFile = filepath.Join(t.TempDir(), "graith.pid")
	if err := os.WriteFile(paths.PIDFile, []byte("48309\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeUpgradeConn{
		daemonPID: 48309,
		responses: []protocol.Envelope{upgradeHandshake("croft-auld"), upgradePreflightOK()},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	probes := 0
	probeDaemonIdentityFn = func(deadline time.Time) (string, string) {
		probes++

		if deadline.IsZero() {
			return version.Version, "bothy-new"
		}

		if err := os.Remove(paths.PIDFile); err != nil {
			t.Fatalf("remove old PID file: %v", err)
		}

		return version.Version, "croft-auld"
	}

	var rendered bytes.Buffer

	out = output.NewWithWriter(false, &rendered)

	cleanCalled := false

	err := restartDaemonPreservingSessions(func() error {
		cleanCalled = true

		return errors.New("clean start must not run")
	})
	if err != nil {
		t.Fatalf("late healthy replacement should reconcile as success: %v", err)
	}

	if cleanCalled {
		t.Fatal("clean restart ran after a healthy replacement generation was serving")
	}

	if probes != 2 {
		t.Fatalf("identity probes = %d, want readiness probe plus final reconciliation", probes)
	}

	got := rendered.String()
	if !strings.Contains(got, "Daemon restarted (sessions preserved)") {
		t.Fatalf("restart output = %q, want preserved-session success", got)
	}

	for _, contradictory := range []string{"Preserve failed", "Falling back", "daemon is still running"} {
		if strings.Contains(got, contradictory) {
			t.Errorf("restart output = %q, must not contain %q after successful reconciliation", got, contradictory)
		}
	}
}

// TestRestartPreserveDoesNotKillWhenReadinessIsUnconfirmed proves an accepted
// preserve request never escalates to the PID-signalling clean path merely
// because both the aggregate wait and final reconciliation remain inconclusive.
// The same PID can exec into the valid replacement immediately after the probe.
func TestRestartPreserveDoesNotKillWhenReadinessIsUnconfirmed(t *testing.T) {
	setupUpgradeTest(t)

	cfg.Connection.StartTimeout = "10ms"

	paths.PIDFile = filepath.Join(t.TempDir(), "graith.pid")
	if err := os.WriteFile(paths.PIDFile, []byte("48200\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeUpgradeConn{
		daemonPID: 48309,
		responses: []protocol.Envelope{upgradeHandshake("strath-auld"), upgradePreflightOK()},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }
	probeDaemonIdentityFn = func(time.Time) (string, string) {
		return version.Version, "strath-auld"
	}
	cleanCalled := false

	err := restartDaemonPreservingSessions(func() error {
		cleanCalled = true

		return nil
	})
	if err == nil {
		t.Fatal("unconfirmed preserve restart should return a precise error")
	}

	if cleanCalled {
		t.Fatal("clean restart ran without proving an accepted preserve restart had stopped progressing")
	}

	if !strings.Contains(err.Error(), "automatic clean fallback skipped") {
		t.Fatalf("error = %q, want explicit safe-fallback explanation", err)
	}
}

// TestRestartPreserveNeverFallsBackAfterUnconfirmedPeerExit proves even a dead
// socket peer cannot turn a mutating preserve request into an implicit clean
// restart: the acknowledgement may have been lost after a successful exec.
func TestRestartPreserveNeverFallsBackAfterUnconfirmedPeerExit(t *testing.T) {
	setupUpgradeTest(t)

	cfg.Connection.StartTimeout = "10ms"

	var rendered bytes.Buffer

	out = output.NewWithWriter(false, &rendered)

	paths.PIDFile = filepath.Join(t.TempDir(), "graith.pid")
	if err := os.WriteFile(paths.PIDFile, []byte("48200\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeUpgradeConn{
		daemonPID: 48309,
		responses: []protocol.Envelope{upgradeHandshake("haar-auld"), upgradePreflightOK()},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }
	cleanStartCalled := false
	probeDaemonIdentityFn = func(time.Time) (string, string) {
		if cleanStartCalled {
			return version.Version, "haar-clean"
		}

		return "", ""
	}
	err := restartDaemonPreservingSessions(func() error {
		cleanStartCalled = true

		return nil
	})
	if err == nil {
		t.Fatal("unconfirmed preserve restart unexpectedly succeeded")
	}

	if cleanStartCalled {
		t.Fatal("clean start ran after an unconfirmed preserve request")
	}

	data, err := os.ReadFile(paths.PIDFile)
	if err != nil {
		t.Fatalf("stale PID file should be left for atomic daemon acquisition: %v", err)
	}

	if string(data) != "48200\n" {
		t.Fatalf("stale PID file changed to %q, want it left untouched", data)
	}

	if rendered.Len() != 0 {
		t.Fatalf("unconfirmed preserve request produced success output %q", rendered.String())
	}
}

// TestExecUpgradeFailsWhenOnlyInheritedListenerResponds proves execUpgrade does
// NOT report success when the inherited listener keeps answering with the
// pre-upgrade instance ID (the exec never took), so the caller falls back to a
// guarded reconciliation instead of reporting a false success (issue #1319).
func TestExecUpgradeFailsWhenOnlyInheritedListenerResponds(t *testing.T) {
	setupUpgradeTest(t)

	cfg.Connection.StartTimeout = "50ms"

	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{upgradeHandshake("old-gen"), upgradePreflightOK()},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	// Inherited listener only: right version, unchanged instance ID forever.
	probeDaemonIdentityFn = func(time.Time) (string, string) { return version.Version, "old-gen" }

	err := execUpgrade("upgraded")
	if err == nil {
		t.Fatal("execUpgrade must fail when only the inherited listener responds (no new generation)")
	}

	var unconfirmed *preserveRestartUnconfirmedError
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("execUpgrade error = %T, want guarded unconfirmed classification", err)
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
			setupUpgradeTest(t)

			fake := &fakeUpgradeConn{responses: tc.responses}
			dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

			probed := false
			probeDaemonIdentityFn = func(time.Time) (string, string) {
				probed = true
				// If readiness ever runs, the old daemon would look "new".
				return version.Version, "old-gen-would-be-accepted"
			}

			err := execUpgrade("done")
			if err == nil {
				t.Fatal("expected execUpgrade to fail when the pre-upgrade handshake can't be captured")
			}

			var unconfirmed *preserveRestartUnconfirmedError
			if errors.As(err, &unconfirmed) {
				t.Fatalf("pre-request handshake error = %T, must not be classified as an initiated upgrade", err)
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
	setupUpgradeTest(t)

	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{upgradeHandshake("old-gen"), upgradePreflightOK()},
		sendErr:   errors.New("write failed"),
		sendErrAt: 2,
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	probed := false
	probeDaemonIdentityFn = func(time.Time) (string, string) {
		probed = true
		return version.Version, "new-gen"
	}

	err := execUpgrade("done")
	if err == nil {
		t.Fatal("expected execUpgrade to fail when the upgrade request can't be sent")
	}

	var unconfirmed *preserveRestartUnconfirmedError
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("send error = %T, want guarded unconfirmed classification", err)
	}

	if probed {
		t.Error("readiness probe ran despite a failed upgrade-request send")
	}
}

func TestExecUpgradeRequiresSocketPeerPIDBeforeRequest(t *testing.T) {
	setupUpgradeTest(t)

	fake := &fakeUpgradeConn{
		daemonPIDErr: errors.New("peer credentials unavailable"),
		responses:    []protocol.Envelope{upgradeHandshake("dreich-auld")},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	err := execUpgrade("done")
	if err == nil || !strings.Contains(err.Error(), "identify daemon process") {
		t.Fatalf("execUpgrade() error = %v, want peer-identity failure", err)
	}

	var unconfirmed *preserveRestartUnconfirmedError
	if errors.As(err, &unconfirmed) {
		t.Fatalf("pre-request peer identity error = %T, must not be an initiated upgrade", err)
	}

	if len(fake.sent) != 0 {
		t.Fatalf("upgrade request sent without a socket-bound daemon PID: %v", fake.sent)
	}
}

func TestRestartPreserveDefiniteRefusalSkipsCleanFallback(t *testing.T) {
	setupUpgradeTest(t)

	fake := &fakeUpgradeConn{
		responses: []protocol.Envelope{
			upgradeHandshake("canny-auld"),
			upgradePreflightOK(),
			errEnv("operation not permitted for agent sessions"),
		},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	cleanCalled := false

	err := restartDaemonPreservingSessions(func() error {
		cleanCalled = true

		return nil
	})
	if err == nil {
		t.Fatal("definite preserve refusal should be returned")
	}

	if cleanCalled {
		t.Fatal("clean fallback ran after the daemon definitively refused preserve restart")
	}

	if !strings.Contains(err.Error(), "operation not permitted") ||
		!strings.Contains(err.Error(), "daemon rejected") ||
		strings.Contains(err.Error(), "may still be") {
		t.Fatalf("refusal error = %q, want precise terminal classification", err)
	}
}

func TestRestartPreservePreRequestFailureSkipsCleanFallback(t *testing.T) {
	setupUpgradeTest(t)

	dialUpgradeClient = func() (upgradeExchangeConn, error) {
		return nil, errors.New("canny socket unavailable")
	}

	cleanCalled := false

	err := restartDaemonPreservingSessions(func() error {
		cleanCalled = true

		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "no preserve request was safely initiated") {
		t.Fatalf("pre-request failure = %v, want safe no-fallback classification", err)
	}

	if cleanCalled {
		t.Fatal("clean fallback ran without a socket-bound preserve attempt")
	}
}

func TestExecUpgradeAlreadyInProgressRemainsUnconfirmed(t *testing.T) {
	setupUpgradeTest(t)

	fake := &fakeUpgradeConn{
		daemonPID: 48309,
		responses: []protocol.Envelope{
			upgradeHandshake("thrawn-auld"),
			upgradePreflightOK(),
			errEnv("upgrade already in progress"),
		},
	}
	dialUpgradeClient = func() (upgradeExchangeConn, error) { return fake, nil }

	err := execUpgrade("done")

	var unconfirmed *preserveRestartUnconfirmedError
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("execUpgrade() error = %T, want in-progress request guarded as unconfirmed", err)
	}

	if unconfirmed.priorPID != 48309 || unconfirmed.priorInstanceID != "thrawn-auld" {
		t.Fatalf("unconfirmed identity = (%d, %q), want socket peer/generation", unconfirmed.priorPID, unconfirmed.priorInstanceID)
	}
}
