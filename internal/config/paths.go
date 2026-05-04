package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/adrg/xdg"
)

const baseAppName = "graith"

var validProfile = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

type Paths struct {
	Profile    string
	AppName    string
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

func ResolveProfile() (profile string, appName string, err error) {
	profile = os.Getenv("GRAITH_PROFILE")
	if profile == "" {
		return "", baseAppName, nil
	}
	if profile == "default" {
		return "", "", fmt.Errorf("invalid profile name %q: \"default\" is reserved", profile)
	}
	if len(profile) > 32 {
		return "", "", fmt.Errorf("invalid profile name %q: must be at most 32 characters", profile)
	}
	if !validProfile.MatchString(profile) {
		return "", "", fmt.Errorf("invalid profile name %q: must be lowercase alphanumeric with hyphens, no leading hyphen", profile)
	}
	return profile, baseAppName + "-" + profile, nil
}

func configHome() string {
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return xdg.ConfigHome
}

func ResolvePaths() (Paths, error) {
	profile, appName, err := ResolveProfile()
	if err != nil {
		return Paths{}, err
	}

	configFile := filepath.Join(configHome(), appName, "config.toml")
	dataDir := filepath.Join(xdg.DataHome, appName)
	runtimeDir := runtimeDirForApp(appName)

	return Paths{
		Profile:    profile,
		AppName:    appName,
		ConfigFile: configFile,
		DataDir:    dataDir,
		RuntimeDir: runtimeDir,
		SocketPath: filepath.Join(runtimeDir, "graith.sock"),
		PIDFile:    filepath.Join(runtimeDir, "graith.pid"),
		StateFile:  filepath.Join(dataDir, "state.json"),
		LogDir:     filepath.Join(dataDir, "logs"),
		DaemonLog:  filepath.Join(dataDir, "daemon.log"),
		MessagesDB: filepath.Join(dataDir, "messages.sqlite"),
	}, nil
}

func runtimeDirForApp(appName string) string {
	if d := xdg.RuntimeDir; d != "" {
		return filepath.Join(d, appName)
	}
	return filepath.Join(xdg.DataHome, appName, "run")
}

func (p Paths) WithDataDir(dataDir string) Paths {
	oldDataDir := p.DataDir
	dataDir = ExpandPath(dataDir)
	p.DataDir = dataDir
	p.StateFile = filepath.Join(dataDir, "state.json")
	p.LogDir = filepath.Join(dataDir, "logs")
	p.DaemonLog = filepath.Join(dataDir, "daemon.log")
	p.MessagesDB = filepath.Join(dataDir, "messages.sqlite")
	if strings.HasPrefix(p.RuntimeDir, oldDataDir+string(filepath.Separator)) || p.RuntimeDir == oldDataDir {
		rel, err := filepath.Rel(oldDataDir, p.RuntimeDir)
		if err == nil {
			runtimeDir := filepath.Join(dataDir, rel)
			p.RuntimeDir = runtimeDir
			p.SocketPath = filepath.Join(runtimeDir, "graith.sock")
			p.PIDFile = filepath.Join(runtimeDir, "graith.pid")
		}
	}
	return p
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
