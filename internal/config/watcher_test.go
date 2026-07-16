package config

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatcher(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	initial := `default_agent = "claude"` + "\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	got := make(chan *Config, 1)
	w := NewWatcher(cfgPath, func(cfg *Config) error {
		got <- cfg
		return nil
	}, slog.Default(), 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = w.Run(ctx)
	}()

	// Give watcher time to start
	time.Sleep(100 * time.Millisecond)

	updated := `default_agent = "codex"` + "\n"
	if err := os.WriteFile(cfgPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case cfg := <-got:
		if cfg.DefaultAgent != "codex" {
			t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, "codex")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for config reload")
	}
}

func TestWatcherInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	initial := `default_agent = "claude"` + "\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	called := make(chan struct{}, 1)
	w := NewWatcher(cfgPath, func(cfg *Config) error {
		called <- struct{}{}
		return nil
	}, slog.Default(), 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = w.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Write invalid TOML — callback should not fire
	if err := os.WriteFile(cfgPath, []byte("{{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case <-called:
		t.Fatal("callback should not fire for invalid config")
	case <-time.After(500 * time.Millisecond):
		// Expected: no callback for invalid config
	}
}

func TestWatcherReportsApplyFailureWithoutLoggingSuccess(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`default_agent = "claude"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	w := NewWatcher(cfgPath, func(*Config) error {
		return errors.New("dreich runtime replacement")
	}, slog.New(slog.NewTextHandler(&logs, nil)), time.Millisecond)
	w.reload()

	got := logs.String()
	if !strings.Contains(got, "failed to apply reloaded config") || !strings.Contains(got, "dreich runtime replacement") {
		t.Errorf("apply failure log = %q", got)
	}
	if strings.Contains(got, "config reloaded") {
		t.Errorf("apply failure was also logged as success: %q", got)
	}
}
