package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePaths(t *testing.T) {
	t.Setenv("GRAITH_PROFILE", "")
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
	}
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
	if p.Profile != "" {
		t.Errorf("Profile = %q, want empty", p.Profile)
	}
	if p.AppName != "graith" {
		t.Errorf("AppName = %q, want graith", p.AppName)
	}
}

func TestResolvePathsWithProfile(t *testing.T) {
	t.Setenv("GRAITH_PROFILE", "dev")
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
	}
	if p.Profile != "dev" {
		t.Errorf("Profile = %q, want dev", p.Profile)
	}
	if p.AppName != "graith-dev" {
		t.Errorf("AppName = %q, want graith-dev", p.AppName)
	}
	if !strings.Contains(p.ConfigFile, "graith-dev") {
		t.Errorf("ConfigFile = %q, want graith-dev in path", p.ConfigFile)
	}
	if !strings.Contains(p.DataDir, "graith-dev") {
		t.Errorf("DataDir = %q, want graith-dev in path", p.DataDir)
	}
	if !strings.Contains(p.RuntimeDir, "graith-dev") {
		t.Errorf("RuntimeDir = %q, want graith-dev in path", p.RuntimeDir)
	}
	if !strings.Contains(p.SocketPath, "graith-dev") {
		t.Errorf("SocketPath = %q, want graith-dev in path", p.SocketPath)
	}
}

func TestResolveProfileValidation(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		wantErr bool
	}{
		{"empty", "", false},
		{"valid lowercase", "dev", false},
		{"valid with numbers", "test123", false},
		{"valid with hyphens", "my-profile", false},
		{"uppercase rejected", "Dev", true},
		{"mixed case rejected", "myProfile", true},
		{"slash rejected", "foo/bar", true},
		{"dot rejected", "foo.bar", true},
		{"space rejected", "foo bar", true},
		{"leading hyphen rejected", "-dev", true},
		{"reserved default", "default", true},
		{"too long", "abcdefghijklmnopqrstuvwxyz1234567", true},
		{"max length ok", "abcdefghijklmnopqrstuvwxyz123456", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAITH_PROFILE", tt.profile)
			_, _, err := ResolveProfile()
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveProfile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
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
	t.Setenv("GRAITH_PROFILE", "")
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
	}
	if !strings.Contains(p.ConfigFile, filepath.Join(".config", "graith")) {
		t.Errorf("ConfigFile = %q, want it under .config/graith", p.ConfigFile)
	}
}
