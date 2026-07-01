package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	path     string
	onChange func(*Config)
	log      *slog.Logger
}

func NewWatcher(path string, onChange func(*Config), log *slog.Logger) *Watcher {
	return &Watcher{
		path:     path,
		onChange: onChange,
		log:      log,
	}
}

func (w *Watcher) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = watcher.Close() }()

	dir := filepath.Dir(w.path)
	if err := watcher.Add(dir); err != nil {
		return err
	}

	var debounce *time.Timer

	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}

			return ctx.Err()

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			if filepath.Clean(event.Name) != filepath.Clean(w.path) {
				continue
			}

			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}

			if debounce != nil {
				debounce.Stop()
			}

			debounce = time.AfterFunc(200*time.Millisecond, func() {
				w.reload()
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}

			w.log.Error("config watcher error", "err", err)
		}
	}
}

func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		w.log.Error("failed to reload config", "err", err)
		return
	}

	w.log.Info("config reloaded", "path", w.path)
	w.onChange(cfg)
}
