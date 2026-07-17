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

func TestWatcherDoesNotReportReloadWhenApplyFails(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configFile, []byte("default_agent = \"canny\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer

	wantErr := errors.New("orchestrator stop failed")
	w := NewWatcher(configFile, func(*Config) error {
		return wantErr
	}, slog.New(slog.NewTextHandler(&logBuf, nil)), 0)

	w.reload()

	logText := logBuf.String()
	if !strings.Contains(logText, "failed to apply reloaded config") || !strings.Contains(logText, wantErr.Error()) {
		t.Fatalf("apply failure log = %q, want callback error", logText)
	}

	if strings.Contains(logText, "config reloaded") {
		t.Fatalf("failed callback was reported as a successful reload: %q", logText)
	}
}

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

// TestWatcherConversationMaxLimitReloadAndRollback exercises the issue #1314 fix
// over the live reload path: lowering conversation_max_limit alone reloads and
// clamps the effective page size, while a subsequent explicit contradictory
// page-size override is rejected — leaving the previously-applied config in place
// (rollback), exactly as an invalid reload does.
func TestWatcherConversationMaxLimitReloadAndRollback(t *testing.T) {
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

	time.Sleep(100 * time.Millisecond)

	// Reload: lowering the max alone must apply and clamp the inherited page size.
	lowered := "[messages]\nconversation_max_limit = 100\n"
	if err := os.WriteFile(cfgPath, []byte(lowered), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case cfg := <-got:
		if l := cfg.Messages.ConversationMaxLimitOrDefault(); l != 100 {
			t.Errorf("reloaded max limit = %d, want 100", l)
		}

		if p := cfg.Messages.ConversationPageSizeOrDefault(); p > 100 {
			t.Errorf("reloaded effective page size = %d, want <= 100 (clamped)", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the lowered-max reload")
	}

	// Rollback: an explicit contradictory page-size override is rejected at load,
	// so the watcher must not fire onChange — the lowered-max config stays live.
	contradictory := "[messages]\nconversation_page_size = 600\nconversation_max_limit = 100\n"
	if err := os.WriteFile(cfgPath, []byte(contradictory), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case <-got:
		t.Fatal("callback should not fire for a contradictory page-size override")
	case <-time.After(500 * time.Millisecond):
		// Expected: the invalid reload is rejected and the prior config retained.
	}
}
