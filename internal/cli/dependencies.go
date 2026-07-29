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
	search      conversationSearchUseCase
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

		if deps.search == nil {
			deps.search = newClientConversationSearchUseCase(cfg, paths, cfgFile)
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
		agent:  newClientAgentUseCase(cfg, paths, cfgFile),
		search: newClientConversationSearchUseCase(cfg, paths, cfgFile),
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
	return controlRequest[protocol.AgentCatalogResponseMsg](
		useCase.connect,
		"agent_catalog",
		protocol.AgentCatalogMsg{},
		"agent_catalog_response",
	)
}

func (useCase clientAgentUseCase) AgentInfo(req protocol.AgentInfoMsg) (protocol.AgentInfoResponseMsg, error) {
	return controlRequest[protocol.AgentInfoResponseMsg](
		useCase.connect,
		"agent_info",
		req,
		"agent_info_response",
	)
}

func controlRequest[T any](connect func() (listConn, error), msgType string, payload any, responseType string) (T, error) {
	var out T

	c, err := connect()
	if err != nil {
		return out, err
	}
	defer c.Close()

	if err := c.SendControl(msgType, payload); err != nil {
		return out, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return out, err
	}

	if resp.Type == "error" {
		return out, fmt.Errorf("%s", errorMessage(resp))
	}

	if resp.Type != responseType {
		return out, fmt.Errorf("unexpected response %q", resp.Type)
	}

	if err := protocol.DecodePayload(resp, &out); err != nil {
		return out, err
	}

	return out, nil
}

func newClientAgentUseCase(cfg *config.Config, paths config.Paths, cfgFile string) agentUseCase {
	return clientAgentUseCase{connect: func() (listConn, error) {
		return listConnectFn(cfg, paths, cfgFile)
	}}
}

type conversationSearchUseCase interface {
	SearchConversations(req protocol.SearchMsg) (protocol.SearchResponseMsg, error)
}

type clientConversationSearchUseCase struct{ connect func() (listConn, error) }

func (useCase clientConversationSearchUseCase) SearchConversations(req protocol.SearchMsg) (protocol.SearchResponseMsg, error) {
	return controlRequest[protocol.SearchResponseMsg](
		useCase.connect,
		"search",
		req,
		"search_response",
	)
}

func newClientConversationSearchUseCase(cfg *config.Config, paths config.Paths, cfgFile string) conversationSearchUseCase {
	return clientConversationSearchUseCase{connect: func() (listConn, error) {
		return listConnectFn(cfg, paths, cfgFile)
	}}
}
