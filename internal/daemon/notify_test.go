package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func TestIsSystemSender(t *testing.T) {
	if !isSystemSender(systemSenderID) {
		t.Errorf("isSystemSender(%q) = false, want true", systemSenderID)
	}

	for _, id := range []string{"", "graith", "braw-session", "device:abc", systemSenderName} {
		if isSystemSender(id) {
			t.Errorf("isSystemSender(%q) = true, want false", id)
		}
	}
}

func TestNotifyFromDaemon_PublishesSystemNotification(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "braw-sess", "braw-session")

	_ = h.sm.notifyFromDaemon("braw-sess", "PR #884 was merged. No further action needed.")

	msgs, err := h.sm.messages.Read("inbox:braw-sess", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	m := msgs[0]
	if !m.System {
		t.Errorf("System = false, want true — daemon notifications must be flagged")
	}

	if m.SenderID != systemSenderID {
		t.Errorf("SenderID = %q, want %q", m.SenderID, systemSenderID)
	}

	if m.SenderName != systemSenderName {
		t.Errorf("SenderName = %q, want %q", m.SenderName, systemSenderName)
	}

	if m.NoReply {
		t.Error("NoReply = true, want false — system identity is a distinct non-replyable semantic")
	}

	// The sender must not look like an addressable session name (issue #887):
	// a real session ID never carries the "graith:" prefix.
	if m.SenderID == "graith" {
		t.Errorf("SenderID = %q — must be distinct from the ambiguous bare 'graith'", m.SenderID)
	}
}

// TestNotifyUnreadInboxNoReplyHint covers the notification function invoked
// after auto-resume. A one-message unread inbox must retain the same explicit
// no-reply hint as delivery to an already-running recipient.
func TestNotifyUnreadInboxNoReplyHint(t *testing.T) {
	h := newTestHarness(t)
	h.addPTYSession(t, "canny-resumed", "canny-session")
	h.sm.cfg.Notifications.Timing.InboxDetachedDelay = "0"

	_, err := h.sm.messages.Publish(PublishOpts{
		Stream: "inbox:canny-resumed", SenderID: "morag-sender", SenderName: "Morag",
		Body: "one-way briefing", NoReply: true,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	h.sm.notifyUnreadInbox("canny-resumed")
	time.Sleep(200 * time.Millisecond)

	ptySess, ok := h.sm.GetPTY("canny-resumed")
	if !ok {
		t.Fatal("resumed PTY session not found")
	}

	tail, err := ptySess.ScrollbackFile().Tail(500)
	if err != nil {
		t.Fatalf("scrollback tail: %v", err)
	}

	scrollback := string(tail)
	if !strings.Contains(scrollback, "No reply expected") {
		t.Errorf("post-resume hint missing no-reply wording; got:\n%s", scrollback)
	}

	if strings.Contains(scrollback, "Reply: gr msg send") {
		t.Errorf("post-resume hint should not offer a reply command; got:\n%s", scrollback)
	}
}

func TestOnAgentStatusChange_PublishesToMessageStore(t *testing.T) {
	dir := t.TempDir()

	ms, err := NewMsgStore(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ms.Close() }()

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
