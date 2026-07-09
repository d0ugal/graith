package cli

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
)

// wrapDialErr wraps an errno the way the net package does for a failed unix
// dial, so classifyDialErr is exercised against the real error shape rather
// than a bare errno.
func wrapDialErr(errno syscall.Errno) error {
	return &net.OpError{
		Op:   "dial",
		Net:  "unix",
		Addr: &net.UnixAddr{Name: "/haar/graith.sock", Net: "unix"},
		Err:  &os.SyscallError{Syscall: "connect", Err: errno},
	}
}

// TestClassifyDialErr is the crux of issue #945: a sandbox denial (EPERM/EACCES)
// must be distinguished from a genuinely-down daemon (ECONNREFUSED) and from an
// absent socket (ENOENT), because only the latter two mean the daemon is down.
func TestClassifyDialErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want daemonReachability
	}{
		{"eperm is sandbox denial", wrapDialErr(syscall.EPERM), daemonReachSandboxed},
		{"eacces is sandbox denial", wrapDialErr(syscall.EACCES), daemonReachSandboxed},
		{"enoent is no socket", wrapDialErr(syscall.ENOENT), daemonReachNoSocket},
		{"econnrefused is down", wrapDialErr(syscall.ECONNREFUSED), daemonReachDown},
		{"timeout is down", wrapDialErr(syscall.ETIMEDOUT), daemonReachDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDialErr(tt.err); got != tt.want {
				t.Errorf("classifyDialErr(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestCheckDaemonSandboxedNoFalseFailure verifies the headline fix: from inside
// a sandboxed session where the socket connect() is denied, gr doctor must NOT
// report the daemon as down or its PID as stale. It reports "cannot verify" as
// a warning and leaves the overall health OK.
func TestCheckDaemonSandboxedNoFalseFailure(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	diag := dc.checkDaemon(daemonProbe{reach: daemonReachSandboxed, dialErr: wrapDialErr(syscall.EPERM)})

	if diag != nil {
		t.Fatalf("expected no diagnostics when sandboxed, got %+v", diag)
	}

	if !dc.ok {
		t.Errorf("sandboxed daemon probe must not fail overall health, checks: %v", dc.checks)
	}

	if failed := checkResults(dc, "fail"); len(failed) != 0 {
		t.Errorf("sandboxed daemon probe must not produce a fail, got: %v", failed)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(strings.ToLower(warned), "sandbox") {
		t.Errorf("expected a warn mentioning the sandbox, got: %q", warned)
	}

	// The false "PID is stale" report came from checkStalePID shelling out to ps
	// (also blocked); assert it never ran by checking no "stale" text appears.
	if strings.Contains(strings.ToLower(warned), "stale") {
		t.Errorf("sandboxed probe must not run the stale-PID probe, got: %q", warned)
	}
}

// TestCheckDaemonDownFails verifies a genuinely-unreachable daemon
// (ECONNREFUSED) still fails, so the fix doesn't mask a real outage.
func TestCheckDaemonDownFails(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	diag := dc.checkDaemon(daemonProbe{reach: daemonReachDown, dialErr: wrapDialErr(syscall.ECONNREFUSED)})

	if diag != nil {
		t.Fatalf("expected no diagnostics when down, got %+v", diag)
	}

	if dc.ok {
		t.Error("a genuinely-down daemon must fail overall health")
	}

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "Cannot connect to daemon") {
		t.Errorf("expected a connect failure, got: %q", failed)
	}
}

// TestCheckDaemonNoSocket verifies an absent socket is a benign "not running"
// warning (the daemon auto-starts on the next command), not a failure.
func TestCheckDaemonNoSocket(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	diag := dc.checkDaemon(daemonProbe{reach: daemonReachNoSocket})

	if diag != nil {
		t.Fatalf("expected no diagnostics with no socket, got %+v", diag)
	}

	if !dc.ok {
		t.Errorf("an absent socket must not fail overall health, checks: %v", dc.checks)
	}
}

// TestCheckDaemonOK verifies a reachable daemon reports Running from the
// daemon-supplied diagnostics and returns them for downstream sections.
func TestCheckDaemonOK(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	diag := dc.checkDaemon(daemonProbe{
		reach: daemonReachOK,
		diag:  &protocol.DiagnosticsMsg{DaemonPID: 4242, DaemonUptime: "1m2s"},
	})

	if diag == nil {
		t.Fatal("expected diagnostics to be returned for a reachable daemon")
	}

	if !dc.ok {
		t.Errorf("a healthy daemon must not fail overall health, checks: %v", dc.checks)
	}

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "Running") || !strings.Contains(passed, "4242") {
		t.Errorf("expected a Running check naming the daemon PID, got: %q", passed)
	}
}

// TestCheckVersionMismatchUnderReachableDaemon verifies version-mismatch
// detection fires whenever the daemon is reachable — the actionable "restart
// needed" hint the old code hid whenever the daemon version wasn't obtained.
func TestCheckVersionMismatchUnderReachableDaemon(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	// version.Version is "dev" in tests, so any concrete daemon version mismatches.
	dc := newDoctorContext()
	dc.checkVersion(daemonProbe{reach: daemonReachOK, daemonVersion: "9.9.9-braw"})

	failed := strings.Join(checkResults(dc, "fail"), "\n")
	if !strings.Contains(failed, "Version mismatch") || !strings.Contains(failed, "9.9.9-braw") {
		t.Errorf("expected a version-mismatch failure naming the daemon version, got: %q", failed)
	}
}

// TestCheckVersionSandboxedCannotVerify verifies that from a sandboxed session
// the Version section reports "cannot verify" as a warning rather than the old
// false "Socket exists but daemon not responding" failure.
func TestCheckVersionSandboxedCannotVerify(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	dc.checkVersion(daemonProbe{reach: daemonReachSandboxed, dialErr: wrapDialErr(syscall.EPERM)})

	if failed := checkResults(dc, "fail"); len(failed) != 0 {
		t.Errorf("sandboxed version check must not fail, got: %v", failed)
	}

	warned := strings.Join(checkResults(dc, "warn"), "\n")
	if !strings.Contains(strings.ToLower(warned), "sandbox") {
		t.Errorf("expected a warn about the sandbox, got: %q", warned)
	}
}

// fakeDaemon listens on a unix socket and answers a handshake (with the given
// daemon version) followed by a diagnostics request, mimicking the real daemon
// so probeDaemon can be exercised over an actual socket.
func fakeDaemon(t *testing.T, sockPath, daemonVersion string, diag protocol.DiagnosticsMsg) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		// A sandboxed test host (e.g. macOS Seatbelt) can deny bind() on the
		// socket — the very condition this change handles. There's nothing to
		// listen on, so skip rather than fail; CI runs unsandboxed and exercises
		// the full path.
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("cannot bind unix socket in this sandbox: %v", err)
		}

		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		reader := protocol.NewFrameReader(conn)
		writer := protocol.NewFrameWriter(conn)

		// Handshake.
		if _, err := reader.ReadFrame(); err != nil {
			return
		}

		hsData, _ := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
			Version:       protocol.Version,
			DaemonVersion: daemonVersion,
		})
		_ = writer.WriteFrame(protocol.ChannelControl, hsData)

		// Diagnostics.
		if _, err := reader.ReadFrame(); err != nil {
			return
		}

		diagData, _ := protocol.EncodeControl("diagnostics", diag)
		_ = writer.WriteFrame(protocol.ChannelControl, diagData)
	}()
}

// TestProbeDaemonOverSocket verifies probeDaemon completes the handshake and
// diagnostics exchange over a real socket and prefers the daemon-reported
// version from diagnostics.
func TestProbeDaemonOverSocket(t *testing.T) {
	oldPaths, oldOut := paths, out

	t.Cleanup(func() { paths, out = oldPaths, oldOut })

	out = output.NewWithWriter(false, io.Discard)

	// Unix socket paths are length-limited; keep it short.
	sock := filepath.Join(t.TempDir(), "d.sock")
	paths = oldPaths
	paths.SocketPath = sock

	fakeDaemon(t, sock, "1.2.3-canny", protocol.DiagnosticsMsg{
		DaemonPID:     777,
		DaemonVersion: "1.2.3-canny",
		DaemonUptime:  "3m",
	})

	dc := newDoctorContext()
	probe := dc.probeDaemon()

	if probe.reach != daemonReachOK {
		t.Fatalf("reach = %d, want daemonReachOK", probe.reach)
	}

	if probe.daemonVersion != "1.2.3-canny" {
		t.Errorf("daemonVersion = %q, want 1.2.3-canny", probe.daemonVersion)
	}

	if probe.diag == nil || probe.diag.DaemonPID != 777 {
		t.Errorf("expected diagnostics with PID 777, got %+v", probe.diag)
	}
}

// TestProbeDaemonNoSocket verifies an absent socket classifies as no-socket
// without any dial attempt masking it as something else.
func TestProbeDaemonNoSocket(t *testing.T) {
	oldPaths, oldOut := paths, out

	t.Cleanup(func() { paths, out = oldPaths, oldOut })

	out = output.NewWithWriter(false, io.Discard)

	paths = oldPaths
	paths.SocketPath = filepath.Join(t.TempDir(), "nae-sic-socket.sock")

	dc := newDoctorContext()

	if probe := dc.probeDaemon(); probe.reach != daemonReachNoSocket {
		t.Errorf("reach = %d, want daemonReachNoSocket", probe.reach)
	}
}

// TestCheckVersionMatchPasses verifies a matching daemon version passes cleanly.
func TestCheckVersionMatchPasses(t *testing.T) {
	oldOut := out

	t.Cleanup(func() { out = oldOut })

	out = output.NewWithWriter(false, io.Discard)

	dc := newDoctorContext()
	dc.checkVersion(daemonProbe{reach: daemonReachOK, daemonVersion: version.Version})

	if failed := checkResults(dc, "fail"); len(failed) != 0 {
		t.Errorf("a matching daemon version must not fail, got: %v", failed)
	}

	passed := strings.Join(checkResults(dc, "ok"), "\n")
	if !strings.Contains(passed, "Daemon version") {
		t.Errorf("expected a passing Daemon version check, got: %q", passed)
	}
}
