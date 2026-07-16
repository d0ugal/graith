//go:build no_tsnet

package daemon

import (
	"errors"

	"github.com/d0ugal/graith/internal/config"
)

// tsnetSupported is false in the no_tsnet build: the embedded Tailscale node
// (tailscale.com/tsnet) and its dependency graph (gVisor netstack, WireGuard)
// are compiled out. Interface mode — which binds the host tailscaled's tailnet
// IP via tailscale.com/client/local — is unaffected. See
// docs/design/2026-07-16-tsnet-footprint.md.
const tsnetSupported = false

// errTSNetUnsupported is the fail-closed error returned when a no_tsnet build is
// asked to serve remote mode "tsnet".
var errTSNetUnsupported = errors.New(
	`remote mode "tsnet" is not supported in this build: it was compiled with the no_tsnet build tag, ` +
		`which omits the embedded Tailscale node. Use remote mode "interface" (requires a running tailscaled), ` +
		`or install a build without the no_tsnet tag`)

// newTSNetListener always fails closed in the no_tsnet build. It never links
// tailscale.com/tsnet, so the netstack dependency graph stays out of the binary.
func newTSNetListener(_ config.RemoteConfig, _ string) (RemoteListener, error) {
	return nil, errTSNetUnsupported
}
