//go:build !no_tsnet

package daemon

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// TestTSNetSupportedInDefaultBuild pins that the default build links the
// embedded Tailscale node. Its counterpart in remote_notsnet_test.go asserts
// the opposite for the no_tsnet build.
func TestTSNetSupportedInDefaultBuild(t *testing.T) {
	if !tsnetSupported {
		t.Fatal("default build must report tsnetSupported == true")
	}
}

// TestNewRemoteListenerTSNetMode confirms tsnet mode resolves to a listener in
// the default build (construction only — no tailnet is dialled here).
func TestNewRemoteListenerTSNetMode(t *testing.T) {
	rl, err := newRemoteListener(t.Context(), config.RemoteConfig{Mode: "tsnet", Hostname: "braw", Port: 4823}, t.TempDir())
	if err != nil {
		t.Fatalf("tsnet mode must build a listener in the default build: %v", err)
	}

	if rl == nil {
		t.Fatal("expected a non-nil listener")
	}
}
