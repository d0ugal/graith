package daemon

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// wedgeDriver is a SessionDriver test double whose process ignores SIGTERM and,
// unless exitOnForce is set, SIGKILL too: Done never closes, so teardownLiveDriver
// cannot confirm the process died and must keep the session for retry rather than
// tear down its worktree while it may still be alive (issue #1326). markExited
// stands in for the process finally dying so a retry teardown completes.
type wedgeDriver struct {
	SessionDriver

	mu          sync.Mutex
	done        chan struct{}
	doneOnce    sync.Once
	exitOnForce bool
	kills       int
	forces      int
	closes      int
	detaches    int
}

func newWedgeDriver(exitOnForce bool) *wedgeDriver {
	return &wedgeDriver{done: make(chan struct{}), exitOnForce: exitOnForce}
}

func (d *wedgeDriver) ProcessPID() int       { return 4242 }
func (d *wedgeDriver) Pgid() int             { return 4242 }
func (d *wedgeDriver) Done() <-chan struct{} { return d.done }

func (d *wedgeDriver) Exited() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

func (d *wedgeDriver) Kill() error {
	d.mu.Lock()
	d.kills++
	d.mu.Unlock()

	return nil
}

func (d *wedgeDriver) ForceKill() error {
	d.mu.Lock()
	d.forces++
	exit := d.exitOnForce
	d.mu.Unlock()

	if exit {
		d.markExited()
	}

	return nil
}

func (d *wedgeDriver) Close() {
	d.mu.Lock()
	d.closes++
	d.mu.Unlock()
}

func (d *wedgeDriver) Detach() {
	d.mu.Lock()
	d.detaches++
	d.mu.Unlock()
}

func (d *wedgeDriver) markExited() { d.doneOnce.Do(func() { close(d.done) }) }

func (d *wedgeDriver) stats() (kills, forces, closes int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.kills, d.forces, d.closes
}

// newTeardownTestManager builds a SessionManager with a short process_kill_grace
// so the SIGTERM→SIGKILL escalation and the post-SIGKILL confirmation windows are
// quick.
func newTeardownTestManager(t *testing.T, grace time.Duration) *SessionManager {
	t.Helper()

	cfg := config.Default()
	cfg.Lifecycle.ProcessKillGrace = grace.String()

	return newSMWithConfig(t, cfg)
}

func tombstoneExists(t *testing.T, sm *SessionManager, id string) bool {
	t.Helper()

	_, err := os.Stat(sm.tombstonePath(id))
	if err == nil {
		return true
	}

	if !os.IsNotExist(err) {
		t.Fatalf("stat tombstone %s: %v", id, err)
	}

	return false
}

// TestHardDeletePropagatesLiveDriverTeardownFailureThenRetries is the single-
// delete regression for issue #1326. A driver that ignores both SIGTERM and
// SIGKILL (Done never closes) must abort the delete rather than commit it and
// tear down the worktree while the process may be alive: the session is kept
// errored with its driver and PID restored, no tombstone survives, and a later
// retry (after the process finally exits) completes idempotently.
func TestHardDeletePropagatesLiveDriverTeardownFailureThenRetries(t *testing.T) {
	sm := newTeardownTestManager(t, 20*time.Millisecond)
	driver := newWedgeDriver(false) // ignores TERM and KILL

	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning, InPlace: true,
		PID: 4242, PIDStartTime: 7,
	}
	sm.sessions["braw"] = driver

	if err := sm.Delete("braw"); err == nil {
		t.Fatal("Delete must fail when the live driver does not terminate")
	}

	sm.mu.RLock()
	s, kept := sm.state.Sessions["braw"]
	_, hasPTY := sm.sessions["braw"]
	sm.mu.RUnlock()

	if !kept {
		t.Fatal("session removed despite an unterminated process — live orphan risk")
	}

	if !hasPTY {
		t.Fatal("driver was not restored into sm.sessions for retry")
	}

	if s.Status != StatusErrored {
		t.Errorf("status = %q, want %q", s.Status, StatusErrored)
	}

	if s.PID != 4242 || s.PIDStartTime != 7 {
		t.Errorf("PID/start = %d/%d, want restored 4242/7", s.PID, s.PIDStartTime)
	}

	if tombstoneExists(t, sm, "braw") {
		t.Error("tombstone survived an aborted delete")
	}

	if _, _, closes := driver.stats(); closes != 0 {
		t.Errorf("driver Close count = %d, want 0 (never closed while process may be alive)", closes)
	}

	// The process finally dies; the retry completes idempotently.
	driver.markExited()

	if err := sm.Delete("braw"); err != nil {
		t.Fatalf("retry Delete after process exit: %v", err)
	}

	sm.mu.RLock()
	_, stillThere := sm.state.Sessions["braw"]
	_, stillPTY := sm.sessions["braw"]
	sm.mu.RUnlock()

	if stillThere || stillPTY {
		t.Fatal("session not fully removed on successful retry")
	}

	if _, _, closes := driver.stats(); closes != 1 {
		t.Errorf("driver Close count = %d, want 1 (closed only on the successful retry)", closes)
	}
}

// TestBulkDeletePropagatesLiveDriverTeardownFailureThenRetries is the bulk-delete
// regression: a wedged child keeps its own state/driver for retry and surfaces an
// error, while a sibling whose teardown succeeds is still removed.
func TestBulkDeletePropagatesLiveDriverTeardownFailureThenRetries(t *testing.T) {
	sm := newTeardownTestManager(t, 20*time.Millisecond)

	rootDriver := newWedgeDriver(true)   // dies on SIGKILL: teardown succeeds
	childDriver := newWedgeDriver(false) // wedged: teardown fails

	sm.state.Sessions["ceilidh"] = &SessionState{ID: "ceilidh", Name: "ceilidh", Status: StatusRunning, InPlace: true}
	sm.state.Sessions["bairn"] = &SessionState{ID: "bairn", Name: "bairn", Status: StatusRunning, InPlace: true, ParentID: "ceilidh", PID: 4242, PIDStartTime: 7}
	sm.sessions["ceilidh"] = rootDriver
	sm.sessions["bairn"] = childDriver

	deleted, err := sm.DeleteWithChildren("ceilidh", false)
	if err == nil {
		t.Fatal("DeleteWithChildren must error when a child's live driver does not terminate")
	}

	sm.mu.RLock()
	_, rootThere := sm.state.Sessions["ceilidh"]
	child, childThere := sm.state.Sessions["bairn"]
	_, childPTY := sm.sessions["bairn"]
	sm.mu.RUnlock()

	if rootThere {
		t.Error("root whose teardown succeeded was not removed")
	}

	if !childThere || !childPTY {
		t.Fatal("wedged child was not kept with its driver for retry — live orphan risk")
	}

	if child.Status != StatusErrored {
		t.Errorf("child status = %q, want %q", child.Status, StatusErrored)
	}

	if child.PID != 4242 {
		t.Errorf("child PID = %d, want restored 4242", child.PID)
	}

	for _, id := range deleted {
		if id == "bairn" {
			t.Fatal("wedged child reported as deleted")
		}
	}

	// Child dies; retry finishes it.
	childDriver.markExited()

	if _, err := sm.DeleteWithChildren("bairn", false); err != nil {
		t.Fatalf("retry DeleteWithChildren after child exit: %v", err)
	}

	sm.mu.RLock()
	_, childStill := sm.state.Sessions["bairn"]
	sm.mu.RUnlock()

	if childStill {
		t.Error("child not removed on successful retry")
	}
}

// TestBulkDeleteRestoresExactStateOnTombstoneWriteFailure proves that when a
// session's recovery marker cannot be written, its process is never signalled and
// it is restored to its EXACT prior state — status, driver, and attached client —
// so a bulk delete never tears down (or downgrades) a session it failed to
// tombstone (issue #1326).
func TestBulkDeleteRestoresExactStateOnTombstoneWriteFailure(t *testing.T) {
	sm := newTeardownTestManager(t, 20*time.Millisecond)
	sm.writeTombstoneFault = func(string) error { return errors.New("dir fsync failed") }

	driver := newWedgeDriver(false)
	sm.state.Sessions["strath"] = &SessionState{ID: "strath", Name: "strath", Status: StatusRunning, InPlace: true, PID: 4242, PIDStartTime: 7}
	sm.sessions["strath"] = driver

	var kicks int32

	sm.attachedClients["strath"] = &attachedClient{kick: func() { atomic.AddInt32(&kicks, 1) }}

	_, err := sm.DeleteWithChildren("strath", false)
	if err == nil {
		t.Fatal("DeleteWithChildren must error when the recovery marker cannot be written")
	}

	sm.mu.RLock()
	s := sm.state.Sessions["strath"]
	_, hasPTY := sm.sessions["strath"]
	_, hasClient := sm.attachedClients["strath"]
	sm.mu.RUnlock()

	if s == nil || s.Status != StatusRunning {
		t.Fatalf("session not restored to Running: %+v", s)
	}

	if s.PID != 4242 || s.PIDStartTime != 7 {
		t.Errorf("PID/start = %d/%d, want untouched 4242/7", s.PID, s.PIDStartTime)
	}

	if !hasPTY || !hasClient {
		t.Error("driver/client not restored after a tombstone-write failure")
	}

	if k, f, c := driver.stats(); k != 0 || f != 0 || c != 0 {
		t.Errorf("driver was signalled (kills=%d forces=%d closes=%d) despite an untouched process", k, f, c)
	}

	if n := atomic.LoadInt32(&kicks); n != 0 {
		t.Errorf("client kicked %d times, want 0 (process never touched)", n)
	}

	if tombstoneExists(t, sm, "strath") {
		t.Error("stray tombstone left behind after a write failure")
	}
}

// TestHardDeleteKeepsMarkerWhenRetrySaveFails proves the single-delete abort path
// retains the recovery marker (and surfaces the error) when the restored retry
// state cannot be persisted: the marker is then the only durable record of the
// still-live PID (issue #1326).
func TestHardDeleteKeepsMarkerWhenRetrySaveFails(t *testing.T) {
	sm := newTeardownTestManager(t, 20*time.Millisecond)
	driver := newWedgeDriver(false)

	sm.state.Sessions["dreich"] = &SessionState{ID: "dreich", Name: "dreich", Status: StatusRunning, InPlace: true, PID: 4242, PIDStartTime: 7}
	sm.sessions["dreich"] = driver

	// Fail every save so the abort path's restore save fails, forcing the marker
	// to be retained.
	sm.saveStateFault = func() error { return errors.New("disk full") }

	err := sm.Delete("dreich")
	if err == nil {
		t.Fatal("Delete must error when the live driver does not terminate")
	}

	if !tombstoneExists(t, sm, "dreich") {
		t.Error("recovery marker was dropped despite a failed retry-state save — a crash would forget the live PID")
	}
}

// TestBulkDeleteKeepsMarkerWhenRetrySaveFails is the bulk analogue: a wedged child
// whose re-armed retry state cannot be saved keeps its recovery marker.
func TestBulkDeleteKeepsMarkerWhenRetrySaveFails(t *testing.T) {
	sm := newTeardownTestManager(t, 20*time.Millisecond)
	driver := newWedgeDriver(false)

	sm.state.Sessions["thrawn"] = &SessionState{ID: "thrawn", Name: "thrawn", Status: StatusRunning, InPlace: true, PID: 4242, PIDStartTime: 7}
	sm.sessions["thrawn"] = driver

	sm.saveStateFault = func() error { return errors.New("disk full") }

	if _, err := sm.DeleteWithChildren("thrawn", false); err == nil {
		t.Fatal("DeleteWithChildren must error when the live driver does not terminate")
	}

	if !tombstoneExists(t, sm, "thrawn") {
		t.Error("recovery marker was dropped despite a failed retry-state save")
	}
}

// TestRemoveTombstonePropagatesDirSyncFailure proves the durable removal surfaces
// a parent-dir fsync failure and leaves the marker in place, so an abort/retry
// caller cannot mistake a non-durable unlink for a clean removal (issue #1326).
func TestRemoveTombstonePropagatesDirSyncFailure(t *testing.T) {
	sm := newTestSessionManager(t)

	if err := sm.writeTombstone(tombstone{teardownSpec: teardownSpec{ID: "croft"}, Name: "croft", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("writeTombstone: %v", err)
	}

	sm.tombstoneDirSyncFault = func() error { return errors.New("dir fsync failed") }

	if err := sm.removeTombstone("croft"); err == nil {
		t.Fatal("removeTombstone must propagate a parent-dir fsync failure")
	}

	// A missing tombstone is still idempotent success even with the fault armed:
	// os.Remove short-circuits on IsNotExist before the fsync.
	if err := sm.removeTombstone("nonexistent"); err != nil {
		t.Errorf("removeTombstone of a missing marker = %v, want nil", err)
	}
}
