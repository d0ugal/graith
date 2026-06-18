package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	sm.onAgentStatusChange("braw-id", "bonnie-session", "active", "approval")
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

	sm.onAgentStatusChange("braw-id", "bonnie-session", "active", "approval")
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
	sm.onAgentStatusChange("braw-id", "bonnie-session", "unknown", "active")
}

func TestSendNotification_CommandUsesEnvVars(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "out.txt")

	sm := &SessionManager{
		cfg: &config.Config{
			Notifications: config.Notifications{
				Enabled:    true,
				OnApproval: true,
				Command:    "printf '%s|%s|%s' \"$GRAITH_SESSION_NAME\" \"$GRAITH_STATUS\" \"$GRAITH_MESSAGE\" > " + outFile,
			},
		},
		log: slog.Default(),
	}

	sm.sendNotification("braw-kirk", "approval", sm.cfg.Notifications.Command)

	deadline := time.After(5 * time.Second)
	for {
		data, err := os.ReadFile(outFile)
		if err == nil && len(data) > 0 {
			got := string(data)
			want := "braw-kirk|approval|braw-kirk needs approval"
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for notification command output")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestSendNotification_CommandInjectionPrevented(t *testing.T) {
	markerFile := filepath.Join(t.TempDir(), "pwned.txt")

	sm := &SessionManager{
		cfg: &config.Config{
			Notifications: config.Notifications{
				Enabled:    true,
				OnApproval: true,
				// Use $GRAITH_SESSION_NAME so the value reaches the shell;
				// under the old {name} interpolation a malicious name would
				// have been executed as a subshell.
				Command: "echo $GRAITH_SESSION_NAME > /dev/null",
			},
		},
		log: slog.Default(),
	}

	malicious := "$(touch " + markerFile + ")"
	sm.sendNotification(malicious, "approval", sm.cfg.Notifications.Command)

	time.Sleep(500 * time.Millisecond)

	if _, err := os.Stat(markerFile); err == nil {
		t.Fatal("command injection succeeded: malicious session name was executed as shell command")
	}
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

	sm.onAgentStatusChange("braw-sess", "braw-kirk", "active", "approval")

	msgs, err := ms.Read("_system.status", "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].SenderName != "braw-kirk" {
		t.Errorf("expected sender_name 'braw-kirk', got %q", msgs[0].SenderName)
	}
}
