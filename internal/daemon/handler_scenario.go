package daemon

import (
	"github.com/d0ugal/graith/internal/protocol"
)

// handleScenarioStart validates and starts a scenario on behalf of the calling
// (authenticated) session, replying with the started scenario's status record.
func handleScenarioStart(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	s, ok := decodePayload[protocol.ScenarioStartMsg](msg, send, "invalid scenario_start message")
	if !ok {
		return
	}

	if !auth.authenticated {
		send("error", protocol.ErrorMsg{Message: "scenario_start requires authentication"})

		return
	}

	s.CallerSessionID = auth.sessionID

	scenario, err := sm.StartScenario(s, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})

		return
	}

	record, _ := sm.ScenarioStatus(scenario.Name)
	send("scenario_started", record)
}

// handleScenarioStop stops all non-shared sessions in a scenario.
//
//nolint:dupl // shares the decode→log→authorize→call→respond shape with handleScenarioDelete but targets a distinct message/method; merging would obscure both.
func handleScenarioStop(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	s, ok := decodePayload[protocol.ScenarioStopMsg](msg, send, "invalid scenario_stop message")
	if !ok {
		return
	}

	sm.log.Debug("control request",
		"op", "scenario_stop", "caller", auth.describe(), "scenario", s.Name)

	if !auth.authorizeScenarioOp(sm, s.Name, send) {
		return
	}

	stopped, err := sm.StopScenario(s.Name)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("scenario_stopped", struct {
			Name    string   `json:"name"`
			Stopped []string `json:"stopped"`
		}{s.Name, stopped})
	}
}

// handleScenarioDelete deletes a scenario and all its owned sessions/worktrees.
//
//nolint:dupl // shares the decode→log→authorize→call→respond shape with handleScenarioStop but targets a distinct message/method; merging would obscure both.
func handleScenarioDelete(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	s, ok := decodePayload[protocol.ScenarioDeleteMsg](msg, send, "invalid scenario_delete message")
	if !ok {
		return
	}

	sm.log.Debug("control request",
		"op", "scenario_delete", "caller", auth.describe(), "scenario", s.Name)

	if !auth.authorizeScenarioOp(sm, s.Name, send) {
		return
	}

	deleted, err := sm.DeleteScenario(s.Name)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("scenario_deleted", struct {
			Name    string   `json:"name"`
			Deleted []string `json:"deleted"`
		}{s.Name, deleted})
	}
}

// handleScenarioStatus returns a scenario's status record. Read-only.
func handleScenarioStatus(sm *SessionManager, send func(string, any), msg protocol.Envelope) {
	s, ok := decodePayload[protocol.ScenarioStatusMsg](msg, send, "invalid scenario_status message")
	if !ok {
		return
	}

	record, err := sm.ScenarioStatus(s.Name)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("scenario_status", protocol.ScenarioStatusResponse{Scenario: *record})
	}
}

// handleScenarioResume resumes all stopped sessions in a scenario.
func handleScenarioResume(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	s, ok := decodePayload[protocol.ScenarioResumeMsg](msg, send, "invalid scenario_resume message")
	if !ok {
		return
	}

	if !auth.authorizeScenarioOp(sm, s.Name, send) {
		return
	}

	resumed, err := sm.ResumeScenario(s.Name, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("scenario_resumed", struct {
			Name    string   `json:"name"`
			Resumed []string `json:"resumed"`
		}{s.Name, resumed})
	}
}

// handleScenarioAdd adds a session to a running scenario.
func handleScenarioAdd(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope, rows, cols uint16) {
	s, ok := decodePayload[protocol.ScenarioAddMsg](msg, send, "invalid scenario_add message")
	if !ok {
		return
	}

	if !auth.authorizeScenarioOp(sm, s.Name, send) {
		return
	}

	sess, err := sm.AddToScenario(s.Name, s.Session, rows, cols)
	if err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("scenario_added", struct {
			Name      string `json:"name"`
			SessionID string `json:"session_id"`
		}{s.Name, sess.ID})
	}
}

// handleScenarioList returns all scenario status records. Read-only.
func handleScenarioList(sm *SessionManager, send func(string, any)) {
	send("scenario_list", protocol.ScenarioListResponse{Scenarios: sm.ListScenarios()})
}
