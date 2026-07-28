package cli

import (
	"context"
	"fmt"

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
	agent       agentUseCase
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
		if deps.out == nil {
			deps.out = out
		}

		if deps.listSession == nil {
			deps.listSession = newClientSessionListUseCase(cfg, paths, cfgFile)
		}

		if deps.agent == nil {
			deps.agent = newClientAgentUseCase(cfg, paths, cfgFile)
		}

		return deps
	}

	return commandDependencies{
		cfg:   cfg,
		paths: paths,
		out:   out,
		listSession: clientSessionListUseCase{connect: func() (listConn, error) {
			return listConnectFn(cfg, paths, cfgFile)
		}},
		agent: newClientAgentUseCase(cfg, paths, cfgFile),
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

type agentUseCase interface {
	AgentCatalog() (protocol.AgentCatalogResponseMsg, error)
	AgentInfo(req protocol.AgentInfoMsg) (protocol.AgentInfoResponseMsg, error)
}

type clientAgentUseCase struct{ connect func() (listConn, error) }

func (useCase clientAgentUseCase) AgentCatalog() (protocol.AgentCatalogResponseMsg, error) {
	c, err := useCase.connect()
	if err != nil {
		return protocol.AgentCatalogResponseMsg{}, err
	}
	defer c.Close()

	if err := c.SendControl("agent_catalog", protocol.AgentCatalogMsg{}); err != nil {
		return protocol.AgentCatalogResponseMsg{}, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return protocol.AgentCatalogResponseMsg{}, err
	}

	if resp.Type == "error" {
		return protocol.AgentCatalogResponseMsg{}, fmt.Errorf("%s", errorMessage(resp))
	}

	if resp.Type != "agent_catalog_response" {
		return protocol.AgentCatalogResponseMsg{}, fmt.Errorf("unexpected response %q", resp.Type)
	}

	var catalog protocol.AgentCatalogResponseMsg
	if err := protocol.DecodePayload(resp, &catalog); err != nil {
		return protocol.AgentCatalogResponseMsg{}, err
	}

	return catalog, nil
}

func (useCase clientAgentUseCase) AgentInfo(req protocol.AgentInfoMsg) (protocol.AgentInfoResponseMsg, error) {
	c, err := useCase.connect()
	if err != nil {
		return protocol.AgentInfoResponseMsg{}, err
	}
	defer c.Close()

	if err := c.SendControl("agent_info", req); err != nil {
		return protocol.AgentInfoResponseMsg{}, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return protocol.AgentInfoResponseMsg{}, err
	}

	if resp.Type == "error" {
		return protocol.AgentInfoResponseMsg{}, fmt.Errorf("%s", errorMessage(resp))
	}

	if resp.Type != "agent_info_response" {
		return protocol.AgentInfoResponseMsg{}, fmt.Errorf("unexpected response %q", resp.Type)
	}

	var info protocol.AgentInfoResponseMsg
	if err := protocol.DecodePayload(resp, &info); err != nil {
		return protocol.AgentInfoResponseMsg{}, err
	}

	return info, nil
}

func newClientAgentUseCase(cfg *config.Config, paths config.Paths, cfgFile string) agentUseCase {
	return clientAgentUseCase{connect: func() (listConn, error) {
		return listConnectFn(cfg, paths, cfgFile)
	}}
}
