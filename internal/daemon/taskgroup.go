package daemon

import (
	"context"
	"sync"
)

// daemonTaskGroup owns a complete background generation, including work
// recursively spawned by its top-level loops. Admission and drain share one
// mutex so Wait never races a positive Add and no descendant can escape after
// cancellation begins.
type daemonTaskGroup struct {
	ctx      context.Context
	cancel   context.CancelFunc
	start    chan struct{}
	startOne sync.Once

	mu       sync.Mutex
	draining bool
	wg       sync.WaitGroup
}

func newDaemonTaskGroup() *daemonTaskGroup {
	ctx, cancel := context.WithCancel(context.Background())

	return &daemonTaskGroup{ctx: ctx, cancel: cancel, start: make(chan struct{})}
}

func (g *daemonTaskGroup) Go(fn func(context.Context)) bool {
	if fn == nil {
		return false
	}

	g.mu.Lock()
	if g.draining {
		g.mu.Unlock()
		return false
	}

	g.wg.Add(1)
	g.mu.Unlock()

	go func() {
		defer g.wg.Done()

		select {
		case <-g.start:
		case <-g.ctx.Done():
			return
		}

		if g.ctx.Err() != nil {
			return
		}

		fn(g.ctx)
	}()

	return true
}

func (g *daemonTaskGroup) Activate() {
	g.startOne.Do(func() { close(g.start) })
}

func (g *daemonTaskGroup) BeginDrain() {
	g.mu.Lock()
	if !g.draining {
		g.draining = true
		g.cancel()
	}
	g.mu.Unlock()
}

func (g *daemonTaskGroup) Wait(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		g.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sm *SessionManager) installBackgroundTasks(group *daemonTaskGroup) bool {
	sm.backgroundTasksMu.Lock()
	defer sm.backgroundTasksMu.Unlock()

	if sm.backgroundTasks != nil {
		return false
	}

	sm.backgroundManaged = true
	sm.backgroundTasks = group

	return true
}

func (sm *SessionManager) clearBackgroundTasks(group *daemonTaskGroup) {
	sm.backgroundTasksMu.Lock()
	if sm.backgroundTasks == group {
		sm.backgroundTasks = nil
	}
	sm.backgroundTasksMu.Unlock()
}

func (sm *SessionManager) finishBackgroundPublication(published bool, after func()) {
	if !published {
		return
	}

	if hook := sm.afterBackgroundPublication; hook != nil {
		hook()
	}

	if after != nil {
		after()
	}
}

// startBackgroundTask attaches a descendant to the active daemon generation.
// Managers constructed directly by unit tests retain the historical async
// behavior; once Run installs generation ownership, a missing/draining group
// rejects new work rather than launching it untracked.
func (sm *SessionManager) startBackgroundTask(ctx context.Context, fn func(context.Context)) bool {
	sm.backgroundTasksMu.Lock()
	group := sm.backgroundTasks
	managed := sm.backgroundManaged
	sm.backgroundTasksMu.Unlock()

	if group == nil {
		if managed {
			return false
		}

		go fn(ctx)

		return true
	}

	return group.Go(func(groupCtx context.Context) {
		if ctx == nil {
			fn(groupCtx)
			return
		}

		combined, cancel := context.WithCancel(groupCtx)

		stop := context.AfterFunc(ctx, cancel)
		defer func() {
			stop()
			cancel()
		}()

		fn(combined)
	})
}
