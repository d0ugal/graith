package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/store"
)

const maxScenarioResultStoreTemplateBytes = 512

var validScenarioResultName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

var scenarioResultFormats = map[string]bool{
	"text":     true,
	"markdown": true,
	"json":     true,
}

// validateScenarioResultDeclarations validates every declaration before
// scenario start performs repo/filesystem work. Synthetic unique IDs exercise
// the same rendering and collision rules used for the final reserved IDs.
func validateScenarioResultDeclarations(scenarioName string, sessions []protocol.ScenarioSessionInput) error {
	seenDestinations := make(map[string]string)

	for i, session := range sessions {
		_, err := compileScenarioResults(
			"sc-validation", scenarioName, fmt.Sprintf("member-%d", i), session.Name,
			session.Results, seenDestinations,
		)
		if err != nil {
			return fmt.Errorf("session %q: %w", session.Name, err)
		}
	}

	return nil
}

func compileScenarioResults(
	scenarioID, scenarioName, sessionID, sessionName string,
	specs []protocol.ScenarioResultSpec,
	seenDestinations map[string]string,
) ([]ScenarioResultState, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	seenNames := make(map[string]bool, len(specs))
	results := make([]ScenarioResultState, len(specs))

	for i, spec := range specs {
		where := fmt.Sprintf("result %d", i)
		if spec.Name != "" {
			where = fmt.Sprintf("result %q", spec.Name)
		}

		if !validScenarioResultName.MatchString(spec.Name) {
			return nil, fmt.Errorf("%s: name is invalid: use 1-64 lowercase alphanumeric or hyphen characters, starting with a letter", where)
		}

		if seenNames[spec.Name] {
			return nil, fmt.Errorf("duplicate result name %q", spec.Name)
		}

		seenNames[spec.Name] = true

		if !scenarioResultFormats[spec.Format] {
			return nil, fmt.Errorf("%s: unsupported format %q (want text, markdown, or json)", where, spec.Format)
		}

		destination, err := renderScenarioResultDestination(
			scenarioID, scenarioName, sessionID, sessionName, spec.Name, spec.Store,
		)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid store destination: %w", where, err)
		}

		if previous, exists := seenDestinations[destination]; exists {
			return nil, fmt.Errorf("%s: store destination %q collides with %s", where, destination, previous)
		}

		seenDestinations[destination] = fmt.Sprintf("session %q result %q", sessionName, spec.Name)

		results[i] = ScenarioResultState{
			Name:          spec.Name,
			Format:        spec.Format,
			StoreTemplate: spec.Store,
			Destination:   destination,
			Required:      spec.Required,
			Status:        ScenarioResultPending,
		}
	}

	return results, nil
}

func renderScenarioResultDestination(
	scenarioID, scenarioName, sessionID, sessionName, resultName, template string,
) (string, error) {
	if template == "" {
		return "", errors.New("store template is required")
	}

	if len(template) > maxScenarioResultStoreTemplateBytes {
		return "", fmt.Errorf("store template must be %d bytes or fewer", maxScenarioResultStoreTemplateBytes)
	}

	rendered := strings.NewReplacer(
		"{scenario_id}", scenarioID,
		"{scenario_name}", scenarioName,
		"{session_id}", sessionID,
		"{session_name}", sessionName,
		"{result_name}", resultName,
	).Replace(template)

	if strings.ContainsAny(rendered, "{}") {
		return "", errors.New("store template contains an unknown or malformed placeholder")
	}

	if strings.HasSuffix(rendered, "/") || strings.Contains(rendered, "//") {
		return "", errors.New("store template must name a document, not an empty path component")
	}

	if err := store.ValidateKey(rendered); err != nil {
		return "", err
	}

	for _, component := range strings.Split(rendered, "/") {
		if len(component) > 255 {
			return "", errors.New("store template contains a path component longer than 255 bytes")
		}
	}

	destination := "scenarios/" + scenarioID + "/results/" + rendered
	if len(destination) > 1024 {
		return "", errors.New("resolved store destination is longer than 1024 bytes")
	}

	if err := store.ValidateKey(destination); err != nil {
		return "", err
	}

	return destination, nil
}

func scenarioResultInfo(result ScenarioResultState) protocol.ScenarioResultInfo {
	status := result.Status
	if status == "" {
		status = ScenarioResultPending
	}

	publishedAt := ""
	if !result.PublishedAt.IsZero() {
		publishedAt = result.PublishedAt.Format(time.RFC3339Nano)
	}

	return protocol.ScenarioResultInfo{
		Name:        result.Name,
		Format:      result.Format,
		Destination: result.Destination,
		Required:    result.Required,
		Status:      string(status),
		SizeBytes:   result.SizeBytes,
		PublishedAt: publishedAt,
		Error:       result.Error,
	}
}

type scenarioResultRef struct {
	scenarioID   string
	scenarioName string
	memberIndex  int
	memberName   string
	resultIndex  int
	result       ScenarioResultState
}

// findScenarioResultLocked resolves only the authenticated member's own result.
// The wire request contains no target member, which is the anti-forgery boundary.
func (sm *SessionManager) findScenarioResultLocked(sessionID, scenarioName, resultName string) (scenarioResultRef, error) {
	var refs []scenarioResultRef

	for scenarioID, scenario := range sm.state.Scenarios {
		if scenarioName != "" && scenario.Name != scenarioName {
			continue
		}

		for memberIndex, memberID := range scenario.SessionIDs {
			if memberID != sessionID || memberIndex >= len(scenario.Sessions) {
				continue
			}

			member := scenario.Sessions[memberIndex]
			for resultIndex, result := range member.Results {
				if result.Name == resultName {
					refs = append(refs, scenarioResultRef{
						scenarioID: scenarioID, scenarioName: scenario.Name,
						memberIndex: memberIndex, memberName: member.Name,
						resultIndex: resultIndex, result: result,
					})
				}
			}
		}
	}

	if len(refs) == 1 {
		return refs[0], nil
	}

	if len(refs) > 1 {
		return scenarioResultRef{}, fmt.Errorf("result %q is declared in multiple scenarios for this member; use --scenario", resultName)
	}

	if scenarioName != "" {
		for _, scenario := range sm.state.Scenarios {
			if scenario.Name != scenarioName {
				continue
			}

			for _, memberID := range scenario.SessionIDs {
				if memberID == sessionID {
					return scenarioResultRef{}, fmt.Errorf("result %q is not declared for this member in scenario %q", resultName, scenarioName)
				}
			}

			return scenarioResultRef{}, fmt.Errorf("authenticated session is not a member of scenario %q", scenarioName)
		}

		return scenarioResultRef{}, fmt.Errorf("scenario %q not found", scenarioName)
	}

	return scenarioResultRef{}, fmt.Errorf("result %q is not declared for this authenticated scenario member", resultName)
}

func validateScenarioResultBody(result ScenarioResultState, body string) error {
	if len(body) > protocol.MaxScenarioResultBodyBytes {
		return fmt.Errorf("result %q is too large: %d bytes (max %d)", result.Name, len(body), protocol.MaxScenarioResultBodyBytes)
	}

	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("result %q must not be empty", result.Name)
	}

	if result.Format == "json" && !json.Valid([]byte(body)) {
		return fmt.Errorf("result %q is not valid JSON", result.Name)
	}

	return nil
}

func (sm *SessionManager) updateScenarioResult(ref scenarioResultRef, update ScenarioResultState) (protocol.ScenarioResultInfo, error) {
	sm.mu.Lock()

	scenario := sm.state.Scenarios[ref.scenarioID]
	if scenario == nil || ref.memberIndex >= len(scenario.Sessions) || ref.memberIndex >= len(scenario.SessionIDs) {
		sm.mu.Unlock()

		return protocol.ScenarioResultInfo{}, errors.New("scenario was deleted during result publication")
	}

	member := &scenario.Sessions[ref.memberIndex]
	if scenario.SessionIDs[ref.memberIndex] == "" || ref.resultIndex >= len(member.Results) {
		sm.mu.Unlock()

		return protocol.ScenarioResultInfo{}, errors.New("result declaration changed during publication")
	}

	current := &member.Results[ref.resultIndex]
	if current.Name != ref.result.Name || current.Destination != ref.result.Destination {
		sm.mu.Unlock()

		return protocol.ScenarioResultInfo{}, errors.New("result declaration changed during publication")
	}

	previous := *current

	*current = update
	if err := sm.saveState(); err != nil {
		*current = previous
		sm.mu.Unlock()

		return protocol.ScenarioResultInfo{}, fmt.Errorf("persist result metadata: %w", err)
	}

	info := scenarioResultInfo(*current)
	sm.mu.Unlock()

	sm.hintScenarioCompletion("scenario:" + ref.scenarioID)

	return info, nil
}

func (sm *SessionManager) recordScenarioResultFailure(ref scenarioResultRef, status ScenarioResultStatus, publicationErr error) error {
	failed := ref.result
	failed.Status = status
	failed.SizeBytes = 0
	failed.PublishedAt = time.Time{}
	failed.Error = publicationErr.Error()

	if status == ScenarioResultFailed {
		// The authenticated publisher receives publicationErr directly, but the
		// durable status is visible to other scenario-status callers. Do not
		// persist filesystem paths or other daemon-internal store details there.
		failed.Error = "result storage failed"
	}

	_, persistErr := sm.updateScenarioResult(ref, failed)
	if persistErr != nil {
		return fmt.Errorf("%w (also failed to record result status: %w)", publicationErr, persistErr)
	}

	return publicationErr
}

// PublishScenarioResult validates and stores one authenticated member result.
// Store I/O runs without sm.mu; scenarioResultMu serializes the write+metadata
// commit pair so concurrent attempts cannot leave mismatched content/status.
func (sm *SessionManager) PublishScenarioResult(auth authContext, msg protocol.ScenarioResultPublishMsg) (protocol.ScenarioResultPublishResponse, error) {
	if !auth.authenticated || (auth.role != roleSession && auth.role != roleOrchestrator) {
		return protocol.ScenarioResultPublishResponse{}, errors.New("scenario result publication requires an authenticated session")
	}

	sm.scenarioResultMu.Lock()
	defer sm.scenarioResultMu.Unlock()

	sm.mu.RLock()
	ref, err := sm.findScenarioResultLocked(auth.sessionID, msg.Scenario, msg.Name)
	sm.mu.RUnlock()

	if err != nil {
		return protocol.ScenarioResultPublishResponse{}, err
	}

	if err := validateScenarioResultBody(ref.result, msg.Body); err != nil {
		return protocol.ScenarioResultPublishResponse{}, sm.recordScenarioResultFailure(ref, ScenarioResultInvalid, err)
	}

	storeDir := store.SharedStorePath(sm.paths.DataDir)
	if err := store.Init(storeDir); err != nil {
		publicationErr := fmt.Errorf("initialize shared result store: %w", err)
		return protocol.ScenarioResultPublishResponse{}, sm.recordScenarioResultFailure(ref, ScenarioResultFailed, publicationErr)
	}

	if err := store.Put(storeDir, ref.result.Destination, msg.Body); err != nil {
		publicationErr := fmt.Errorf("store result %q: %w", ref.result.Name, err)
		return protocol.ScenarioResultPublishResponse{}, sm.recordScenarioResultFailure(ref, ScenarioResultFailed, publicationErr)
	}

	available := ref.result
	available.Status = ScenarioResultAvailable
	available.SizeBytes = len(msg.Body)
	available.PublishedAt = time.Now().UTC()
	available.Error = ""

	info, err := sm.updateScenarioResult(ref, available)
	if err != nil {
		return protocol.ScenarioResultPublishResponse{}, err
	}

	return protocol.ScenarioResultPublishResponse{
		Scenario: ref.scenarioName,
		Member:   ref.memberName,
		Result:   info,
	}, nil
}
