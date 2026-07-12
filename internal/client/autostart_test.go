package client

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
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

	err = startDaemonWithLauncher("", func(string, []string) error {
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

	err := startDaemonExecutable("", executable, false, func(gotExecutable string, args []string) error {
		launched = true

		if gotExecutable != executable {
			t.Errorf("launcher executable = %q, want %q", gotExecutable, executable)
		}

		if len(args) != 2 || args[0] != "daemon" || args[1] != "start" {
			t.Errorf("launcher args = %v, want [daemon start]", args)
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

	if daemonResponds(sockPath, "") {
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

	if daemonResponds(sockPath, "") {
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

	if daemonResponds(sockPath, "") {
		t.Fatal("expected daemonResponds to be false for a non-graith server")
	}
}

func TestDaemonRespondsTrueOnHandshakeOK(t *testing.T) {
	shortenHandshakeTimeout(t, 2*time.Second)

	sockPath := shortSockPath(t, "braw.sock")

	serveHandshake(t, sockPath, func(conn net.Conn) {
		writeHandshakeResponse(t, conn, "handshake_ok")
	})

	if !daemonResponds(sockPath, "") {
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

	if !daemonResponds(sockPath, "") {
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

	if !daemonResponds(sockPath, "") {
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

	if !daemonResponds(sockPath, "human-braw") {
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
	startDaemonFn = fn

	t.Cleanup(func() { startDaemonFn = orig })
}

func shortenStartTimeout(t *testing.T, d time.Duration) {
	t.Helper()

	orig := daemonStartTimeout
	daemonStartTimeout = d

	t.Cleanup(func() { daemonStartTimeout = orig })
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

	_, err := EnsureDaemon(config.Paths{SocketPath: sockPath}, "")
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

	conn, err := EnsureDaemon(config.Paths{SocketPath: sockPath}, "")
	if err != nil {
		t.Fatalf("EnsureDaemon returned error for a live daemon: %v", err)
	}

	_ = conn.Close()
}
