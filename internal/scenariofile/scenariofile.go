// Package scenariofile parses graith scenario TOML files. It is shared by the
// CLI (gr scenario start) and the daemon (scenario trigger action) so both build
// a protocol.ScenarioStartMsg from the same code.
package scenariofile

import (
	"bytes"
	"fmt"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/d0ugal/graith/internal/protocol"
)

// File is the on-disk scenario definition.
type File struct {
	Version  int       `toml:"version"`
	Scenario Meta      `toml:"scenario"`
	Sessions []Session `toml:"sessions"`
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
	return &sf, nil
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
		})
	}
	return inputs, nil
}
