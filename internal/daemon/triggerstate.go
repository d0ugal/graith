package daemon

import (
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/cronx"
	"github.com/fsnotify/fsnotify"
)

// triggerState is the daemon-owned, in-memory runtime state for triggers. Its
// mutex is independent of sm.mu (like prWatchState) so slow action dispatch
// never blocks gr list. Persisted per-definition facts live in
// State.TriggerRuntime; everything here is derived and rebuilt on start/reload.
type triggerState struct {
	mu sync.Mutex

	// schedule source (keyed by trigger name)
	cron     map[string]cronx.Schedule // parsed cron schedule (cron triggers only)
	nextFire map[string]time.Time      // in-memory next-fire cursor
	inFlight map[string]int            // per-definition in-flight fire count (overlap guard)

	// rate-limit log, keyed by name (schedule) or binding key (watch)
	rateLog map[string][]time.Time

	// running is the count of in-flight action dispatches, bounded by
	// [triggers] max_concurrent.
	running int

	// watch source: bindings keyed by bindingKey(name, sessionID)
	bindings map[string]*watchBinding

	// gcx source: one poll binding per trigger definition. The durable seen-ID
	// cursor lives in TriggerRuntimeState; these process-local fields deliberately
	// reset on daemon start so catch_up=false can prime the current snapshot.
	gcxBindings map[string]*gcxBinding

	// tracker action: when an issue's session was first seen with its issue no
	// longer active, keyed by trackerGraceKey(name, issueKey). Drives the reap
	// grace window. In-memory only: losing it on restart is fail-safe (re-observe
	// → restart the grace clock → reap later, never sooner).
	trackerObsolete map[string]time.Time
}

// watchBinding is one (watch trigger, bound source session) pair. Not persisted:
// rebuilt from live sessions on reconcile.
type watchBinding struct {
	bmu                sync.Mutex // guards changed/debounce/inFlight (per-binding)
	triggerName        string
	sessionID          string
	worktree           string
	fingerprint        string // definition fingerprint; a change recreates the binding
	builtinFingerprint string // resolved daemon-wide watch-ignore policy fingerprint; a change recreates the binding
	watcher            *fsnotify.Watcher
	debounce           *time.Timer
	changed            map[string]bool // coalesced changed paths since last fire
	inFlight           bool            // command action in flight for this binding
	reactorID          string          // ensure-reviewer session owned by this binding
	degraded           string          // watcher failure/limit reason ("" once healthy)
	retryCount         int             // consecutive degraded (re)creation attempts (drives backoff)
	nextRetryAt        time.Time       // when a degraded binding is next retried (zero when healthy)
	canceled           bool            // set on teardown; a pending debounce callback checks it
	cancel             func()          // stops the binding's event goroutine
}

type gcxBinding struct {
	fingerprint string
	nextPoll    time.Time
	inFlight    bool
	prime       bool
	onCallKnown bool
	onCall      bool
}

// actionInFlight reports whether a serialised action (command or
// ensure-reviewer) is currently executing for this binding. Reconcile consults
// it to avoid recreating a binding out from under an in-flight reserve→create.
func (b *watchBinding) actionInFlight() bool {
	b.bmu.Lock()
	defer b.bmu.Unlock()

	return b.inFlight
}

func newTriggerState() *triggerState {
	return &triggerState{
		cron:            make(map[string]cronx.Schedule),
		nextFire:        make(map[string]time.Time),
		inFlight:        make(map[string]int),
		rateLog:         make(map[string][]time.Time),
		bindings:        make(map[string]*watchBinding),
		gcxBindings:     make(map[string]*gcxBinding),
		trackerObsolete: make(map[string]time.Time),
	}
}

// bindingKey uniquely identifies a (trigger, source session) binding.
func bindingKey(triggerName, sessionID string) string {
	return triggerName + "\x00" + sessionID
}

// trackerGraceKey uniquely identifies a (tracker trigger, issue) pair for the
// reap grace window.
func trackerGraceKey(triggerName, issueKey string) string {
	return triggerName + "\x00" + issueKey
}
