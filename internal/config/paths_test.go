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
	t.Setenv("GRAITH_PROFILE", "braw")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
	}

	if p.Profile != "braw" {
		t.Errorf("Profile = %q, want braw", p.Profile)
	}

	if p.AppName != "graith-braw" {
		t.Errorf("AppName = %q, want graith-braw", p.AppName)
	}

	if !strings.Contains(p.ConfigFile, "graith-braw") {
		t.Errorf("ConfigFile = %q, want graith-braw in path", p.ConfigFile)
	}

	if !strings.Contains(p.DataDir, "graith-braw") {
		t.Errorf("DataDir = %q, want graith-braw in path", p.DataDir)
	}

	if !strings.Contains(p.RuntimeDir, "graith-braw") {
		t.Errorf("RuntimeDir = %q, want graith-braw in path", p.RuntimeDir)
	}

	if !strings.Contains(p.SocketPath, "graith-braw") {
		t.Errorf("SocketPath = %q, want graith-braw in path", p.SocketPath)
	}
}

func TestResolveProfileValidation(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		wantErr bool
	}{
		{"empty", "", false},
		{"valid lowercase", "braw", false},
		{"valid with numbers", "kirk123", false},
		{"valid with hyphens", "bonnie-profile", false},
		{"uppercase rejected", "Braw", true},
		{"mixed case rejected", "bonnieProfile", true},
		{"slash rejected", "glen/kirk", true},
		{"dot rejected", "glen.kirk", true},
		{"space rejected", "glen kirk", true},
		{"leading hyphen rejected", "-braw", true},
		{"trailing hyphen rejected", "braw-", true},
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

func TestWithDataDir(t *testing.T) {
	t.Setenv("GRAITH_PROFILE", "")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
	}

	override := p.WithDataDir("/tmp/graith-test-data")

	if override.DataDir != "/tmp/graith-test-data" {
		t.Errorf("DataDir = %q, want /tmp/graith-test-data", override.DataDir)
	}

	if override.StateFile != filepath.Join("/tmp/graith-test-data", "state.json") {
		t.Errorf("StateFile = %q, want state.json under new DataDir", override.StateFile)
	}

	if override.LogDir != filepath.Join("/tmp/graith-test-data", "logs") {
		t.Errorf("LogDir = %q, want logs under new DataDir", override.LogDir)
	}

	if override.DaemonLog != filepath.Join("/tmp/graith-test-data", "daemon.log") {
		t.Errorf("DaemonLog = %q, want daemon.log under new DataDir", override.DaemonLog)
	}

	if override.MessagesDB != filepath.Join("/tmp/graith-test-data", "messages.sqlite") {
		t.Errorf("MessagesDB = %q, want messages.sqlite under new DataDir", override.MessagesDB)
	}

	if override.ConfigFile != p.ConfigFile {
		t.Errorf("ConfigFile changed: got %q, want %q", override.ConfigFile, p.ConfigFile)
	}
}

func TestWithDataDirUpdatesRuntimeWhenUnderDataDir(t *testing.T) {
	t.Setenv("GRAITH_PROFILE", "")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
	}

	if !strings.HasPrefix(p.RuntimeDir, p.DataDir) {
		t.Skip("RuntimeDir is not under DataDir on this system")
	}

	override := p.WithDataDir("/tmp/graith-test-data")

	if !strings.HasPrefix(override.RuntimeDir, "/tmp/graith-test-data") {
		t.Errorf("RuntimeDir = %q, want it under /tmp/graith-test-data", override.RuntimeDir)
	}

	if !strings.HasPrefix(override.SocketPath, override.RuntimeDir) {
		t.Errorf("SocketPath = %q, want it under RuntimeDir %q", override.SocketPath, override.RuntimeDir)
	}

	if !strings.HasPrefix(override.PIDFile, override.RuntimeDir) {
		t.Errorf("PIDFile = %q, want it under RuntimeDir %q", override.PIDFile, override.RuntimeDir)
	}
}

func TestWithDataDirPreservesIndependentRuntime(t *testing.T) {
	p := Paths{
		DataDir:    "/old/data",
		RuntimeDir: "/var/run/graith",
		SocketPath: "/var/run/graith/graith.sock",
		PIDFile:    "/var/run/graith/graith.pid",
	}
	override := p.WithDataDir("/new/data")

	if override.RuntimeDir != "/var/run/graith" {
		t.Errorf("RuntimeDir changed: got %q, want /var/run/graith", override.RuntimeDir)
	}

	if override.SocketPath != "/var/run/graith/graith.sock" {
		t.Errorf("SocketPath changed: got %q", override.SocketPath)
	}
}

func TestWithDataDirExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	t.Setenv("GRAITH_PROFILE", "")

	p, _ := ResolvePaths()
	override := p.WithDataDir("~/.graith")

	want := filepath.Join(home, ".graith")
	if override.DataDir != want {
		t.Errorf("DataDir = %q, want %q", override.DataDir, want)
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
