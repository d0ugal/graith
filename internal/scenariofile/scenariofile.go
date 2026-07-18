// Package scenariofile parses graith scenario TOML files. It is shared by the
// CLI (gr scenario start) and the daemon (scenario trigger action) so both build
// a protocol.ScenarioStartMsg from the same code.
package scenariofile

import (
	"bytes"
	"errors"
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
	Name      string                         `toml:"name"`
	Goal      string                         `toml:"goal"`
	Lifecycle config.ScenarioLifecycleConfig `toml:"lifecycle"`
	Policy    *PolicyConfig                  `toml:"policy"`
}

// Session is one [[sessions]] entry.
type Session struct {
	Name       string   `toml:"name"`
	Repo       string   `toml:"repo"`
	Mirror     string   `toml:"mirror"`
	Agent      string   `toml:"agent"`
	Model      string   `toml:"model"`
	Base       string   `toml:"base"`
	Role       string   `toml:"role"`
	Prompt     string   `toml:"prompt"`
	Task       string   `toml:"task"`
	DependsOn  []string `toml:"depends_on"`
	AgentHooks *bool    `toml:"agent_hooks"`
	Shared     bool     `toml:"shared"`
	// Includes attaches extra worktrees to the session, in addition to any
	// inherited from the repo's [[repos]] config. See issue #1046.
	Includes []string `toml:"includes"`
	// Star creates the session starred so it is protected from an accidental
	// manual `gr delete` (shared = true only shields from scenario stop/delete).
	Star    bool                `toml:"star"`
	Results []Result            `toml:"results"`
	Policy  *MemberPolicyConfig `toml:"policy"`
}

// Result is one [[sessions.results]] declaration. Store is relative to the
// scenario's shared-store result directory and may contain supported template
// placeholders; the daemon validates and resolves it authoritatively.
type Result struct {
	Name     string `toml:"name"`
	Format   string `toml:"format"`
	Store    string `toml:"store"`
	Required bool   `toml:"required"`
}

// MirrorMember is the structural subset of a scenario member needed to
// validate scenario-local mirror references. Keeping this independent of the
// CLI and protocol representations lets every scenario entry point enforce the
// same rules before it performs filesystem or daemon mutations.
type MirrorMember struct {
	Name     string
	Mirror   string
	Repo     string
	Base     string
	Shared   bool
	Includes int
}

// ValidateMirrorMembers checks mirror field compatibility and returns each
// member's dependency depth (roots are zero, a mirror of a root is one, and so
// on). The daemon uses the depths as creation waves so every source exists
// before its readers start. References are names within this slice only.
func ValidateMirrorMembers(members []MirrorMember) ([]int, error) {
	indexes := make(map[string]int, len(members))

	for i, member := range members {
		if previous, ok := indexes[member.Name]; ok {
			return nil, fmt.Errorf("duplicate session name %q at indexes %d and %d makes mirror references ambiguous", member.Name, previous, i)
		}

		indexes[member.Name] = i
	}

	targets := make([]int, len(members))
	for i := range targets {
		targets[i] = -1
	}

	for i, member := range members {
		if member.Mirror == "" {
			continue
		}

		switch {
		case member.Shared:
			return nil, fmt.Errorf("session %q: mirror and shared are mutually exclusive", member.Name)
		case member.Repo != "":
			return nil, fmt.Errorf("session %q: mirror and repo are mutually exclusive (repo is derived from the mirror target)", member.Name)
		case member.Base != "":
			return nil, fmt.Errorf("session %q: mirror and base are mutually exclusive (base is derived from the mirror target)", member.Name)
		case member.Includes > 0:
			return nil, fmt.Errorf("session %q: mirror and includes are mutually exclusive (includes are inherited from the mirror target)", member.Name)
		}

		target, ok := indexes[member.Mirror]
		if !ok {
			return nil, fmt.Errorf("session %q: mirror target %q is not a member of this scenario", member.Name, member.Mirror)
		}

		if target == i {
			return nil, fmt.Errorf("session %q: mirror reference is cyclic (a member cannot mirror itself)", member.Name)
		}

		targets[i] = target
	}

	var (
		depths   = make([]int, len(members))
		visiting = make([]bool, len(members))
		visited  = make([]bool, len(members))
		visit    func(int) (int, error)
	)

	visit = func(index int) (int, error) {
		if visited[index] {
			return depths[index], nil
		}

		if visiting[index] {
			return 0, fmt.Errorf("session %q: cyclic mirror references are not allowed", members[index].Name)
		}

		visiting[index] = true
		if target := targets[index]; target >= 0 {
			depth, err := visit(target)
			if err != nil {
				return 0, err
			}

			depths[index] = depth + 1
		}

		visiting[index] = false
		visited[index] = true

		return depths[index], nil
	}

	for i := range members {
		if _, err := visit(i); err != nil {
			return nil, err
		}
	}

	return depths, nil
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
		return nil, errors.New("scenario.name is required")
	}

	if len(sf.Sessions) == 0 {
		return nil, errors.New("at least one [[sessions]] entry is required")
	}

	if err := config.ValidateScenarioLifecycle(sf.Scenario.Lifecycle); err != nil {
		return nil, err
	}

	depInputs := make([]protocol.ScenarioSessionInput, len(sf.Sessions))
	for i, s := range sf.Sessions {
		depInputs[i] = protocol.ScenarioSessionInput{
			Name: s.Name, Prompt: s.Prompt, Task: s.Task, DependsOn: s.DependsOn, Shared: s.Shared,
		}
	}

	if err := ValidateSessionContracts(depInputs, config.TodoMaxTitleCeiling); err != nil {
		return nil, err
	}

	if err := ValidateSessionDependencies(depInputs); err != nil {
		return nil, err
	}

	members := make([]MirrorMember, len(sf.Sessions))
	for i, s := range sf.Sessions {
		members[i] = MirrorMember{
			Name: s.Name, Mirror: s.Mirror, Repo: s.Repo, Base: s.Base,
			Shared: s.Shared, Includes: len(s.Includes),
		}
	}

	if _, err := ValidateMirrorMembers(members); err != nil {
		return nil, err
	}

	if err := ValidateScenarioTriggers(sf.Triggers, sf.DefinedRoles(), sf.DefinedMembers(), sf.DefinedOwnedMembers()); err != nil {
		return nil, err
	}

	policyMembers := make([]PolicyMember, len(sf.Sessions))
	for i, session := range sf.Sessions {
		policyMembers[i] = PolicyMember{
			Name: session.Name, Task: session.Task, Shared: session.Shared,
			HasRequiredResult: sessionHasRequiredResult(session), Policy: session.Policy,
		}
	}

	policy, err := NormalizePolicy(sf.Scenario.Policy, policyMembers)
	if err != nil {
		return nil, err
	}

	if err := ValidatePolicyContracts(policy, policyMembers, config.TodoMaxTitleCeiling); err != nil {
		return nil, err
	}

	return &sf, nil
}

func sessionHasRequiredResult(session Session) bool {
	for _, result := range session.Results {
		if result.Required {
			return true
		}
	}

	return false
}

// DefinedOwnedMembers returns the non-shared members that can safely supply a
// completion trigger's execution/mirror context.
func (sf *File) DefinedOwnedMembers() map[string]bool {
	members := make(map[string]bool, len(sf.Sessions))
	for _, s := range sf.Sessions {
		if s.Name != "" && !s.Shared {
			members[s.Name] = true
		}
	}

	return members
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
func ValidateScenarioTriggers(triggers []config.TriggerConfig, roles, members map[string]bool, ownedMembersOpt ...map[string]bool) error {
	ownedMembers := members
	if len(ownedMembersOpt) > 0 {
		ownedMembers = ownedMembersOpt[0]
	}

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

		if err := validateScenarioTriggerRestrictions(where, t, roles, members, ownedMembers); err != nil {
			return err
		}
	}

	return nil
}

// validateScenarioTriggerRestrictions enforces the scenario-scope limits on a
// structurally-valid trigger: role-only watch selection against defined roles,
// no external repo, and no delivery to a session outside the scenario.
func validateScenarioTriggerRestrictions(where string, t *config.TriggerConfig, roles, members, ownedMembers map[string]bool) error {
	// gcx is a daemon-global external integration whose credentials, cursor, and
	// on-call gate outlive any one scenario. Scenario triggers are scoped to their
	// member sessions, so they cannot own this source.
	if t.IsGCX() {
		return fmt.Errorf("%s: scenario triggers cannot use a [gcx] source (it is a daemon-global external integration)", where)
	}

	// A scenario must not spawn another scenario (that would name a scenario
	// outside its own membership).
	if t.Action.Type == config.ActionScenario {
		return fmt.Errorf("%s: scenario triggers cannot start scenarios", where)
	}

	// A tracker action polls an external tracker and spawns work in a repo outside
	// the scenario's own sessions, so it has no place inside a scenario.
	if t.Action.Type == config.ActionTracker {
		return fmt.Errorf("%s: scenario triggers cannot use the tracker action (it operates on an external repo/tracker)", where)
	}

	// A schedule command needs an execution root repo, which would point outside
	// the scenario. Scenario command triggers must derive their root from a bound
	// worktree, i.e. use a [watch] or [completion] source.
	if t.Action.Type == config.ActionCommand && t.IsSchedule() {
		return fmt.Errorf("%s: scenario command triggers require a [watch] or [completion] source (a schedule command names a repo outside the scenario)", where)
	}

	if t.IsCompletion() {
		member := t.Completion.Session
		if (t.Action.Type == config.ActionCommand || t.Action.Type == config.ActionSession) && member == "" {
			return fmt.Errorf("%s: completion %s action requires completion.session", where, t.Action.Type)
		}

		if member != "" && !ownedMembers[member] {
			return fmt.Errorf("%s: completion.session %q is not a non-shared session in this scenario", where, member)
		}
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
			return nil, errors.New("every [[sessions]] entry needs a name")
		}

		if s.Repo == "" && !s.Shared && s.Mirror == "" {
			return nil, fmt.Errorf("session %q: repo is required (unless shared or mirrored)", s.Name)
		}

		results := make([]protocol.ScenarioResultSpec, len(s.Results))
		for i, result := range s.Results {
			results[i] = protocol.ScenarioResultSpec{
				Name: result.Name, Format: result.Format, Store: result.Store, Required: result.Required,
			}
		}

		inputs = append(inputs, protocol.ScenarioSessionInput{
			Name:       s.Name,
			Repo:       s.Repo,
			Mirror:     s.Mirror,
			Agent:      s.Agent,
			Model:      s.Model,
			Base:       s.Base,
			Role:       s.Role,
			Prompt:     s.Prompt,
			Task:       s.Task,
			DependsOn:  append([]string(nil), s.DependsOn...),
			AgentHooks: s.AgentHooks == nil || *s.AgentHooks,
			Shared:     s.Shared,
			Includes:   s.Includes,
			Star:       s.Star,
			Results:    results,
			Policy:     MemberPolicyInput(s.Policy),
		})
	}

	if err := ValidateSessionContracts(inputs, config.TodoMaxTitleCeiling); err != nil {
		return nil, err
	}

	if err := ValidateSessionDependencies(inputs); err != nil {
		return nil, err
	}

	return inputs, nil
}

// ValidateSessionContracts checks the independent launch and tracked-work
// fields shared by every scenario entry point. maxTitle is the effective todo
// title limit; pass zero to skip only that configurable check. Prompt has a
// fixed body-sized wire limit and is invalid for an already-running shared
// member, where it could never be delivered at startup.
func ValidateSessionContracts(sessions []protocol.ScenarioSessionInput, maxTitle int) error {
	for _, session := range sessions {
		task := strings.TrimSpace(session.Task)
		if task != "" && maxTitle > 0 && len(task) > maxTitle {
			return fmt.Errorf("session %q: task exceeds todo title limit %d bytes", session.Name, maxTitle)
		}

		if session.Shared && strings.TrimSpace(session.Prompt) != "" {
			return fmt.Errorf("session %q: prompt is not valid for a shared session because it is already running", session.Name)
		}

		if prompt := session.StartupPrompt(); len(prompt) > protocol.MaxScenarioPromptBytes {
			return fmt.Errorf(
				"session %q: prompt is too large: %d bytes (max %d)",
				session.Name, len(prompt), protocol.MaxScenarioPromptBytes,
			)
		}
	}

	return nil
}

// PolicyInput maps a parsed scenario policy into the wire shape.
func PolicyInput(policy *PolicyConfig) *protocol.ScenarioPolicyInput {
	if policy == nil {
		return nil
	}

	return &protocol.ScenarioPolicyInput{
		Completion: policy.Completion, Quorum: policy.Quorum, OnExhausted: policy.OnExhausted,
	}
}

// MemberPolicyInput maps a parsed member policy into the wire shape.
func MemberPolicyInput(policy *MemberPolicyConfig) *protocol.ScenarioMemberPolicyInput {
	if policy == nil {
		return nil
	}

	return &protocol.ScenarioMemberPolicyInput{
		Required: policy.Required, Timeout: policy.Timeout, Retries: policy.Retries,
	}
}

// ValidateSessionDependencies validates the member-name DAG carried by a
// scenario definition. Both the CLI parser and daemon call it so a client
// cannot bypass unknown-member, missing-task, or cycle checks.
func ValidateSessionDependencies(sessions []protocol.ScenarioSessionInput) error {
	byName := make(map[string]protocol.ScenarioSessionInput, len(sessions))
	for _, s := range sessions {
		if s.Name != "" {
			byName[s.Name] = s
		}
	}

	for _, s := range sessions {
		if len(s.DependsOn) == 0 {
			continue
		}

		if strings.TrimSpace(s.Task) == "" {
			return fmt.Errorf("session %q: depends_on requires a task", s.Name)
		}

		seen := make(map[string]bool, len(s.DependsOn))
		for _, depName := range s.DependsOn {
			if depName == s.Name {
				return fmt.Errorf("session %q: depends_on cannot reference itself", s.Name)
			}

			if seen[depName] {
				return fmt.Errorf("session %q: duplicate depends_on member %q", s.Name, depName)
			}

			seen[depName] = true

			dep, ok := byName[depName]
			if !ok {
				return fmt.Errorf("session %q: depends_on member %q is not defined", s.Name, depName)
			}

			if strings.TrimSpace(dep.Task) == "" {
				return fmt.Errorf("session %q: depends_on member %q has no task to track", s.Name, depName)
			}
		}
	}

	const (
		visiting = iota + 1
		visited
	)

	state := make(map[string]int, len(sessions))

	var visit func(string) error

	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return fmt.Errorf("scenario task dependencies contain a cycle at %q", name)
		case visited:
			return nil
		}

		state[name] = visiting
		for _, dep := range byName[name].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}

		state[name] = visited

		return nil
	}

	for name := range byName {
		if err := visit(name); err != nil {
			return err
		}
	}

	return nil
}
