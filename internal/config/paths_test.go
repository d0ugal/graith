package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePaths(t *testing.T) {
	p := ResolvePaths()
	if !strings.HasSuffix(p.ConfigFile, filepath.Join("graith", "config.toml")) {
		t.Errorf("ConfigFile = %q, want suffix graith/config.toml", p.ConfigFile)
	}
	if !strings.HasSuffix(p.DataDir, "graith") {
		t.Errorf("DataDir = %q, want suffix graith", p.DataDir)
	}
	if !strings.HasSuffix(p.SocketPath, "graith.sock") {
		t.Errorf("SocketPath = %q, want suffix graith.sock", p.SocketPath)
	}
	if !strings.HasSuffix(p.PIDFile, "graith.pid") {
		t.Errorf("PIDFile = %q, want suffix graith.pid", p.PIDFile)
	}
}
