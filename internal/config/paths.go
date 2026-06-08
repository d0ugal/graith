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
	// Fall back to the data dir rather than /tmp or $TMPDIR so that the
	// daemon socket is not inside paths that safehouse grants by default.
	return filepath.Join(xdg.DataHome, appName, "run")
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

// LegacyRuntimeDirs returns paths where older versions stored the socket and
// PID file (TMPDIR or /tmp fallbacks). Used during startup to detect and clean
// up an orphaned daemon after the socket location changed.
func LegacyRuntimeDirs() []string {
	var dirs []string
	if d := os.Getenv("TMPDIR"); d != "" {
		dirs = append(dirs, filepath.Join(d, fmt.Sprintf("graith-%d", os.Getuid())))
	}
	dirs = append(dirs, filepath.Join("/tmp", fmt.Sprintf("graith-%d", os.Getuid())))
	return dirs
}
