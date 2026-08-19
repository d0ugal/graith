package config

import "testing"

func TestDependencyHealthTOMLConfigAndCustomAgentRouting(t *testing.T) {
	cfg, err := LoadBytes("/tmp/bothy/config.toml", []byte(`
[dependency_health]
enabled = true
poll_interval = "5m"
recovery_poll_interval = "30s"
timeout = "5s"
[[dependency_health.service]]
name = "my-github"
provider = "statuspage"
base_url = "https://www.githubstatus.com"
global = true
[[dependency_health.service]]
name = "my-model"
provider = "statuspage"
base_url = "https://status.example"
agent_types = ["my-codex"]
[agents.my-codex]
command = "codex"
`))
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.DependencyHealth.Enabled || len(cfg.DependencyHealth.Services) != 2 {
		t.Fatalf("dependency health config = %+v", cfg.DependencyHealth)
	}
}

func TestDependencyHealthRejectsUnknownAgentRouting(t *testing.T) {
	_, err := LoadBytes("/tmp/bothy/config.toml", []byte(`
[[dependency_health.service]]
name = "braw"
provider = "statuspage"
base_url = "https://status.example"
agent_types = ["not-configured"]
`))
	if err == nil {
		t.Fatal("expected unknown agent routing to fail")
	}
}
