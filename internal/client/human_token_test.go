package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestReadHumanToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "human.token")
	if err := os.WriteFile(path, []byte("canny-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := readHumanToken(config.Paths{HumanTokenFile: path}); got != "canny-token" {
		t.Errorf("token = %q, want canny-token", got)
	}

	if got := readHumanToken(config.Paths{HumanTokenFile: filepath.Join(t.TempDir(), "dreich")}); got != "" {
		t.Errorf("missing token = %q, want empty", got)
	}
}

func TestResolveClientToken(t *testing.T) {
	dir := t.TempDir()

	tokenFile := filepath.Join(dir, "human.token")
	if err := os.WriteFile(tokenFile, []byte("hame-file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{HumanTokenFile: tokenFile}

	t.Run("braw env token wins over file", func(t *testing.T) {
		t.Setenv("GRAITH_TOKEN", "braw-env-token")

		if got := resolveClientToken(paths); got != "braw-env-token" {
			t.Errorf("token = %q, want the env token", got)
		}
	})

	t.Run("empty env falls back to file", func(t *testing.T) {
		t.Setenv("GRAITH_TOKEN", "")

		if got := resolveClientToken(paths); got != "hame-file-token" {
			t.Errorf("token = %q, want the file token", got)
		}
	})

	t.Run("dreich neither yields empty", func(t *testing.T) {
		t.Setenv("GRAITH_TOKEN", "")

		if got := resolveClientToken(config.Paths{HumanTokenFile: filepath.Join(dir, "absent.token")}); got != "" {
			t.Errorf("token = %q, want empty", got)
		}
	})
}
