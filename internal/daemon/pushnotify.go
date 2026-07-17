package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/tools"
)

// pushNotification is a proactive user-facing notification requested via
// `gr notify` or a trigger's notify_on_complete.
type pushNotification struct {
	Title    string
	Message  string
	Priority string // low | normal | high
}

// pushGateInput is the immutable state a push-notification gating decision reads.
// recent is the rolling window ALREADY pruned to the last hour, and coalesceAt
// is the last delivered time for this notification's key (zero if none). The
// gate is a pure decision; the caller does the pruning and commits state.
type pushGateInput struct {
	cfg            config.Notifications
	priority       string
	now            time.Time
	recent         []time.Time // pruned rolling window of prior delivered timestamps
	coalesceAt     time.Time   // last delivered time for this key (zero if none)
	coalesceWindow time.Duration
}

// pushGateResult is the outcome of a gating decision.
type pushGateResult struct {
	deliver bool
	reason  string
}

// pruneWindow returns the timestamps in log that fall within the last hour of
// now (a timestamp exactly one hour old is expired). Used for the rate-limit
// rolling window.
func pruneWindow(log []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Hour)
	out := make([]time.Time, 0, len(log))

	for _, ts := range log {
		if ts.After(cutoff) {
			out = append(out, ts)
		}
	}

	return out
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

	// Coalesce identical rapid-fire notifications (all priorities), keyed per
	// notification so interleaved A/B/A duplicates each coalesce against their
	// own last send.
	if in.coalesceWindow > 0 && !in.coalesceAt.IsZero() && in.now.Sub(in.coalesceAt) < in.coalesceWindow {
		return pushGateResult{
			deliver: false,
			reason:  fmt.Sprintf("coalesced (identical notification within the last %s)", in.coalesceWindow),
		}
	}

	if in.priority == config.NotifyPriorityHigh {
		// High priority bypasses quiet hours + the rate limit.
		return pushGateResult{deliver: true}
	}

	if in.cfg.InQuietHours(in.now) {
		return pushGateResult{
			deliver: false,
			reason:  fmt.Sprintf("suppressed by quiet hours (%s–%s); use --priority high to override", in.cfg.QuietHoursStart, in.cfg.QuietHoursEnd),
		}
	}

	if limit := in.cfg.MaxPerHourValue(); len(in.recent) >= limit {
		return pushGateResult{
			deliver: false,
			reason:  fmt.Sprintf("rate limited (%d/hour reached); use --priority high to override", limit),
		}
	}

	return pushGateResult{deliver: true}
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
	coalesceWindow := notifCfg.Timing.CoalesceWindowDuration()
	key := priority + "|" + title + "|" + message
	now := time.Now()

	// Evaluate the gate AND reserve the delivery slot under one lock hold, so a
	// burst of concurrent sends can't all read the same under-limit window and
	// blow past the cap (the gate check and the commit must be atomic). The
	// reservation is rolled back below if the backend dispatch fails.
	sm.pushMu.Lock()

	sm.pushLog = pruneWindow(sm.pushLog, now)

	if sm.pushCoalesce == nil {
		sm.pushCoalesce = make(map[string]time.Time)
	}

	prunePushCoalesce(sm.pushCoalesce, now, coalesceWindow)

	res := evaluatePushGate(pushGateInput{
		cfg:            notifCfg,
		priority:       priority,
		now:            now,
		recent:         sm.pushLog,
		coalesceAt:     sm.pushCoalesce[key],
		coalesceWindow: coalesceWindow,
	})

	if !res.deliver {
		sm.pushMu.Unlock()
		sm.log.Info("push notification suppressed", "reason", res.reason, "priority", priority)

		return false, res.reason
	}

	// Reserve: record the send now so concurrent gate checks see it.
	sm.pushLog = append(sm.pushLog, now)
	prevCoalesce, hadCoalesce := sm.pushCoalesce[key]
	sm.pushCoalesce[key] = now
	dispatch := sm.pushDispatch
	sm.pushMu.Unlock()

	if dispatch == nil {
		dispatch = defaultPushDispatch
	}

	backend := notifCfg.NotifyBackendName()

	sm.log.Info("sending push notification", "backend", backend, "priority", priority, "title", title)

	if err := dispatch(backend, title, message, priority); err != nil {
		// Roll back the reservation: a failed send must not consume rate-limit
		// budget or start a coalescing window (that would suppress the retry of a
		// notification the user never actually got).
		sm.pushMu.Lock()
		sm.pushLog = removeOneTimestamp(sm.pushLog, now)

		if cur, ok := sm.pushCoalesce[key]; ok && cur.Equal(now) {
			if hadCoalesce {
				sm.pushCoalesce[key] = prevCoalesce
			} else {
				delete(sm.pushCoalesce, key)
			}
		}
		sm.pushMu.Unlock()

		sm.log.Error("push notification dispatch failed", "backend", backend, "err", err)

		return false, fmt.Sprintf("backend %q dispatch failed: %v", backend, err)
	}

	return true, ""
}

// prunePushCoalesce drops per-key coalescing entries older than the window so
// the map can't grow without bound.
func prunePushCoalesce(m map[string]time.Time, now time.Time, window time.Duration) {
	for k, t := range m {
		if now.Sub(t) >= window {
			delete(m, k)
		}
	}
}

// removeOneTimestamp returns log with a single occurrence of t removed (used to
// roll back a reserved rate-limit slot when dispatch fails). If t isn't present
// the slice is returned unchanged.
func removeOneTimestamp(log []time.Time, t time.Time) []time.Time {
	for i, ts := range log {
		if ts.Equal(t) {
			return append(log[:i], log[i+1:]...)
		}
	}

	return log
}

// defaultPushDispatch delivers a notification via the named backend. The
// "command" backend runs [notifications] command — but that value is not
// available here, so it is resolved by dispatchCommandBackend at call sites that
// hold the config. This indirection keeps evaluatePushGate pure and testable;
// production wiring sets sm.pushDispatch in Run.
//
// It is only reached when no dispatch closure is installed (never in production,
// where Run sets sm.pushDispatch to newPushDispatch), so it uses the default
// dispatch timeout rather than the configured one.
func defaultPushDispatch(backend, title, message, priority string) error {
	switch backend {
	case "macos":
		return dispatchMacNotification(title, message, priority, config.NotifyDispatchTimeoutDefault)
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
		notifCfg := sm.Config().Notifications
		timeout := notifCfg.Timing.DispatchTimeoutDuration()

		switch backend {
		case "macos":
			return dispatchMacNotification(title, message, priority, timeout)
		case "command":
			cmdStr := strings.TrimSpace(notifCfg.Command)
			if cmdStr == "" {
				return errors.New("backend=\"command\" but no command configured")
			}

			return dispatchCommandBackend(cmdStr, title, message, priority, timeout)
		default:
			return fmt.Errorf("push backend %q not available", backend)
		}
	}
}

// dispatchMacNotification shows a macOS desktop notification. On non-darwin
// hosts it is a no-op error so the caller reports the backend as unavailable
// rather than silently succeeding.
//
// It prefers the bundled graith-notifier .app, which posts via
// UNUserNotificationCenter under graith's own bundle identifier so
// notifications appear (and can be configured) under "Graith" in System
// Settings > Notifications. If the helper isn't installed — or is installed but
// fails — it falls back to osascript, whose notifications show up under "Script
// Editor" but still reach the user (graceful degradation, issue #1094).
func dispatchMacNotification(title, message, priority string, timeout time.Duration) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("macos backend requires macOS (running on %s)", runtime.GOOS)
	}

	if exe, ok := findMacNotifierApp(); ok {
		err := dispatchViaNotifierApp(exe, title, message, priority, timeout)
		if err == nil {
			return nil
		}

		if errors.Is(err, errNotifierPermissionDenied) {
			// The user has explicitly disabled notifications for Graith. Honour
			// that — do NOT fall back to osascript, which would show the same
			// notification under "Script Editor" and defeat the per-app control
			// this feature exists to provide (issue #1094 review).
			return nil
		}
		// Helper present but failed for another reason (not installed correctly,
		// launch error, delivery error); fall through to osascript so the
		// notification still reaches the user.
	}

	return dispatchViaOsascript(title, message, priority, timeout)
}

// macNotifierExecutable is the path, relative to the .app bundle root, of the
// notifier executable built by macos/notifier/build.sh.
const macNotifierExecutable = "Contents/MacOS/graith-notifier"

// notifierDeniedExitCode is the exit status the helper uses to signal that the
// user has explicitly disabled notifications for Graith. It must match
// exitDenied in macos/notifier/main.swift.
const notifierDeniedExitCode = 3

// errNotifierPermissionDenied is returned by dispatchViaNotifierApp when the
// helper reports the user has turned off Graith's notifications. The caller
// suppresses the osascript fallback in that case.
var errNotifierPermissionDenied = errors.New("notifications disabled for Graith by the user")

// findMacNotifierApp locates the bundled macOS notification helper and returns
// the path to its inner executable. Search order: an explicit override
// (GRAITH_NOTIFIER_APP), next to the running binary, common Homebrew/libexec
// layouts, then the standard /Applications directories. A candidate may be
// either a GraithNotifier.app bundle or the inner executable directly.
func findMacNotifierApp() (string, bool) {
	var candidates []string

	if override := strings.TrimSpace(os.Getenv("GRAITH_NOTIFIER_APP")); override != "" {
		candidates = append(candidates, override)
	}

	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, notifierCandidatesForExe(exe)...)
	}

	candidates = append(candidates, "/Applications/GraithNotifier.app")

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Applications", "GraithNotifier.app"))
	}

	for _, c := range candidates {
		if exe, ok := resolveNotifierExecutable(c); ok {
			return exe, true
		}
	}

	return "", false
}

// notifierCandidatesForExe returns the bundle locations to probe relative to the
// gr executable at exe. It searches relative to both the executable's directory
// and its symlink-resolved directory: Homebrew installs gr as a symlink in
// <prefix>/bin pointing into the Cellar and drops GraithNotifier.app under the
// Cellar's libexec/graith, which is only reachable via the resolved path
// (<prefix>/libexec isn't symlinked, issue #1101).
func notifierCandidatesForExe(exe string) []string {
	var candidates []string

	seen := make(map[string]bool)

	for _, p := range []string{exe, resolveSymlink(exe)} {
		dir := filepath.Dir(p)
		if dir == "" || seen[dir] {
			continue
		}

		seen[dir] = true

		candidates = append(candidates,
			filepath.Join(dir, "GraithNotifier.app"),
			filepath.Join(dir, "..", "libexec", "graith", "GraithNotifier.app"),
			filepath.Join(dir, "..", "share", "graith", "GraithNotifier.app"),
		)
	}

	return candidates
}

// resolveSymlink returns path with symlinks resolved, or path unchanged if it
// can't be resolved (e.g. the path doesn't exist).
func resolveSymlink(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return path
}

// resolveNotifierExecutable maps a candidate path to a runnable notifier
// executable. A ".app" path is resolved to its inner executable; any other path
// is treated as the executable itself. It reports ok=false unless the resolved
// path exists, is a regular file, and is executable — so a non-executable stale
// candidate is skipped rather than selected (letting findMacNotifierApp fall
// through to a valid later candidate, issue #1094 review). The path is cleaned
// first so a trailing-slash override (e.g. ".../GraithNotifier.app/") still
// resolves as a bundle.
func resolveNotifierExecutable(path string) (string, bool) {
	exe := filepath.Clean(path)
	if strings.HasSuffix(exe, ".app") {
		exe = filepath.Join(exe, macNotifierExecutable)
	}

	if info, err := os.Stat(exe); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return exe, true
	}

	return "", false
}

// dispatchViaNotifierApp runs the notifier helper with the notification
// content. The helper exits non-zero on any failure so the caller can fall
// back; the dedicated notifierDeniedExitCode is mapped to
// errNotifierPermissionDenied so the caller can suppress the fallback for an
// explicit user opt-out.
func dispatchViaNotifierApp(exe, title, message, priority string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := exec.CommandContext(ctx, exe, title, message, priority).Run()
	if err == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == notifierDeniedExitCode {
		return errNotifierPermissionDenied
	}

	return fmt.Errorf("notifier app: %w", err)
}

// dispatchViaOsascript shows a notification via osascript. High-priority
// notifications play a sound. This is the fallback when the bundled helper
// isn't available.
func dispatchViaOsascript(title, message, priority string, timeout time.Duration) error {
	script := fmt.Sprintf("display notification %s with title %s", osaQuote(message), osaQuote(title))
	if priority == config.NotifyPriorityHigh {
		script += " sound name " + osaQuote("Ping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := exec.CommandContext(ctx, tools.OSAScript(), "-e", script).Run(); err != nil {
		return fmt.Errorf("osascript: %w", err)
	}

	return nil
}

// osaQuote renders a Go string as an AppleScript string literal: wrap in double
// quotes and backslash-escape backslashes and embedded quotes. This prevents a
// message containing a quote from breaking out of the string (or injecting
// further AppleScript). Raw newlines/tabs/carriage returns are folded to spaces
// first — an AppleScript string literal in a one-line `-e` script can't span
// them, so a multi-line body would otherwise fail to compile.
func osaQuote(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")

	return "\"" + s + "\""
}

// dispatchCommandBackend runs the configured shell command with GRAITH_NOTIFY_*
// env vars, mirroring the status-change custom-command escape hatch. It is
// bounded by the configured dispatch timeout so a hung command can't wedge the
// caller.
func dispatchCommandBackend(command, title, message, priority string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, tools.Shell(), "-c", command)

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
