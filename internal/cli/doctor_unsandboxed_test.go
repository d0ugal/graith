package cli

import (
	"reflect"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestDisabledGraithSandboxAgents(t *testing.T) {
	cfg := config.Default()
	// The global sandbox is off by default; this case exercises per-agent
	// disables while the global sandbox is enabled, so turn it on explicitly.
	cfg.Sandbox.Enabled = true
	disabled := true

	braw := cfg.Agents["claude"]
	braw.Sandbox.Disabled = &disabled
	cfg.Agents["claude"] = braw

	canny := cfg.Agents["codex"]
	canny.Sandbox.Disabled = &disabled
	cfg.Agents["codex"] = canny

	if got, want := disabledGraithSandboxAgents(cfg), []string{"claude", "codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled agents = %v, want %v", got, want)
	}

	cfg.Sandbox.Enabled = false
	if got := disabledGraithSandboxAgents(cfg); got != nil {
		t.Fatalf("globally disabled result = %v, want nil (covered by global warning)", got)
	}
}
