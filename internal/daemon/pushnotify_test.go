package daemon

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseNotifications() config.Notifications {
	return config.Notifications{Enabled: true}
}

func TestEvaluatePushGate_Disabled(t *testing.T) {
	res := evaluatePushGate(pushGateInput{
		cfg:      config.Notifications{Enabled: false},
		priority: config.NotifyPriorityNormal,
		now:      time.Now(),
	})
	if res.deliver {
		t.Fatal("disabled notifications should not deliver")
	}
}

func TestEvaluatePushGate_Coalesces(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	in := pushGateInput{
		cfg:            baseNotifications(),
		priority:       config.NotifyPriorityNormal,
		now:            now,
		coalesceAt:     now.Add(-5 * time.Second),
		coalesceWindow: config.NotifyCoalesceWindowDefault,
	}
	if res := evaluatePushGate(in); res.deliver {
		t.Fatal("identical notification within window should be coalesced")
	}

	// Outside the window it delivers.
	in.coalesceAt = now.Add(-2 * config.NotifyCoalesceWindowDefault)
	if res := evaluatePushGate(in); !res.deliver {
		t.Fatal("identical notification outside window should deliver")
	}

	// No prior send for this key delivers.
	in.coalesceAt = time.Time{}
	if res := evaluatePushGate(in); !res.deliver {
		t.Fatal("first send for a key should deliver")
	}
}

func TestEvaluatePushGate_CoalesceReasonUsesEffectiveWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	res := evaluatePushGate(pushGateInput{
		cfg:            baseNotifications(),
		priority:       config.NotifyPriorityNormal,
		now:            now,
		coalesceAt:     now.Add(-5 * time.Second),
		coalesceWindow: 45 * time.Second,
	})

	if res.deliver {
		t.Fatal("identical notification within window should be coalesced")
	}

	if want := "coalesced (identical notification within the last 45s)"; res.reason != want {
		t.Fatalf("reason = %q, want %q", res.reason, want)
	}

	res = evaluatePushGate(pushGateInput{
		cfg:            baseNotifications(),
		priority:       config.NotifyPriorityNormal,
		now:            now,
		coalesceAt:     now,
		coalesceWindow: 0,
	})
	if !res.deliver {
		t.Fatalf("zero coalescing window should be disabled, got %q", res.reason)
	}
}

func TestEvaluatePushGate_QuietHours(t *testing.T) {
	cfg := baseNotifications()
	cfg.QuietHoursStart = "22:00"
	cfg.QuietHoursEnd = "07:00"

	nightNormal := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityNormal,
		now: time.Date(2026, 7, 11, 23, 0, 0, 0, time.UTC), coalesceWindow: config.NotifyCoalesceWindowDefault,
	})
	if nightNormal.deliver {
		t.Fatal("normal notification in quiet hours should be suppressed")
	}

	// High priority bypasses quiet hours.
	nightHigh := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityHigh,
		now: time.Date(2026, 7, 11, 23, 0, 0, 0, time.UTC), coalesceWindow: config.NotifyCoalesceWindowDefault,
	})
	if !nightHigh.deliver {
		t.Fatal("high priority should bypass quiet hours")
	}
}

func TestEvaluatePushGate_RateLimit(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	cfg := baseNotifications()
	cfg.MaxPerHour = 2

	// Two recent deliveries already (pruned window); a normal one is rate limited.
	recent := []time.Time{now.Add(-10 * time.Minute), now.Add(-5 * time.Minute)}

	res := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityNormal, now: now, recent: recent, coalesceWindow: config.NotifyCoalesceWindowDefault,
	})
	if res.deliver {
		t.Fatal("normal notification over the hourly cap should be rate limited")
	}

	// High priority bypasses the cap.
	resHigh := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityHigh, now: now, recent: recent, coalesceWindow: config.NotifyCoalesceWindowDefault,
	})
	if !resHigh.deliver {
		t.Fatal("high priority should bypass the rate limit")
	}
}

func TestPruneWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	// One stale (>1h), one exactly 1h (expired), one fresh.
	log := []time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour), now.Add(-5 * time.Minute)}

	got := pruneWindow(log, now)
	if len(got) != 1 {
		t.Fatalf("expected only the fresh timestamp to survive, got len %d", len(got))
	}

	if !got[0].Equal(now.Add(-5 * time.Minute)) {
		t.Errorf("unexpected surviving timestamp %v", got[0])
	}
}

func TestRemoveOneTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	a, b := now.Add(-time.Minute), now
	log := []time.Time{a, b}

	got := removeOneTimestamp(log, b)
	if len(got) != 1 || !got[0].Equal(a) {
		t.Fatalf("expected b removed, got %v", got)
	}

	// Removing an absent timestamp is a no-op.
	if got := removeOneTimestamp([]time.Time{a}, b); len(got) != 1 {
		t.Fatalf("removing absent timestamp should be a no-op, got len %d", len(got))
	}
}

// fakeDispatch captures dispatch calls for SendPushNotification tests.
type fakeDispatch struct {
	mu    sync.Mutex
	calls []pushNotification
	err   error
}

func (f *fakeDispatch) fn(backend, title, message, priority string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, pushNotification{Title: title, Message: message, Priority: priority})

	return f.err
}

func newPushSM(n config.Notifications, d func(string, string, string, string) error) *SessionManager {
	return &SessionManager{
		cfg:          &config.Config{Notifications: n},
		log:          quietTestLogger(),
		pushDispatch: d,
	}
}

func TestSendPushNotification_Delivers(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	ok, reason := sm.SendPushNotification(pushNotification{Message: "briefing ready"})
	if !ok {
		t.Fatalf("expected delivery, got suppressed: %s", reason)
	}

	if len(fd.calls) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(fd.calls))
	}

	if fd.calls[0].Title != "graith" || fd.calls[0].Priority != config.NotifyPriorityNormal {
		t.Errorf("unexpected dispatch: %+v", fd.calls[0])
	}

	// Rolling window should now hold one entry.
	if len(sm.pushLog) != 1 {
		t.Errorf("expected pushLog len 1, got %d", len(sm.pushLog))
	}
}

func TestSendPushNotification_InvalidPriority(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "x", Priority: "screaming"}); ok {
		t.Fatal("invalid priority should not deliver")
	}

	if len(fd.calls) != 0 {
		t.Fatal("invalid priority should not dispatch")
	}
}

func TestSendPushNotification_EmptyMessage(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "   "}); ok {
		t.Fatal("empty message should not deliver")
	}
}

func TestSendPushNotification_DispatchError(t *testing.T) {
	fd := &fakeDispatch{err: errors.New("boom")}
	sm := newPushSM(baseNotifications(), fd.fn)

	ok, reason := sm.SendPushNotification(pushNotification{Message: "x"})
	if ok {
		t.Fatal("dispatch error should report not delivered")
	}

	if reason == "" {
		t.Fatal("expected a failure reason")
	}

	// A failed dispatch must not consume rate-limit budget or open a coalescing
	// window — the notification never reached the user, so a retry must be allowed.
	if len(sm.pushLog) != 0 {
		t.Errorf("failed dispatch should not record a rate-limit entry, got len %d", len(sm.pushLog))
	}

	if len(sm.pushCoalesce) != 0 {
		t.Errorf("failed dispatch should not leave a coalescing entry, got %d", len(sm.pushCoalesce))
	}

	// The retry (dispatch now succeeds) should go through.
	fd.err = nil

	if ok, reason := sm.SendPushNotification(pushNotification{Message: "x"}); !ok {
		t.Fatalf("retry after a failed dispatch should deliver, got suppressed: %s", reason)
	}
}

func TestSendPushNotification_Coalesced(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "same"}); !ok {
		t.Fatal("first send should deliver")
	}

	if ok, reason := sm.SendPushNotification(pushNotification{Message: "same"}); ok {
		t.Fatal("identical immediate resend should be coalesced")
	} else if reason == "" {
		t.Fatal("expected a coalescing reason")
	}

	if len(fd.calls) != 1 {
		t.Fatalf("expected exactly 1 dispatch after coalescing, got %d", len(fd.calls))
	}
}

// TestSendPushNotification_CoalesceWindowConfigurable is the regression guard
// for issue #1245: SendPushNotification must honour the configured
// [notifications.timing] coalesce_window rather than a fixed constant. With the
// window disabled ("0"), two identical immediate resends both deliver — under
// the old hard-coded 30s window the second would have been coalesced.
func TestSendPushNotification_CoalesceWindowConfigurable(t *testing.T) {
	fd := &fakeDispatch{}
	cfg := baseNotifications()
	cfg.Timing.CoalesceWindow = "0" // disable coalescing
	sm := newPushSM(cfg, fd.fn)

	if ok, reason := sm.SendPushNotification(pushNotification{Message: "same"}); !ok {
		t.Fatalf("first send should deliver, got suppressed: %s", reason)
	}

	if ok, reason := sm.SendPushNotification(pushNotification{Message: "same"}); !ok {
		t.Fatalf("identical resend should deliver with coalescing disabled, got suppressed: %s", reason)
	}

	if len(fd.calls) != 2 {
		t.Fatalf("expected 2 dispatches with coalescing disabled, got %d", len(fd.calls))
	}
}

func TestSendPushNotification_ConcurrentRespectsCap(t *testing.T) {
	fd := &fakeDispatch{}
	cfg := baseNotifications()
	cfg.MaxPerHour = 5
	sm := newPushSM(cfg, fd.fn)

	// Fire 20 distinct normal notifications concurrently. Distinct messages so
	// coalescing doesn't interfere — the rate-limit cap is what's under test.
	const n = 20

	var wg sync.WaitGroup

	delivered := make([]bool, n)

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			ok, _ := sm.SendPushNotification(pushNotification{Message: fmt.Sprintf("bairn-%d", i)})
			delivered[i] = ok
		}(i)
	}

	wg.Wait()

	count := 0

	for _, d := range delivered {
		if d {
			count++
		}
	}

	if count != 5 {
		t.Fatalf("expected exactly 5 deliveries under the cap, got %d", count)
	}

	if len(fd.calls) != 5 {
		t.Fatalf("expected exactly 5 dispatches, got %d", len(fd.calls))
	}

	if len(sm.pushLog) != 5 {
		t.Fatalf("expected pushLog to hold exactly 5 entries, got %d", len(sm.pushLog))
	}
}

func TestSendPushNotification_InterleavedCoalescing(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	// A, B, then A again immediately: the second A must coalesce against the
	// first A even though B was sent in between (per-key coalescing).
	if ok, _ := sm.SendPushNotification(pushNotification{Message: "alpha"}); !ok {
		t.Fatal("first A should deliver")
	}

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "beta"}); !ok {
		t.Fatal("B should deliver")
	}

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "alpha"}); ok {
		t.Fatal("second A should be coalesced against the first A, not delivered")
	}

	if len(fd.calls) != 2 {
		t.Fatalf("expected 2 dispatches (A, B), got %d", len(fd.calls))
	}
}

// writeExecutable creates a regular, executable file at path (with parent dirs)
// and returns path, for exercising notifier-app discovery.
func writeExecutable(t *testing.T, path string) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: test fixture
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // G306: notifier stub must be executable
		t.Fatalf("write executable: %v", err)
	}

	return path
}

func TestResolveNotifierExecutable(t *testing.T) {
	dir := t.TempDir()

	// A ".app" bundle resolves to its inner executable.
	appExe := writeExecutable(t, filepath.Join(dir, "GraithNotifier.app", macNotifierExecutable))

	if got, ok := resolveNotifierExecutable(filepath.Join(dir, "GraithNotifier.app")); !ok || got != appExe {
		t.Errorf("app bundle: got (%q, %v), want (%q, true)", got, ok, appExe)
	}

	// A plain executable path is used directly.
	bare := writeExecutable(t, filepath.Join(dir, "bothy", "graith-notifier"))

	if got, ok := resolveNotifierExecutable(bare); !ok || got != bare {
		t.Errorf("bare executable: got (%q, %v), want (%q, true)", got, ok, bare)
	}

	// A missing path is not resolved.
	if got, ok := resolveNotifierExecutable(filepath.Join(dir, "haar", "nope")); ok {
		t.Errorf("missing path should not resolve, got %q", got)
	}

	// A directory (not a .app, no inner executable) is not a runnable file.
	if got, ok := resolveNotifierExecutable(dir); ok {
		t.Errorf("directory should not resolve as an executable, got %q", got)
	}

	// A .app whose inner executable is absent does not resolve.
	emptyApp := filepath.Join(dir, "Dreich.app")
	if err := os.MkdirAll(emptyApp, 0o755); err != nil { //nolint:gosec // G301: test fixture
		t.Fatalf("mkdir: %v", err)
	}

	if _, ok := resolveNotifierExecutable(emptyApp); ok {
		t.Error("a .app without its inner executable should not resolve")
	}

	// A trailing-slash bundle path (e.g. from shell completion) still resolves
	// as a bundle, not a bare executable.
	if got, ok := resolveNotifierExecutable(filepath.Join(dir, "GraithNotifier.app") + "/"); !ok || got != appExe {
		t.Errorf("trailing-slash bundle: got (%q, %v), want (%q, true)", got, ok, appExe)
	}

	// A regular file without the executable bit is not runnable and must not
	// resolve — otherwise a stale non-executable candidate would shadow a valid
	// later one during discovery.
	nonExec := filepath.Join(dir, "scunner", "graith-notifier")
	if err := os.MkdirAll(filepath.Dir(nonExec), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(nonExec, []byte("not executable"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got, ok := resolveNotifierExecutable(nonExec); ok {
		t.Errorf("non-executable file should not resolve, got %q", got)
	}
}

// notifierStub writes a shell script that records its argv to argsFile and
// exits with the given code, then returns its path — a stand-in for the real
// notifier executable so dispatchViaNotifierApp can be tested without swiftc.
func notifierStub(t *testing.T, exitCode int, argsFile string) string {
	t.Helper()

	dir := t.TempDir()
	stub := filepath.Join(dir, "graith-notifier")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nexit %d\n", argsFile, exitCode)

	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable
		t.Fatalf("write stub: %v", err)
	}

	return stub
}

func TestDispatchViaNotifierApp_SuccessPassesArgs(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stub := notifierStub(t, 0, argsFile)

	if err := dispatchViaNotifierApp(stub, "Braw title", "hello bothy", "high", config.NotifyDispatchTimeoutDefault); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}

	want := "Braw title\nhello bothy\nhigh\n"
	if string(got) != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestDispatchViaNotifierApp_DeniedExitCode(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stub := notifierStub(t, notifierDeniedExitCode, argsFile)

	err := dispatchViaNotifierApp(stub, "t", "m", "normal", config.NotifyDispatchTimeoutDefault)
	if !errors.Is(err, errNotifierPermissionDenied) {
		t.Fatalf("exit %d should map to errNotifierPermissionDenied, got %v", notifierDeniedExitCode, err)
	}
}

func TestDispatchViaNotifierApp_GenericFailure(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stub := notifierStub(t, 1, argsFile)

	err := dispatchViaNotifierApp(stub, "t", "m", "normal", config.NotifyDispatchTimeoutDefault)
	if err == nil {
		t.Fatal("exit 1 should be an error")
	}

	if errors.Is(err, errNotifierPermissionDenied) {
		t.Fatal("a generic exit-1 failure must not be treated as permission-denied")
	}
}

func TestNotifierCandidatesForExe_HomebrewSymlink(t *testing.T) {
	// Simulate a Homebrew layout: gr lives in the Cellar and the .app is under
	// the Cellar's libexec/graith, but gr is invoked via a symlink in
	// <prefix>/bin (which is all os.Executable may report). Discovery must
	// resolve the symlink to find the bundle.
	// Resolve the temp root's own symlinks up front: on macOS t.TempDir() lives
	// under /var, which is itself a symlink to /private/var. Discovery resolves
	// symlinks (resolveSymlink), so the paths it returns are under /private/var;
	// comparing against unresolved /var paths would spuriously fail (the
	// /var→/private/var trap). Anchor every constructed path on the resolved root.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}

	cellarBin := filepath.Join(root, "Cellar", "graith", "1.0", "bin")
	realGr := writeExecutable(t, filepath.Join(cellarBin, "gr"))
	appExe := writeExecutable(t, filepath.Join(root, "Cellar", "graith", "1.0", "libexec", "graith", "GraithNotifier.app", macNotifierExecutable))

	optBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(optBin, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	symGr := filepath.Join(optBin, "gr")
	if err := os.Symlink(realGr, symGr); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Resolve candidates as if gr were launched via the <prefix>/bin symlink.
	var found string

	for _, c := range notifierCandidatesForExe(symGr) {
		if exe, ok := resolveNotifierExecutable(c); ok {
			found = exe
			break
		}
	}

	if found != appExe {
		t.Errorf("expected discovery via the resolved Cellar path to find %q, got %q", appExe, found)
	}
}

func TestFindMacNotifierApp_OverrideBundle(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "GraithNotifier.app")
	exe := writeExecutable(t, filepath.Join(app, macNotifierExecutable))

	t.Setenv("GRAITH_NOTIFIER_APP", app)

	got, ok := findMacNotifierApp()
	if !ok {
		t.Fatal("expected the override bundle to be found")
	}

	if got != exe {
		t.Errorf("got %q, want inner executable %q", got, exe)
	}
}

func TestFindMacNotifierApp_OverrideExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := writeExecutable(t, filepath.Join(dir, "graith-notifier"))

	t.Setenv("GRAITH_NOTIFIER_APP", exe)

	got, ok := findMacNotifierApp()
	if !ok || got != exe {
		t.Fatalf("got (%q, %v), want (%q, true)", got, ok, exe)
	}
}

func TestFindMacNotifierApp_OverrideMissingFallsThrough(t *testing.T) {
	// An override pointing at a nonexistent path must not resolve to it; the
	// search falls through to the remaining candidates (none of which exist in
	// the test's temp home), so discovery reports not-found deterministically.
	missing := filepath.Join(t.TempDir(), "GraithNotifier.app")
	t.Setenv("GRAITH_NOTIFIER_APP", missing)
	t.Setenv("HOME", t.TempDir())

	if got, ok := findMacNotifierApp(); ok {
		// A real GraithNotifier.app installed next to the test binary or in
		// /Applications would legitimately be found; only fail if it wrongly
		// returned the missing override path.
		if got == filepath.Join(missing, macNotifierExecutable) {
			t.Errorf("missing override should not resolve, got %q", got)
		}
	}
}

func TestOsaQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello", `"hello"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"line1\nline2", `"line1 line2"`}, // newline folded to space
		{"a\tb\rc", `"a b c"`},            // tab + CR folded to spaces
	}
	for _, c := range cases {
		if got := osaQuote(c.in); got != c.want {
			t.Errorf("osaQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
