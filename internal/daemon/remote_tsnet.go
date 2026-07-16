//go:build !no_tsnet

package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/d0ugal/graith/internal/config"
	"tailscale.com/tsnet"
)

// tsnetSupported reports whether this build links the embedded Tailscale node
// (tailscale.com/tsnet). It is true here and false in the no_tsnet build
// (remote_notsnet.go), letting the daemon fail closed on a tsnet-mode config it
// cannot honour. See docs/design/2026-07-16-tsnet-footprint.md.
const tsnetSupported = true

// tsnetListener is the tsnet-mode RemoteListener: an embedded Tailscale node.
type tsnetListener struct {
	srv  *tsnet.Server
	port int
}

func newTSNetListener(cfg config.RemoteConfig, stateDir string) (RemoteListener, error) {
	srv := &tsnet.Server{
		Hostname: cfg.Hostname,
		Dir:      filepath.Join(stateDir, "tsnet"),
	}

	if cfg.AuthKeyFile != "" {
		b, err := os.ReadFile(config.ExpandPath(cfg.AuthKeyFile))
		if err != nil {
			return nil, fmt.Errorf("read tsnet auth key: %w", err)
		}

		srv.AuthKey = strings.TrimSpace(string(b))
	}

	return &tsnetListener{srv: srv, port: cfg.Port}, nil
}

func (l *tsnetListener) Listen() (net.Listener, error) {
	return l.srv.Listen("tcp", fmt.Sprintf(":%d", l.port))
}

func (l *tsnetListener) WhoIs(ctx context.Context, remoteAddr string) (*TailnetIdentity, error) {
	lc, err := l.srv.LocalClient()
	if err != nil {
		return nil, err
	}

	w, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}

	return identityFromWhoIs(w), nil
}

func (l *tsnetListener) Close() error { return l.srv.Close() }
