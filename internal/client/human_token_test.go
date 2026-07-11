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
