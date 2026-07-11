package daemon

import "testing"

func TestCheckNotifyOp(t *testing.T) {
	sm := newTestSMWithSessions(map[string]*SessionState{
		"orch":  {ID: "orch", SystemKind: SystemKindOrchestrator},
		"child": {ID: "child", ParentID: "orch"},
		"other": {ID: "other"},
	})

	// The orchestrator may notify.
	if err := (authContext{sessionID: "orch", authenticated: true, role: roleOrchestrator}).checkNotifyOp(sm); err != nil {
		t.Errorf("orchestrator should be allowed: %v", err)
	}

	// The local human may notify.
	if err := (authContext{role: roleLocalHuman}).checkNotifyOp(sm); err != nil {
		t.Errorf("human caller should be allowed: %v", err)
	}

	// A plain descendant session is rejected (stricter than triggers) to prevent
	// notification spam.
	if err := (authContext{sessionID: "child", authenticated: true, role: roleSession}).checkNotifyOp(sm); err == nil {
		t.Error("a plain agent session (even a descendant) must not notify")
	}

	// An unrelated session is rejected.
	if err := (authContext{sessionID: "other", authenticated: true, role: roleSession}).checkNotifyOp(sm); err == nil {
		t.Error("unrelated session should be rejected")
	}

	// A read-only remote guest is rejected.
	if err := (authContext{role: roleRemoteGuest, deviceID: "dreich"}).checkNotifyOp(sm); err == nil {
		t.Error("read-only guest must not notify")
	}
}
