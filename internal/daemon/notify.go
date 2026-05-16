package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

	if !sm.cfg.Notifications.Enabled {
		return
	}

	switch newStatus {
	case "approval":
		if !sm.cfg.Notifications.OnApproval {
			return
		}
	case "stopped":
		if !sm.cfg.Notifications.OnStopped {
			return
		}
	default:
		return
	}

	sm.sendNotification(sessionName, newStatus)
}

func (sm *SessionManager) sendNotification(sessionName, status string) {
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

	command := sm.cfg.Notifications.Command
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
