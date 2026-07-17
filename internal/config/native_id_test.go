package config

import (
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// TestNativeIDEmbeddedDefaults asserts the embedded defaults keep the pre-#1236
// native-id strategies: claude forces (with the claude locator so resume can
// confirm the conversation), codex scrapes via the codex locator.
func TestNativeIDEmbeddedDefaults(t *testing.T) {
	def := Default()

	claude := def.Agents["claude"]
	if !claude.ForcesNativeID() || claude.NativeIDLocator() != NativeIDLocatorClaude {
		t.Errorf("claude native_id = %+v; want force + locator %q", claude.NativeID, NativeIDLocatorClaude)
	}

	codex := def.Agents["codex"]
	if codex.ForcesNativeID() || !codex.ScrapesNativeID() || codex.NativeIDLocator() != NativeIDLocatorCodex {
		t.Errorf("codex native_id = %+v; want scrape via locator %q", codex.NativeID, NativeIDLocatorCodex)
	}
}

// TestNativeIDValidation covers the coherence rules: known locator, force needs
// id-passing args + a locator, and a non-forced claude locator (no scraper) is
// rejected.
func TestNativeIDValidation(t *testing.T) {
	cases := []struct {
		name    string
		agent   Agent
		wantErr string
	}{
		{
			"force without id-passing args",
			Agent{Command: "x", Args: []string{"start"}, NativeID: &AgentNativeIDConfig{Force: true, Locator: NativeIDLocatorClaude}},
			"do not pass {agent_session_id}",
		},
		{
			"force without locator",
			Agent{Command: "x", Args: []string{"--session-id", "{agent_session_id}"}, NativeID: &AgentNativeIDConfig{Force: true}},
			"no locator is configured",
		},
		{
			"force with non-empty resume_args missing id",
			Agent{
				Command:    "x",
				Args:       []string{"--session-id", "{agent_session_id}"},
				ResumeArgs: []string{"--resume-last"},
				NativeID:   &AgentNativeIDConfig{Force: true, Locator: NativeIDLocatorClaude},
			},
			"resume_args does not pass {agent_session_id}",
		},
		{
			"force with non-empty fork_args missing id",
			Agent{
				Command:  "x",
				Args:     []string{"--session-id", "{agent_session_id}"},
				ForkArgs: []string{"--fork"},
				NativeID: &AgentNativeIDConfig{Force: true, Locator: NativeIDLocatorClaude},
			},
			"fork_args does not pass {agent_session_id}",
		},
		{
			"unknown locator",
			Agent{Command: "x", NativeID: &AgentNativeIDConfig{Locator: "gemini"}},
			"native_id.locator",
		},
		{
			"non-forced claude locator has no scraper",
			Agent{Command: "x", NativeID: &AgentNativeIDConfig{Locator: NativeIDLocatorClaude}},
			"no self-mint scraper",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Default()
			cfg.Agents["thrawn"] = c.agent

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, c.wantErr)
			}
		})
	}

	// Valid: force + id-passing args + locator; and scrape with codex locator.
	ok := Default()
	ok.Agents["myclaude"] = Agent{Command: "x", Args: []string{"--session-id", "{agent_session_id}"}, NativeID: &AgentNativeIDConfig{Force: true, Locator: NativeIDLocatorClaude}}
	ok.Agents["mycodex"] = Agent{Command: "x", NativeID: &AgentNativeIDConfig{Locator: NativeIDLocatorCodex}}

	if err := ok.Validate(); err != nil {
		t.Fatalf("valid custom native_id agents rejected: %v", err)
	}
}

// TestNativeIDDisabledRoundTrips is the config round-trip blocker regression: a
// built-in whose inherited strategy is disabled with explicit force=false /
// locator="" must survive gr config show's marshal → reload over Default()
// without silently re-enabling. Force/Locator therefore carry no omitempty
// (issue #1236).
func TestNativeIDDisabledRoundTrips(t *testing.T) {
	cfg := Default()
	claude := cfg.Agents["claude"]
	claude.NativeID = &AgentNativeIDConfig{Force: false, Locator: ""}
	// Drop the id-passing args too so the disabled agent is coherent.
	cfg.Agents["claude"] = claude

	out, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The marshaled table must carry BOTH explicit zero values, not an empty table
	// (either omitted would re-inherit the built-in on reload).
	if !strings.Contains(string(out), "force = false") {
		t.Errorf("marshaled config does not carry explicit force = false:\n%s", grepAgentTable(string(out)))
	}

	if !strings.Contains(string(out), `locator = ""`) && !strings.Contains(string(out), "locator = ''") {
		t.Errorf("marshaled config does not carry explicit locator = \"\":\n%s", grepAgentTable(string(out)))
	}

	// Reload the marshaled effective config over Default(): the strategy must stay
	// disabled, not re-inherit force=true.
	reloaded := Default()
	defaultAgents := reloaded.Agents

	if err := toml.Unmarshal(out, reloaded); err != nil {
		t.Fatalf("reload: %v", err)
	}

	reloaded.Agents = mergeAgents(defaultAgents, reloaded.Agents)

	if reloaded.Agents["claude"].ForcesNativeID() {
		t.Error("disabled claude force re-enabled after marshal → reload round-trip")
	}

	if got := reloaded.Agents["claude"].NativeIDLocator(); got != "" {
		t.Errorf("disabled claude locator = %q after round-trip; want \"\" (not re-inherited)", got)
	}
}

func grepAgentTable(s string) string {
	var b strings.Builder

	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "native_id") || strings.Contains(line, "force") || strings.Contains(line, "locator") {
			b.WriteString(line + "\n")
		}
	}

	return b.String()
}
