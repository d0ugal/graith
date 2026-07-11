package daemon

import "testing"

func TestCheckTriggerOp(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"orch":  {ID: "orch", SystemKind: SystemKindOrchestrator},
		"child": {ID: "child", ParentID: "orch"},
		"other": {ID: "other"},
	})

	// The orchestrator itself is allowed.
	if err := (authContext{sessionID: "orch", authenticated: true, role: roleOrchestrator}).checkTriggerOp(sm); err != nil {
		t.Errorf("orchestrator should be allowed: %v", err)
	}

	// A descendant of the orchestrator is allowed.
	if err := (authContext{sessionID: "child", authenticated: true, role: roleSession}).checkTriggerOp(sm); err != nil {
		t.Errorf("descendant should be allowed: %v", err)
	}

	// An unrelated session is rejected.
	if err := (authContext{sessionID: "other", authenticated: true, role: roleSession}).checkTriggerOp(sm); err == nil {
		t.Error("unrelated session should be rejected")
	}

	// A human caller (local socket) is always allowed.
	if err := (authContext{role: roleLocalHuman}).checkTriggerOp(sm); err != nil {
		t.Errorf("human caller should be allowed: %v", err)
	}
}

func TestCheckTriggerOp_NoOrchestrator(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"s1": {ID: "s1"},
	})
	if err := (authContext{sessionID: "s1", authenticated: true, role: roleSession}).checkTriggerOp(sm); err == nil {
		t.Error("expected rejection when there is no orchestrator to authorize against")
	}
}
