package config

import (
	"os"
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

func TestConfigHomeDefaultsToDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	got := configHome()
	want := filepath.Join(home, ".config")
	if got != want {
		t.Errorf("configHome() = %q, want %q", got, want)
	}
}

func TestConfigHomeRespectsXDGEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	got := configHome()
	if got != "/custom/config" {
		t.Errorf("configHome() = %q, want /custom/config", got)
	}
}

func TestConfigFileUnderDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	p := ResolvePaths()
	if !strings.Contains(p.ConfigFile, filepath.Join(".config", "graith")) {
		t.Errorf("ConfigFile = %q, want it under .config/graith", p.ConfigFile)
	}
}
