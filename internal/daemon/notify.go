package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/d0ugal/graith/internal/tools"
)

func (sm *SessionManager) onAgentStatusChange(sessionID, sessionName, oldStatus, newStatus string) {
	sm.log.Info("agent status changed",
		"session", sessionName, "id", sessionID,
		"from", oldStatus, "to", newStatus)

	if sm.messages != nil {
		body := fmt.Sprintf("Session %q status changed: %s → %s", sessionName, oldStatus, newStatus)

		_, err := sm.messages.Publish(PublishOpts{Stream: "_system.status", SenderID: sessionID, SenderName: sessionName, Body: body})
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

// systemSenderID / systemSenderName identify messages the daemon authors
// itself (e.g. PR/CI notices). The ID uses a "graith:" prefix — mirroring the
// synthetic "device:" sender IDs — so it can never collide with a real session
// ID (those are random hex) and is recognisable as an automated source. The
// display name makes it obvious the message is an automated notification, not
// an LLM/session that can be replied to. See issue #887.
const (
	systemSenderID   = "graith:system"
	systemSenderName = "graith notifications"
)

// isSystemSender reports whether a sender ID identifies an automated,
// non-replyable graith notification (rather than a session or human). Display
// and notification code uses this to mark such messages as system-sourced and
// to avoid suggesting a reply path to a session that does not exist.
func isSystemSender(senderID string) bool {
	return senderID == systemSenderID
}

// notifyFromDaemon publishes a daemon-authored message into a session's inbox
// and then explicitly triggers notifyInbox (which auto-resumes a stopped
// session). A bare MsgStore.Publish does NOT notify — only the handler send-path
// and this helper wire Publish→notifyInbox. The full detail lives in the body;
// the agent reads it via `gr msg inbox --ack`.
//
// It returns an error when the message could not be published (no message store
// wired, or the store's Publish failed), so callers that must not treat an
// undelivered notification as delivered — e.g. the once-per-author trust prompt,
// which would otherwise mark an author surfaced and never retry — can react.
// Fire-and-forget callers may ignore the return.
func (sm *SessionManager) notifyFromDaemon(sessionID, body string) error {
	if sm.messages == nil {
		return fmt.Errorf("no message store to publish notification to session %q", sessionID)
	}

	if _, err := sm.messages.Publish(PublishOpts{Stream: "inbox:" + sessionID, SenderID: systemSenderID, SenderName: systemSenderName, Body: body}); err != nil {
		sm.log.Error("failed to publish daemon notification", "session", sessionID, "err", err)
		return err
	}
	go sm.notifyInbox(sessionID, systemSenderID, systemSenderName, false)

	return nil
}

// notifyInbox injects a notification into the target session's PTY when a
// message is published to its inbox. If the session is stopped, it is
// auto-resumed first — the resume flow's notifyUnreadInbox handles the
// notification in that case. This runs daemon-side so it bypasses the
// per-session auth check on the "type" command.
func (sm *SessionManager) notifyInbox(targetID, senderID, senderName string, noReply bool) {
	ptySess, ok := sm.GetPTY(targetID)
	if !ok {
		sm.resumeForInbox(targetID, senderID, senderName, noReply)
		return
	}

	hint := inboxNotificationHint(senderID, senderName, noReply)

	if sm.HasAttachedClient(targetID) {
		timing := sm.Config().Notifications.Timing
		ptySess.WaitForUserIdle(timing.InboxIdleTimeoutDuration(), timing.InboxMaxWaitDuration())
	}

	if err := ptySess.WriteInputAndSubmit([]byte(hint)); err != nil {
		sm.log.Debug("inbox notification write failed", "target", targetID, "err", err)
	}

	ptySess.Poke()
}

// inboxNotificationHint renders the immediate PTY hint for one inbox message.
// System identity and explicit reply expectation are independent reasons to
// omit the reply command: the former has no addressable sender, while the latter
// records an ordinary sender's intent.
func inboxNotificationHint(senderID, senderName string, noReply bool) string {
	sender := senderName
	if sender == "" {
		sender = senderID
	}

	if isSystemSender(senderID) {
		return "System notice. Read: gr msg inbox --ack"
	}

	if noReply {
		return fmt.Sprintf("New message from %s. Read: gr msg inbox --ack | No reply expected", sender)
	}

	return fmt.Sprintf("New message from %s. Read: gr msg inbox --ack | Reply: gr msg send %s \"<reply>\"", sender, sender)
}

// resumeForInbox auto-resumes a stopped session when an inbox message arrives.
// The resume flow calls notifyUnreadInbox which handles the PTY notification.
func (sm *SessionManager) resumeForInbox(targetID, senderID, senderName string, noReply bool) {
	sess, ok := sm.Get(targetID)
	if !ok || sess.Status != StatusStopped {
		return
	}

	sender := senderName
	if sender == "" {
		sender = senderID
	}

	summary := "Resumed by inbox message from " + sender
	if isSystemSender(senderID) {
		summary = "Resumed by automated notification from " + sender
	} else if noReply {
		summary += " (no reply expected)"
	}

	sm.log.Info("auto-resuming stopped session on inbox message",
		"session", sess.Name, "id", targetID, "sender", sender)

	lc := sm.Config().Lifecycle
	if _, err := sm.resumeWithSummary(targetID, lc.DefaultRowsOrDefault(), lc.DefaultColsOrDefault(), summary); err != nil {
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

	timing := sm.Config().Notifications.Timing

	sm.mu.Lock()

	last, ok := sm.lastInboxNotifyAt[sessionID]
	if ok && time.Since(last) < timing.InboxCooldownDuration() {
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
		ptySess.WaitForUserIdle(timing.InboxIdleTimeoutDuration(), timing.InboxMaxWaitDuration())
	} else {
		time.Sleep(timing.InboxDetachedDelayDuration())
	}

	count := sm.messages.TotalUnread(sessionID)
	if count == 0 {
		return
	}

	hint := fmt.Sprintf("You have %d unread inbox message(s). Read: gr msg inbox --ack", count)
	if count == 1 {
		messages, err := sm.messages.Read("inbox:"+sessionID, sessionID, true, "")
		if err == nil && len(messages) == 1 {
			m := messages[0]
			hint = inboxNotificationHint(m.SenderID, m.SenderName, m.NoReply)
		}
	}

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
		return errors.New("session has no live process to interrupt")
	}

	// A session deleted between GetPTY and here yields a zero-value state whose
	// Agent is "", which resolves to a zero Agent config → the single-press
	// default. That degrades gracefully rather than erroring, so the found flag
	// is intentionally ignored.
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
		message = sessionName + " needs approval"
	case "stopped":
		message = sessionName + " has stopped"
	default:
		message = fmt.Sprintf("%s: %s", sessionName, status)
	}

	sm.log.Info("sending notification", "session", sessionName, "status", status)

	// Terminal bell to daemon log (triggers terminal emulator notifications)
	fmt.Print("\a")

	if command != "" {
		go func() {
			cmd := exec.Command(tools.Shell(), "-c", command)

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
			if err := exec.Command(tools.OSAScript(), "-e", script).Run(); err != nil {
				sm.log.Error("osascript notification failed", "err", err)
			}
		}()
	}
}
