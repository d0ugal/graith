package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestValidateDaemonExecutableRejectsGoTestBinary(t *testing.T) {
	testBinary := filepath.Join(t.TempDir(), "dreich.test")

	err := validateDaemonExecutable(testBinary, false)
	if err == nil {
		t.Fatal("expected Go test binary to be rejected")
	}

	if !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("expected clear Go test binary error, got %q", err)
	}
}

func TestReconcileUnresponsiveDaemonVerifiesOwnedGeneration(t *testing.T) {
	oldIsDaemon := recoveryIsDaemon
	oldStartTime := recoveryStartTime
	oldStop := recoveryStopIdentity
	oldSocketGone := recoverySocketGone

	t.Cleanup(func() {
		recoveryIsDaemon, recoveryStartTime = oldIsDaemon, oldStartTime
		recoveryStopIdentity, recoverySocketGone = oldStop, oldSocketGone
	})

	pidFile := filepath.Join(t.TempDir(), "braw.pid")
	if err := os.WriteFile(pidFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{PIDFile: pidFile, SocketPath: filepath.Join(t.TempDir(), "braw.sock")}

	var stopped DaemonIdentity

	recoveryIsDaemon = func(pid int) bool { return pid == 4242 }

	recoveryStartTime = func(pid int) (int64, error) { return int64(pid), nil }
	recoveryStopIdentity = func(pid int, start int64) error {
		stopped = DaemonIdentity{PID: pid, StartTime: start}
		return nil
	}

	recoverySocketGone = func(string) bool { return true }

	recovered, err := reconcileUnresponsiveDaemonGeneration(paths, nil)

	if err != nil || !recovered {
		t.Fatalf("reconcileUnresponsiveDaemonGeneration() = (%t, %v)", recovered, err)
	}

	if stopped != (DaemonIdentity{PID: 4242, StartTime: 4242}) {
		t.Fatalf("stopped identity = %#v", stopped)
	}
}

func TestEnsureDaemonRecoversLivePIDAfterLaunchFailure(t *testing.T) {
	shortenStartTimeout(t, 100*time.Millisecond)

	pidFile := filepath.Join(t.TempDir(), "braw.pid")
	if err := os.WriteFile(pidFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{PIDFile: pidFile, SocketPath: filepath.Join(t.TempDir(), "braw.sock")}
	origIsDaemon := recoveryIsDaemon
	origStartTime := recoveryStartTime
	origStop := recoveryStopIdentity
	origSocketGone := recoverySocketGone
	origStart := startDaemonFn

	t.Cleanup(func() {
		recoveryIsDaemon, recoveryStartTime = origIsDaemon, origStartTime
		recoveryStopIdentity, recoverySocketGone, startDaemonFn = origStop, origSocketGone, origStart
	})

	recoveryIsDaemon = func(pid int) bool { return pid == 4242 }
	recoveryStartTime = func(int) (int64, error) { return 17, nil }
	recoveryStopIdentity = func(int, int64) error { return nil }
	recoverySocketGone = func(string) bool { return true }

	launches := 0
	startDaemonFn = func(context.Context, *config.Config, config.Paths, string) error {
		launches++
		if launches == 1 {
			return errors.New("launcher failed")
		}

		return errors.New("replacement launcher failed")
	}

	_, err := EnsureDaemonConfigured(config.Default(), paths, "")
	if err == nil || !strings.Contains(err.Error(), "replacement launcher failed") {
		t.Fatalf("EnsureDaemonConfigured() error = %v, want replacement launch failure", err)
	}

	if launches != 2 {
		t.Fatalf("launch attempts = %d, want initial launch plus recovery retry", launches)
	}
}

func TestEnsureDaemonRecoversAfterReadinessTimeout(t *testing.T) {
	shortenStartTimeout(t, 60*time.Millisecond)

	pidFile := filepath.Join(t.TempDir(), "braw.pid")
	if err := os.WriteFile(pidFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{PIDFile: pidFile, SocketPath: filepath.Join(t.TempDir(), "braw.sock")}
	origIsDaemon, origStartTime, origStop, origSocketGone, origStart := recoveryIsDaemon, recoveryStartTime, recoveryStopIdentity, recoverySocketGone, startDaemonFn

	t.Cleanup(func() {
		recoveryIsDaemon, recoveryStartTime = origIsDaemon, origStartTime
		recoveryStopIdentity, recoverySocketGone, startDaemonFn = origStop, origSocketGone, origStart
	})

	recoveryIsDaemon = func(int) bool { return true }
	recoveryStartTime = func(int) (int64, error) { return 17, nil }
	recoveryStopIdentity = func(int, int64) error {
		return os.Remove(pidFile)
	}
	recoverySocketGone = func(string) bool { return true }

	launches := 0
	startDaemonFn = func(context.Context, *config.Config, config.Paths, string) error {
		launches++
		return nil
	}

	_, err := EnsureDaemonConfigured(config.Default(), paths, "")
	if err == nil || !strings.Contains(err.Error(), "daemon did not start in time") {
		t.Fatalf("EnsureDaemonConfigured() error = %v, want readiness timeout", err)
	}

	if launches != 2 {
		t.Fatalf("launch attempts = %d, want initial launch plus recovery retry", launches)
	}
}

func TestEnsureDaemonDoesNotLaunchDuringPendingUpgradeAdoption(t *testing.T) {
	shortenStartTimeout(t, 40*time.Millisecond)
	shortenStartPollInterval(t, time.Millisecond)

	runtimeDir := t.TempDir()
	writePendingUpgradeAdoptionJournal(t, runtimeDir)

	pidFile := filepath.Join(t.TempDir(), "braw.pid")
	if err := os.WriteFile(pidFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{
		PIDFile:    pidFile,
		RuntimeDir: runtimeDir,
		SocketPath: filepath.Join(t.TempDir(), "braw.sock"),
	}

	origIsDaemon, origStartTime, origStop, origStart := recoveryIsDaemon, recoveryStartTime, recoveryStopIdentity, startDaemonFn

	t.Cleanup(func() {
		recoveryIsDaemon, recoveryStartTime = origIsDaemon, origStartTime
		recoveryStopIdentity, startDaemonFn = origStop, origStart
	})

	recoveryIsDaemon = func(pid int) bool { return pid == 4242 }
	recoveryStartTime = func(int) (int64, error) { return 17, nil }
	recoveryStopIdentity = func(int, int64) error {
		t.Fatal("pending upgrade adoption daemon was stopped")

		return nil
	}
	startDaemonFn = func(context.Context, *config.Config, config.Paths, string) error {
		t.Fatal("pending upgrade adoption launched a competing daemon")

		return nil
	}

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		return nil, errors.New("adoption not serving yet")
	})

	_, err := EnsureDaemonConfigured(config.Default(), paths, "")
	if !errors.Is(err, errUpgradeAdoptionInProgress) {
		t.Fatalf("EnsureDaemonConfigured() error = %v, want pending upgrade adoption", err)
	}
}

func TestEnsureDaemonDoesNotRecoverWhenUpgradeJournalAppearsAfterLaunch(t *testing.T) {
	shortenStartTimeout(t, 40*time.Millisecond)
	shortenStartPollInterval(t, time.Millisecond)

	runtimeDir := t.TempDir()

	pidFile := filepath.Join(t.TempDir(), "braw.pid")
	if err := os.WriteFile(pidFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{
		PIDFile:    pidFile,
		RuntimeDir: runtimeDir,
		SocketPath: filepath.Join(t.TempDir(), "braw.sock"),
	}

	origIsDaemon, origStartTime, origStop, origSocketGone, origStart := recoveryIsDaemon, recoveryStartTime, recoveryStopIdentity, recoverySocketGone, startDaemonFn

	t.Cleanup(func() {
		recoveryIsDaemon, recoveryStartTime = origIsDaemon, origStartTime
		recoveryStopIdentity, recoverySocketGone, startDaemonFn = origStop, origSocketGone, origStart
	})

	recoveryIsDaemon = func(pid int) bool { return pid == 4242 }
	recoveryStartTime = func(int) (int64, error) { return 17, nil }
	recoveryStopIdentity = func(int, int64) error {
		t.Fatal("pending upgrade adoption daemon was recovered destructively")

		return nil
	}
	recoverySocketGone = func(string) bool {
		t.Fatal("pending upgrade adoption recovery waited for socket removal")

		return false
	}

	launches := 0
	startDaemonFn = func(context.Context, *config.Config, config.Paths, string) error {
		launches++

		writePendingUpgradeAdoptionJournal(t, runtimeDir)

		return nil
	}

	stubDialLocalDaemon(t, func() (net.Conn, error) {
		return nil, errors.New("adoption not serving yet")
	})

	_, err := EnsureDaemonConfigured(config.Default(), paths, "")
	if !errors.Is(err, errUpgradeAdoptionInProgress) {
		t.Fatalf("EnsureDaemonConfigured() error = %v, want pending upgrade adoption", err)
	}

	if launches != 1 {
		t.Fatalf("launch attempts = %d, want initial launch only", launches)
	}
}

func TestReconcileUnresponsiveDaemonDoesNotKillNewGeneration(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "braw.pid")
	if err := os.WriteFile(pidFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{PIDFile: pidFile, SocketPath: filepath.Join(t.TempDir(), "braw.sock")}
	origIsDaemon, origStartTime, origStop, origSocketGone := recoveryIsDaemon, recoveryStartTime, recoveryStopIdentity, recoverySocketGone

	t.Cleanup(func() {
		recoveryIsDaemon, recoveryStartTime = origIsDaemon, origStartTime
		recoveryStopIdentity, recoverySocketGone = origStop, origSocketGone
	})

	recoveryIsDaemon = func(int) bool { return true }
	recoveryStartTime = func(int) (int64, error) { return 18, nil }
	recoveryStopIdentity = func(int, int64) error { t.Fatal("new daemon generation was stopped"); return nil }
	recoverySocketGone = func(string) bool { return true }

	recovered, err := reconcileUnresponsiveDaemonGeneration(paths, &DaemonIdentity{PID: 4242, StartTime: 17})
	if err != nil || recovered {
		t.Fatalf("reconcileUnresponsiveDaemonGeneration() = (%t, %v), want (false, nil)", recovered, err)
	}
}

func TestValidateDaemonExecutableRejectsCustomNamedGoTestBinary(t *testing.T) {
	testBinary := filepath.Join(t.TempDir(), "canny")

	err := validateDaemonExecutable(testBinary, true)
	if err == nil {
		t.Fatal("expected custom-named Go test binary to be rejected")
	}

	if !strings.Contains(err.Error(), filepath.Base(testBinary)) {
		t.Fatalf("expected error to identify rejected executable, got %q", err)
	}
}

func TestValidateDaemonExecutableAllowsGraithBinary(t *testing.T) {
	graithBinary := filepath.Join(t.TempDir(), "gr")

	if err := validateDaemonExecutable(graithBinary, false); err != nil {
		t.Fatalf("expected graith binary to be allowed, got %v", err)
	}
}

func TestStartDaemonRejectsGoTestBinaryWithoutLaunching(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}

	launched := false

	err = startDaemonWithLauncher("", config.Paths{}, func(string, []string, config.Paths) error {
		launched = true
		return nil
	})
	if err == nil {
		t.Fatal("expected startDaemon to reject the Go test binary")
	}

	if launched {
		t.Fatal("startDaemon launched a child process for a Go test binary")
	}

	if !strings.Contains(err.Error(), filepath.Base(executable)) {
		t.Fatalf("expected error to identify rejected executable, got %q", err)
	}
}

func TestStartDaemonExecutableLaunchesGraithBinary(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "gr")
	launched := false

	paths := config.Paths{DataDir: "/bothy"}

	err := startDaemonExecutable("", executable, paths, false, func(gotExecutable string, args []string, gotPaths config.Paths) error {
		launched = true

		if gotExecutable != executable {
			t.Errorf("launcher executable = %q, want %q", gotExecutable, executable)
		}

		if len(args) != 2 || args[0] != "daemon" || args[1] != "start" {
			t.Errorf("launcher args = %v, want [daemon start]", args)
		}

		if gotPaths.DataDir != paths.DataDir {
			t.Errorf("launcher paths = %#v, want %#v", gotPaths, paths)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("startDaemonExecutable returned error: %v", err)
	}

	if !launched {
		t.Fatal("expected regular graith binary to be launched")
	}
}

func TestPrepareDaemonCommandCapturesRuntimeStderrAppendOnly(t *testing.T) {
	dir := t.TempDir()

	script := filepath.Join(dir, "braw-stderr.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >&2\nprintf 'stdout:%s\\n' \"$1\"\n"), 0o755); err != nil { //nolint:gosec // G306: executable shell fixture.
		t.Fatal(err)
	}

	paths := config.Paths{DataDir: filepath.Join(dir, "data")}

	runPreparedDaemonCommand(t, script, []string{"dreich"}, paths)
	runPreparedDaemonCommand(t, script, []string{"canny"}, paths)

	stderrPath := filepath.Join(paths.DataDir, "daemon.stderr.log")

	got, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "dreich\ncanny\n" {
		t.Fatalf("daemon stderr log = %q, want appended runtime stderr only", got)
	}

	info, err := os.Stat(stderrPath)
	if err != nil {
		t.Fatal(err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("daemon stderr log mode = %o, want 0600", mode)
	}
}

func TestPrepareDaemonCommandReportsBlockedStderrLog(t *testing.T) {
	dir := t.TempDir()

	blocker := filepath.Join(dir, "data")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, cleanup, err := prepareDaemonCommand("braw", nil, config.Paths{DataDir: blocker})
	if cleanup != nil {
		cleanup()
	}

	if err == nil {
		t.Fatal("expected blocked stderr log path to fail")
	}

	if cmd != nil {
		t.Fatalf("prepareDaemonCommand returned cmd %#v despite stderr log failure", cmd)
	}

	if !strings.Contains(err.Error(), "create daemon stderr log directory") {
		t.Fatalf("prepareDaemonCommand error = %v, want stderr log directory failure", err)
	}
}

func TestDaemonStartArgsStripsConfigInsideSession(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "braw-session-123")

	args := daemonStartArgs("/tmp/evil.toml")

	for _, arg := range args {
		if arg == "--config" || arg == "/tmp/evil.toml" {
			t.Fatalf("daemon start args should not contain --config inside a session, got %v", args)
		}
	}

	if len(args) != 2 || args[0] != "daemon" || args[1] != "start" {
		t.Errorf("expected [daemon start], got %v", args)
	}
}

func TestDaemonStartArgsAllowsConfigOutsideSession(t *testing.T) {
	if v, ok := os.LookupEnv("GRAITH_SESSION_ID"); ok {
		t.Cleanup(func() { _ = os.Setenv("GRAITH_SESSION_ID", v) })
	}

	_ = os.Unsetenv("GRAITH_SESSION_ID")

	args := daemonStartArgs("/home/user/custom.toml")

	if len(args) != 4 || args[2] != "--config" || args[3] != "/home/user/custom.toml" {
		t.Errorf("expected [daemon start --config /home/user/custom.toml], got %v", args)
	}
}

func TestDaemonStartArgsStripsConfigWhenSessionIDEmpty(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	args := daemonStartArgs("/tmp/evil.toml")

	for _, arg := range args {
		if arg == "--config" || arg == "/tmp/evil.toml" {
			t.Fatalf("daemon start args should not contain --config when GRAITH_SESSION_ID is set (even empty), got %v", args)
		}
	}
}

func TestDaemonStartArgsEmptyConfigFile(t *testing.T) {
	args := daemonStartArgs("")

	if len(args) != 2 || args[0] != "daemon" || args[1] != "start" {
		t.Errorf("expected [daemon start] for empty config, got %v", args)
	}
}

func runPreparedDaemonCommand(t *testing.T, executable string, args []string, paths config.Paths) {
	t.Helper()

	cmd, cleanup, err := prepareDaemonCommand(executable, args, paths)
	if err != nil {
		t.Fatalf("prepareDaemonCommand: %v", err)
	}
	defer cleanup()

	if err := cmd.Run(); err != nil {
		t.Fatalf("daemon command run: %v", err)
	}
}

// shortSockPath returns a Unix socket path in /tmp, keeping it under the
// macOS 104-byte sun_path limit that t.TempDir's long paths can exceed.
func shortSockPath(t *testing.T, name string) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "gr-sock-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return filepath.Join(dir, name)
}

// shortenHandshakeTimeout swaps in a small handshake timeout for the duration
// of a test so probes against unresponsive sockets don't wait the full 5s.
func shortenHandshakeTimeout(t *testing.T, d time.Duration) {
	t.Helper()

	orig := daemonHandshakeTimeout
	daemonHandshakeTimeout = d

	t.Cleanup(func() { daemonHandshakeTimeout = orig })
}

// serveHandshake starts a Unix listener at sockPath whose behaviour on each
// accepted connection is supplied by handle. It returns once the listener is
// ready. The listener is closed via t.Cleanup.
func serveHandshake(t *testing.T, sockPath string, handle func(net.Conn)) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	var wg sync.WaitGroup

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				wg.Wait()
				return
			}

			wg.Add(1)

			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()

				handle(conn)
			}()
		}
	}()
}

func TestDaemonRespondsFalseWhenNothingListening(t *testing.T) {
	shortenHandshakeTimeout(t, 200*time.Millisecond)

	sockPath := shortSockPath(t, "graith.sock")

	if daemonRespondsWithDeadline(sockPath, "", "", time.Time{}) {
		t.Fatal("expected daemonResponds to be false when nothing is listening")
	}
}

func TestDaemonRespondsFalseOnStuckSocket(t *testing.T) {
	shortenHandshakeTimeout(t, 200*time.Millisecond)

	sockPath := shortSockPath(t, "dreich.sock")

	// A stuck process: reads the probe's full handshake frame, then stays
	// silent. The probe must hit its handshake deadline to give up — not EOF —
	// so removing the deadline would hang instead of returning quickly.
	serveHandshake(t, sockPath, func(conn net.Conn) {
		reader := protocol.NewFrameReader(conn)
		// Drain the handshake frame the probe writes.
		_, _ = reader.ReadFrame()
		// Block until the probe closes the connection at its deadline.
		_, _ = reader.ReadFrame()
	})

	start := time.Now()

	if daemonRespondsWithDeadline(sockPath, "", "", time.Time{}) {
		t.Fatal("expected daemonResponds to be false for a socket that never replies")
	}

	elapsed := time.Since(start)
	// The probe should have blocked until roughly the (shortened) handshake
	// deadline, proving the deadline — not an immediate EOF — is what unblocked
	// it. Allow generous slack on both ends for scheduling jitter.
	if elapsed < 100*time.Millisecond {
		t.Fatalf("daemonResponds returned in %v; expected it to wait for the handshake deadline", elapsed)
	}

	if elapsed > 2*time.Second {
		t.Fatalf("daemonResponds took %v; handshake deadline was not enforced", elapsed)
	}
}

func TestDaemonRespondsFalseOnForeignSocket(t *testing.T) {
	shortenHandshakeTimeout(t, 500*time.Millisecond)

	sockPath := shortSockPath(t, "thrawn.sock")

	// A non-graith server: sends bytes that aren't a valid graith frame.
	serveHandshake(t, sockPath, func(conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\ngarbage"))
	})

	if daemonRespondsWithDeadline(sockPath, "", "", time.Time{}) {
		t.Fatal("expected daemonResponds to be false for a non-graith server")
	}
}

func TestDaemonRespondsTrueOnHandshakeOK(t *testing.T) {
	shortenHandshakeTimeout(t, 2*time.Second)

	sockPath := shortSockPath(t, "braw.sock")

	serveHandshake(t, sockPath, func(conn net.Conn) {
		writeHandshakeResponse(t, conn, "handshake_ok")
	})

	if !daemonRespondsWithDeadline(sockPath, "", "", time.Time{}) {
		t.Fatal("expected daemonResponds to be true for a graith daemon replying handshake_ok")
	}
}

func TestDaemonRespondsTrueOnHandshakeErr(t *testing.T) {
	shortenHandshakeTimeout(t, 2*time.Second)

	sockPath := shortSockPath(t, "canny.sock")

	// A protocol-level rejection still proves a graith daemon is present.
	serveHandshake(t, sockPath, func(conn net.Conn) {
		writeHandshakeResponse(t, conn, "handshake_err")
	})

	if !daemonRespondsWithDeadline(sockPath, "", "", time.Time{}) {
		t.Fatal("expected daemonResponds to be true for a daemon replying handshake_err")
	}
}

func TestDaemonRespondsTrueOnAuthError(t *testing.T) {
	shortenHandshakeTimeout(t, 2*time.Second)

	sockPath := shortSockPath(t, "fash.sock")

	// A fail-closed daemon rejects a tokenless handshake at the auth gate with a
	// generic "error" frame (not handshake_err). That reply still proves a graith
	// daemon is present, so the probe must report it as alive — otherwise every
	// CLI command would treat the live daemon as dead and autostart a doomed
	// second daemon (the v0.67.1 regression this fix closes).
	serveHandshake(t, sockPath, func(conn net.Conn) {
		writeHandshakeResponse(t, conn, "error")
	})

	if !daemonRespondsWithDeadline(sockPath, "", "", time.Time{}) {
		t.Fatal("expected daemonResponds to be true for a daemon replying error (auth rejection)")
	}
}

func TestDaemonRespondsSendsToken(t *testing.T) {
	shortenHandshakeTimeout(t, 2*time.Second)

	sockPath := shortSockPath(t, "ken.sock")

	gotToken := make(chan string, 1)

	// The daemon records the token the probe presents, then replies handshake_ok.
	serveHandshake(t, sockPath, func(conn net.Conn) {
		reader := protocol.NewFrameReader(conn)
		writer := protocol.NewFrameWriter(conn)

		frame, err := reader.ReadFrame()
		if err != nil {
			return
		}

		env, err := protocol.DecodeControl(frame.Payload)
		if err != nil {
			return
		}

		gotToken <- env.Token

		data, err := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{Version: protocol.Version})
		if err != nil {
			return
		}

		_ = writer.WriteFrame(protocol.ChannelControl, data)
	})

	if !daemonRespondsWithDeadline(sockPath, "human-braw", "", time.Time{}) {
		t.Fatal("expected daemonResponds to be true when the daemon replies handshake_ok")
	}

	select {
	case tok := <-gotToken:
		if tok != "human-braw" {
			t.Fatalf("probe presented token %q, want %q", tok, "human-braw")
		}
	case <-time.After(time.Second):
		t.Fatal("daemon never received the probe handshake")
	}
}

func TestDaemonRespondsSendsProfile(t *testing.T) {
	shortenHandshakeTimeout(t, 2*time.Second)

	sockPath := shortSockPath(t, "kirk.sock")

	gotProfile := make(chan string, 1)

	// The daemon records the profile the probe presents in its handshake, then
	// replies handshake_ok. A daemon on a non-default profile checks this field,
	// so the probe must forward it to get handshake_ok instead of a spurious
	// handshake_err.
	serveHandshake(t, sockPath, func(conn net.Conn) {
		reader := protocol.NewFrameReader(conn)
		writer := protocol.NewFrameWriter(conn)

		frame, err := reader.ReadFrame()
		if err != nil {
			return
		}

		env, err := protocol.DecodeControl(frame.Payload)
		if err != nil {
			return
		}

		var hs protocol.HandshakeMsg
		if err := protocol.DecodePayload(env, &hs); err != nil {
			return
		}

		gotProfile <- hs.Profile

		data, err := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{Version: protocol.Version})
		if err != nil {
			return
		}

		_ = writer.WriteFrame(protocol.ChannelControl, data)
	})

	if !daemonRespondsWithDeadline(sockPath, "", "bothy", time.Time{}) {
		t.Fatal("expected daemonResponds to be true when the daemon replies handshake_ok")
	}

	select {
	case prof := <-gotProfile:
		if prof != "bothy" {
			t.Fatalf("probe presented profile %q, want %q", prof, "bothy")
		}
	case <-time.After(time.Second):
		t.Fatal("daemon never received the probe handshake")
	}
}

// writeHandshakeResponse reads the client's handshake frame and replies with a
// control frame of the given type, mimicking a graith daemon.
func writeHandshakeResponse(t *testing.T, conn net.Conn, respType string) {
	t.Helper()

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	if _, err := reader.ReadFrame(); err != nil {
		return
	}

	var payload any

	switch respType {
	case "handshake_ok":
		payload = protocol.HandshakeOkMsg{Version: protocol.Version}
	case "error":
		payload = protocol.ErrorMsg{Message: "invalid token"}
	default:
		payload = protocol.HandshakeErrMsg{Reason: "thrawn"}
	}

	data, err := protocol.EncodeControl(respType, payload)
	if err != nil {
		return
	}

	_ = writer.WriteFrame(protocol.ChannelControl, data)
}

// stubStartDaemon replaces the daemon-spawning function for the duration of a
// test so EnsureDaemon can be exercised without exec'ing a real process.
func stubStartDaemon(t *testing.T, fn func(configFile string) error) {
	t.Helper()

	orig := startDaemonFn
	startDaemonFn = func(_ context.Context, _ *config.Config, _ config.Paths, configFile string) error {
		return fn(configFile)
	}

	t.Cleanup(func() { startDaemonFn = orig })
}

func shortenStartTimeout(t *testing.T, d time.Duration) {
	t.Helper()

	orig := daemonStartTimeout
	origUpgrade := upgradeReadinessFloor
	daemonStartTimeout = d
	upgradeReadinessFloor = 0

	t.Cleanup(func() {
		daemonStartTimeout = orig
		upgradeReadinessFloor = origUpgrade
	})
}

func writePendingUpgradeAdoptionJournal(t *testing.T, dir string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, "upgrade-adoption-"+strings.Repeat("a", 32)+".pending"), []byte("braw"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIsPendingUpgradeAdoptionJournalName(t *testing.T) {
	tests := map[string]struct {
		name string
		want bool
	}{
		"pending": {
			name: "upgrade-adoption-" + strings.Repeat("a", 32) + ".pending",
			want: true,
		},
		"committed marker": {
			name: "upgrade-adoption-" + strings.Repeat("a", 32) + ".pending.committed",
			want: false,
		},
		"rolled back marker": {
			name: "upgrade-adoption-" + strings.Repeat("a", 32) + ".pending.rolledback",
			want: false,
		},
		"quarantine": {
			name: "upgrade-adoption-" + strings.Repeat("a", 32) + ".quarantine",
			want: false,
		},
		"short id": {
			name: "upgrade-adoption-" + strings.Repeat("a", 31) + ".pending",
			want: false,
		},
		"uppercase id": {
			name: "upgrade-adoption-" + strings.Repeat("A", 32) + ".pending",
			want: false,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := isPendingUpgradeAdoptionJournalName(test.name); got != test.want {
				t.Fatalf("isPendingUpgradeAdoptionJournalName(%q) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}

func TestEnsureDaemonStartsFreshWhenSocketStale(t *testing.T) {
	shortenHandshakeTimeout(t, 200*time.Millisecond)
	shortenStartTimeout(t, 200*time.Millisecond)

	sockPath := shortSockPath(t, "haar.sock")

	// A stale/foreign server occupies the socket but never speaks graith: it
	// drains the probe's handshake and then stays silent.
	serveHandshake(t, sockPath, func(conn net.Conn) {
		reader := protocol.NewFrameReader(conn)
		_, _ = reader.ReadFrame()
		_, _ = reader.ReadFrame()
	})

	// EnsureDaemon must not unlink the socket itself (that could orphan a
	// live-but-slow daemon); it delegates cleanup to the fresh daemon's own
	// startup. The stub only records that a start was attempted and does not
	// produce a real daemon, so EnsureDaemon times out waiting for a response.
	started := false

	stubStartDaemon(t, func(string) error {
		started = true
		return nil
	})

	_, err := EnsureDaemonConfigured(config.Default(), config.Paths{SocketPath: sockPath}, "")
	if err == nil {
		t.Fatal("expected EnsureDaemon to fail when no real daemon starts")
	}

	if !started {
		t.Fatal("expected EnsureDaemon to attempt a fresh daemon start after an unresponsive socket")
	}

	// The socket file is left intact: EnsureDaemon delegates removal to the
	// daemon's Listen, which unlinks before binding.
	if _, statErr := os.Stat(sockPath); statErr != nil {
		t.Fatalf("expected EnsureDaemon to leave the socket file untouched, stat err = %v", statErr)
	}
}

// A started daemon socket may accept the readiness connection and then never
// answer the handshake. The aggregate start_timeout must cap that first probe,
// even when the independent dial/handshake policies are much longer (#1319).
func TestEnsureDaemonStartBudgetCapsStalledHandshake(t *testing.T) {
	shortenHandshakeTimeout(t, 5*time.Second)
	shortenStartTimeout(t, 60*time.Millisecond)
	shortenStartPollInterval(t, 5*time.Millisecond)

	origDial := dialLocalDaemon

	t.Cleanup(func() { dialLocalDaemon = origDial })

	var (
		started              bool
		readinessDialTimeout time.Duration
	)

	dialLocalDaemon = func(_, _ string, timeout time.Duration) (net.Conn, error) {
		if !started {
			return nil, errors.New("dreich: not started")
		}

		readinessDialTimeout = timeout
		clientConn, serverConn := net.Pipe()

		go func() {
			defer func() { _ = serverConn.Close() }()

			reader := protocol.NewFrameReader(serverConn)
			_, _ = reader.ReadFrame() // accept and drain the probe handshake
			_, _ = reader.ReadFrame() // stay silent until the client deadline closes
		}()

		return clientConn, nil
	}

	stubStartDaemon(t, func(string) error {
		started = true
		return nil
	})

	start := time.Now()

	conn, err := EnsureDaemonConfigured(config.Default(), config.Paths{SocketPath: "/bothy/stalled.sock"}, "")
	if conn != nil {
		_ = conn.Close()

		t.Fatal("EnsureDaemon returned a connection for a daemon that never handshook")
	}

	if err == nil {
		t.Fatal("EnsureDaemon should time out when the accepted readiness socket never handshakes")
	}

	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("EnsureDaemon took %v, want the stalled handshake capped by the 60ms aggregate start budget", elapsed)
	}

	if readinessDialTimeout <= 0 || readinessDialTimeout > 60*time.Millisecond {
		t.Fatalf("readiness dial timeout = %v, want it capped by the remaining 60ms start budget", readinessDialTimeout)
	}
}

func TestEnsureDaemonStartBudgetIncludesManagedLaunch(t *testing.T) {
	shortenStartTimeout(t, 40*time.Millisecond)

	origDial := dialLocalDaemon
	origStart := startDaemonFn
	dialLocalDaemon = func(_, _ string, _ time.Duration) (net.Conn, error) {
		return nil, errors.New("dreich: daemon absent")
	}
	startDaemonFn = func(ctx context.Context, _ *config.Config, _ config.Paths, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}

	t.Cleanup(func() {
		dialLocalDaemon = origDial
		startDaemonFn = origStart
	})

	started := time.Now()

	_, err := EnsureDaemonConfigured(config.Default(), config.Paths{SocketPath: "/bothy/absent.sock"}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("EnsureDaemon error = %v, want managed launch deadline", err)
	}

	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("EnsureDaemon launch took %v, want it bounded by start_timeout", elapsed)
	}
}

func TestEnsureDaemonConfiguredContextHonorsCanceledCaller(t *testing.T) {
	origDial := dialLocalDaemon
	origStart := startDaemonFn
	dialLocalDaemon = func(_, _ string, _ time.Duration) (net.Conn, error) {
		t.Fatal("canceled startup attempted to dial the daemon")

		return nil, errors.New("dreich")
	}
	startDaemonFn = func(context.Context, *config.Config, config.Paths, string) error {
		t.Fatal("canceled startup attempted to launch the daemon")

		return nil
	}

	t.Cleanup(func() {
		dialLocalDaemon = origDial
		startDaemonFn = origStart
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := EnsureDaemonConfiguredContext(ctx, config.Default(), config.Paths{SocketPath: "/bothy/canceled.sock"}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureDaemonConfiguredContext() = %v, want canceled caller", err)
	}
}

func TestEnsureDaemonConfiguredContextBoundsInitialSocketProbe(t *testing.T) {
	origDial := dialLocalDaemon
	origStart := startDaemonFn

	probeConn := &deadlineFailConn{}

	t.Cleanup(func() {
		dialLocalDaemon = origDial
		startDaemonFn = origStart
	})

	var (
		probeDialTimeout time.Duration
		startErr         error
		launchErr        = errors.New("dreich: launch stopped")
	)

	dialLocalDaemon = func(_, _ string, timeout time.Duration) (net.Conn, error) {
		probeDialTimeout = timeout

		return probeConn, nil
	}

	startDaemonFn = func(ctx context.Context, _ *config.Config, _ config.Paths, _ string) error {
		startErr = ctx.Err()

		return launchErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	callerDeadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("caller context has no deadline")
	}

	_, err := EnsureDaemonConfiguredContext(ctx, config.Default(), config.Paths{SocketPath: "/bothy/stuck.sock"}, "")
	if !errors.Is(err, launchErr) {
		t.Fatalf("EnsureDaemonConfiguredContext() = %v, want launch error", err)
	}

	if !probeConn.writeCalled {
		t.Fatal("initial socket probe did not attempt a handshake")
	}

	if probeConn.deadline.IsZero() {
		t.Fatal("initial socket probe did not set a deadline")
	}

	if !probeConn.deadline.Before(callerDeadline) {
		t.Fatal("initial socket probe consumed the entire startup budget")
	}

	if probeDialTimeout <= 0 {
		t.Fatalf("initial socket probe dial timeout = %v, want positive timeout", probeDialTimeout)
	}

	if startErr != nil {
		t.Fatalf("launcher context was expired before start: %v", startErr)
	}
}

type deadlineFailConn struct {
	deadline    time.Time
	writeCalled bool
}

func (c *deadlineFailConn) Read([]byte) (int, error) {
	return 0, errors.New("dreich: no frame")
}

func (c *deadlineFailConn) Write([]byte) (int, error) {
	c.writeCalled = true

	if c.deadline.IsZero() {
		return 0, errors.New("dreich: write before deadline")
	}

	return 0, os.ErrDeadlineExceeded
}

func (c *deadlineFailConn) Close() error {
	return nil
}

func (c *deadlineFailConn) LocalAddr() net.Addr {
	return staticTestAddr("local")
}

func (c *deadlineFailConn) RemoteAddr() net.Addr {
	return staticTestAddr("remote")
}

func (c *deadlineFailConn) SetDeadline(t time.Time) error {
	c.deadline = t

	return nil
}

func (c *deadlineFailConn) SetReadDeadline(t time.Time) error {
	return c.SetDeadline(t)
}

func (c *deadlineFailConn) SetWriteDeadline(t time.Time) error {
	return c.SetDeadline(t)
}

type staticTestAddr string

func (a staticTestAddr) Network() string {
	return string(a)
}

func (a staticTestAddr) String() string {
	return string(a)
}

func TestPrepareDaemonCleanRestartRejectsManagedAgentBeforeResolution(t *testing.T) {
	originalMode := detectDaemonServiceModeForCleanRestart
	originalBoundary := cleanRestartSecurityBoundaryDetected

	t.Cleanup(func() {
		detectDaemonServiceModeForCleanRestart = originalMode
		cleanRestartSecurityBoundaryDetected = originalBoundary
	})

	detectDaemonServiceModeForCleanRestart = func() (daemonservice.Mode, string, error) {
		return daemonservice.ModeManaged, "braw", nil
	}
	cleanRestartSecurityBoundaryDetected = func() bool { return true }

	err := prepareDaemonCleanRestartWithGuard(context.Background(), config.Paths{Profile: "canny"}, allowDaemonLifecycleMutation)
	if err == nil || !strings.Contains(err.Error(), "agent-mode caller") {
		t.Fatalf("PrepareDaemonCleanRestart() = %v, want preflight trust rejection", err)
	}
}

func TestPrepareDaemonCleanRestartKeepsFallbackAgentBehavior(t *testing.T) {
	originalMode := detectDaemonServiceModeForCleanRestart
	originalBoundary := cleanRestartSecurityBoundaryDetected

	t.Cleanup(func() {
		detectDaemonServiceModeForCleanRestart = originalMode
		cleanRestartSecurityBoundaryDetected = originalBoundary
	})

	detectDaemonServiceModeForCleanRestart = func() (daemonservice.Mode, string, error) {
		return daemonservice.ModeLinuxFallback, "canny", nil
	}
	cleanRestartSecurityBoundaryDetected = func() bool { return true }

	if err := prepareDaemonCleanRestartWithGuard(context.Background(), config.Paths{Profile: "canny"}, allowDaemonLifecycleMutation); err != nil {
		t.Fatalf("fallback PrepareDaemonCleanRestart() changed behavior: %v", err)
	}
}

func TestEnsureDaemonReusesLiveDaemon(t *testing.T) {
	shortenHandshakeTimeout(t, 2*time.Second)

	sockPath := shortSockPath(t, "bide.sock")

	serveHandshake(t, sockPath, func(conn net.Conn) {
		writeHandshakeResponse(t, conn, "handshake_ok")
	})

	stubStartDaemon(t, func(string) error {
		t.Error("EnsureDaemon should not start a daemon when a live one responds")
		return nil
	})

	conn, err := EnsureDaemonConfigured(config.Default(), config.Paths{SocketPath: sockPath}, "")
	if err != nil {
		t.Fatalf("EnsureDaemon returned error for a live daemon: %v", err)
	}

	_ = conn.Close()
}
