package dependencyhealth

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConfigValidationRoutingAndDefaults(t *testing.T) {
	c := Config{Services: []Service{{Name: "my-github", Provider: "statuspage", BaseURL: "https://status.example", AgentTypes: []string{"my-codex"}}}}
	if err := c.Validate(time.ParseDuration, map[string]struct{}{"my-codex": {}}); err != nil {
		t.Fatal(err)
	}

	if got := c.PollIntervalDuration(); got != DefaultPollInterval {
		t.Errorf("poll interval = %s", got)
	}

	if got := c.RecoveryPollIntervalDuration(); got != DefaultRecoveryPollInterval {
		t.Errorf("recovery interval = %s", got)
	}
}

func TestConfigDurationAccessorsSupportDaySyntax(t *testing.T) {
	c := Config{PollInterval: "1d2h", RecoveryPollInterval: "12h"}
	if got, want := c.PollIntervalDuration(), 26*time.Hour; got != want {
		t.Fatalf("poll interval = %s, want %s", got, want)
	}
	if got, want := c.RecoveryPollIntervalDuration(), 12*time.Hour; got != want {
		t.Fatalf("recovery interval = %s, want %s", got, want)
	}
}

func TestConfigValidationRejectsUnsafeOrAmbiguousSources(t *testing.T) {
	tests := map[string]Config{
		"http":                 {Services: []Service{{Name: "braw", Provider: "statuspage", BaseURL: "http://status.example", Global: true}}},
		"query":                {Services: []Service{{Name: "braw", Provider: "statuspage", BaseURL: "https://status.example/?x=1", Global: true}}},
		"private ip":           {Services: []Service{{Name: "braw", Provider: "statuspage", BaseURL: "https://127.0.0.1", Global: true}}},
		"private ipv4":         {Services: []Service{{Name: "braw", Provider: "statuspage", BaseURL: "https://192.168.1.1", Global: true}}},
		"private ipv6":         {Services: []Service{{Name: "braw", Provider: "statuspage", BaseURL: "https://[fd00::1]", Global: true}}},
		"ambiguous routing":    {Services: []Service{{Name: "braw", Provider: "statuspage", BaseURL: "https://status.example", Global: true, AgentTypes: []string{"codex"}}}},
		"unknown custom agent": {Services: []Service{{Name: "braw", Provider: "statuspage", BaseURL: "https://status.example", AgentTypes: []string{"codex"}}}},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(time.ParseDuration, map[string]struct{}{"my-codex": {}}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPersistedStateCorruptionIsolated(t *testing.T) {
	var state struct {
		Sessions         map[string]string `json:"sessions"`
		DependencyHealth *PersistedState   `json:"dependency_health"`
	}
	if err := json.Unmarshal([]byte(`{"sessions":{"braw":"running"},"dependency_health":{"schema_version":1,"services":`), &state); err == nil {
		t.Fatal("expected outer JSON error")
	}

	if err := json.Unmarshal([]byte(`{"sessions":{"braw":"running"},"dependency_health":{"schema_version":1,"services":"dreich"}}`), &state); err != nil {
		t.Fatal(err)
	}

	if state.Sessions["braw"] != "running" || state.DependencyHealth == nil || len(state.DependencyHealth.Services) != 0 {
		t.Fatalf("state = %+v", state)
	}
}

func TestPersistedStateRejectsInvalidEnums(t *testing.T) {
	var state PersistedState
	if err := json.Unmarshal([]byte(`{"schema_version":1,"services":{"braw":{"observed_state":"bogus","source_health":"fresh"}}}`), &state); err != nil {
		t.Fatal(err)
	}

	if len(state.Services) != 0 || state.SchemaVersion != 0 {
		t.Fatalf("invalid state was retained: %+v", state)
	}
}
