package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/store"
)

func newTriggerTestSM(t *testing.T, triggers ...config.TriggerConfig) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Triggers = triggers
	cfg.Orchestrator.Enabled = true

	return &SessionManager{
		state:    NewState(),
		cfg:      cfg,
		paths:    config.Paths{StateFile: filepath.Join(dir, "state.json"), DataDir: dir},
		log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		triggers: newTriggerState(),
	}
}

func TestTriggerFingerprint(t *testing.T) {
	a := config.TriggerConfig{Name: "x", Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "hi"}}

	b := a
	if triggerFingerprint(&a) != triggerFingerprint(&b) {
		t.Error("identical triggers should share fingerprint")
	}

	c := a

	c.Schedule = &config.ScheduleConfig{Cron: "@hourly"}
	if triggerFingerprint(&a) == triggerFingerprint(&c) {
		t.Error("changed schedule should change fingerprint")
	}
	// Name is NOT part of the fingerprint (only fire-affecting fields).
	d := a

	d.Name = "renamed"
	if triggerFingerprint(&a) != triggerFingerprint(&d) {
		t.Error("rename should not change fingerprint")
	}
}

func TestTriggerRuntimePersistence(t *testing.T) {
	sm := newTriggerTestSM(t)
	_ = sm.updateTriggerRuntime("braw", func(r *TriggerRuntimeState) { r.RunCount = 1 })
	sm.recordTriggerRun("braw", TriggerRun{Cause: causeSchedule, Result: "published", ScheduledAt: time.Now()})
	sm.recordTriggerError("braw", "boom")

	rt := sm.getTriggerRuntime("braw")
	if rt == nil || rt.RunCount != 2 || rt.LastError != "boom" || len(rt.History) != 1 {
		t.Fatalf("runtime = %+v", rt)
	}

	// Persisted to disk?
	loaded, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.TriggerRuntime["braw"] == nil || loaded.TriggerRuntime["braw"].RunCount != 2 {
		t.Fatalf("not persisted: %+v", loaded.TriggerRuntime["braw"])
	}
}

func TestTriggerHistoryBounded(t *testing.T) {
	sm := newTriggerTestSM(t)
	for i := 0; i < triggerHistoryMax+10; i++ {
		sm.recordTriggerRun("dreich", TriggerRun{Cause: causeSchedule, ScheduledAt: time.Now()})
	}

	rt := sm.getTriggerRuntime("dreich")
	if len(rt.History) != triggerHistoryMax {
		t.Fatalf("history len = %d, want %d", len(rt.History), triggerHistoryMax)
	}
}

func TestDueSchedules_AtMostOnce(t *testing.T) {
	trig := config.TriggerConfig{
		Name:     "canny",
		Schedule: &config.ScheduleConfig{Every: "1h"},
		Action:   config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "blether"}},
	}
	sm := newTriggerTestSM(t, trig)
	now := time.Now()
	// Cursor already due.
	sm.triggers.nextFire["canny"] = now.Add(-time.Minute)

	due := sm.dueSchedules(now)
	if len(due) != 1 || due[0] != "canny" {
		t.Fatalf("expected canny due, got %v", due)
	}
	// LastScheduledFireAt committed durably before dispatch.
	if got := sm.getTriggerRuntime("canny"); got.LastScheduledFireAt == nil {
		t.Fatal("LastScheduledFireAt not committed")
	}
	// Second immediate call must NOT re-fire (cursor advanced past now).
	if due2 := sm.dueSchedules(now); len(due2) != 0 {
		t.Fatalf("re-fired same instant: %v", due2)
	}
}

func TestArmSchedule(t *testing.T) {
	now := time.Now()

	// Interval: next fire strictly in the future, anchored on ActivatedAt.
	interval := config.TriggerConfig{Name: "iv", Schedule: &config.ScheduleConfig{Every: "1h"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}
	sm := newTriggerTestSM(t, interval)
	rt := &TriggerRuntimeState{Name: "iv", ActivatedAt: ptrTime(now)}
	sm.armSchedule(&interval, rt, now)

	if next := sm.triggers.nextFire["iv"]; !next.After(now) {
		t.Errorf("interval next %v should be after %v", next, now)
	}

	// Cron: parses and arms.
	cronTrig := config.TriggerConfig{Name: "cr", Schedule: &config.ScheduleConfig{Cron: "0 9 * * *"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}
	sm2 := newTriggerTestSM(t, cronTrig)
	sm2.armSchedule(&cronTrig, &TriggerRuntimeState{Name: "cr"}, now)

	if _, ok := sm2.triggers.cron["cr"]; !ok {
		t.Error("cron schedule not parsed")
	}

	if sm2.triggers.nextFire["cr"].Before(now) {
		t.Error("cron next fire should be in the future")
	}

	// catch_up: a missed instant fires immediately.
	cu := config.TriggerConfig{Name: "cu", Schedule: &config.ScheduleConfig{Every: "1h"}, Policy: config.TriggerPolicy{CatchUp: true}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}
	sm3 := newTriggerTestSM(t, cu)
	rt3 := &TriggerRuntimeState{Name: "cu", ActivatedAt: ptrTime(now.Add(-3 * time.Hour)), NextScheduledFireAt: ptrTime(now.Add(-time.Hour))}
	sm3.armSchedule(&cu, rt3, now)

	if next := sm3.triggers.nextFire["cu"]; next.After(now) {
		t.Errorf("catch_up should arm at/before now, got %v", next)
	}
}

func TestReconcileSchedules_Prune(t *testing.T) {
	trig := config.TriggerConfig{Name: "keep", Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}
	sm := newTriggerTestSM(t, trig)
	now := time.Now()
	sm.reconcileSchedules(sm.cfg, now)

	if _, ok := sm.triggers.nextFire["keep"]; !ok {
		t.Fatal("keep should be armed after reconcile")
	}
	// Remove from config and reconcile: pruned.
	sm.cfg.Triggers = nil
	sm.reconcileSchedules(sm.cfg, now)
	// reconcile returns early when no triggers; call the prune path with an empty set.
	sm.cfg.Triggers = []config.TriggerConfig{{Name: "other", Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}}
	sm.reconcileSchedules(sm.cfg, now)

	if _, ok := sm.triggers.nextFire["keep"]; ok {
		t.Error("keep should have been pruned")
	}
}

func TestTriggerPause(t *testing.T) {
	trig := config.TriggerConfig{Name: "bide", Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}

	sm := newTriggerTestSM(t, trig)
	if err := sm.TriggerPause("bide", true); err != nil {
		t.Fatal(err)
	}

	if rt := sm.getTriggerRuntime("bide"); rt == nil || !rt.Paused {
		t.Fatal("not paused")
	}

	if err := sm.TriggerPause("missing", true); err == nil {
		t.Error("expected error for unknown trigger")
	}
	// A config-disabled trigger cannot be resumed.
	dis := config.TriggerConfig{Name: "auld", Enabled: boolFalse(), Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}

	sm2 := newTriggerTestSM(t, dis)
	if err := sm2.TriggerPause("auld", false); err == nil {
		t.Error("expected error resuming disabled trigger")
	}
}

func TestTriggerRunNow_WatchRejected(t *testing.T) {
	trig := config.TriggerConfig{Name: "watchy", Watch: &config.WatchConfig{Repo: "/r"}, Action: config.ActionConfig{Type: config.ActionCommand, Command: "x"}}

	sm := newTriggerTestSM(t, trig)
	if err := sm.TriggerRunNow(t.Context(), "watchy"); err == nil {
		t.Error("expected watch trigger to reject manual run")
	}
}

func TestDeliverStorePath(t *testing.T) {
	sm := newTriggerTestSM(t)

	dir, key := sm.deliverStorePath("shared:reviews/x.md", "/repo")
	if dir != store.SharedStorePath(sm.paths.DataDir) || key != "reviews/x.md" {
		t.Errorf("shared: got %s %s", dir, key)
	}

	dir, key = sm.deliverStorePath("builds/x.log", "/repo")
	if dir != store.StorePath(sm.paths.DataDir, "/repo") || key != "builds/x.log" {
		t.Errorf("repo: got %s %s", dir, key)
	}

	dir, _ = sm.deliverStorePath("builds/x.log", "")
	if dir != store.SharedStorePath(sm.paths.DataDir) {
		t.Errorf("no-repo should be shared: got %s", dir)
	}
}

func TestDeliverStore(t *testing.T) {
	sm := newTriggerTestSM(t)
	vars := config.TriggerVars{Date: "2026-07-11"}
	sm.deliver(t.Context(), config.DeliverConfig{Store: "shared:reports/{date}.md"}, "hello", "", vars)

	got, err := store.Get(store.SharedStorePath(sm.paths.DataDir), "reports/2026-07-11.md")
	if err != nil {
		t.Fatalf("store get: %v", err)
	}

	if got != "hello" {
		t.Errorf("store body = %q", got)
	}
}

func TestRateLimited(t *testing.T) {
	sm := newTriggerTestSM(t)

	now := time.Now()
	for i := 0; i < 3; i++ {
		if sm.rateLimited("k", 3, time.Minute, now) {
			t.Fatalf("fire %d unexpectedly limited", i)
		}
	}

	if !sm.rateLimited("k", 3, time.Minute, now) {
		t.Error("4th fire should be rate-limited")
	}
	// After the window, allowed again.
	if sm.rateLimited("k", 3, time.Minute, now.Add(2*time.Minute)) {
		t.Error("should be allowed after window")
	}
}

func TestTriggerListAndStatus(t *testing.T) {
	sched := config.TriggerConfig{Name: "braw", Schedule: &config.ScheduleConfig{Cron: "0 9 * * *"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}
	watch := config.TriggerConfig{Name: "canny", Watch: &config.WatchConfig{Role: "implementer"}, Action: config.ActionConfig{Type: config.ActionSession, Ensure: true}}
	sm := newTriggerTestSM(t, sched, watch)

	list := sm.TriggerList()
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}

	rec, err := sm.TriggerStatus("braw")
	if err != nil {
		t.Fatal(err)
	}

	if rec.Source != "schedule" || rec.Action != config.ActionMessage || rec.Schedule != "0 9 * * *" {
		t.Errorf("braw record = %+v", rec)
	}

	wrec, _ := sm.TriggerStatus("canny")
	if wrec.Source != "watch" || wrec.WatchScope != "role:implementer" {
		t.Errorf("canny record = %+v", wrec)
	}

	if _, err := sm.TriggerStatus("ghost"); err == nil {
		t.Error("expected error for unknown trigger")
	}
}

func TestMatchingWatchSessions(t *testing.T) {
	sm := newTriggerTestSM(t)
	sm.state.Sessions["s1"] = &SessionState{ID: "s1", Name: "one", Status: StatusRunning, RepoPath: "/repo/a", WorktreePath: "/wt/1"}
	sm.state.Sessions["s2"] = &SessionState{ID: "s2", Name: "two", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: "/wt/2"}
	sm.state.Sessions["s3"] = &SessionState{ID: "s3", Name: "gone", Status: StatusStopped, RepoPath: "/repo/a", WorktreePath: "/wt/3"}

	byRepo := sm.matchingWatchSessions(&config.WatchConfig{Repo: "/repo/a"})
	if len(byRepo) != 1 || byRepo[0].id != "s1" {
		t.Errorf("byRepo = %+v", byRepo)
	}

	byRole := sm.matchingWatchSessions(&config.WatchConfig{Role: "implementer"})
	if len(byRole) != 1 || byRole[0].id != "s2" {
		t.Errorf("byRole = %+v", byRole)
	}
}

func TestReuseReactor(t *testing.T) {
	sm := newTriggerTestSM(t)
	sm.state.Sessions["r1"] = &SessionState{ID: "r1", Status: StatusStopped, TriggerReactor: true}
	key := bindingKey("rev", "src")
	sm.triggers.bindings[key] = &watchBinding{triggerName: "rev", sessionID: "src", reactorID: "r1"}

	if got := sm.reuseReactor("rev", "src"); got != "r1" {
		t.Errorf("stopped reactor should be reused, got %q", got)
	}
	// Soft-deleted reactor is not reused.
	now := time.Now()

	sm.state.Sessions["r1"].DeletedAt = &now
	if got := sm.reuseReactor("rev", "src"); got != "" {
		t.Errorf("soft-deleted reactor should not be reused, got %q", got)
	}
}

func TestActionSession_NoOrchestrator(t *testing.T) {
	trig := config.TriggerConfig{Name: "s", Watch: &config.WatchConfig{Role: "r"}, Action: config.ActionConfig{Type: config.ActionSession, Prompt: "hi"}}

	sm := newTriggerTestSM(t, trig)
	if _, err := sm.actionSession(t.Context(), &trig, fireContext{now: time.Now()}); err == nil {
		t.Error("expected error when no orchestrator")
	}
}

func TestActionScenario_NoOrchestrator(t *testing.T) {
	trig := config.TriggerConfig{Name: "sc", Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionScenario, Scenario: "x"}}

	sm := newTriggerTestSM(t, trig)
	if _, err := sm.actionScenario(t.Context(), &trig); err == nil {
		t.Error("expected error when no orchestrator")
	}
}

func TestActionScenario_MissingFile(t *testing.T) {
	trig := config.TriggerConfig{Name: "sc", Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionScenario, Scenario: "ghost"}}
	sm := newTriggerTestSM(t, trig)
	sm.state.Sessions["o"] = &SessionState{ID: "o", SystemKind: SystemKindOrchestrator}

	sm.paths.ConfigFile = filepath.Join(t.TempDir(), "config.toml")
	if _, err := sm.actionScenario(t.Context(), &trig); err == nil {
		t.Error("expected error for missing scenario file")
	}
}

func TestRunTriggerLoop_FiresMessage(t *testing.T) {
	trig := config.TriggerConfig{
		Name: "loop-tick", Schedule: &config.ScheduleConfig{Every: "1s"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "tick", Deliver: config.DeliverConfig{Topic: "loch"}},
	}
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	go sm.RunTriggerLoop(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if msgs, _ := ms.Read("loch", "r", false, ""); len(msgs) >= 1 {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatal("RunTriggerLoop did not fire the schedule trigger")
}

func TestRunFileWatchLoop_FiresMessage(t *testing.T) {
	worktree := t.TempDir()
	trig := config.TriggerConfig{
		Name:   "wl",
		Watch:  &config.WatchConfig{Role: "implementer", Debounce: "50ms"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "changed", Deliver: config.DeliverConfig{Topic: "brae"}},
	}
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)
	sm.state.Sessions["src"] = &SessionState{ID: "src", Name: "ben", Status: StatusRunning, ScenarioRole: "implementer", WorktreePath: worktree}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	go sm.RunFileWatchLoop(ctx)

	// Wait for the binding to be created (reconcile tick is 2s), then edit.
	time.Sleep(2500 * time.Millisecond)

	_ = os.WriteFile(filepath.Join(worktree, "x.go"), []byte("package x\n"), 0o600)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if msgs, _ := ms.Read("brae", "r", false, ""); len(msgs) >= 1 {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatal("RunFileWatchLoop did not fire on the file change")
}

func ptrTime(t time.Time) *time.Time { return &t }
func boolFalse() *bool               { b := false; return &b }

func withMsgStore(t *testing.T, sm *SessionManager) *MsgStore {
	t.Helper()

	ms, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = ms.Close() })

	sm.messages = ms

	return ms
}

func TestActionMessage_Topic(t *testing.T) {
	trig := config.TriggerConfig{
		Name: "blether", Schedule: &config.ScheduleConfig{Cron: "@daily"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "hail {name}", Deliver: config.DeliverConfig{Topic: "kirk"}},
	}
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)

	result, err := sm.fireAction(t.Context(), &trig, fireContext{now: time.Now()})
	if err != nil || result != "published" {
		t.Fatalf("fireAction = %q, %v", result, err)
	}

	msgs, err := ms.Read("kirk", "reader", false, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 1 || msgs[0].Body != "hail blether" {
		t.Fatalf("topic messages = %+v", msgs)
	}
}

func TestActionCommand_Unsandboxed(t *testing.T) {
	repo := t.TempDir()
	trig := config.TriggerConfig{
		Name: "neep", Schedule: &config.ScheduleConfig{Cron: "@daily"},
		Action: config.ActionConfig{Type: config.ActionCommand, Command: "echo hallo", Repo: repo, Sandbox: boolFalse()},
	}
	sm := newTriggerTestSM(t, trig)

	result, err := sm.actionCommand(t.Context(), &trig, fireContext{now: time.Now()})
	if err != nil {
		t.Fatalf("actionCommand: %v", err)
	}

	if result != "exit 0" {
		t.Errorf("result = %q, want exit 0", result)
	}
}

func TestActionCommand_NonZeroExit(t *testing.T) {
	repo := t.TempDir()
	trig := config.TriggerConfig{
		Name: "thrawn", Schedule: &config.ScheduleConfig{Cron: "@daily"},
		Action: config.ActionConfig{Type: config.ActionCommand, Command: "exit 3", Repo: repo, Sandbox: boolFalse()},
	}
	sm := newTriggerTestSM(t, trig)

	result, err := sm.actionCommand(t.Context(), &trig, fireContext{now: time.Now()})
	if err == nil {
		t.Error("non-zero exit should return an error (surfaces in LastError)")
	}

	if result != "exit 3" {
		t.Errorf("result = %q, want exit 3", result)
	}
}

func TestTriggerRunNow_RespectsOverlap(t *testing.T) {
	trig := config.TriggerConfig{Name: "canny", Schedule: &config.ScheduleConfig{Cron: "@daily"}, Action: config.ActionConfig{Type: config.ActionCommand, Command: "sleep 1", Repo: t.TempDir(), Sandbox: boolFalse()}}
	sm := newTriggerTestSM(t, trig)
	sm.triggers.mu.Lock()
	sm.triggers.inFlight["canny"] = 1
	sm.triggers.mu.Unlock()

	if err := sm.TriggerRunNow(t.Context(), "canny"); err == nil {
		t.Error("manual run should be rejected while a run is in flight (overlap=skip)")
	}
}

func TestAcquireSlot(t *testing.T) {
	sm := newTriggerTestSM(t)

	sm.cfg.TriggersRuntime.MaxConcurrent = 2
	if !sm.acquireSlot() {
		t.Fatal("first slot should acquire")
	}

	if !sm.acquireSlot() {
		t.Fatal("second slot should acquire")
	}

	if sm.acquireSlot() {
		t.Error("third slot should be refused at cap 2")
	}

	sm.releaseSlot()

	if !sm.acquireSlot() {
		t.Error("slot should acquire after release")
	}
}

func TestAdvanceScheduleInterval_NoDrift(t *testing.T) {
	trig := config.TriggerConfig{Name: "iv", Schedule: &config.ScheduleConfig{Every: "1h"}, Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}}}
	sm := newTriggerTestSM(t, trig)
	base := time.Now().Truncate(time.Hour)
	sm.triggers.nextFire["iv"] = base

	next := sm.advanceSchedule("iv", &trig, base.Add(90*time.Second))
	if !next.Equal(base.Add(time.Hour)) {
		t.Errorf("interval drifted: next=%v want=%v", next, base.Add(time.Hour))
	}
}

func TestSessionDeliveryInstruction(t *testing.T) {
	sm := newTriggerTestSM(t)

	vars := config.TriggerVars{Date: "2026-07-11", SessionName: "ben"}
	if got, _ := sm.sessionDeliveryInstruction(config.DeliverConfig{}, vars); got != "" {
		t.Errorf("empty deliver should give no instruction, got %q", got)
	}

	got, err := sm.sessionDeliveryInstruction(config.DeliverConfig{Inbox: "orchestrator", Store: "reports/{date}.md"}, vars)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"orchestrator", "gr store put reports/2026-07-11.md", "deliver your result"} {
		if !contains(got, want) {
			t.Errorf("instruction missing %q:\n%s", want, got)
		}
	}
}

func TestFindReactor(t *testing.T) {
	sm := newTriggerTestSM(t)

	sm.state.Sessions["r"] = &SessionState{ID: "r", TriggerReactor: true, TriggerID: "rev", SharedWorktreeSourceID: "src"}
	if got := sm.findReactor("rev", "src"); got != "r" {
		t.Errorf("findReactor = %q, want r", got)
	}

	if got := sm.findReactor("rev", "other"); got != "" {
		t.Errorf("findReactor for wrong source = %q, want empty", got)
	}
}

func TestActionCommand_FailClosed(t *testing.T) {
	repo := t.TempDir()
	trig := config.TriggerConfig{
		Name: "fash", Schedule: &config.ScheduleConfig{Cron: "@daily"},
		Action: config.ActionConfig{Type: config.ActionCommand, Command: "echo x", Repo: repo}, // sandbox nil => on
	}
	sm := newTriggerTestSM(t, trig)
	sm.cfg.Sandbox.Enabled = false // cannot enforce

	_, err := sm.actionCommand(t.Context(), &trig, fireContext{now: time.Now()})
	if err == nil {
		t.Fatal("expected fail-closed error when sandbox unavailable")
	}
}

func TestDeliverInbox(t *testing.T) {
	sm := newTriggerTestSM(t)
	ms := withMsgStore(t, sm)
	sm.state.Sessions["s1"] = &SessionState{ID: "s1", Name: "ben", Status: StatusStopped}

	sm.deliverInbox(t.Context(), "ben", "wheesht", false) // bare publish, no wake

	msgs, err := ms.Read("inbox:s1", "reader", false, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 1 || msgs[0].Body != "wheesht" {
		t.Fatalf("inbox = %+v", msgs)
	}

	// Soft-deleted target is never delivered to.
	now := time.Now()
	sm.state.Sessions["s2"] = &SessionState{ID: "s2", Name: "gone", Status: StatusStopped, DeletedAt: &now}
	sm.deliverInbox(t.Context(), "gone", "x", true)

	if m, _ := ms.Read("inbox:s2", "r", false, ""); len(m) != 0 {
		t.Errorf("soft-deleted got %d messages", len(m))
	}
}

func TestTriggerVars(t *testing.T) {
	sm := newTriggerTestSM(t)
	trig := config.TriggerConfig{Name: "canny", Watch: &config.WatchConfig{Role: "r"}}
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	v := sm.triggerVars(&trig, fireContext{now: now, sessionName: "braw", worktree: "/wt", changedFiles: []string{"a.go", "b.go"}})
	if v.Name != "canny" || v.Date != "2026-07-11" || v.SessionName != "braw" || v.ChangeCount != "2" || v.ChangedFiles != "a.go, b.go" {
		t.Fatalf("vars = %+v", v)
	}
}

func TestTriggerReactorName(t *testing.T) {
	if got := triggerReactorName("rev", "braw"); got != "rev-braw" {
		t.Errorf("got %q", got)
	}

	long := triggerReactorName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb")
	if len(long) > 40 {
		t.Errorf("name too long: %d", len(long))
	}
}

func TestFireSchedule_RecordsRun(t *testing.T) {
	trig := config.TriggerConfig{
		Name: "kirk", Schedule: &config.ScheduleConfig{Cron: "@daily"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "hi", Deliver: config.DeliverConfig{Topic: "t"}},
	}
	sm := newTriggerTestSM(t, trig)
	withMsgStore(t, sm)
	sm.fireSchedule(t.Context(), "kirk", causeSchedule)

	rt := sm.getTriggerRuntime("kirk")
	if rt == nil || rt.RunCount != 1 || len(rt.History) != 1 || rt.History[0].Result != "published" {
		t.Fatalf("runtime = %+v", rt)
	}
}

func TestFireWatch_RateLimited(t *testing.T) {
	trig := config.TriggerConfig{
		Name: "haar", Watch: &config.WatchConfig{Role: "r"},
		Action: config.ActionConfig{Type: config.ActionMessage, Body: "x", Deliver: config.DeliverConfig{Topic: "t"}},
		Policy: config.TriggerPolicy{RateLimit: "1/1h"},
	}
	sm := newTriggerTestSM(t, trig)
	withMsgStore(t, sm)

	fc := fireContext{now: time.Now(), sessionID: "src"}
	sm.fireWatch(t.Context(), &trig, fc) // first fires
	sm.fireWatch(t.Context(), &trig, fc) // second rate-limited

	rt := sm.getTriggerRuntime("haar")
	if rt == nil || rt.RunCount != 1 {
		t.Fatalf("expected 1 run (2nd rate-limited), got %+v", rt)
	}
}

func TestOrchestratorIDAndBindingReactor(t *testing.T) {
	sm := newTriggerTestSM(t)
	if sm.orchestratorID() != "" {
		t.Error("no orchestrator expected")
	}

	sm.state.Sessions["o"] = &SessionState{ID: "o", SystemKind: SystemKindOrchestrator}
	if sm.orchestratorID() != "o" {
		t.Error("orchestrator not found")
	}

	sm.triggers.bindings[bindingKey("rev", "src")] = &watchBinding{triggerName: "rev", sessionID: "src"}
	sm.setBindingReactor("rev", "src", "r1")

	if sm.triggers.bindings[bindingKey("rev", "src")].reactorID != "r1" {
		t.Error("reactor not set")
	}
}

func TestTruncateOutput(t *testing.T) {
	short := truncateOutput("  hi  ")
	if short != "hi" {
		t.Errorf("got %q", short)
	}

	long := make([]byte, triggerCommandOutputCap+100)
	for i := range long {
		long[i] = 'a'
	}

	if out := truncateOutput(string(long)); len(out) <= triggerCommandOutputCap || !contains(out, "truncated") {
		t.Errorf("long output not truncated: len=%d", len(out))
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}

	return -1
}
