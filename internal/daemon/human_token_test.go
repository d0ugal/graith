package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestLoadOrCreateHumanToken(t *testing.T) {
	dataDir := t.TempDir()
	paths := config.Paths{DataDir: dataDir, HumanTokenFile: filepath.Join(dataDir, "human.token")}
	sm := NewSessionManager(config.Default(), paths, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sm.loadOrCreateHumanToken(); err != nil {
		t.Fatal(err)
	}

	first := sm.humanToken
	if first == "" {
		t.Fatal("created token is empty")
	}

	info, err := os.Stat(paths.HumanTokenFile)
	if err != nil {
		t.Fatal(err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}

	sm2 := NewSessionManager(config.Default(), paths, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sm2.loadOrCreateHumanToken(); err != nil {
		t.Fatal(err)
	}

	if sm2.humanToken != first {
		t.Errorf("restarted token = %q, want original %q", sm2.humanToken, first)
	}
}

func TestLoadOrCreateHumanTokenFailsClosedOnUnreadableExistingPath(t *testing.T) {
	dataDir := t.TempDir()

	tokenPath := filepath.Join(dataDir, "human.token")
	if err := os.Mkdir(tokenPath, 0o700); err != nil {
		t.Fatal(err)
	}

	sm := NewSessionManager(config.Default(), config.Paths{HumanTokenFile: tokenPath}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sm.loadOrCreateHumanToken(); err == nil {
		t.Fatal("expected unreadable existing token path to fail closed")
	}

	if sm.humanToken != "" {
		t.Fatal("human token populated after read failure")
	}
}

func TestLoadHumanTokenRejectsPermissiveMode(t *testing.T) {
	dataDir := t.TempDir()

	tokenPath := filepath.Join(dataDir, "human.token")
	if err := os.WriteFile(tokenPath, []byte("dreich-token\n"), 0o644); err != nil { //nolint:gosec // deliberately over-permissive fixture
		t.Fatal(err)
	}

	sm := NewSessionManager(config.Default(), config.Paths{HumanTokenFile: tokenPath}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sm.loadOrCreateHumanToken(); err == nil {
		t.Fatal("expected a group/other-readable token to be rejected")
	}

	if sm.humanToken != "" {
		t.Fatal("human token populated despite insecure mode")
	}
}

func TestLoadHumanTokenRejectsSymlink(t *testing.T) {
	dataDir := t.TempDir()

	realPath := filepath.Join(dataDir, "real.token")
	if err := os.WriteFile(realPath, []byte("thrawn-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tokenPath := filepath.Join(dataDir, "human.token")
	if err := os.Symlink(realPath, tokenPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	sm := NewSessionManager(config.Default(), config.Paths{HumanTokenFile: tokenPath}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sm.loadOrCreateHumanToken(); err == nil {
		t.Fatal("expected a symlinked token to be rejected")
	}

	if sm.humanToken != "" {
		t.Fatal("human token populated despite symlink")
	}
}
