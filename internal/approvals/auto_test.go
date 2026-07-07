package approvals

import (
	"context"
	"testing"
)

// TestAutoBackendAlwaysAllows verifies the auto backend approves every request
// regardless of tool name or input — it is the engine behind yolo mode.
func TestAutoBackendAlwaysAllows(t *testing.T) {
	be := autoBackend{}

	if be.Name() != BackendAuto {
		t.Errorf("Name() = %q, want %q", be.Name(), BackendAuto)
	}

	reqs := []Request{
		{ToolName: "Bash", ToolInput: `{"command":"rm -rf /"}`},
		{ToolName: "Edit", ToolInput: `{"file":"x"}`},
		{ToolName: "", ToolInput: ""},
	}
	for _, req := range reqs {
		d, err := be.Decide(context.Background(), req, Config{})
		if err != nil {
			t.Fatalf("Decide(%q) unexpected error: %v", req.ToolName, err)
		}

		if d.Decision != DecisionAllow {
			t.Errorf("Decide(%q) = %q, want %q", req.ToolName, d.Decision, DecisionAllow)
		}
	}
}

// TestAutoBackendAvailabilityNeverFails verifies the auto backend can always
// enforce — unlike command/localmost there is no external binary to find, so a
// yolo session never fails closed at session-create.
func TestAutoBackendAvailabilityNeverFails(t *testing.T) {
	if av := (autoBackend{}).Availability(Config{}); !av.CanEnforce {
		t.Error("auto backend must always be available")
	}
}
