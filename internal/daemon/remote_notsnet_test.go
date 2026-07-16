//go:build no_tsnet

package daemon

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestTSNetUnsupportedInNoTSNetBuild pins that the no_tsnet build compiles the
// embedded node out. Its counterpart in remote_tsnet_test.go asserts the
// opposite for the default build.
func TestTSNetUnsupportedInNoTSNetBuild(t *testing.T) {
	if tsnetSupported {
		t.Fatal("no_tsnet build must report tsnetSupported == false")
	}
}

// TestNewRemoteListenerTSNetFailsClosed confirms a no_tsnet build refuses a
// tsnet-mode config with a clear error rather than silently degrading — the
// fail-closed guarantee for a build that omits tsnet.
func TestNewRemoteListenerTSNetFailsClosed(t *testing.T) {
	rl, err := newRemoteListener(t.Context(), config.RemoteConfig{Mode: "tsnet", Hostname: "dreich", Port: 4823}, t.TempDir())
	if err == nil {
		t.Fatal("tsnet mode must fail closed in a no_tsnet build")
	}

	if rl != nil {
		t.Fatal("expected a nil listener on failure")
	}

	if !strings.Contains(err.Error(), "not supported in this build") {
		t.Errorf("error should explain the build omits tsnet, got: %v", err)
	}
}

// TestNewRemoteListenerInterfaceStillDispatches confirms interface mode is still
// reachable in a no_tsnet build (it errors only because no tailscaled is
// running here — not because the mode was compiled out).
func TestNewRemoteListenerInterfaceStillDispatches(t *testing.T) {
	_, err := newRemoteListener(t.Context(), config.RemoteConfig{Mode: "interface", Port: 4823}, t.TempDir())
	if err != nil && strings.Contains(err.Error(), "not supported in this build") {
		t.Errorf("interface mode must not be compiled out by no_tsnet, got: %v", err)
	}
}
