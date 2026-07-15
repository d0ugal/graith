package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/fsnotify/fsnotify"
)

func mustWatcher(t *testing.T) *fsnotify.Watcher {
	t.Helper()

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}

	return w
}

func TestWatchMatcher_BuiltinAndUser(t *testing.T) {
	root := t.TempDir()
	m := newWatchMatcher(root, &config.WatchConfig{Ignore: []string{"docs/**"}})

	cases := []struct {
		path string
		want bool // fires?
	}{
		{"glen/handler.go", true},
		{"glen/util.go", true},
		{".git/config", false},    // builtin ignore
		{"editor.swp", false},     // builtin ignore
		{"docs/design.md", false}, // user ignore
		{"docs/sub/x.md", false},  // user ignore glob
	}
	for _, tc := range cases {
		if got := m.fires(tc.path); got != tc.want {
			t.Errorf("fires(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestWatchMatcher_IncludeGlobs(t *testing.T) {
	root := t.TempDir()
	m := newWatchMatcher(root, &config.WatchConfig{Paths: []string{"**/*.go"}})

	if !m.fires("glen/a.go") {
		t.Error("*.go should fire with include **/*.go")
	}

	if m.fires("glen/a.ts") {
		t.Error("*.ts should NOT fire with include **/*.go")
	}

	if m.fires("README.md") {
		t.Error("non-go should NOT fire with include **/*.go")
	}
}

func TestWatchMatcher_Gitignore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newWatchMatcher(root, &config.WatchConfig{})

	if m.fires("build/out.bin") {
		t.Error("gitignored build/ should not fire")
	}

	if m.fires("run.log") {
		t.Error("gitignored *.log should not fire")
	}

	if !m.fires("main.go") {
		t.Error("tracked main.go should fire")
	}

	if !m.ignoredDir("build") {
		t.Error("build/ should be pruned from the watch set")
	}

	if m.ignoredDir("src") {
		t.Error("src should not be pruned")
	}
}

func TestAddWatchRecursive(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))
	mustMkdir(t, filepath.Join(root, ".git")) // builtin-ignored, should be skipped
	mustMkdir(t, filepath.Join(root, ".git", "objects"))

	w := mustWatcher(t)
	defer func() { _ = w.Close() }()

	m := newWatchMatcher(root, &config.WatchConfig{})
	if degraded := addWatchRecursive(w.Add, root, m); degraded != "" {
		t.Fatalf("unexpected degraded: %s", degraded)
	}
	// .git should not be watched; adding a file there must not surface an event
	// within the matcher's fires() filter anyway — just assert no crash above.
}

func TestFileWatch_EndToEnd(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "wf",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.go"}, Debounce: "50ms"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "changed {change_count}", Deliver: config.DeliverConfig{Topic: "wynd"}},
	}
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)

	w := mustWatcher(t)
	if err := w.Add(worktree); err != nil {
		t.Fatal(err)
	}

	b := &watchBinding{triggerName: "wf", sessionID: "src", worktree: worktree, fingerprint: triggerFingerprint(&trig), watcher: w, changed: make(map[string]bool)}
	matcher := newWatchMatcher(worktree, trig.Watch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sm.runBinding(ctx, "wf", b, matcher)

	// Give the watcher a moment, then write a matching file.
	time.Sleep(50 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(worktree, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := ms.Read("wynd", "reader", false, "")
		if len(msgs) >= 1 {
			return // fired
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("watch trigger did not fire within timeout")
}

func TestReconcileBindings_Lifecycle(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "rev",
		Watch:  &config.WatchConfig{Role: "implementer"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}},
	}
	sm := newTriggerTestSM(t, trig)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "ben", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	ctx := context.Background()
	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	if len(sm.triggers.bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(sm.triggers.bindings))
	}

	// Session stops → binding is torn down.
	sm.state.Sessions["src"].Status = StatusStopped
	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	if len(sm.triggers.bindings) != 0 {
		t.Fatalf("expected binding torn down, got %d", len(sm.triggers.bindings))
	}

	sm.teardownAllBindings()
}

// TestReconcileBindings_HotReload asserts that editing a watch trigger's
// definition (paths/ignore/debounce) under the same name recreates the binding
// — the matcher and debounce are captured at creation, so a stale binding must
// be torn down and rebuilt when the fingerprint changes (issue #1028). An
// unchanged reconcile must leave the binding in place.
func TestReconcileBindings_HotReload(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "bide",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.go"}, Debounce: "50ms"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "blether"}},
	}
	sm := newTriggerTestSM(t, trig)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "canny", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	ctx := context.Background()
	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	key := bindingKey("bide", "src")
	first := sm.triggers.bindings[key]

	if first == nil {
		t.Fatalf("expected binding after first reconcile")
	}

	// Reconcile again with no change: the same binding must be kept.
	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	if got := sm.triggers.bindings[key]; got != first {
		t.Fatalf("unchanged reconcile recreated the binding")
	}

	// Edit the watch definition under the same name — paths + debounce change.
	// A real config reload swaps sm.cfg for a fresh *config.Config under the
	// lock (a new WatchConfig), so mimic that rather than mutating the shared
	// struct in place (which would race the running binding goroutine).
	reloaded := config.TriggerConfig{
		Name:   "bide",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.md"}, Debounce: "200ms"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "blether"}},
	}

	sm.mu.Lock()
	newCfg := *sm.cfg
	newCfg.Triggers = []config.TriggerConfig{reloaded}
	sm.cfg = &newCfg
	sm.mu.Unlock()

	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	second := sm.triggers.bindings[key]
	if second == nil {
		t.Fatalf("expected binding after hot reload")
	}

	if second == first {
		t.Fatalf("changed definition did not recreate the binding")
	}

	if second.fingerprint == first.fingerprint {
		t.Fatalf("recreated binding kept the stale fingerprint")
	}

	if want := triggerFingerprint(&reloaded); second.fingerprint != want {
		t.Fatalf("binding fingerprint = %q, want %q", second.fingerprint, want)
	}

	sm.teardownAllBindings()
}

// TestWatchFire_StaleGenerationDoesNotFire asserts the generation guard: a
// binding whose stored fingerprint no longer matches the current definition
// must not fire the (new) action from events the old matcher collected. Without
// the guard, a hot-reload landing between an event and the debounce firing
// would dispatch the reloaded action against a stale change set (#1028).
func TestWatchFire_StaleGenerationDoesNotFire(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "thrawn",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.go"}, Debounce: "10ms"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "fash"}},
	}
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)

	// A binding stamped with a fingerprint that does NOT match the live config
	// stands in for a stale generation mid-reload.
	b := &watchBinding{
		triggerName: "thrawn",
		sessionID:   "src",
		worktree:    worktree,
		fingerprint: "stalegeneration",
		changed:     map[string]bool{"main.go": true},
	}

	sm.watchFire(context.Background(), "thrawn", b)

	if msgs, _ := ms.Read("fash", "reader", false, ""); len(msgs) != 0 {
		t.Fatalf("stale-generation binding fired the action (%d messages)", len(msgs))
	}

	// A matching fingerprint fires as normal.
	b.fingerprint = triggerFingerprint(&trig)
	b.changed = map[string]bool{"main.go": true}
	sm.watchFire(context.Background(), "thrawn", b)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if msgs, _ := ms.Read("fash", "reader", false, ""); len(msgs) >= 1 {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("current-generation binding did not fire")
}

// TestReconcileBindings_DefersRecreateWhileInFlight asserts that a fingerprint
// change does not tear the binding down while a serialised action is in flight
// — recreating it then would let a fire on the fresh binding double-spawn an
// ensure-reviewer reactor (the new binding starts with a cleared inFlight
// guard). The recreate must wait for the in-flight fire to clear (#1028).
func TestReconcileBindings_DefersRecreateWhileInFlight(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "bide",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.go"}},
		Action: config.ActionConfig{Type: config.ActionCommand, Command: "true"},
	}
	sm := newTriggerTestSM(t, trig)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "canny", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	ctx := context.Background()
	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	key := bindingKey("bide", "src")
	first := sm.triggers.bindings[key]

	if first == nil {
		t.Fatalf("expected binding after first reconcile")
	}

	// Simulate a serialised action mid-flight on the current binding.
	first.bmu.Lock()
	first.inFlight = true
	first.bmu.Unlock()

	// Reload the definition (paths change → new fingerprint) while in flight.
	reloaded := config.TriggerConfig{
		Name:   "bide",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.md"}},
		Action: config.ActionConfig{Type: config.ActionCommand, Command: "true"},
	}

	sm.mu.Lock()
	newCfg := *sm.cfg
	newCfg.Triggers = []config.TriggerConfig{reloaded}
	sm.cfg = &newCfg
	sm.mu.Unlock()

	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	if got := sm.triggers.bindings[key]; got != first {
		t.Fatalf("recreated the binding while an action was in flight")
	}

	// Action finishes → next reconcile recreates the binding.
	first.bmu.Lock()
	first.inFlight = false
	first.bmu.Unlock()

	sm.reconcileBindings(ctx, sm.allTriggers(), time.Now())

	second := sm.triggers.bindings[key]
	if second == first {
		t.Fatalf("binding not recreated after in-flight action cleared")
	}

	if want := triggerFingerprint(&reloaded); second.fingerprint != want {
		t.Fatalf("recreated binding fingerprint = %q, want %q", second.fingerprint, want)
	}

	sm.teardownAllBindings()
}

func TestWatchRetryBackoff(t *testing.T) {
	cases := []struct {
		retry int
		want  time.Duration
	}{
		{0, watchRetryBaseBackoff}, // clamped to 1
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{99, watchRetryMaxBackoff}, // capped
	}
	for _, tc := range cases {
		if got := watchRetryBackoff(tc.retry); got != tc.want {
			t.Errorf("watchRetryBackoff(%d) = %v, want %v", tc.retry, got, tc.want)
		}
	}
}

// TestReconcileBindings_DegradedRecovers is the #1029 regression: a binding that
// degrades (watch limit exhausted) must retry on a backoff and recover on its
// own once the limit clears — without the source session restarting.
func TestReconcileBindings_DegradedRecovers(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "dreich",
		Watch:  &config.WatchConfig{Role: "implementer"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "blether"}},
	}
	sm := newTriggerTestSM(t, trig)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "ben", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	// Simulate an exhausted watch limit: every directory add fails.
	sm.watchAdd = func(_ *fsnotify.Watcher, _ string) error {
		return errors.New("no space left on device")
	}

	ctx := context.Background()
	key := bindingKey("dreich", "src")

	t0 := time.Now()
	sm.reconcileBindings(ctx, sm.allTriggers(), t0)

	b := sm.triggers.bindings[key]
	if b == nil || b.degraded == "" {
		t.Fatalf("expected degraded binding, got %+v", b)
	}

	if b.retryCount != 1 {
		t.Fatalf("expected retryCount 1, got %d", b.retryCount)
	}

	if b.nextRetryAt.IsZero() || !b.nextRetryAt.After(t0) {
		t.Fatalf("expected nextRetryAt scheduled after t0, got %v", b.nextRetryAt)
	}

	// Before the backoff elapses, reconcile must NOT retry (no thrashing on the
	// 2s tick while the limit is still exhausted).
	firstRetry := b.nextRetryAt

	sm.reconcileBindings(ctx, sm.allTriggers(), t0.Add(time.Second))

	if got := sm.triggers.bindings[key]; got.retryCount != 1 {
		t.Fatalf("expected no retry before backoff, retryCount=%d", got.retryCount)
	}

	// Still exhausted at the scheduled retry time → retry fires, backoff grows.
	sm.reconcileBindings(ctx, sm.allTriggers(), firstRetry.Add(time.Millisecond))

	b2 := sm.triggers.bindings[key]
	if b2.retryCount != 2 {
		t.Fatalf("expected retryCount 2 after second attempt, got %d", b2.retryCount)
	}

	if !b2.nextRetryAt.After(firstRetry) {
		t.Fatalf("expected backoff to grow: %v not after %v", b2.nextRetryAt, firstRetry)
	}

	// Watch limit clears → the next reconcile after the backoff recreates a
	// healthy binding, with no session restart.
	sm.watchAdd = nil
	sm.reconcileBindings(ctx, sm.allTriggers(), b2.nextRetryAt.Add(time.Millisecond))

	healthy := sm.triggers.bindings[key]
	if healthy == nil {
		t.Fatal("expected binding to persist after recovery")
	}

	if healthy.degraded != "" {
		t.Fatalf("expected recovered binding, still degraded: %s", healthy.degraded)
	}

	if healthy.retryCount != 0 || !healthy.nextRetryAt.IsZero() {
		t.Fatalf("expected retry state reset on recovery, got count=%d next=%v", healthy.retryCount, healthy.nextRetryAt)
	}

	// The recovered binding must be genuinely live — a real watcher with a running
	// event goroutine (cancel set) — not merely a cleared degraded flag.
	if healthy.watcher == nil || healthy.cancel == nil {
		t.Fatalf("expected recovered binding to be live, got watcher=%v cancel=%v", healthy.watcher != nil, healthy.cancel != nil)
	}

	sm.teardownAllBindings()
}

// TestRecordDegradedBinding_Recovers covers the watcher-less degraded path (e.g.
// fsnotify.NewWatcher itself failing on the inotify instance limit): the binding
// is recorded with a backoff and no live watcher, then recreated healthy by the
// reconcile loop once the limit clears — without leaking or thrashing.
func TestRecordDegradedBinding_Recovers(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "thrawn",
		Watch:  &config.WatchConfig{Role: "implementer"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "fash"}},
	}
	sm := newTriggerTestSM(t, trig)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "ben", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	key := bindingKey("thrawn", "src")
	sess := watchSession{id: "src", name: "ben", worktree: worktree}

	t0 := time.Now()
	sm.recordDegradedBinding(key, &sm.cfg.Triggers[0], sess, "fsnotify.NewWatcher failed: too many open files", 1, t0)

	b := sm.triggers.bindings[key]
	if b == nil || b.degraded == "" || b.watcher != nil || b.cancel != nil {
		t.Fatalf("expected watcher-less degraded binding, got %+v", b)
	}

	if b.retryCount != 1 || !b.nextRetryAt.After(t0) {
		t.Fatalf("expected backoff scheduled, got count=%d next=%v", b.retryCount, b.nextRetryAt)
	}

	// Before the backoff, reconcile must not thrash.
	sm.reconcileBindings(context.Background(), sm.allTriggers(), t0.Add(time.Second))

	if got := sm.triggers.bindings[key]; got.watcher != nil {
		t.Fatal("expected no recreation before backoff elapses")
	}

	// After the backoff, and with watcher construction working, it recovers.
	sm.reconcileBindings(context.Background(), sm.allTriggers(), b.nextRetryAt.Add(time.Millisecond))

	healthy := sm.triggers.bindings[key]
	if healthy == nil || healthy.degraded != "" || healthy.watcher == nil || healthy.cancel == nil {
		t.Fatalf("expected recovered live binding, got %+v", healthy)
	}

	sm.teardownAllBindings()
}

func TestDegradedTriggerDiagnostics(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "haar",
		Watch:  &config.WatchConfig{Role: "implementer"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "loch"}},
	}
	sm := newTriggerTestSM(t, trig)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "ben", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	// No degraded bindings yet.
	if got := sm.degradedTriggerDiagnostics(); got != nil {
		t.Fatalf("expected nil diagnostics when healthy, got %+v", got)
	}

	sm.watchAdd = func(_ *fsnotify.Watcher, _ string) error {
		return errors.New("too many open files")
	}
	sm.reconcileBindings(context.Background(), sm.allTriggers(), time.Now())

	diag := sm.degradedTriggerDiagnostics()
	if len(diag) != 1 {
		t.Fatalf("expected 1 degraded diagnostic, got %d", len(diag))
	}

	d := diag[0]
	if d.Name != "haar" || d.SessionName != "ben" || d.SessionID != "src" {
		t.Fatalf("unexpected diagnostic identity: %+v", d)
	}

	if d.Degraded == "" || d.RetryCount != 1 || d.NextRetryAt == "" {
		t.Fatalf("expected populated degraded diagnostic, got %+v", d)
	}

	sm.teardownAllBindings()
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()

	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
}

func TestWatchMatcher_DotAndEmpty(t *testing.T) {
	m := newWatchMatcher(t.TempDir(), &config.WatchConfig{})
	if m.fires(".") || m.fires("") {
		t.Error("'.' and '' should never fire")
	}

	if m.ignoredDir(".") {
		t.Error("root '.' is never an ignored dir")
	}
}
