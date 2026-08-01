package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDaemonTaskGroupOwnsNestedTasksAndClosesAdmissionBeforeDrain(t *testing.T) {
	group := newDaemonTaskGroup()
	parentStarted := make(chan struct{})
	childStarted := make(chan struct{})
	releaseChild := make(chan struct{})

	var calls atomic.Int32

	if !group.Go(func(ctx context.Context) {
		calls.Add(1)
		close(parentStarted)

		if !group.Go(func(context.Context) {
			calls.Add(1)
			close(childStarted)
			<-releaseChild
		}) {
			t.Error("nested task was rejected before drain")
		}
	}) {
		t.Fatal("top-level task was rejected")
	}

	select {
	case <-parentStarted:
		t.Fatal("task ran before complete generation activation")
	default:
	}

	group.Activate()
	<-parentStarted
	<-childStarted
	group.BeginDrain()

	if group.Go(func(context.Context) { calls.Add(100) }) {
		t.Fatal("task admission remained open after drain")
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if err := group.Wait(timeoutCtx); err == nil {
		t.Fatal("drain returned while an accepted nested task was live")
	}

	close(releaseChild)

	finalCtx, finalCancel := context.WithTimeout(context.Background(), time.Second)
	defer finalCancel()

	if err := group.Wait(finalCtx); err != nil {
		t.Fatalf("final drain: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("task calls = %d, want exact parent+child", got)
	}
}

func TestDaemonTaskGroupDrainBeforeActivationLaunchesNothing(t *testing.T) {
	group := newDaemonTaskGroup()

	var called atomic.Bool

	if !group.Go(func(context.Context) { called.Store(true) }) {
		t.Fatal("pre-activation task was rejected")
	}

	group.BeginDrain()
	group.Activate()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := group.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	if called.Load() {
		t.Fatal("canceled pre-activation task launched")
	}
}

func TestDaemonTaskGroupReportsActiveDrainBlockers(t *testing.T) {
	group := newDaemonTaskGroup()
	started := make(chan struct{})
	release := make(chan struct{})

	if !group.Go(func(context.Context) {
		close(started)
		<-release
	}) {
		t.Fatal("task was rejected")
	}

	group.Activate()
	<-started

	group.BeginDrain()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if err := group.Wait(ctx); err == nil {
		t.Fatal("drain returned while a task was active")
	}

	tasks := group.ActiveTasks(time.Now())
	if len(tasks) != 1 {
		t.Fatalf("active tasks = %d, want 1: %+v", len(tasks), tasks)
	}

	if tasks[0].Age <= 0 {
		t.Fatalf("active task age = %s, want positive", tasks[0].Age)
	}

	if !strings.Contains(tasks[0].Name, "TestDaemonTaskGroupReportsActiveDrainBlockers") {
		t.Fatalf("active task name = %q, want test function name", tasks[0].Name)
	}

	close(release)

	finalCtx, finalCancel := context.WithTimeout(context.Background(), time.Second)
	defer finalCancel()

	if err := group.Wait(finalCtx); err != nil {
		t.Fatalf("final drain: %v", err)
	}

	if tasks := group.ActiveTasks(time.Now()); len(tasks) != 0 {
		t.Fatalf("active tasks after drain = %+v, want none", tasks)
	}
}

func TestStartBackgroundTaskReportsCallerName(t *testing.T) {
	sm := sleeperSM(t)
	group := newDaemonTaskGroup()
	started := make(chan struct{})
	release := make(chan struct{})

	if !sm.installBackgroundTasks(group) {
		t.Fatal("background generation was not installed")
	}

	if !sm.startBackgroundTask(context.Background(), func(context.Context) {
		close(started)
		<-release
	}) {
		t.Fatal("background task was rejected")
	}

	group.Activate()
	<-started

	group.BeginDrain()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if err := group.Wait(ctx); err == nil {
		t.Fatal("drain returned while a task was active")
	}

	tasks := group.ActiveTasks(time.Now())
	if len(tasks) != 1 {
		t.Fatalf("active tasks = %d, want 1: %+v", len(tasks), tasks)
	}

	if strings.Contains(tasks[0].Name, "startBackgroundTask") {
		t.Fatalf("active task name = %q, want logical caller name", tasks[0].Name)
	}

	if !strings.Contains(tasks[0].Name, "TestStartBackgroundTaskReportsCallerName") {
		t.Fatalf("active task name = %q, want test function name", tasks[0].Name)
	}

	close(release)

	finalCtx, finalCancel := context.WithTimeout(context.Background(), time.Second)
	defer finalCancel()

	if err := group.Wait(finalCtx); err != nil {
		t.Fatalf("final drain: %v", err)
	}
}

func TestBackgroundPublicationShutdownDoesNotReopenUpgradeAdmission(t *testing.T) {
	sm := sleeperSM(t)
	sm.upgradePending = true
	group := newDaemonTaskGroup()
	started := make(chan struct{})
	stopped := make(chan struct{})

	if !group.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	}) {
		t.Fatal("replacement task was rejected before publication")
	}

	if !sm.installBackgroundTasks(group) {
		t.Fatal("replacement generation was not published")
	}

	group.Activate()
	<-started

	published := make(chan struct{})
	releaseAfter := make(chan struct{})
	sm.afterBackgroundPublication = func() {
		close(published)
		<-releaseAfter
	}
	afterDone := make(chan struct{})

	go func() {
		sm.finishBackgroundPublication(true, sm.endUpgradeReservation)
		close(afterDone)
	}()

	<-published

	// SIGTERM wins after the replacement generation is visible but before the
	// rollback completion callback can clear upgradePending.
	sm.beginShutdownBarrier()
	group.BeginDrain()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := group.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	<-stopped

	if sm.startBackgroundTask(context.Background(), func(context.Context) {
		t.Error("late task ran after replacement generation drain")
	}) {
		t.Fatal("late task published after replacement generation drain")
	}

	close(releaseAfter)
	<-afterDone

	sm.mu.RLock()
	upgradePending := sm.upgradePending
	shutdownPending := sm.shutdownPending
	sm.mu.RUnlock()

	if !upgradePending || !shutdownPending {
		t.Fatalf("shutdown reservation reopened: upgrade=%v shutdown=%v", upgradePending, shutdownPending)
	}
}
