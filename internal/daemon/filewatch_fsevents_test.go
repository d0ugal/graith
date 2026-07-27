//go:build darwin && cgo

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/fsnotify/fsevents"
	"github.com/fsnotify/fsnotify"
)

func TestFSEventsBackendUsesSingleRecursiveStream(t *testing.T) {
	worktree := t.TempDir()
	for _, dir := range []string{"cmd", "internal/daemon", "website/content/docs"} {
		mustMkdir(t, filepath.Join(worktree, filepath.FromSlash(dir)))
	}

	trig := config.TriggerConfig{
		Name:   "braw",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.go"}},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "blether"}},
	}
	sm := newTriggerTestSM(t, trig)
	sm.watchBackend = nil
	sm.cfg.TriggersRuntime.Advanced.WatchMaxDirectories = 1
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "ben", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	sm.reconcileBindings(context.Background(), sm.allTriggers(), time.Now())
	defer sm.teardownAllBindings()

	b := sm.triggers.bindings[bindingKey("braw", "src")]
	if b == nil || b.degraded != "" || b.backend == nil {
		t.Fatalf("expected healthy FSEvents binding, got %+v", b)
	}

	if got := b.backend.Name(); got != "fsevents" {
		t.Fatalf("backend = %q, want fsevents", got)
	}

	if got := len(b.watchPaths); got != 1 {
		t.Fatalf("watch path count = %d, want one recursive stream", got)
	}

	if got := sm.triggers.watchDirs; got != 1 {
		t.Fatalf("watch budget usage = %d, want one stream cost", got)
	}
}

func TestFSEventsBackendEndToEndNestedCreate(t *testing.T) {
	worktree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktree, "internal", "daemon"), 0o750); err != nil {
		t.Fatal(err)
	}

	trig := config.TriggerConfig{
		Name:   "thrawn",
		Watch:  &config.WatchConfig{Role: "implementer", Paths: []string{"**/*.go"}, Debounce: "50ms"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "changed {change_count}", Deliver: config.DeliverConfig{Topic: "fash"}},
	}
	sm := newTriggerTestSM(t, trig)
	sm.watchBackend = nil
	ms := withMsgStore(t, sm)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "ben", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	sm.reconcileBindings(context.Background(), sm.allTriggers(), time.Now())
	defer sm.teardownAllBindings()

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(worktree, "internal", "daemon", "watch.go"), []byte("package daemon\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := ms.Read("fash", "reader", false, "")
		if len(msgs) >= 1 {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("FSEvents watch trigger did not fire within timeout")
}

func TestFSEventsBackendDirectoryRenameRequestsSubtreeScan(t *testing.T) {
	root := t.TempDir()
	b := &fseventsWatchBackend{logicalRoot: root, watchRoot: root}

	got, ok := b.translate(fsevents.Event{
		Path:  filepath.Join(root, "src"),
		Flags: fsevents.ItemRenamed | fsevents.ItemIsDir,
	})
	if !ok {
		t.Fatal("directory rename event was not translated")
	}

	if got.Op&fsnotify.Rename == 0 {
		t.Fatalf("translated op = %s, want rename", got.Op)
	}

	if !got.Scan {
		t.Fatal("directory rename event did not request subtree scan")
	}

	if got.LossyScan {
		t.Fatal("precise directory rename event requested lossy subtree fallback")
	}
}
