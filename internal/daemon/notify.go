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

// daemonSenderID / daemonSenderName identify messages the daemon authors
// itself (e.g. PR/CI notices), so an agent sees "New message from graith".
const (
	daemonSenderID   = "graith"
	daemonSenderName = "graith"
)

// notifyFromDaemon publishes a daemon-authored message into a session's inbox
// and then explicitly triggers notifyInbox (which auto-resumes a stopped
// session). A bare MsgStore.Publish does NOT notify — only the handler send-path
// and this helper wire Publish→notifyInbox. The full detail lives in the body;
// the agent reads it via `gr msg inbox --all --ack`.
func (sm *SessionManager) notifyFromDaemon(sessionID, body string) {
	if sm.messages == nil {
		return
	}

	if _, err := sm.messages.Publish("inbox:"+sessionID, daemonSenderID, daemonSenderName, body, "", ""); err != nil {
		sm.log.Error("failed to publish daemon notification", "session", sessionID, "err", err)
		return
	}
	go sm.notifyInbox(sessionID, daemonSenderID, daemonSenderName)
}

// notifyInbox injects a notification into the target session's PTY when a
// message is published to its inbox. If the session is stopped, it is
// auto-resumed first — the resume flow's notifyUnreadInbox handles the
// notification in that case. This runs daemon-side so it bypasses the
// per-session auth check on the "type" command.
func (sm *SessionManager) notifyInbox(targetID, senderID, senderName string) {
	ptySess, ok := sm.GetPTY(targetID)
	if !ok {
		sm.resumeForInbox(targetID, senderID, senderName)
		return
	}

	sender := senderName
	if sender == "" {
		sender = senderID
	}

	hint := fmt.Sprintf("New message from %s. Read: gr msg inbox --all --ack | Reply: gr msg send %s \"<reply>\"", sender, sender)

	if sm.HasAttachedClient(targetID) {
		ptySess.WaitForUserIdle(10*time.Second, 2*time.Minute)
	}

	if err := ptySess.WriteInputAndSubmit([]byte(hint)); err != nil {
		sm.log.Debug("inbox notification write failed", "target", targetID, "err", err)
	}

	ptySess.Poke()
}

// resumeForInbox auto-resumes a stopped session when an inbox message arrives.
// The resume flow calls notifyUnreadInbox which handles the PTY notification.
func (sm *SessionManager) resumeForInbox(targetID, senderID, senderName string) {
	sess, ok := sm.Get(targetID)
	if !ok || sess.Status != StatusStopped {
		return
	}

	sender := senderName
	if sender == "" {
		sender = senderID
	}

	summary := fmt.Sprintf("Resumed by inbox message from %s", sender)
	sm.log.Info("auto-resuming stopped session on inbox message",
		"session", sess.Name, "id", targetID, "sender", sender)

	if _, err := sm.resumeWithSummary(targetID, 24, 80, summary); err != nil {
		sm.log.Error("failed to auto-resume session for inbox",
			"session", targetID, "err", err)
	}
}

// notifyUnreadInbox checks for unread inbox messages and injects a PTY
// notification if any exist. Always call as a goroutine — the idle-wait
// can block for up to 2 minutes.
func (sm *SessionManager) notifyUnreadInbox(sessionID string) {
	if sm.messages == nil {
		return
	}

	sm.mu.Lock()

	last, ok := sm.lastInboxNotifyAt[sessionID]
	if ok && time.Since(last) < 30*time.Second {
		sm.mu.Unlock()
		return
	}

	sm.lastInboxNotifyAt[sessionID] = time.Now()
	sm.mu.Unlock()

	ptySess, ok := sm.GetPTY(sessionID)
	if !ok {
		return
	}

	if sm.HasAttachedClient(sessionID) {
		ptySess.WaitForUserIdle(10*time.Second, 2*time.Minute)
	} else {
		time.Sleep(5 * time.Second)
	}

	count := sm.messages.TotalUnread(sessionID)
	if count == 0 {
		return
	}

	hint := fmt.Sprintf("You have %d unread inbox message(s). Read: gr msg inbox --all --ack", count)
	if err := ptySess.WriteInputAndSubmit([]byte(hint)); err != nil {
		sm.log.Debug("unread inbox notification write failed", "session", sessionID, "err", err)
	}

	ptySess.Poke()
}

// InterruptSession delivers an interrupt (Ctrl-C) to a session's live PTY using
// the agent's configured interrupt count and delay. Different agent CLIs need
// different sequences — e.g. Claude's TUI ignores a single Ctrl-C and needs two
// rapid presses — so delivery is agent-aware rather than a single 0x03 (issue
// #620). Returns an error if the session has no live PTY.
func (sm *SessionManager) InterruptSession(sessionID string) error {
	ptySess, ok := sm.GetPTY(sessionID)
	if !ok {
		return fmt.Errorf("session not found")
	}

	sess, _ := sm.Get(sessionID)

	sm.mu.RLock()
	agentCfg := sm.cfg.Agents[sess.Agent]
	sm.mu.RUnlock()

	count := agentCfg.InterruptCountValue()
	delay := agentCfg.InterruptDelay()

	sm.log.Info("interrupting session",
		"session", sessionID, "agent", sess.Agent, "count", count, "delay", delay)

	if err := ptySess.Interrupt(count, delay); err != nil {
		return err
	}

	ptySess.Poke()

	return nil
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
