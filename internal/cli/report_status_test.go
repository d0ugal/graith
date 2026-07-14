package cli

import (
	"encoding/json"
	"testing"
)

// TestHookStdinDecodesNotificationType verifies the notification subtype field
// decodes off the hook payload.
func TestHookStdinDecodesNotificationType(t *testing.T) {
	var parsed hookStdin
	if err := json.Unmarshal([]byte(`{"notification_type":"idle_prompt","tool_name":"Read"}`), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.NotificationType != "idle_prompt" {
		t.Errorf("NotificationType = %q, want %q", parsed.NotificationType, "idle_prompt")
	}

	if parsed.ToolName != "Read" {
		t.Errorf("ToolName = %q, want %q", parsed.ToolName, "Read")
	}
}

func TestBuildStatusReport(t *testing.T) {
	t.Run("forwards parsed notification subtype", func(t *testing.T) {
		msg := buildStatusReport("ken", "Notification", "", hookStdin{NotificationType: "permission_prompt"}, true)

		if msg.NotificationType != "permission_prompt" {
			t.Errorf("NotificationType = %q, want %q", msg.NotificationType, "permission_prompt")
		}

		if msg.Event != "Notification" || msg.SessionID != "ken" {
			t.Errorf("msg = %+v, want Event=Notification SessionID=ken", msg)
		}
	})

	// Regression: an unparsed hook (parse timeout) forwards an EMPTY subtype so
	// the daemon leaves status unchanged, instead of the old behaviour where a
	// bare Notification was mapped to approval.
	t.Run("unparsed forwards empty subtype", func(t *testing.T) {
		msg := buildStatusReport("haar", "Notification", "", hookStdin{NotificationType: "idle_prompt"}, false)

		if msg.NotificationType != "" {
			t.Errorf("NotificationType = %q, want empty when unparsed", msg.NotificationType)
		}
	})

	t.Run("tool flag takes precedence over stdin tool", func(t *testing.T) {
		msg := buildStatusReport("braw", "PreToolUse", "Bash", hookStdin{ToolName: "Edit"}, true)

		if msg.ToolName != "Bash" {
			t.Errorf("ToolName = %q, want flag value %q", msg.ToolName, "Bash")
		}
	})

	t.Run("stdin tool fills empty flag", func(t *testing.T) {
		msg := buildStatusReport("bonnie", "PreToolUse", "", hookStdin{ToolName: "Write"}, true)

		if msg.ToolName != "Write" {
			t.Errorf("ToolName = %q, want stdin value %q", msg.ToolName, "Write")
		}
	})
}

// decodeHookStdin mirrors the non-blocking stdin parse in report-status: it
// decodes a raw Claude/Codex hook payload into hookStdin.
func decodeHookStdin(t *testing.T, raw string) hookStdin {
	t.Helper()

	var data hookStdin
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("unmarshal hook payload: %v", err)
	}

	return data
}

func TestBuildStatusReportCompaction(t *testing.T) {
	// PreCompact carries a trigger; it must flow onto the message unchanged.
	data := decodeHookStdin(t, `{"trigger":"auto"}`)

	msg := buildStatusReport("braw", "PreCompact", "", data, true)

	if msg.Event != "PreCompact" {
		t.Errorf("Event = %q, want PreCompact", msg.Event)
	}

	if msg.Trigger != "auto" {
		t.Errorf("Trigger = %q, want auto", msg.Trigger)
	}

	if msg.AgentID != "" || msg.AgentType != "" {
		t.Errorf("agent fields set for compaction: id=%q type=%q", msg.AgentID, msg.AgentType)
	}
}

func TestBuildStatusReportSubagent(t *testing.T) {
	data := decodeHookStdin(t, `{"agent_id":"bairn-1","agent_type":"canny"}`)

	msg := buildStatusReport("braw", "SubagentStart", "", data, true)

	if msg.AgentID != "bairn-1" {
		t.Errorf("AgentID = %q, want bairn-1", msg.AgentID)
	}

	if msg.AgentType != "canny" {
		t.Errorf("AgentType = %q, want canny", msg.AgentType)
	}
}

func TestBuildStatusReportUnparsedDropsNewFields(t *testing.T) {
	// When stdin didn't parse within the 100ms budget, the compaction/sub-agent
	// fields stay empty (nothing to carry).
	msg := buildStatusReport("braw", "PostCompact", "", hookStdin{}, false)

	if msg.Trigger != "" || msg.AgentID != "" || msg.AgentType != "" {
		t.Errorf("unparsed stdin leaked fields: %+v", msg)
	}
}
