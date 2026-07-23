package cli

import (
	"context"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
)

type commandDependencies struct {
	cfg         *config.Config
	paths       config.Paths
	out         *output.Writer
	listSession sessionListUseCase
}

// listConn is retained as a test-only-compatible transport shape while
// command execution uses sessionListUseCase through the context bundle.
type listConn interface {
	controlConn
	Close()
}

var listConnectFn = func(cfg *config.Config, paths config.Paths, cfgFile string) (listConn, error) {
	return client.Connect(cfg, paths, cfgFile)
}

type sessionListUseCase interface {
	ListSessions(deleted bool) ([]protocol.SessionInfo, error)
}

type commandDependenciesContextKey struct{}

func withCommandDependencies(ctx context.Context, deps commandDependencies) context.Context {
	return context.WithValue(ctx, commandDependenciesContextKey{}, deps)
}

//nolint:contextcheck // direct-call tests may provide a nil Cobra context.
func commandDeps(ctx context.Context) commandDependencies {
	if ctx == nil {
		ctx = context.Background()
	}

	if deps, ok := ctx.Value(commandDependenciesContextKey{}).(commandDependencies); ok {
		return deps
	}

	return commandDependencies{
		cfg:   cfg,
		paths: paths,
		out:   out,
		listSession: clientSessionListUseCase{connect: func() (listConn, error) {
			return listConnectFn(cfg, paths, cfgFile)
		}},
	}
}

type clientSessionListUseCase struct{ connect func() (listConn, error) }

func (useCase clientSessionListUseCase) ListSessions(deleted bool) ([]protocol.SessionInfo, error) {
	c, err := useCase.connect()
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if err := c.SendControl("list", protocol.ListMsg{Deleted: deleted}); err != nil {
		return nil, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}

	var list protocol.SessionListMsg

	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}

	return list.Sessions, nil
}

func newClientSessionListUseCase(cfg *config.Config, paths config.Paths, cfgFile string) sessionListUseCase {
	return clientSessionListUseCase{connect: func() (listConn, error) {
		return listConnectFn(cfg, paths, cfgFile)
	}}
}
