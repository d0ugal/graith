package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func (sm *SessionManager) onAgentStatusChange(sessionID, sessionName, oldStatus, newStatus string) {
	sm.log.Info("agent status changed",
		"session", sessionName, "id", sessionID,
		"from", oldStatus, "to", newStatus)

	if sm.messages != nil {
		body := fmt.Sprintf("Session %q status changed: %s → %s", sessionName, oldStatus, newStatus)
		_, err := sm.messages.Publish("_system.status", sessionID, sessionName, body, "", "")
		if err != nil {
			sm.log.Error("failed to publish status change", "err", err)
		}
	}

	sm.mu.RLock()
	notifCfg := sm.cfg.Notifications
	sm.mu.RUnlock()

	if !notifCfg.Enabled {
		return
	}

	switch newStatus {
	case "approval":
		if !notifCfg.OnApproval {
			return
		}
	case "stopped":
		if !notifCfg.OnStopped {
			return
		}
	default:
		return
	}

	sm.sendNotification(sessionName, newStatus, notifCfg.Command)
}

// notifyInbox injects a notification into the target session's PTY when a
// message is published to its inbox. This runs daemon-side so it bypasses
// the per-session auth check on the "type" command.
func (sm *SessionManager) notifyInbox(targetID, senderID, senderName string) {
	ptySess, ok := sm.GetPTY(targetID)
	if !ok {
		return
	}
	sender := senderName
	if sender == "" {
		sender = senderID
	}
	hint := fmt.Sprintf("New message from %s. Read: gr msg sub --topic inbox:%s --all --ack | Reply: gr msg send %s \"<reply>\"", sender, targetID, sender)
	if sm.HasAttachedClient(targetID) {
		ptySess.WaitForUserIdle(10*time.Second, 2*time.Minute)
	}
	if err := ptySess.WriteInputAndSubmit([]byte(hint)); err != nil {
		sm.log.Debug("inbox notification write failed", "target", targetID, "err", err)
	}
	ptySess.Poke()
}

func (sm *SessionManager) sendNotification(sessionName, status, command string) {
	if err := ValidateSessionName(sessionName); err != nil {
		sm.log.Error("refusing to send notification for unsafe session name", "name", sessionName, "err", err)
		return
	}
	title := "graith"
	var message string
	switch status {
	case "approval":
		message = fmt.Sprintf("%s needs approval", sessionName)
	case "stopped":
		message = fmt.Sprintf("%s has stopped", sessionName)
	default:
		message = fmt.Sprintf("%s: %s", sessionName, status)
	}

	sm.log.Info("sending notification", "session", sessionName, "status", status)

	// Terminal bell to daemon log (triggers terminal emulator notifications)
	fmt.Print("\a")

	if command != "" {
		go func() {
			cmd := exec.Command("sh", "-c", command)
			cmd.Env = append(os.Environ(),
				"GRAITH_SESSION_NAME="+sessionName,
				"GRAITH_STATUS="+status,
				"GRAITH_MESSAGE="+message,
			)
			if err := cmd.Run(); err != nil {
				sm.log.Error("custom notification command failed", "err", err)
			}
		}()
		return
	}

	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf(`display notification %q with title %q`, message, title)
		go func() {
			if err := exec.Command("osascript", "-e", script).Run(); err != nil {
				sm.log.Error("osascript notification failed", "err", err)
			}
		}()
	}
}
