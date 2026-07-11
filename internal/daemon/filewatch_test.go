package daemon

import (
	"context"
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
	defer w.Close()
	m := newWatchMatcher(root, &config.WatchConfig{})
	if degraded := addWatchRecursive(w, root, m); degraded != "" {
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
	b := &watchBinding{triggerName: "wf", sessionID: "src", worktree: worktree, watcher: w, changed: make(map[string]bool)}
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
	sm.reconcileBindings(ctx, sm.cfg)
	if len(sm.triggers.bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(sm.triggers.bindings))
	}

	// Session stops → binding is torn down.
	sm.state.Sessions["src"].Status = StatusStopped
	sm.reconcileBindings(ctx, sm.cfg)
	if len(sm.triggers.bindings) != 0 {
		t.Fatalf("expected binding torn down, got %d", len(sm.triggers.bindings))
	}
	sm.teardownAllBindings()
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
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
