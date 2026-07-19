package client

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/version"
	"golang.org/x/sys/unix"
)

func TestIncompatibleProtocolUpgradeUsesExactCleanRestart(t *testing.T) {
	tests := []struct {
		name       string
		stopErr    error
		socketGone bool
		wantErr    bool
		wantResume int32
	}{
		{name: "verified old daemon exits", socketGone: true, wantResume: 1},
		{name: "verified stop fails closed", stopErr: errors.New("dreich timeout"), socketGone: true, wantErr: true},
		{name: "old socket remains", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			socketPath, receivedUpgrade, dial := serveLegacyProtocolDaemon(t)
			origVersion := version.Version
			origDial := dialLocalDaemon
			origRequest := requestUpgradeForClient
			origStop := stopDaemonIdentityForUpgrade
			origSocket := waitForSocketGoneForUpgrade
			origReconnect := reconnectAfterCleanUpgrade
			version.Version = "0.70.0"
			dialLocalDaemon = dial

			var requests, reconnects atomic.Int32

			requestUpgradeForClient = func(*Client) bool {
				requests.Add(1)
				return true
			}
			stopDaemonIdentityForUpgrade = func(pid int, start int64) error {
				if pid != os.Getpid() || start == 0 {
					t.Errorf("stop identity = pid %d start %d, want exact mock peer identity", pid, start)
				}

				return tc.stopErr
			}
			waitForSocketGoneForUpgrade = func(path string) bool {
				if path != socketPath {
					t.Errorf("wait socket = %q, want %q", path, socketPath)
				}

				return tc.socketGone
			}
			wantClient := &Client{}
			reconnectAfterCleanUpgrade = func(*config.Config, config.Paths, string) (*Client, error) {
				reconnects.Add(1)
				return wantClient, nil
			}

			t.Cleanup(func() {
				version.Version = origVersion
				dialLocalDaemon = origDial
				requestUpgradeForClient = origRequest
				stopDaemonIdentityForUpgrade = origStop
				waitForSocketGoneForUpgrade = origSocket
				reconnectAfterCleanUpgrade = origReconnect
			})

			got, err := connect(config.Default(), config.Paths{SocketPath: socketPath}, "", true)
			if (err != nil) != tc.wantErr {
				t.Fatalf("connect error = %v, wantErr %v", err, tc.wantErr)
			}

			if !tc.wantErr && got != wantClient {
				t.Fatalf("connect client = %p, want clean-restart client %p", got, wantClient)
			}

			if requests.Load() != 0 {
				t.Fatalf("protocol-2 client sent %d preserve/exec requests to protocol-1 daemon", requests.Load())
			}

			if reconnects.Load() != tc.wantResume {
				t.Fatalf("clean restart count = %d, want %d", reconnects.Load(), tc.wantResume)
			}

			select {
			case gotType := <-receivedUpgrade:
				if gotType != "" {
					t.Fatalf("legacy daemon received post-handshake message %q, want connection close", gotType)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("legacy daemon did not observe the clean transition connection close")
			}
		})
	}
}

func TestStopDaemonIdentityRejectsReusedPID(t *testing.T) {
	start, err := grpty.ProcessStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	err = stopDaemonIdentity(os.Getpid(), start+1)
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("stop identity error = %v, want identity-changed failure", err)
	}
}

func TestOlderServerProtocolFromHandshakeError(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{reason: "protocol version mismatch: client=2.0, server=1.0; try upgrading the client", want: "1.0"},
		{reason: "protocol version mismatch: client=2.0, server=2.0; incompatible minor"},
		{reason: "protocol version mismatch: client=2.0, server=thrawn; malformed"},
		{reason: "protocol version mismatch: client=2.0, server=1.thrawn; malformed minor"},
		{reason: "protocol version mismatch: client=2.0, server=-1.0; negative major"},
		{reason: "protocol version mismatch: client=3.0, server=1.0; wrong client"},
		{reason: "profile mismatch: client is braw"},
	}
	for _, tc := range tests {
		got, ok := olderServerProtocolFromHandshakeError(tc.reason)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("older protocol from %q = %q, %v; want %q", tc.reason, got, ok, tc.want)
		}
	}
}

func serveLegacyProtocolDaemon(t *testing.T) (string, <-chan string, func(string, string, time.Duration) (net.Conn, error)) {
	t.Helper()

	socketPath := "/bothy/legacy.sock"
	received := make(chan string, 1)

	var connections atomic.Int32

	dial := func(network, address string, _ time.Duration) (net.Conn, error) {
		if network != "unix" || address != socketPath {
			return nil, errors.New("unexpected legacy daemon address")
		}

		connection := int(connections.Add(1) - 1)
		if connection >= 2 {
			return nil, errors.New("unexpected extra legacy daemon connection")
		}

		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}

		clientFile := os.NewFile(uintptr(fds[0]), "legacy-client.sock")
		serverFile := os.NewFile(uintptr(fds[1]), "legacy-server.sock")
		clientConn, clientErr := net.FileConn(clientFile)
		serverConn, serverErr := net.FileConn(serverFile)
		_ = clientFile.Close()
		_ = serverFile.Close()

		if clientErr != nil {
			if serverConn != nil {
				_ = serverConn.Close()
			}

			return nil, clientErr
		}

		if serverErr != nil {
			_ = clientConn.Close()
			return nil, serverErr
		}

		go func(conn net.Conn) {
			defer func() { _ = conn.Close() }()

			reader := protocol.NewFrameReader(conn)
			writer := protocol.NewFrameWriter(conn)

			if _, err := reader.ReadFrame(); err != nil {
				return
			}

			response, _ := protocol.EncodeControl("handshake_err", protocol.HandshakeErrMsg{
				Reason: "protocol version mismatch: client=2.0, server=1.0; try upgrading the client and running: gr daemon restart",
			})
			_ = writer.WriteFrame(protocol.ChannelControl, response)

			if connection == 0 {
				return
			}

			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

			frame, err := reader.ReadFrame()
			if err != nil {
				received <- ""
			} else {
				envelope, _ := protocol.DecodeControl(frame.Payload)
				received <- envelope.Type
			}
		}(serverConn)

		return clientConn, nil
	}

	return socketPath, received, dial
}
