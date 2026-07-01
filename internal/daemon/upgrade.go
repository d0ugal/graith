package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type UpgradeManifest struct {
	ListenerFd int              `json:"listener_fd"`
	ConfigFile string           `json:"config_file"`
	Profile    string           `json:"profile,omitempty"`
	Sessions   []UpgradeSession `json:"sessions"`
}

type UpgradeSession struct {
	ID  string `json:"id"`
	Fd  int    `json:"fd"`
	PID int    `json:"pid"`
}

func clearCloseOnExec(fd int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0)
	if errno != 0 {
		return errno
	}

	return nil
}

func (sm *SessionManager) PrepareUpgrade(listenerFd uintptr, configFile string) (*UpgradeManifest, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.saveState(); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}

	manifest := &UpgradeManifest{
		ListenerFd: int(listenerFd),
		ConfigFile: configFile,
		Profile:    sm.paths.Profile,
	}

	if err := clearCloseOnExec(int(listenerFd)); err != nil {
		return nil, fmt.Errorf("clear cloexec on listener fd %d: %w", listenerFd, err)
	}

	for id, sess := range sm.sessions {
		if sess.Exited() {
			continue
		}

		fd := int(sess.Fd())
		if err := clearCloseOnExec(fd); err != nil {
			sm.log.Warn("skipping session for upgrade", "id", id, "err", err)
			continue
		}

		manifest.Sessions = append(manifest.Sessions, UpgradeSession{
			ID:  id,
			Fd:  fd,
			PID: sess.ProcessPID(),
		})
	}

	return manifest, nil
}

func WriteManifest(dir string, m *UpgradeManifest) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, "upgrade-manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}

	return path, nil
}

func ReadManifest(path string) (*UpgradeManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var m UpgradeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return &m, nil
}

func ExecUpgrade(manifestPath, configFile, clientExecPath string) error {
	execPath := clientExecPath
	if execPath != "" {
		if _, err := os.Stat(execPath); err != nil {
			execPath = ""
		}
	}

	if execPath == "" {
		var err error

		execPath, err = resolveExecutable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w", err)
		}
	}

	args := []string{execPath, "daemon", "start", "--adopt-from", manifestPath}
	if configFile != "" {
		args = append(args, "--config", configFile)
	}

	return syscall.Exec(execPath, args, os.Environ())
}

// resolveExecutable finds the binary to exec into during an upgrade.
// PATH lookup is preferred because package managers (e.g. Homebrew) update
// the symlink in PATH to point to the new version while os.Executable()
// still returns the path of the currently running (old) binary.
func resolveExecutable() (string, error) {
	name := "gr"
	if execPath, err := os.Executable(); err == nil {
		name = filepath.Base(execPath)
	}

	if lookPath, err := exec.LookPath(name); err == nil {
		return lookPath, nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable not in PATH and os.Executable failed: %w", err)
	}

	if _, err := os.Stat(execPath); err != nil {
		return "", fmt.Errorf("executable not in PATH and original path gone: %w", err)
	}

	return execPath, nil
}

func StopDaemon(pidFile string) error {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("daemon not running (no pid file)")
		}

		return err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.Remove(pidFile)
		return fmt.Errorf("invalid pid file: %w", err)
	}

	if pid <= 1 {
		os.Remove(pidFile)
		return fmt.Errorf("refusing to signal invalid pid %d", pid)
	}

	if !IsGraithDaemon(pid) {
		os.Remove(pidFile)
		return fmt.Errorf("pid %d is not a graith daemon, removing stale pid file", pid)
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		os.Remove(pidFile)
		return fmt.Errorf("send SIGTERM to pid %d: %w", pid, err)
	}

	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("daemon did not stop within 5s (pid %d)", pid)
}
