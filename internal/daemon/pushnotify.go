package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// pushCoalesceWindow is the window within which an identical (title+message+
// priority) push notification is dropped as a duplicate, to coalesce rapid-fire
// events (e.g. a flapping check firing the same alert repeatedly).
const pushCoalesceWindow = 30 * time.Second

// pushNotification is a proactive user-facing notification requested via
// `gr notify` or a trigger's notify_on_complete.
type pushNotification struct {
	Title    string
	Message  string
	Priority string // low | normal | high
}

// pushGateInput is the immutable state a push-notification gating decision reads.
type pushGateInput struct {
	cfg            config.Notifications
	priority       string
	key            string // coalescing key (priority|title|message)
	now            time.Time
	log            []time.Time // rolling window of prior *delivered* timestamps
	lastKey        string
	lastAt         time.Time
	coalesceWindow time.Duration
}

// pushGateResult is the outcome of a gating decision. When deliver is true, log
// is the pruned+appended rolling window to persist.
type pushGateResult struct {
	deliver bool
	reason  string
	log     []time.Time
}

// evaluatePushGate is the pure gating decision for a push notification. It is
// side-effect free so it can be unit-tested with an injected clock and state.
//
// High-priority notifications bypass quiet hours and the rate limit (they are
// meant to always reach the user), but identical duplicates are still coalesced
// regardless of priority so nobody gets the same alert twice within the window.
func evaluatePushGate(in pushGateInput) pushGateResult {
	if !in.cfg.Enabled {
		return pushGateResult{deliver: false, reason: "notifications are disabled ([notifications] enabled = false)"}
	}

	high := in.priority == config.NotifyPriorityHigh

	// Coalesce identical rapid-fire notifications (all priorities).
	if in.key == in.lastKey && !in.lastAt.IsZero() && in.now.Sub(in.lastAt) < in.coalesceWindow {
		return pushGateResult{deliver: false, reason: "coalesced (identical notification within the last 30s)"}
	}

	if !high {
		if in.cfg.InQuietHours(in.now) {
			return pushGateResult{
				deliver: false,
				reason:  fmt.Sprintf("suppressed by quiet hours (%s–%s); use --priority high to override", in.cfg.QuietHoursStart, in.cfg.QuietHoursEnd),
			}
		}

		// Rate limit: prune the rolling window to the last hour, then compare.
		cutoff := in.now.Add(-time.Hour)
		recent := make([]time.Time, 0, len(in.log))

		for _, ts := range in.log {
			if ts.After(cutoff) {
				recent = append(recent, ts)
			}
		}

		if limit := in.cfg.MaxPerHourValue(); len(recent) >= limit {
			return pushGateResult{
				deliver: false,
				reason:  fmt.Sprintf("rate limited (%d/hour reached); use --priority high to override", limit),
			}
		}

		return pushGateResult{deliver: true, log: append(recent, in.now)}
	}

	// High priority: bypass quiet hours + rate limit but still record the send so
	// it counts toward the window (a later low/normal send sees it).
	cutoff := in.now.Add(-time.Hour)
	recent := make([]time.Time, 0, len(in.log))

	for _, ts := range in.log {
		if ts.After(cutoff) {
			recent = append(recent, ts)
		}
	}

	return pushGateResult{deliver: true, log: append(recent, in.now)}
}

// SendPushNotification applies gating (enabled, coalescing, quiet hours, rate
// limit) then dispatches to the configured backend. It returns whether the
// notification was delivered and a human-readable reason when suppressed. A
// backend dispatch error is logged and reported (delivered=false, reason=err)
// but is not returned as a Go error — callers treat suppression and failure the
// same way (the notification did not reach the user).
func (sm *SessionManager) SendPushNotification(n pushNotification) (bool, string) {
	priority, ok := config.NormalizeNotifyPriority(n.Priority)
	if !ok {
		return false, fmt.Sprintf("invalid priority %q (want low|normal|high)", n.Priority)
	}

	title := strings.TrimSpace(n.Title)
	if title == "" {
		title = "graith"
	}

	message := strings.TrimSpace(n.Message)
	if message == "" {
		return false, "message is required"
	}

	notifCfg := sm.Config().Notifications
	key := priority + "|" + title + "|" + message
	now := time.Now()

	sm.pushMu.Lock()

	res := evaluatePushGate(pushGateInput{
		cfg:            notifCfg,
		priority:       priority,
		key:            key,
		now:            now,
		log:            sm.pushLog,
		lastKey:        sm.lastPushKey,
		lastAt:         sm.lastPushAt,
		coalesceWindow: pushCoalesceWindow,
	})

	if !res.deliver {
		sm.pushMu.Unlock()
		sm.log.Info("push notification suppressed", "reason", res.reason, "priority", priority)

		return false, res.reason
	}

	sm.pushLog = res.log
	sm.lastPushKey = key
	sm.lastPushAt = now
	dispatch := sm.pushDispatch
	sm.pushMu.Unlock()

	if dispatch == nil {
		dispatch = defaultPushDispatch
	}

	backend := notifCfg.NotifyBackendName()

	sm.log.Info("sending push notification", "backend", backend, "priority", priority, "title", title)

	if err := dispatch(backend, title, message, priority); err != nil {
		sm.log.Error("push notification dispatch failed", "backend", backend, "err", err)
		return false, fmt.Sprintf("backend %q dispatch failed: %v", backend, err)
	}

	return true, ""
}

// defaultPushDispatch delivers a notification via the named backend. The
// "command" backend runs [notifications] command — but that value is not
// available here, so it is resolved by dispatchCommandBackend at call sites that
// hold the config. This indirection keeps evaluatePushGate pure and testable;
// production wiring sets sm.pushDispatch in Run.
func defaultPushDispatch(backend, title, message, priority string) error {
	switch backend {
	case "macos":
		return dispatchMacNotification(title, message, priority)
	default:
		// "command" needs the configured command string; it is dispatched via the
		// closure installed on sm.pushDispatch (see newPushDispatch). Reaching here
		// means no closure was installed for a non-macos backend.
		return fmt.Errorf("push backend %q not available", backend)
	}
}

// newPushDispatch returns the production dispatch closure for a SessionManager,
// resolving the "command" backend against the live config each call.
func (sm *SessionManager) newPushDispatch() func(backend, title, message, priority string) error {
	return func(backend, title, message, priority string) error {
		switch backend {
		case "macos":
			return dispatchMacNotification(title, message, priority)
		case "command":
			cmdStr := strings.TrimSpace(sm.Config().Notifications.Command)
			if cmdStr == "" {
				return fmt.Errorf("backend=\"command\" but no command configured")
			}

			return dispatchCommandBackend(cmdStr, title, message, priority)
		default:
			return fmt.Errorf("push backend %q not available", backend)
		}
	}
}

// dispatchMacNotification shows a macOS desktop notification via osascript.
// High-priority notifications play a sound. On non-darwin hosts it is a no-op
// error so the caller reports the backend as unavailable rather than silently
// succeeding.
func dispatchMacNotification(title, message, priority string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("macos backend requires macOS (running on %s)", runtime.GOOS)
	}

	script := fmt.Sprintf("display notification %s with title %s", osaQuote(message), osaQuote(title))
	if priority == config.NotifyPriorityHigh {
		script += " sound name " + osaQuote("Ping")
	}

	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		return fmt.Errorf("osascript: %w", err)
	}

	return nil
}

// osaQuote renders a Go string as an AppleScript string literal: wrap in double
// quotes and backslash-escape backslashes and embedded quotes. This prevents a
// message containing a quote from breaking out of the string (or injecting
// further AppleScript).
func osaQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")

	return "\"" + s + "\""
}

// dispatchCommandBackend runs the configured shell command with GRAITH_NOTIFY_*
// env vars, mirroring the status-change custom-command escape hatch.
func dispatchCommandBackend(command, title, message, priority string) error {
	cmd := exec.Command("sh", "-c", command)

	cmd.Env = append(os.Environ(),
		"GRAITH_NOTIFY_TITLE="+title,
		"GRAITH_NOTIFY_MESSAGE="+message,
		"GRAITH_NOTIFY_PRIORITY="+priority,
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command backend: %w", err)
	}

	return nil
}
