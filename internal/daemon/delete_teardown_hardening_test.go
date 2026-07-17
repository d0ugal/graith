package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
)

// markExited unblocks a wedged teardownFakeDriver, standing in for the process
// finally dying so a retry teardown observes Exited() and completes.
func (d *teardownFakeDriver) markExited() {
	d.doneOnce.Do(func() { close(d.done) })
}

// injectingTeardownDriver runs a one-shot injection the first time it is killed,
// standing in for a child agent that spawns a new session mid-delete so the bulk
// sweep (not the initial pass) discovers it.
type injectingTeardownDriver struct {
	*teardownFakeDriver

	once   sync.Once
	inject func()
}

func (d *injectingTeardownDriver) Kill() error {
	d.once.Do(d.inject)

	return d.teardownFakeDriver.Kill()
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}

	return false
}

// TestHardDeletePropagatesLiveDriverTeardownFailureThenRetries is the single-
// delete regression for issue #1326. A driver that ignores both SIGTERM and
// SIGKILL (Done never closes) must abort the delete rather than tombstone-commit
// and tear down the worktree while the process may be alive: the session is kept
// errored with its driver and PID restored, no tombstone survives, and a later
// retry (after the process finally exits) completes the delete.
func TestHardDeletePropagatesLiveDriverTeardownFailureThenRetries(t *testing.T) {
	const grace = 20 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)
	driver := newTeardownFakeDriver(false) // ignores TERM and KILL
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning, InPlace: true,
		PID: 4242, PIDStartTime: 7,
	}
	sm.sessions["braw"] = driver

	err := sm.Delete("braw")
	if err == nil {
		t.Fatal("Delete must fail when the live driver does not terminate")
	}

	sm.mu.RLock()
	s, kept := sm.state.Sessions["braw"]
	_, hasPTY := sm.sessions["braw"]
	sm.mu.RUnlock()

	if !kept {
		t.Fatal("session was removed despite an unterminated process — live orphan risk")
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

	if _, statErr := os.Stat(sm.tombstonePath("braw")); !os.IsNotExist(statErr) {
		t.Errorf("tombstone survived an aborted delete: %v", statErr)
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

	if _, _, closes, _ := driver.teardownStats(); closes != 1 {
		t.Errorf("driver Close count = %d, want 1 (closed only on the successful retry)", closes)
	}
}

// TestBulkDeletePropagatesLiveDriverTeardownFailureThenRetries is the bulk-delete
// regression for issue #1326: a wedged child keeps its own artifacts for retry
// and surfaces an error, while a sibling whose teardown succeeds is still removed.
func TestBulkDeletePropagatesLiveDriverTeardownFailureThenRetries(t *testing.T) {
	const grace = 20 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)

	rootDriver := newTeardownFakeDriver(true)   // dies on SIGKILL: teardown succeeds
	childDriver := newTeardownFakeDriver(false) // wedged: teardown fails

	sm.state.Sessions["ceilidh"] = &SessionState{
		ID: "ceilidh", Name: "ceilidh", Status: StatusRunning, InPlace: true,
	}
	sm.state.Sessions["bairn"] = &SessionState{
		ID: "bairn", Name: "bairn", Status: StatusRunning, InPlace: true,
		ParentID: "ceilidh", PID: 555, PIDStartTime: 9,
	}
	sm.sessions["ceilidh"] = rootDriver
	sm.sessions["bairn"] = childDriver

	deleted, err := sm.DeleteWithChildren("ceilidh", false)
	if err == nil || !strings.Contains(err.Error(), "bairn") {
		t.Fatalf("DeleteWithChildren err = %v, want a kept-for-retry error naming bairn", err)
	}

	if len(deleted) != 1 || deleted[0] != "ceilidh" {
		t.Fatalf("deleted = %v, want only the successfully-torn-down root", deleted)
	}

	sm.mu.RLock()
	_, rootPresent := sm.state.Sessions["ceilidh"]
	child, childKept := sm.state.Sessions["bairn"]
	_, childHasPTY := sm.sessions["bairn"]
	sm.mu.RUnlock()

	if rootPresent {
		t.Error("root with a successful teardown should have been removed")
	}

	if !childKept || !childHasPTY {
		t.Fatal("wedged child must be kept with its driver restored for retry")
	}

	if child.Status != StatusErrored || child.PID != 555 {
		t.Errorf("child kept as %q PID %d, want errored PID 555", child.Status, child.PID)
	}

	// The child process dies; deleting it now succeeds.
	childDriver.markExited()

	if err := sm.Delete("bairn"); err != nil {
		t.Fatalf("retry Delete of the child: %v", err)
	}

	sm.mu.RLock()
	_, childStill := sm.state.Sessions["bairn"]
	sm.mu.RUnlock()

	if childStill {
		t.Fatal("child not removed on successful retry")
	}
}

// TestBulkDeleteKillsLateOrphanBeforeTeardown covers the sweep-path orphan branch
// (issue #1326): a late-arriving descendant with a live PID but no live driver
// must be verified-killed before its worktree is torn down, rather than silently
// proceeding to artifact teardown with the process still alive.
func TestBulkDeleteKillsLateOrphanBeforeTeardown(t *testing.T) {
	const grace = 20 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)

	// A real reapable process stands in for the late orphan so killVerifiedProcess
	// actually signals it.
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	rootDriver := &injectingTeardownDriver{
		teardownFakeDriver: newTeardownFakeDriver(true),
		inject: func() {
			// Fires during the root's teardown in the main kill loop, before the
			// sweep runs and rebuilds its descendant set.
			sm.mu.Lock()
			sm.state.Sessions["glen"] = &SessionState{
				ID: "glen", Name: "glen", Status: StatusRunning, InPlace: true,
				ParentID: "strath", PID: pid, PIDStartTime: start,
			}
			sm.mu.Unlock()
		},
	}

	sm.state.Sessions["strath"] = &SessionState{
		ID: "strath", Name: "strath", Status: StatusRunning, InPlace: true,
	}
	sm.sessions["strath"] = rootDriver

	deleted, err := sm.DeleteWithChildren("strath", false)
	if err != nil {
		t.Fatalf("DeleteWithChildren: %v", err)
	}

	if !containsID(deleted, "glen") {
		t.Errorf("late orphan glen not in deleted set %v", deleted)
	}

	if isProcessAlive(pid) {
		t.Error("late-orphan process still alive after bulk delete — worktree torn down without a kill")
	}
}

// TestBulkDeleteKeepsLateOrphanWhenKillFails covers the sweep orphan branch's
// failure path (issue #1326): when a late descendant's process cannot be killed
// (unverifiable identity), the session is kept — errored, PID/worktree intact —
// and the bulk delete returns a non-nil error naming it, rather than tearing down
// its worktree and reporting success.
func TestBulkDeleteKeepsLateOrphanWhenKillFails(t *testing.T) {
	const grace = 20 * time.Millisecond

	sm := newLiveDriverLifecycleTestManager(t, grace)

	// A live, reapable process whose recorded start time is deliberately wrong, so
	// killVerifiedProcess refuses to signal it (identity mismatch) and the kill
	// "fails" without our leaving a real orphan behind.
	pid := spawnReapableSleeper(t)
	worktree := t.TempDir()

	rootDriver := &injectingTeardownDriver{
		teardownFakeDriver: newTeardownFakeDriver(true),
		inject: func() {
			sm.mu.Lock()
			sm.state.Sessions["glen"] = &SessionState{
				ID: "glen", Name: "glen", Status: StatusRunning,
				ParentID: "strath", PID: pid, PIDStartTime: 1, // wrong identity → kill refused
				WorktreePath: worktree,
			}
			sm.mu.Unlock()
		},
	}

	sm.state.Sessions["strath"] = &SessionState{
		ID: "strath", Name: "strath", Status: StatusRunning, InPlace: true,
	}
	sm.sessions["strath"] = rootDriver

	deleted, err := sm.DeleteWithChildren("strath", false)
	if err == nil || !strings.Contains(err.Error(), "glen") {
		t.Fatalf("DeleteWithChildren err = %v, want a non-nil error naming the un-killable late orphan glen", err)
	}

	if containsID(deleted, "glen") {
		t.Errorf("late orphan glen must not be reported deleted when its kill failed: %v", deleted)
	}

	sm.mu.RLock()
	glen, kept := sm.state.Sessions["glen"]
	sm.mu.RUnlock()

	if !kept {
		t.Fatal("un-killable late orphan must be kept for retry, not removed")
	}

	if glen.Status != StatusErrored || glen.PID != pid {
		t.Errorf("kept orphan = %q PID %d, want errored with PID %d preserved", glen.Status, glen.PID, pid)
	}

	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Errorf("worktree of an un-killable orphan was torn down: %v", statErr)
	}
}

// faultOnForceDriver flips a hook the first time it is force-killed, letting a
// test wedge the teardown and, at that exact point, arm a saveState fault so the
// subsequent retry-state persist fails.
type faultOnForceDriver struct {
	*teardownFakeDriver

	once    sync.Once
	onForce func()
}

func (d *faultOnForceDriver) ForceKill() error {
	d.once.Do(d.onForce)

	return d.teardownFakeDriver.ForceKill()
}

// TestBulkDeleteRestoresExactStateOnTombstoneWriteFailure proves the pre-teardown
// fail-closed path (issue #1326): if the recovery tombstone cannot be written the
// process is never signalled, so the session is restored to its EXACT prior state
// (Running, driver and client re-armed) and the bulk delete returns an error
// naming the failure rather than tearing anything down.
func TestBulkDeleteRestoresExactStateOnTombstoneWriteFailure(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)
	driver := newTeardownFakeDriver(true)
	sm.state.Sessions["ceilidh"] = &SessionState{
		ID: "ceilidh", Name: "ceilidh", Status: StatusRunning, InPlace: true,
	}
	sm.sessions["ceilidh"] = driver

	// Block every tombstone write: a regular file where the tombstone dir must be
	// makes writeTombstone's MkdirAll fail.
	if err := os.WriteFile(sm.tombstoneDir(), []byte("block"), 0o600); err != nil {
		t.Fatalf("plant tombstone-dir blocker: %v", err)
	}

	deleted, err := sm.DeleteWithChildren("ceilidh", false)
	if err == nil || !strings.Contains(err.Error(), "tombstone") {
		t.Fatalf("DeleteWithChildren err = %v, want a tombstone-write failure", err)
	}

	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want nothing torn down", deleted)
	}

	sm.mu.RLock()
	s, kept := sm.state.Sessions["ceilidh"]
	_, hasPTY := sm.sessions["ceilidh"]
	sm.mu.RUnlock()

	if !kept || !hasPTY {
		t.Fatal("session must be kept with its driver re-armed after a tombstone-write failure")
	}

	if s.Status != StatusRunning {
		t.Errorf("status = %q, want the exact prior Running (process untouched)", s.Status)
	}

	if kills, forces, closes, _ := driver.teardownStats(); kills != 0 || forces != 0 || closes != 0 {
		t.Errorf("process was signalled (TERM=%d KILL=%d Close=%d); it must be untouched on tombstone-write failure", kills, forces, closes)
	}
}

// TestBulkDeleteTombstonePhaseIsCrashRecoverable proves the crash-safety invariant
// at the bulk-teardown boundary (issue #1326): once tombstoneBeforeBulkTeardown
// runs, the PID is cleared from live state but a durable tombstone carries it and
// the worktree is untouched, so a crash there is fully recovered by the real
// startup path — the orphan is reaped and the worktree removed. The initial and
// late-sweep paths share this helper, so this covers both.
func TestBulkDeleteTombstonePhaseIsCrashRecoverable(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)

	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	worktree := filepath.Join(sm.paths.DataDir, "worktrees", "bothy", "hash", "glen")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	sm.state.Sessions["glen"] = &SessionState{
		ID: "glen", Name: "glen", Status: StatusDeleting,
		WorktreePath: worktree, PID: pid, PIDStartTime: start,
	}

	sm.mu.Lock()
	_ = sm.saveState()
	sm.mu.Unlock()

	snap := bulkDeleteSnapshot{id: "glen", name: "glen", worktreePath: worktree, pid: pid, pidStartTime: start}
	if err := sm.tombstoneBeforeBulkTeardown(snap.teardownSpec(), snap.name, snap.pid, snap.pidStartTime); err != nil {
		t.Fatalf("tombstoneBeforeBulkTeardown: %v", err)
	}

	// Boundary: PID cleared from live state, tombstone carries it, worktree and
	// process untouched (teardown has not run).
	sm.mu.RLock()
	gotPID := sm.state.Sessions["glen"].PID
	sm.mu.RUnlock()

	if gotPID != 0 {
		t.Errorf("live PID = %d, want cleared once the tombstone is durable", gotPID)
	}

	if !isProcessAlive(pid) {
		t.Fatal("process killed at the tombstone boundary; teardown must not have run yet")
	}

	if _, err := os.Stat(worktree); err != nil {
		t.Errorf("worktree removed before recovery: %v", err)
	}

	// Simulate a crash + restart: a fresh manager on the same data dir runs the
	// real startup recovery.
	sm2 := NewSessionManager(sm.cfg, sm.paths, quietLogger())
	if err := sm2.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	sm2.resumeTombstones()

	if isProcessAlive(pid) {
		t.Error("orphan process not reaped by startup recovery")
	}

	if _, ok := sm2.state.Sessions["glen"]; ok {
		t.Error("session not removed by startup recovery")
	}

	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree not removed by startup recovery (err=%v)", err)
	}

	if _, err := os.Stat(sm2.tombstonePath("glen")); !os.IsNotExist(err) {
		t.Error("tombstone not cleared after startup recovery")
	}
}

// TestBulkDeleteKeepsMarkerWhenRetrySaveFails proves the core durability invariant
// (issue #1326): when a live driver will not die AND persisting the restored
// retry-state fails, the recovery marker must be kept — it is then the sole record
// of the still-live PID — and startup recovery can still reap from it. Without the
// gate, the marker would be dropped after the PID was already cleared on disk,
// forgetting the process.
func TestBulkDeleteKeepsMarkerWhenRetrySaveFails(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)

	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	worktree := filepath.Join(sm.paths.DataDir, "worktrees", "bothy", "hash", "glen")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// A wedged driver that arms a saveState fault the moment it is force-killed:
	// the tombstone-clear save (before the kill) succeeds, but the post-kill
	// retry-state save fails.
	driver := &faultOnForceDriver{
		teardownFakeDriver: newTeardownFakeDriver(false),
		onForce: func() {
			sm.saveStateFault = func() error { return errors.New("induced persist failure") }
		},
	}

	sm.state.Sessions["glen"] = &SessionState{
		ID: "glen", Name: "glen", Status: StatusRunning,
		WorktreePath: worktree, PID: pid, PIDStartTime: start,
	}
	sm.sessions["glen"] = driver

	_, err = sm.DeleteWithChildren("glen", false)
	if err == nil || !strings.Contains(err.Error(), "persist") {
		t.Fatalf("DeleteWithChildren err = %v, want a surfaced persistence failure", err)
	}

	// The marker must survive: the on-disk state shows the cleared PID, so the
	// tombstone is the only durable record of the live process.
	if _, statErr := os.Stat(sm.tombstonePath("glen")); statErr != nil {
		t.Fatalf("recovery marker was dropped despite a failed retry-state save: %v", statErr)
	}

	// Startup recovery on a fresh manager reaps the orphan from the retained marker.
	sm.saveStateFault = nil

	sm2 := NewSessionManager(sm.cfg, sm.paths, quietLogger())
	if err := sm2.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	sm2.resumeTombstones()

	if isProcessAlive(pid) {
		t.Error("orphan process not reaped by startup recovery from the retained marker")
	}

	if _, ok := sm2.state.Sessions["glen"]; ok {
		t.Error("session not removed by startup recovery")
	}
}

// TestHardDeleteKeepsMarkerWhenRetrySaveFails is the single-delete counterpart of
// TestBulkDeleteKeepsMarkerWhenRetrySaveFails (issue #1326): if the live driver
// will not die and persisting the restored PID fails, the recovery marker must be
// retained and startup recovery must reap from it.
func TestHardDeleteKeepsMarkerWhenRetrySaveFails(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)

	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	worktree := filepath.Join(sm.paths.DataDir, "worktrees", "bothy", "hash", "braw")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	driver := &faultOnForceDriver{
		teardownFakeDriver: newTeardownFakeDriver(false),
		onForce: func() {
			sm.saveStateFault = func() error { return errors.New("induced persist failure") }
		},
	}

	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning,
		WorktreePath: worktree, PID: pid, PIDStartTime: start,
	}
	sm.sessions["braw"] = driver

	if err := sm.Delete("braw"); err == nil || !strings.Contains(err.Error(), "persist") {
		t.Fatalf("Delete err = %v, want a surfaced persistence failure", err)
	}

	if _, statErr := os.Stat(sm.tombstonePath("braw")); statErr != nil {
		t.Fatalf("recovery marker dropped despite a failed retry-state save: %v", statErr)
	}

	sm.saveStateFault = nil

	sm2 := NewSessionManager(sm.cfg, sm.paths, quietLogger())
	if err := sm2.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	sm2.resumeTombstones()

	if isProcessAlive(pid) {
		t.Error("orphan not reaped by startup recovery from the retained marker")
	}

	if _, ok := sm2.state.Sessions["braw"]; ok {
		t.Error("session not removed by startup recovery")
	}
}

// TestInterruptedDeleteBeforeMarkerReapsOrphanOnRestart covers the crash-before-
// tombstone window (issue #1326): a session persisted as StatusDeleting with a
// live verified PID but no recovery marker must have its orphaned process reaped
// on the next startup rather than being reverted to a stopped session whose
// process leaks forever.
func TestInterruptedDeleteBeforeMarkerReapsOrphanOnRestart(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)

	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	// The state a crash leaves after marking deleting but before the tombstone.
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusDeleting, PID: pid, PIDStartTime: start,
	}

	sm.mu.Lock()
	_ = sm.saveState()
	sm.mu.Unlock()

	// Restart: a fresh manager runs the real startup recovery sequence.
	sm2 := NewSessionManager(sm.cfg, sm.paths, quietLogger())
	if err := sm2.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	sm2.cleanupOrphanedProcesses()

	if isProcessAlive(pid) {
		t.Error("interrupted-delete orphan was not reaped on restart — live process leaked")
	}

	s, ok := sm2.Get("braw")
	if !ok {
		t.Fatal("session vanished from state")
	}

	if s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped after the orphan was reaped", s.Status)
	}
}

// reapErroredOrphanOnRestart asserts that a session persisted as errored with a
// live verified PID and no driver has that orphan reaped by the real startup
// recovery on a fresh manager — the no-live-orphan-after-restart invariant for a
// delete that failed to kill its process (issue #1326).
func reapErroredOrphanOnRestart(t *testing.T, sm *SessionManager, id string, pid int) {
	t.Helper()

	s, ok := sm.Get(id)
	if !ok || s.Status != StatusErrored || s.PID != pid {
		t.Fatalf("precondition: session %q = %+v, want errored with PID %d", id, s, pid)
	}

	sm2 := NewSessionManager(sm.cfg, sm.paths, quietLogger())
	if err := sm2.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	sm2.cleanupOrphanedProcesses()

	if isProcessAlive(pid) {
		t.Errorf("errored+PID orphan for %q was not reaped on restart — live process leaked", id)
	}
}

// TestErroredOrphanReapedOnRestartAfterSingleDeleteFailure: a single delete whose
// live driver would not die keeps the session errored with its PID; a subsequent
// daemon crash must not leave that process unreaped (issue #1326).
func TestErroredOrphanReapedOnRestartAfterSingleDeleteFailure(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)

	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	driver := newTeardownFakeDriver(false) // wedged: teardown fails, session kept errored
	sm.state.Sessions["braw"] = &SessionState{
		ID: "braw", Name: "braw", Status: StatusRunning, InPlace: true,
		PID: pid, PIDStartTime: start,
	}
	sm.sessions["braw"] = driver

	if err := sm.Delete("braw"); err == nil {
		t.Fatal("Delete should fail when the live driver does not terminate")
	}

	reapErroredOrphanOnRestart(t, sm, "braw", pid)
}

// TestErroredOrphanReapedOnRestartAfterBulkDeleteFailure is the bulk counterpart.
func TestErroredOrphanReapedOnRestartAfterBulkDeleteFailure(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)

	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	sm.state.Sessions["ceilidh"] = &SessionState{
		ID: "ceilidh", Name: "ceilidh", Status: StatusRunning, InPlace: true,
	}
	sm.state.Sessions["bairn"] = &SessionState{
		ID: "bairn", Name: "bairn", Status: StatusRunning, InPlace: true,
		ParentID: "ceilidh", PID: pid, PIDStartTime: start,
	}
	sm.sessions["ceilidh"] = newTeardownFakeDriver(true) // succeeds
	sm.sessions["bairn"] = newTeardownFakeDriver(false)  // wedged: kept errored

	if _, err := sm.DeleteWithChildren("ceilidh", false); err == nil {
		t.Fatal("DeleteWithChildren should fail on the wedged child")
	}

	reapErroredOrphanOnRestart(t, sm, "bairn", pid)
}

// TestBulkDeletePostRenameTombstoneFailureRecovers exercises the true post-rename
// durability failure (issue #1326): the marker lands on disk but the write reports
// a dir-fsync failure AND the subsequent exact-restore save also fails, so the
// session stays persisted as deleting with its PID. The fail-closed path must
// clean the landed marker, leave the process and worktree untouched (nothing was
// killed), and — since the persisted deleting+PID record survives — let startup
// recovery reap the orphan via the interrupted-delete path.
func TestBulkDeletePostRenameTombstoneFailureRecovers(t *testing.T) {
	sm := newLiveDriverLifecycleTestManager(t, 20*time.Millisecond)

	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported: %v", err)
	}

	worktree := filepath.Join(sm.paths.DataDir, "worktrees", "bothy", "hash", "glen")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	sm.state.Sessions["glen"] = &SessionState{
		ID: "glen", Name: "glen", Status: StatusRunning,
		WorktreePath: worktree, PID: pid, PIDStartTime: start,
	}
	sm.sessions["glen"] = newTeardownFakeDriver(true)

	// The marker is written (renamed into place) and only then the write reports a
	// post-rename dir-fsync failure. At that instant, arm a save fault so the
	// exact-restore save also fails — the harshest case, leaving the session
	// persisted as deleting+PID with the (cleaned) marker gone.
	sm.writeTombstoneFault = func(string) error {
		sm.saveStateFault = func() error { return errors.New("induced restore-save failure") }

		return errors.New("induced post-rename dir-fsync failure")
	}

	if _, err := sm.DeleteWithChildren("glen", false); err == nil {
		t.Fatal("DeleteWithChildren should fail on the post-rename tombstone failure")
	}

	// Fail-closed cleanup removed the landed marker.
	if _, statErr := os.Stat(sm.tombstonePath("glen")); !os.IsNotExist(statErr) {
		t.Errorf("post-rename marker not cleaned up: %v", statErr)
	}

	// Nothing was signalled and the worktree is intact (tombstone failed pre-kill).
	if !isProcessAlive(pid) {
		t.Error("process was signalled despite a tombstone-write failure")
	}

	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Errorf("worktree torn down on a tombstone-write failure: %v", statErr)
	}

	// Restart: the persisted deleting+PID record is recovered and the orphan reaped.
	sm.writeTombstoneFault = nil
	sm.saveStateFault = nil

	sm2 := NewSessionManager(sm.cfg, sm.paths, quietLogger())
	if err := sm2.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	sm2.cleanupOrphanedProcesses()

	if isProcessAlive(pid) {
		t.Error("orphan not reaped on restart after a post-rename tombstone failure")
	}
}
