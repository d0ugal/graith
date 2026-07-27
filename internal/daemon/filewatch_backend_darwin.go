//go:build darwin && cgo

package daemon

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
	"github.com/fsnotify/fsnotify"
)

const fseventsLatency = 200 * time.Millisecond

type fseventsWatchBackend struct {
	stream *fsevents.EventStream

	logicalRoot string
	watchRoot   string
	events      chan watchEvent
	errors      chan error
	done        chan struct{}
	once        sync.Once
}

func (sm *SessionManager) newDefaultWatchBackend(root string, matcher *watchMatcher) (watchBackend, map[string]int, string) {
	backend, path, degraded := newFSEventsWatchBackend(root)
	if degraded != "" {
		return nil, nil, degraded
	}

	if err := sm.reserveWatchPath(path, 1); err != nil {
		_ = backend.Close()

		return nil, nil, err.Error()
	}

	return backend, map[string]int{path: 1}, ""
}

func newFSEventsWatchBackend(root string) (watchBackend, string, string) {
	logicalRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, "", "fsevents root resolve failed: " + err.Error()
	}

	watchRoot := logicalRoot
	if resolved, err := filepath.EvalSymlinks(logicalRoot); err == nil {
		watchRoot = resolved
	}

	b := &fseventsWatchBackend{
		logicalRoot: logicalRoot,
		watchRoot:   watchRoot,
		events:      make(chan watchEvent),
		errors:      make(chan error),
		done:        make(chan struct{}),
	}
	b.stream = &fsevents.EventStream{
		Paths:   []string{watchRoot},
		Events:  make(chan []fsevents.Event, 64),
		Latency: fseventsLatency,
		Flags:   fsevents.FileEvents | fsevents.WatchRoot | fsevents.NoDefer,
	}

	if err := b.stream.Start(); err != nil {
		return nil, "", "fsevents.Start failed: " + err.Error()
	}

	go b.pump()

	return b, logicalRoot, ""
}

func (b *fseventsWatchBackend) Name() string { return "fsevents" }

func (b *fseventsWatchBackend) Events() <-chan watchEvent { return b.events }

func (b *fseventsWatchBackend) Errors() <-chan error { return b.errors }

func (b *fseventsWatchBackend) Add(string) error { return nil }

func (b *fseventsWatchBackend) Remove(string) error { return nil }

func (b *fseventsWatchBackend) WatchList() []string { return []string{b.logicalRoot} }

func (b *fseventsWatchBackend) Recursive() bool { return true }

func (b *fseventsWatchBackend) Close() error {
	b.once.Do(func() {
		close(b.done)
		b.stream.Stop()
	})

	return nil
}

func (b *fseventsWatchBackend) pump() {
	defer close(b.events)
	defer close(b.errors)

	for {
		select {
		case <-b.done:
			return
		case batch, ok := <-b.stream.Events:
			if !ok {
				return
			}

			for _, ev := range batch {
				translated, ok := b.translate(ev)
				if !ok {
					continue
				}

				select {
				case b.events <- translated:
				case <-b.done:
					return
				}
			}
		}
	}
}

func (b *fseventsWatchBackend) translate(ev fsevents.Event) (watchEvent, bool) {
	if ev.Flags&fsevents.HistoryDone != 0 {
		return watchEvent{}, false
	}

	name, ok := b.logicalPath(ev.Path)
	if !ok {
		return watchEvent{}, false
	}

	lossyScan := ev.Flags&(fsevents.MustScanSubDirs|fsevents.UserDropped|fsevents.KernelDropped|fsevents.RootChanged) != 0
	out := watchEvent{
		Name:      name,
		Op:        fsnotify.Write,
		Scan:      lossyScan,
		LossyScan: lossyScan,
	}

	if ev.Flags&fsevents.ItemCreated != 0 {
		out.Op |= fsnotify.Create
	}
	if ev.Flags&fsevents.ItemModified != 0 ||
		ev.Flags&fsevents.ItemInodeMetaMod != 0 ||
		ev.Flags&fsevents.ItemFinderInfoMod != 0 ||
		ev.Flags&fsevents.ItemChangeOwner != 0 ||
		ev.Flags&fsevents.ItemXattrMod != 0 {
		out.Op |= fsnotify.Write
	}
	if ev.Flags&fsevents.ItemRemoved != 0 {
		out.Op |= fsnotify.Remove
	}
	if ev.Flags&fsevents.ItemRenamed != 0 {
		out.Op |= fsnotify.Rename
		if ev.Flags&fsevents.ItemIsDir != 0 {
			out.Scan = true
		}
	}

	return out, true
}

func (b *fseventsWatchBackend) logicalPath(name string) (string, bool) {
	clean := filepath.Clean(name)
	if !filepath.IsAbs(clean) {
		clean = string(filepath.Separator) + clean
	}

	if rel, ok := pathWithin(b.watchRoot, clean); ok {
		return filepath.Join(b.logicalRoot, rel), true
	}

	if _, ok := pathWithin(b.logicalRoot, clean); ok {
		return clean, true
	}

	return "", false
}

func pathWithin(root, name string) (string, bool) {
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return "", false
	}

	return rel, rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
