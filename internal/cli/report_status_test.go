package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// maxLastMessageRunes is the default last-message rune cap, used across these
// tests since the constant moved into the [limits] config section (issue #1252).
const maxLastMessageRunes = config.LimitsLastMessageRunesDefault

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
		msg := buildStatusReport("ken", "Notification", "", hookStdin{NotificationType: "permission_prompt"}, true, maxLastMessageRunes)

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
		msg := buildStatusReport("haar", "Notification", "", hookStdin{NotificationType: "idle_prompt"}, false, maxLastMessageRunes)

		if msg.NotificationType != "" {
			t.Errorf("NotificationType = %q, want empty when unparsed", msg.NotificationType)
		}
	})

	t.Run("tool flag takes precedence over stdin tool", func(t *testing.T) {
		msg := buildStatusReport("braw", "PreToolUse", "Bash", hookStdin{ToolName: "Edit"}, true, maxLastMessageRunes)

		if msg.ToolName != "Bash" {
			t.Errorf("ToolName = %q, want flag value %q", msg.ToolName, "Bash")
		}
	})

	t.Run("stdin tool fills empty flag", func(t *testing.T) {
		msg := buildStatusReport("bonnie", "PreToolUse", "", hookStdin{ToolName: "Write"}, true, maxLastMessageRunes)

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

	msg := buildStatusReport("braw", "PreCompact", "", data, true, maxLastMessageRunes)

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

	msg := buildStatusReport("braw", "SubagentStart", "", data, true, maxLastMessageRunes)

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
	msg := buildStatusReport("braw", "PostCompact", "", hookStdin{}, false, maxLastMessageRunes)

	if msg.Trigger != "" || msg.AgentID != "" || msg.AgentType != "" {
		t.Errorf("unparsed stdin leaked fields: %+v", msg)
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Run("short string unchanged", func(t *testing.T) {
		in := "braw wee message"
		if got := truncateRunes(in, maxLastMessageRunes); got != in {
			t.Errorf("truncateRunes(%q) = %q, want unchanged", in, got)
		}
	})

	t.Run("long string truncated to bound", func(t *testing.T) {
		in := strings.Repeat("a", maxLastMessageRunes+500)

		got := truncateRunes(in, maxLastMessageRunes)
		if len([]rune(got)) != maxLastMessageRunes {
			t.Errorf("truncated length = %d runes, want %d", len([]rune(got)), maxLastMessageRunes)
		}
	})

	// Issue #1252: a maxRunes < 1 falls back to the config default rather than
	// truncating everything to nothing.
	t.Run("non-positive bound uses the config default", func(t *testing.T) {
		in := strings.Repeat("a", maxLastMessageRunes+500)
		if got := truncateRunes(in, 0); len([]rune(got)) != maxLastMessageRunes {
			t.Errorf("truncateRunes(_, 0) = %d runes, want default %d", len([]rune(got)), maxLastMessageRunes)
		}
	})

	t.Run("cuts on a rune boundary", func(t *testing.T) {
		// Multi-byte runes must never be split mid-character.
		in := strings.Repeat("thrawn✓", 1000) // ✓ is 3 bytes

		got := truncateRunes(in, 10)
		if len([]rune(got)) != 10 {
			t.Errorf("truncated length = %d runes, want 10", len([]rune(got)))
		}

		if !json.Valid([]byte(`"` + got + `"`)) {
			t.Errorf("truncated output %q is not a valid JSON string (split a rune)", got)
		}
	})
}

// TestHookStdinParse verifies the SessionEnd reason and Stop last_assistant_message
// fields decode off the hook JSON payload, and that an over-long final message is
// truncated before it would hit the wire.
func TestHookStdinParse(t *testing.T) {
	t.Run("SessionEnd reason", func(t *testing.T) {
		var parsed hookStdin
		if err := json.Unmarshal([]byte(`{"reason":"logout"}`), &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if parsed.Reason != "logout" {
			t.Errorf("Reason = %q, want %q", parsed.Reason, "logout")
		}
	})

	t.Run("Stop last_assistant_message truncated", func(t *testing.T) {
		long := strings.Repeat("dreich ", maxLastMessageRunes) // well over the bound

		payload, err := json.Marshal(map[string]string{"last_assistant_message": long})
		if err != nil {
			t.Fatal(err)
		}

		var parsed hookStdin
		if err := json.Unmarshal(payload, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if parsed.LastAssistantMsg != long {
			t.Fatal("parse did not round-trip last_assistant_message")
		}

		out := truncateRunes(parsed.LastAssistantMsg, maxLastMessageRunes)
		if len([]rune(out)) != maxLastMessageRunes {
			t.Errorf("forwarded message = %d runes, want capped at %d", len([]rune(out)), maxLastMessageRunes)
		}
	})

	t.Run("Stop message via buildStatusReport is truncated", func(t *testing.T) {
		long := strings.Repeat("x", maxLastMessageRunes+123)
		msg := buildStatusReport("braw", "Stop", "", hookStdin{LastAssistantMsg: long}, true, maxLastMessageRunes)

		if len([]rune(msg.LastMessage)) != maxLastMessageRunes {
			t.Errorf("LastMessage = %d runes, want capped at %d", len([]rune(msg.LastMessage)), maxLastMessageRunes)
		}
	})
}
