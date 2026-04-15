package daemon

import (
	"log/slog"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestOnAgentStatusChange_NotifiesOnApproval(t *testing.T) {
	sm := &SessionManager{
		cfg: &config.Config{
			Notifications: config.Notifications{
				Enabled:    true,
				OnApproval: true,
			},
		},
		log: slog.Default(),
	}

	// Should not panic with nil messages store
	sm.onAgentStatusChange("test-id", "test-session", "active", "approval")
}

func TestOnAgentStatusChange_DisabledNotifications(t *testing.T) {
	sm := &SessionManager{
		cfg: &config.Config{
			Notifications: config.Notifications{
				Enabled: false,
			},
		},
		log: slog.Default(),
	}

	sm.onAgentStatusChange("test-id", "test-session", "active", "approval")
}

func TestOnAgentStatusChange_SkipsNonApprovalWhenOnlyApprovalEnabled(t *testing.T) {
	sm := &SessionManager{
		cfg: &config.Config{
			Notifications: config.Notifications{
				Enabled:    true,
				OnApproval: true,
				OnStopped:  false,
			},
		},
		log: slog.Default(),
	}

	// "active" transitions should not trigger notifications
	sm.onAgentStatusChange("test-id", "test-session", "unknown", "active")
}

func TestOnAgentStatusChange_PublishesToMessageStore(t *testing.T) {
	dir := t.TempDir()
	ms, err := NewMsgStore(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	sm := &SessionManager{
		cfg: &config.Config{
			Notifications: config.Notifications{
				Enabled: false,
			},
		},
		log:      slog.Default(),
		messages: ms,
	}

	sm.onAgentStatusChange("sess-1", "my-session", "active", "approval")

	msgs, err := ms.Read("_system.status", "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].SenderName != "my-session" {
		t.Errorf("expected sender_name 'my-session', got %q", msgs[0].SenderName)
	}
}
