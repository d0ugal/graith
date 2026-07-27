package daemon

import (
	"os"
	"runtime"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type watchEvent struct {
	Name string
	Op   fsnotify.Op

	// Scan asks the trigger layer to rescan Name as a subtree.
	Scan bool

	// LossyScan says the backend may have dropped event detail, so the scan path
	// may note an eligible subtree even when no matching surviving file remains.
	LossyScan bool
}

type watchBackend interface {
	Name() string
	Events() <-chan watchEvent
	Errors() <-chan error
	Add(path string) error
	Remove(path string) error
	WatchList() []string
	Close() error
	Recursive() bool
}

type fsnotifyWatchBackend struct {
	watcher *fsnotify.Watcher
	add     func(string) error

	events chan watchEvent
	errors chan error
	done   chan struct{}
	once   sync.Once
}

func newFSNotifyWatchBackend(w *fsnotify.Watcher, add func(string) error) *fsnotifyWatchBackend {
	if add == nil {
		add = w.Add
	}

	b := &fsnotifyWatchBackend{
		watcher: w,
		add:     add,
		events:  make(chan watchEvent),
		errors:  make(chan error),
		done:    make(chan struct{}),
	}
	go b.pump()

	return b
}

func (b *fsnotifyWatchBackend) Name() string { return "fsnotify" }

func (b *fsnotifyWatchBackend) Events() <-chan watchEvent { return b.events }

func (b *fsnotifyWatchBackend) Errors() <-chan error { return b.errors }

func (b *fsnotifyWatchBackend) Add(path string) error { return b.add(path) }

func (b *fsnotifyWatchBackend) Remove(path string) error { return b.watcher.Remove(path) }

func (b *fsnotifyWatchBackend) WatchList() []string { return b.watcher.WatchList() }

func (b *fsnotifyWatchBackend) Recursive() bool { return false }

func (b *fsnotifyWatchBackend) Close() error {
	var err error

	b.once.Do(func() {
		close(b.done)
		err = b.watcher.Close()
	})

	return err
}

func (b *fsnotifyWatchBackend) pump() {
	defer close(b.events)
	defer close(b.errors)

	for {
		select {
		case <-b.done:
			return
		case ev, ok := <-b.watcher.Events:
			if !ok {
				return
			}

			select {
			case b.events <- watchEvent{Name: ev.Name, Op: ev.Op}:
			case <-b.done:
				return
			}
		case err, ok := <-b.watcher.Errors:
			if !ok {
				return
			}

			select {
			case b.errors <- err:
			case <-b.done:
				return
			}
		}
	}
}

func (sm *SessionManager) newFSNotifyRecursiveWatchBackend(root string, matcher *watchMatcher) (watchBackend, map[string]int, string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, "fsnotify.NewWatcher failed: " + err.Error()
	}

	backend := newFSNotifyWatchBackend(watcher, sm.watchAddFunc(watcher))

	watchPaths := make(map[string]int)
	if degraded := sm.addWatchRecursiveBudgeted(backend, root, matcher, watchPaths); degraded != "" {
		_ = backend.Close()

		return nil, nil, degraded
	}

	return backend, watchPaths, ""
}

func (sm *SessionManager) newWatchBackend(root string, matcher *watchMatcher) (watchBackend, map[string]int, string) {
	if sm.watchBackend != nil {
		return sm.watchBackend(root, matcher)
	}

	if sm.watchAdd != nil {
		return sm.newFSNotifyRecursiveWatchBackend(root, matcher)
	}

	return sm.newDefaultWatchBackend(root, matcher)
}

// watchAddFunc returns the directory-registration function used when building a
// binding's watch set. It normally delegates to the fsnotify watcher; tests
// override sm.watchAdd to simulate an exhausted watch limit.
func (sm *SessionManager) watchAddFunc(w *fsnotify.Watcher) func(string) error {
	if sm.watchAdd != nil {
		return func(path string) error { return sm.watchAdd(w, path) }
	}

	return w.Add
}

// fsnotifyWatchPathCost estimates the backend resources charged for one
// fsnotify directory registration.
func fsnotifyWatchPathCost(path string) int {
	return fsnotifyWatchPathCostForGOOS(runtime.GOOS, path)
}

func fsnotifyWatchPathCostForGOOS(goos, path string) int {
	if goos != "darwin" {
		return 1
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return 1
	}

	return 1 + len(entries)
}
