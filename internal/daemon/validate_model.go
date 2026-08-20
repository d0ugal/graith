package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/pelletier/go-toml/v2"
)

type codexCatalogModel struct {
	Slug               string `json:"slug"`
	SupportedReasoning []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	ServiceTiers []struct {
		ID string `json:"id"`
	} `json:"service_tiers"`
}
type codexCatalog struct {
	Models []codexCatalogModel `json:"models"`
}

// codexModelCatalog obtains the catalog through the same configured provider
// info runner and cache used by `gr agent info`. This keeps sandboxing, command
// execution, timeouts, and refresh semantics in one place.
func (sm *SessionManager) codexModelCatalog(cfg *config.Config, agentName string, agent config.Agent) (codexCatalog, bool) {
	info, exists, err := agent.Info.Command("model_catalog")
	if err != nil || !exists {
		return codexCatalog{}, false
	}

	defaultTTL := cfg.AgentInfo.CacheTTLDuration()
	globalDisabled := strings.TrimSpace(cfg.AgentInfo.CacheTTL) != "" && defaultTTL == 0

	result, err := sm.agentInfoResult(context.Background(), cfg, agentName, agent, "model_catalog", info, defaultTTL, globalDisabled, protocol.AgentInfoMsg{})
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		return codexCatalog{}, false
	}

	var catalog codexCatalog
	if json.Unmarshal([]byte(result.Stdout), &catalog) != nil || len(catalog.Models) == 0 {
		return codexCatalog{}, false
	}

	return catalog, true
}

func validateCodexOptions(catalog codexCatalog, model string, options config.CodexOptions) error {
	return validateCodexOptionsWithEnv(catalog, model, options, nil)
}

func validateCodexOptionsWithEnv(catalog codexCatalog, model string, options config.CodexOptions, agentEnv map[string]string) error {
	if options.ReasoningEffort == "" && options.ServiceTier == "" && model == "" {
		return nil
	}

	effective := model
	if effective == "" {
		effective = codexConfiguredModel(options.Profile, agentEnv)
	}

	if effective == "" {
		return nil
	}

	var selected *codexCatalogModel

	for i := range catalog.Models {
		if catalog.Models[i].Slug == effective {
			selected = &catalog.Models[i]
			break
		}
	}

	if selected == nil && model != "" {
		return fmt.Errorf("invalid Codex model %q; valid models: %s (catalog refreshes automatically)", model, codexCatalogChoices(catalog))
	}

	if selected == nil {
		return nil
	}

	if options.ReasoningEffort != "" {
		valid := make([]string, 0, len(selected.SupportedReasoning))
		for _, level := range selected.SupportedReasoning {
			valid = append(valid, level.Effort)
		}

		if len(valid) > 0 && !codexContainsString(valid, options.ReasoningEffort) {
			return fmt.Errorf("invalid Codex reasoning effort %q for model %q; valid values: %s", options.ReasoningEffort, effective, strings.Join(valid, ", "))
		}
	}

	if options.ServiceTier != "" {
		valid := make([]string, 0, len(selected.ServiceTiers))
		for _, tier := range selected.ServiceTiers {
			valid = append(valid, tier.ID)
		}

		if len(valid) > 0 && !codexTierAllowed(options.ServiceTier, valid) {
			return fmt.Errorf("invalid Codex service tier %q for model %q; valid values: %s", options.ServiceTier, effective, strings.Join(valid, ", "))
		}
	}

	return nil
}

func codexConfiguredModel(profile string, agentEnv map[string]string) string {
	home := os.Getenv("CODEX_HOME")
	if configuredHome, ok := agentEnv["CODEX_HOME"]; ok && configuredHome != "" {
		home = configuredHome
	}

	if home == "" {
		home, _ = os.UserHomeDir()
	}

	data, err := os.ReadFile(filepath.Join(home, "config.toml")) //nolint:gosec // CODEX_HOME is an explicit user configuration path
	if err != nil {
		return ""
	}

	var raw struct {
		Model    string `toml:"model"`
		Profiles map[string]struct {
			Model string `toml:"model"`
		} `toml:"profiles"`
	}
	if toml.Unmarshal(data, &raw) != nil {
		return ""
	}

	if profile != "" {
		return raw.Profiles[profile].Model
	}

	return raw.Model
}

func codexCatalogChoices(c codexCatalog) string {
	ids := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		ids = append(ids, m.Slug)
	}

	sort.Strings(ids)

	return strings.Join(ids, ", ")
}

func codexContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}

	return false
}

func codexTierAllowed(value string, valid []string) bool {
	if codexContainsString(valid, value) {
		return true
	}

	if value == "fast" {
		return codexContainsString(valid, "priority")
	}

	if value == "priority" {
		return codexContainsString(valid, "fast")
	}

	return false
}

func validateModel(agent config.Agent, model string) error {
	if model == "" || agent.ValidateModel == "" {
		return nil
	}

	parts := strings.Fields(agent.ValidateModel)
	if len(parts) == 0 {
		return nil
	}

	bin, lookErr := exec.LookPath(parts[0])
	if lookErr != nil {
		return fmt.Errorf("validate model: cannot resolve %q: %w", parts[0], lookErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, parts[1:]...)

	var stderr strings.Builder

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		msg := fmt.Sprintf("validate model: %s failed: %v", bin, err)
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg += "\n" + s
		}

		return fmt.Errorf("%s", msg)
	}

	var valid []string

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		before, _, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}

		if id := strings.TrimSpace(before); id != "" {
			valid = append(valid, id)
		}
	}

	if len(valid) == 0 {
		return fmt.Errorf("validate model: %s produced no recognized models", bin)
	}

	for _, v := range valid {
		if v == model {
			return nil
		}
	}

	return fmt.Errorf("invalid model %q; valid models:\n  %s", model, strings.Join(valid, "\n  "))
}
