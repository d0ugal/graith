// Package scenariofile parses graith scenario TOML files. It is shared by the
// CLI (gr scenario start) and the daemon (scenario trigger action) so both build
// a protocol.ScenarioStartMsg from the same code.
package scenariofile

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	toml "github.com/pelletier/go-toml/v2"
)

// File is the on-disk scenario definition.
type File struct {
	Version  int                    `toml:"version"`
	Scenario Meta                   `toml:"scenario"`
	Sessions []Session              `toml:"sessions"`
	Triggers []config.TriggerConfig `toml:"trigger"`
}

// Meta is the [scenario] block.
type Meta struct {
	Name string `toml:"name"`
	Goal string `toml:"goal"`
}

// Session is one [[sessions]] entry.
type Session struct {
	Name       string `toml:"name"`
	Repo       string `toml:"repo"`
	Agent      string `toml:"agent"`
	Model      string `toml:"model"`
	Base       string `toml:"base"`
	Role       string `toml:"role"`
	Task       string `toml:"task"`
	AgentHooks *bool  `toml:"agent_hooks"`
	Shared     bool   `toml:"shared"`
	// Includes attaches extra worktrees to the session, in addition to any
	// inherited from the repo's [[repos]] config. See issue #1046.
	Includes []string `toml:"includes"`
	// Star creates the session starred so it is protected from an accidental
	// manual `gr delete` (shared = true only shields from scenario stop/delete).
	Star bool `toml:"star"`
}

// Parse decodes and validates a scenario TOML document. Unknown fields are
// rejected so typos surface as errors.
func Parse(data []byte) (*File, error) {
	var sf File

	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&sf); err != nil {
		return nil, fmt.Errorf("parse scenario TOML: %w", err)
	}

	if sf.Version != 1 {
		return nil, fmt.Errorf("unsupported scenario version %d (expected 1)", sf.Version)
	}

	if sf.Scenario.Name == "" {
		return nil, fmt.Errorf("scenario.name is required")
	}

	if len(sf.Sessions) == 0 {
		return nil, fmt.Errorf("at least one [[sessions]] entry is required")
	}

	if err := ValidateScenarioTriggers(sf.Triggers, sf.DefinedRoles(), sf.DefinedMembers()); err != nil {
		return nil, err
	}

	return &sf, nil
}

// DefinedRoles returns the set of non-empty roles the scenario's own
// (non-shared) sessions declare. A scenario [[trigger]] watch may only select by
// one of these — a shared session keeps its original scenario identity, so a
// watch trigger could never bind to it, and allowing its role would validate a
// trigger that can never fire.
func (sf *File) DefinedRoles() map[string]bool {
	roles := make(map[string]bool, len(sf.Sessions))
	for _, s := range sf.Sessions {
		if s.Role != "" && !s.Shared {
			roles[s.Role] = true
		}
	}

	return roles
}

// DefinedMembers returns the set of session names in the scenario (including
// shared members). A scenario trigger's literal inbox delivery target must be
// one of these (or "orchestrator", or a template) — it may not name a session
// outside the scenario.
func (sf *File) DefinedMembers() map[string]bool {
	members := make(map[string]bool, len(sf.Sessions))
	for _, s := range sf.Sessions {
		if s.Name != "" {
			members[s.Name] = true
		}
	}

	return members
}

// ValidateScenarioTriggers validates the scenario-embedded [[trigger]] blocks.
// Each trigger must pass the shared structural validation (config.ValidateTriggerStructure)
// and the scenario-specific restrictions: it may only select sessions by a
// `role` the scenario defines (never a `repo`), it may not set an external
// execution repo, and its delivery inbox may only name a scenario member (or
// "orchestrator"/a template) — never a session outside the scenario. See issue
// #1027. Returns the first error found.
func ValidateScenarioTriggers(triggers []config.TriggerConfig, roles, members map[string]bool) error {
	seen := make(map[string]bool, len(triggers))

	for i := range triggers {
		t := &triggers[i]

		where := fmt.Sprintf("scenario trigger[%d]", i)
		if t.Name != "" {
			where = fmt.Sprintf("scenario trigger %q", t.Name)
		}

		if t.Name == "" {
			return fmt.Errorf("%s: name is required", where)
		}

		if seen[t.Name] {
			return fmt.Errorf("%s: duplicate trigger name", where)
		}

		seen[t.Name] = true

		if errs := config.ValidateTriggerStructure(where, t); len(errs) > 0 {
			return fmt.Errorf("%w", errs[0])
		}

		if err := validateScenarioTriggerRestrictions(where, t, roles, members); err != nil {
			return err
		}
	}

	return nil
}

// validateScenarioTriggerRestrictions enforces the scenario-scope limits on a
// structurally-valid trigger: role-only watch selection against defined roles,
// no external repo, and no delivery to a session outside the scenario.
func validateScenarioTriggerRestrictions(where string, t *config.TriggerConfig, roles, members map[string]bool) error {
	// A scenario must not spawn another scenario (that would name a scenario
	// outside its own membership).
	if t.Action.Type == config.ActionScenario {
		return fmt.Errorf("%s: scenario triggers cannot start scenarios", where)
	}

	// A schedule command needs an execution root repo, which would point outside
	// the scenario. Scenario command triggers must derive their root from a bound
	// worktree, i.e. use a [watch] source.
	if t.Action.Type == config.ActionCommand && t.IsSchedule() {
		return fmt.Errorf("%s: scenario command triggers require a [watch] source (a schedule command names a repo outside the scenario)", where)
	}

	// No action may name an external execution repo. Watch actions derive their
	// root from the bound member worktree; a session action's repo would spawn
	// work in a repo outside the scenario.
	if t.Action.Repo != "" {
		return fmt.Errorf("%s: scenario triggers must not set action.repo (they operate within the scenario's own sessions)", where)
	}

	if t.IsWatch() {
		if t.Watch.Repo != "" {
			return fmt.Errorf("%s: scenario triggers must select sessions by role, not repo", where)
		}

		if t.Watch.Role == "" {
			return fmt.Errorf("%s: scenario watch trigger requires a role", where)
		}

		if !roles[t.Watch.Role] {
			return fmt.Errorf("%s: role %q is not defined by any scenario session", where, t.Watch.Role)
		}
	}

	return validateScenarioDeliverInbox(where, t.Action.Deliver.Inbox, members)
}

// validateScenarioDeliverInbox rejects a literal inbox target that is not a
// scenario member. "orchestrator" (the scenario's owner) and templates like
// "{session_name}" (resolved to a bound member at fire time) are allowed; an
// empty inbox (topic/store-only delivery) is fine.
func validateScenarioDeliverInbox(where, inbox string, members map[string]bool) error {
	if inbox == "" || inbox == "orchestrator" || strings.Contains(inbox, "{") {
		return nil
	}

	if !members[inbox] {
		return fmt.Errorf("%s: action.deliver.inbox %q is not a session in this scenario", where, inbox)
	}

	return nil
}

// SessionInputs maps a parsed File's sessions to protocol.ScenarioSessionInput.
// agent_hooks defaults to true when unset.
func SessionInputs(sf *File) ([]protocol.ScenarioSessionInput, error) {
	inputs := make([]protocol.ScenarioSessionInput, 0, len(sf.Sessions))
	for _, s := range sf.Sessions {
		if s.Name == "" {
			return nil, fmt.Errorf("every [[sessions]] entry needs a name")
		}

		if s.Repo == "" && !s.Shared {
			return nil, fmt.Errorf("session %q: repo is required (unless shared)", s.Name)
		}

		inputs = append(inputs, protocol.ScenarioSessionInput{
			Name:       s.Name,
			Repo:       s.Repo,
			Agent:      s.Agent,
			Model:      s.Model,
			Base:       s.Base,
			Role:       s.Role,
			Task:       s.Task,
			AgentHooks: s.AgentHooks == nil || *s.AgentHooks,
			Shared:     s.Shared,
			Includes:   s.Includes,
			Star:       s.Star,
		})
	}

	return inputs, nil
}
