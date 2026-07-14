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
