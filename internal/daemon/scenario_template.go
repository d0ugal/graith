package daemon

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

const maxScenarioIDAllocationAttempts = 32

var scenarioNameTemplatePattern = regexp.MustCompile(`\{([^{}]+)\}`)

// reserveScenarioID allocates the stable ID used by both the render context and
// the eventual ScenarioState. The transient reservation closes the gap between
// rendering and the reserve phase for concurrent starts. Call release on every
// return path; once state.Scenarios contains the ID, releasing only removes the
// transient marker.
func (sm *SessionManager) reserveScenarioID() (string, func(), error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.scenarioStartIDs == nil {
		sm.scenarioStartIDs = make(map[string]bool)
	}

	generate := sm.scenarioIDGenerator
	if generate == nil {
		generate = generateID
	}

	for range maxScenarioIDAllocationAttempts {
		shortID := generate()
		if !validSessionID.MatchString(shortID) {
			return "", nil, fmt.Errorf("allocate scenario id: generator returned invalid short id %q", shortID)
		}

		id := "sc-" + shortID
		if sm.scenarioStartIDs[id] || sm.state.Scenarios[id] != nil {
			continue
		}

		sm.scenarioStartIDs[id] = true
		released := false

		return id, func() {
			sm.mu.Lock()
			if !released {
				delete(sm.scenarioStartIDs, id)

				released = true
			}
			sm.mu.Unlock()
		}, nil
	}

	return "", nil, fmt.Errorf("allocate unique scenario id: exhausted %d attempts", maxScenarioIDAllocationAttempts)
}

func scenarioRenderIdentity(session *SessionState) ScenarioRenderIdentityState {
	return ScenarioRenderIdentityState{SessionID: session.ID, Name: session.Name}
}

func newScenarioRenderState(
	authoredName, scenarioID string,
	renderedAt time.Time,
	caller, parent, initiator *SessionState,
) *ScenarioRenderState {
	return &ScenarioRenderState{
		AuthoredName: authoredName,
		ScenarioID:   scenarioID,
		ShortID:      strings.TrimPrefix(scenarioID, "sc-"),
		RenderedAt:   renderedAt.UTC(),
		Caller:       scenarioRenderIdentity(caller),
		Parent:       scenarioRenderIdentity(parent),
		Initiator:    scenarioRenderIdentity(initiator),
	}
}

func scenarioRenderValues(state *ScenarioRenderState, renderedScenario string) map[string]string {
	return map[string]string{
		"caller":      state.Caller.Name,
		"parent":      state.Parent.Name,
		"initiator":   state.Initiator.Name,
		"date":        state.RenderedAt.Format("20060102"),
		"time":        state.RenderedAt.Format("150405"),
		"datetime":    state.RenderedAt.Format("20060102t150405z"),
		"scenario_id": state.ScenarioID,
		"short_id":    state.ShortID,
		"scenario":    renderedScenario,
	}
}

// renderScenarioTemplate performs one non-recursive substitution pass. A
// trigger inbox may retain known fire-time trigger variables; every other
// name-bearing field must resolve completely at scenario start.
func renderScenarioTemplate(input, field string, values map[string]string, allowTriggerVars bool) (string, error) {
	var renderErr error

	rendered := scenarioNameTemplatePattern.ReplaceAllStringFunc(input, func(match string) string {
		name := match[1 : len(match)-1]
		if value, ok := values[name]; ok {
			if name == "scenario" && value == "" {
				if renderErr == nil {
					renderErr = fmt.Errorf("scenario name template variable %q is not available while rendering %s", name, field)
				}

				return match
			}

			return value
		}

		if allowTriggerVars && config.IsTriggerTemplateVar(name) {
			return match
		}

		if renderErr == nil {
			renderErr = fmt.Errorf("unknown scenario name template variable %q in %s", name, field)
		}

		return match
	})

	if renderErr != nil {
		return "", renderErr
	}

	// The regexp deliberately accepts only balanced, non-nested braces. Catch
	// unmatched or nested syntax rather than allowing it to fall through to a
	// less useful name-validation error.
	syntaxRemainder := rendered
	if allowTriggerVars {
		syntaxRemainder = scenarioNameTemplatePattern.ReplaceAllStringFunc(syntaxRemainder, func(match string) string {
			if config.IsTriggerTemplateVar(match[1 : len(match)-1]) {
				return ""
			}

			return match
		})
	}

	if strings.ContainsAny(syntaxRemainder, "{}") {
		return "", fmt.Errorf("invalid scenario name template syntax in %s: %q", field, input)
	}

	return rendered, nil
}

// renderScenarioStart renders a deep-enough copy of every name-bearing field.
// It does not validate the resulting graph; StartScenario immediately runs the
// existing authoritative validators against the returned message.
func renderScenarioStart(authored protocol.ScenarioStartMsg, state *ScenarioRenderState) (protocol.ScenarioStartMsg, error) {
	rendered := authored
	rendered.Sessions = make([]protocol.ScenarioSessionInput, len(authored.Sessions))
	rendered.Triggers = make([]protocol.TriggerConfig, len(authored.Triggers))

	baseValues := scenarioRenderValues(state, "")

	name, err := renderScenarioTemplate(authored.Name, "scenario.name", baseValues, false)
	if err != nil {
		return protocol.ScenarioStartMsg{}, err
	}

	rendered.Name = name
	values := scenarioRenderValues(state, name)

	state.Members = make([]ScenarioRenderMemberState, len(authored.Sessions))
	for i, authoredSession := range authored.Sessions {
		session := authoredSession
		session.DependsOn = append([]string(nil), authoredSession.DependsOn...)
		session.Includes = append([]string(nil), authoredSession.Includes...)
		session.Results = append([]protocol.ScenarioResultSpec(nil), authoredSession.Results...)

		path := fmt.Sprintf("sessions[%d].name", i)

		session.Name, err = renderScenarioTemplate(authoredSession.Name, path, values, false)
		if err != nil {
			return protocol.ScenarioStartMsg{}, err
		}

		state.Members[i] = ScenarioRenderMemberState{
			AuthoredName: authoredSession.Name,
			RenderedName: session.Name,
		}
		rendered.Sessions[i] = session
	}

	for i, authoredSession := range authored.Sessions {
		session := &rendered.Sessions[i]
		if authoredSession.Mirror != "" {
			path := fmt.Sprintf("sessions[%d].mirror", i)

			session.Mirror, err = renderScenarioTemplate(authoredSession.Mirror, path, values, false)
			if err != nil {
				return protocol.ScenarioStartMsg{}, err
			}

			state.References = append(state.References, ScenarioRenderReferenceState{
				Path: path, Authored: authoredSession.Mirror, Rendered: session.Mirror,
			})
		}

		for j, dependency := range authoredSession.DependsOn {
			path := fmt.Sprintf("sessions[%d].depends_on[%d]", i, j)

			session.DependsOn[j], err = renderScenarioTemplate(dependency, path, values, false)
			if err != nil {
				return protocol.ScenarioStartMsg{}, err
			}

			state.References = append(state.References, ScenarioRenderReferenceState{
				Path: path, Authored: dependency, Rendered: session.DependsOn[j],
			})
		}
	}

	for i, authoredTrigger := range authored.Triggers {
		trigger := authoredTrigger
		if authoredTrigger.Completion != nil {
			completion := *authoredTrigger.Completion

			trigger.Completion = &completion
			if completion.Session != "" {
				path := fmt.Sprintf("triggers[%d].completion.session", i)

				trigger.Completion.Session, err = renderScenarioTemplate(completion.Session, path, values, false)
				if err != nil {
					return protocol.ScenarioStartMsg{}, err
				}

				state.References = append(state.References, ScenarioRenderReferenceState{
					Path: path, Authored: completion.Session, Rendered: trigger.Completion.Session,
				})
			}
		}

		if inbox := authoredTrigger.Action.Deliver.Inbox; inbox != "" && inbox != "orchestrator" {
			path := fmt.Sprintf("triggers[%d].action.deliver.inbox", i)

			trigger.Action.Deliver.Inbox, err = renderScenarioTemplate(inbox, path, values, true)
			if err != nil {
				return protocol.ScenarioStartMsg{}, err
			}

			state.References = append(state.References, ScenarioRenderReferenceState{
				Path: path, Authored: inbox, Rendered: trigger.Action.Deliver.Inbox,
			})
		}

		rendered.Triggers[i] = trigger
	}

	return rendered, nil
}

func scenarioRenderInfo(state *ScenarioRenderState) *protocol.ScenarioRenderInfo {
	if state == nil {
		return nil
	}

	members := make([]protocol.ScenarioRenderMemberInfo, len(state.Members))
	for i, member := range state.Members {
		members[i] = protocol.ScenarioRenderMemberInfo{
			AuthoredName: member.AuthoredName,
			RenderedName: member.RenderedName,
		}
	}

	references := make([]protocol.ScenarioRenderReferenceInfo, len(state.References))
	for i, reference := range state.References {
		references[i] = protocol.ScenarioRenderReferenceInfo{
			Path: reference.Path, Authored: reference.Authored, Rendered: reference.Rendered,
		}
	}

	identity := func(value ScenarioRenderIdentityState) protocol.ScenarioRenderIdentityInfo {
		return protocol.ScenarioRenderIdentityInfo{SessionID: value.SessionID, Name: value.Name}
	}

	return &protocol.ScenarioRenderInfo{
		AuthoredName: state.AuthoredName,
		ScenarioID:   state.ScenarioID,
		ShortID:      state.ShortID,
		RenderedAt:   state.RenderedAt.UTC().Format(time.RFC3339Nano),
		Caller:       identity(state.Caller),
		Parent:       identity(state.Parent),
		Initiator:    identity(state.Initiator),
		Members:      members,
		References:   references,
	}
}
