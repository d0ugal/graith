package cli

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

type deadlineRecordingConn struct {
	net.Conn

	deadline time.Time
}

func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.deadline = deadline

	return nil
}

func preserveCLIConnectionPolicy(t *testing.T) {
	t.Helper()

	oldCfg, oldPaths := cfg, paths
	oldDial, oldNow := dialLocalDaemon, connectionNow

	t.Cleanup(func() {
		cfg, paths = oldCfg, oldPaths
		dialLocalDaemon, connectionNow = oldDial, oldNow
	})
}

func TestDoctorProbeUsesConfiguredLocalDialTimeout(t *testing.T) {
	preserveCLIConnectionPolicy(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{DialTimeout: "1234ms"}}

	paths.SocketPath = filepath.Join(t.TempDir(), "doctor.sock")
	if err := os.WriteFile(paths.SocketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var captured time.Duration

	dialLocalDaemon = func(network, address string, timeout time.Duration) (net.Conn, error) {
		captured = timeout

		return nil, errors.New("dreich dial")
	}

	probe := newDoctorContext().probeDaemon()
	if probe.reach != daemonReachDown {
		t.Fatalf("doctor probe reach = %v, want daemonReachDown", probe.reach)
	}

	if captured != 1234*time.Millisecond {
		t.Fatalf("doctor dial timeout = %v, want configured 1234ms", captured)
	}
}

func TestDaemonVersionProbeUsesConfiguredConnectionPolicy(t *testing.T) {
	preserveCLIConnectionPolicy(t)

	cfg = &config.Config{Connection: config.ConnectionConfig{
		DialTimeout:      "1357ms",
		HandshakeTimeout: "2468ms",
	}}
	paths.SocketPath = "/bothy/daemon.sock"
	paths.HumanTokenFile = filepath.Join(t.TempDir(), "missing-human-token")

	fixedNow := time.Unix(1_000_000, 0)
	connectionNow = func() time.Time { return fixedNow }

	clientConn, serverConn := net.Pipe()

	t.Cleanup(func() { _ = serverConn.Close() })

	recorded := &deadlineRecordingConn{Conn: clientConn}

	var captured time.Duration

	dialLocalDaemon = func(network, address string, timeout time.Duration) (net.Conn, error) {
		captured = timeout

		return recorded, nil
	}

	go func() {
		reader := protocol.NewFrameReader(serverConn)
		writer := protocol.NewFrameWriter(serverConn)

		if _, err := reader.ReadFrame(); err != nil {
			return
		}

		data, _ := protocol.EncodeControl("handshake_ok", protocol.HandshakeOkMsg{
			Version:          protocol.Version,
			DaemonVersion:    "1.2.3-braw",
			DaemonInstanceID: "inst-braw",
		})
		_ = writer.WriteFrame(protocol.ChannelControl, data)
	}()

	if got, id := probeDaemonIdentity(); got != "1.2.3-braw" || id != "inst-braw" {
		t.Fatalf("probeDaemonIdentity() = (%q, %q), want (1.2.3-braw, inst-braw)", got, id)
	}

	if captured != 1357*time.Millisecond {
		t.Errorf("daemon version probe dial timeout = %v, want configured 1357ms", captured)
	}

	wantDeadline := fixedNow.Add(2468 * time.Millisecond)
	if !recorded.deadline.Equal(wantDeadline) {
		t.Errorf("daemon version probe deadline = %v, want %v", recorded.deadline, wantDeadline)
	}
}
