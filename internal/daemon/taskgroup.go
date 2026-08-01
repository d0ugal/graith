package daemon

import (
	"context"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
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
	nextID   uint64
	active   map[uint64]daemonTaskGroupTask
}

func newDaemonTaskGroup() *daemonTaskGroup {
	ctx, cancel := context.WithCancel(context.Background())

	return &daemonTaskGroup{ctx: ctx, cancel: cancel, start: make(chan struct{})}
}

type daemonTaskGroupTask struct {
	Name      string
	StartedAt time.Time
}

type daemonTaskGroupTaskSnapshot struct {
	ID        uint64
	Name      string
	StartedAt time.Time
	Age       time.Duration
}

func (g *daemonTaskGroup) Go(fn func(context.Context)) bool {
	return g.goNamed(daemonTaskGroupFunctionName(fn), fn)
}

func (g *daemonTaskGroup) goNamed(name string, fn func(context.Context)) bool {
	if fn == nil {
		return false
	}

	if name == "" {
		name = "background-task"
	}

	g.mu.Lock()
	if g.draining {
		g.mu.Unlock()
		return false
	}

	g.nextID++
	id := g.nextID
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

		done := g.markActive(id, name)
		defer done()

		fn(g.ctx)
	}()

	return true
}

func daemonTaskGroupFunctionName(fn func(context.Context)) string {
	name := "background-task"

	value := reflect.ValueOf(fn)
	if !value.IsValid() {
		return name
	}

	runtimeFn := runtime.FuncForPC(value.Pointer())
	if runtimeFn == nil {
		return name
	}

	name = runtimeFn.Name()
	if i := strings.LastIndexByte(name, '/'); i >= 0 && i+1 < len(name) {
		name = name[i+1:]
	}

	return name
}

func (g *daemonTaskGroup) markActive(id uint64, name string) func() {
	g.mu.Lock()
	if g.active == nil {
		g.active = make(map[uint64]daemonTaskGroupTask)
	}

	g.active[id] = daemonTaskGroupTask{Name: name, StartedAt: time.Now()}
	g.mu.Unlock()

	return func() {
		g.mu.Lock()
		delete(g.active, id)
		g.mu.Unlock()
	}
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

func (g *daemonTaskGroup) ActiveTasks(now time.Time) []daemonTaskGroupTaskSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()

	tasks := make([]daemonTaskGroupTaskSnapshot, 0, len(g.active))
	for id, task := range g.active {
		age := time.Duration(0)
		if !task.StartedAt.IsZero() {
			age = now.Sub(task.StartedAt)
			if age < 0 {
				age = 0
			}
		}

		tasks = append(tasks, daemonTaskGroupTaskSnapshot{
			ID:        id,
			Name:      task.Name,
			StartedAt: task.StartedAt,
			Age:       age,
		})
	}

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	return tasks
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

	return group.goNamed(daemonTaskGroupFunctionName(fn), func(groupCtx context.Context) {
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
