package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/d0ugal/graith/internal/commandpolicy"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// CheckCommandPolicy evaluates the optional restriction layer synchronously.
// Every failure is an immediate deny; there is no pending state or human path.
func (sm *SessionManager) CheckCommandPolicy(ctx context.Context, req protocol.CommandPolicyCheckMsg) protocol.CommandPolicyDecisionMsg {
	sm.mu.RLock()
	policyCfg := sm.cfg.CommandPolicy
	var sessionName, agent string
	if sess, ok := sm.state.Sessions[req.SessionID]; ok {
		sessionName, agent = sess.Name, sess.Agent
	}
	sm.mu.RUnlock()

	if !policyCfg.Enabled() {
		return protocol.CommandPolicyDecisionMsg{Decision: commandpolicy.DecisionAllow}
	}
	backend, err := commandpolicy.BackendByName(strings.TrimSpace(policyCfg.Backend))
	if err != nil {
		return sm.commandPolicyError(req, err)
	}
	cfg, err := commandPolicyBackendConfig(policyCfg, sm.commandPolicyConfigDir())
	if err != nil {
		return sm.commandPolicyError(req, err)
	}
	decision, err := backend.Evaluate(ctx, commandpolicy.Request{
		SessionID: req.SessionID, SessionName: sessionName, Agent: agent,
		ToolName: req.ToolName, ToolInput: req.ToolInput, HookPayload: req.HookPayload,
	}, cfg)
	if err != nil {
		return sm.commandPolicyError(req, err)
	}
	if decision.Decision != commandpolicy.DecisionAllow && decision.Decision != commandpolicy.DecisionDeny {
		return sm.commandPolicyError(req, fmt.Errorf("backend returned invalid decision %q", decision.Decision))
	}
	return protocol.CommandPolicyDecisionMsg{Decision: decision.Decision, Reason: decision.Reason}
}

func (sm *SessionManager) commandPolicyError(req protocol.CommandPolicyCheckMsg, err error) protocol.CommandPolicyDecisionMsg {
	sm.log.Warn("command policy denied after evaluation failure", "session", req.SessionID, "tool", req.ToolName, "err", err)
	return protocol.CommandPolicyDecisionMsg{
		Decision: commandpolicy.DecisionDeny,
		Reason:   fmt.Sprintf("command policy could not be enforced: %v", err),
	}
}

func (sm *SessionManager) commandPolicyConfigDir() string {
	if file := strings.TrimSpace(sm.configFile); file != "" {
		return filepath.Dir(file)
	}
	if sm.paths.ConfigFile == "" {
		return ""
	}
	return filepath.Dir(sm.paths.ConfigFile)
}

func commandPolicyBackendConfig(cfg config.CommandPolicy, configDir string) (commandpolicy.Config, error) {
	out := commandpolicy.Config{
		Command:       cfg.Command,
		BuiltinConfig: config.ExpandPathRelative(cfg.Builtin.Config, configDir),
		ExecTimeout:   cfg.TimeoutDuration(),
	}
	if cfg.Builtin.HasInline() {
		inline, err := cfg.Builtin.InlineJSON()
		if err != nil {
			return commandpolicy.Config{}, fmt.Errorf("encode inline command policy rules: %w", err)
		}
		out.BuiltinInline = inline
	}
	return out, nil
}
