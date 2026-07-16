package daemon

import (
	"sort"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// TestAgentCatalogHandlerReturnsConfiguredAgents verifies the "agent_catalog"
// control message returns the daemon's configured agents (sorted by name) plus
// the configured default agent, so GUI pickers don't hardcode the list (#1234).
func TestAgentCatalogHandlerReturnsConfiguredAgents(t *testing.T) {
	h := newTestHarness(t)

	h.sendControl(t, "agent_catalog", struct{}{})

	env := h.expectType(t, "agent_catalog_response")

	var resp protocol.AgentCatalogResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", resp.DefaultAgent)
	}

	if len(resp.Agents) == 0 {
		t.Fatal("expected a non-empty agent catalog")
	}

	// The built-in catalog must be reported in full, including cursor, which the
	// old hardcoded GUI list omitted.
	names := make([]string, len(resp.Agents))
	for i, a := range resp.Agents {
		names[i] = a.Name
	}

	for _, want := range []string{"claude", "codex", "opencode", "cursor", "agy"} {
		if !containsStr(names, want) {
			t.Errorf("catalog missing built-in agent %q; got %v", want, names)
		}
	}

	// Entries must be sorted by name for a stable display order.
	if !sort.StringsAreSorted(names) {
		t.Errorf("agent catalog not sorted by name: %v", names)
	}

	// The default agent must resolve to one of the reported entries.
	if !containsStr(names, resp.DefaultAgent) {
		t.Errorf("default agent %q not present in catalog %v", resp.DefaultAgent, names)
	}
}

// TestAgentCatalogHandlerIncludesCustomAgentAndDefault verifies a custom
// [agents.<name>] entry appears in the catalog and that a non-default
// default_agent is reported verbatim, with the launch command carried through.
func TestAgentCatalogHandlerIncludesCustomAgentAndDefault(t *testing.T) {
	h := newTestHarness(t)

	h.sm.mu.Lock()
	if h.sm.cfg.Agents == nil {
		h.sm.cfg.Agents = map[string]config.Agent{}
	}

	h.sm.cfg.Agents["thrawn"] = config.Agent{Command: "thrawn-cli"}
	h.sm.cfg.DefaultAgent = "thrawn"
	h.sm.mu.Unlock()

	h.sendControl(t, "agent_catalog", struct{}{})

	env := h.expectType(t, "agent_catalog_response")

	var resp protocol.AgentCatalogResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.DefaultAgent != "thrawn" {
		t.Errorf("DefaultAgent = %q, want thrawn", resp.DefaultAgent)
	}

	var found *protocol.AgentCatalogEntry

	for i := range resp.Agents {
		if resp.Agents[i].Name == "thrawn" {
			found = &resp.Agents[i]

			break
		}
	}

	if found == nil {
		t.Fatalf("custom agent %q missing from catalog", "thrawn")
	}

	if found.Command != "thrawn-cli" {
		t.Errorf("custom agent command = %q, want thrawn-cli", found.Command)
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}

	return false
}
