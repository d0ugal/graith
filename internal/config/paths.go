package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

const appName = "graith"

type Paths struct {
	ConfigFile string
	DataDir    string
	RuntimeDir string
	SocketPath string
	PIDFile    string
	StateFile  string
	LogDir     string
	DaemonLog  string
	MessagesDB string
}

func ResolvePaths() Paths {
	configFile := filepath.Join(xdg.ConfigHome, appName, "config.toml")
	dataDir := filepath.Join(xdg.DataHome, appName)
	runtimeDir := runtimeDirForGraith()

	return Paths{
		ConfigFile: configFile,
		DataDir:    dataDir,
		RuntimeDir: runtimeDir,
		SocketPath: filepath.Join(runtimeDir, "graith.sock"),
		PIDFile:    filepath.Join(runtimeDir, "graith.pid"),
		StateFile:  filepath.Join(dataDir, "state.json"),
		LogDir:     filepath.Join(dataDir, "logs"),
		DaemonLog:  filepath.Join(dataDir, "daemon.log"),
		MessagesDB: filepath.Join(dataDir, "messages.sqlite"),
	}
}

func runtimeDirForGraith() string {
	if d := xdg.RuntimeDir; d != "" {
		return filepath.Join(d, appName)
	}
	if d := os.Getenv("TMPDIR"); d != "" {
		return filepath.Join(d, fmt.Sprintf("graith-%d", os.Getuid()))
	}
	return filepath.Join("/tmp", fmt.Sprintf("graith-%d", os.Getuid()))
}

func (p Paths) EnsureDirs() error {
	dirs := []string{filepath.Dir(p.ConfigFile), p.DataDir, p.RuntimeDir, p.LogDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}
