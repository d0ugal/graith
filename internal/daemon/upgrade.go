package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
	grpty "github.com/d0ugal/graith/internal/pty"
)

type UpgradeManifest struct {
	ListenerFd int              `json:"listener_fd"`
	ConfigFile string           `json:"config_file"`
	Profile    string           `json:"profile,omitempty"`
	Sessions   []UpgradeSession `json:"sessions"`
}

type UpgradeSession struct {
	ID           string `json:"id"`
	Fd           int    `json:"fd"`
	HasPTY       bool   `json:"has_pty"`
	PID          int    `json:"pid"`
	PIDStartTime int64  `json:"pid_start_time"`
}

// UpgradeFailureGuard is armed by the CLI before configuration loading for an
// exec-replacement start. If any pre-Run/bootstrap error returns to main, the
// guard identity-checks and terminates every process the old daemon recorded.
// Run disarms it only after installing its own manifest cleanup defer.
type UpgradeFailureGuard struct {
	manifest *UpgradeManifest
	once     sync.Once
	err      error
}

// ArmUpgradeFailureGuard reads the process identities before normal CLI
// initialization can fail.
func ArmUpgradeFailureGuard(manifestPath string) (*UpgradeFailureGuard, error) {
	manifest, err := ReadManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read upgrade manifest for early cleanup: %w", err)
	}

	return &UpgradeFailureGuard{manifest: manifest}, nil
}

// Cleanup terminates every still-live, identity-matching manifest process once.
func (g *UpgradeFailureGuard) Cleanup() error {
	if g == nil {
		return nil
	}

	g.once.Do(func() {
		g.err = cleanupUpgradeProcesses(g.manifest, config.ProcessKillGraceDefault)
	})

	return g.err
}

// Disarm transfers cleanup ownership to Run. Cleanup calls after this point are
// no-ops, so a later unrelated daemon error cannot terminate processes from the
// startup manifest.
func (g *UpgradeFailureGuard) Disarm() {
	if g == nil {
		return
	}

	g.once.Do(func() {})
}

func (g *UpgradeFailureGuard) Profile() string {
	if g == nil || g.manifest == nil {
		return ""
	}

	return g.manifest.Profile
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

		pid := sess.ProcessPID()
		if pid <= 1 {
			return nil, fmt.Errorf("prepare session %q for upgrade: invalid PID %d", id, pid)
		}

		state := sm.state.Sessions[id]
		if state == nil || state.PID != pid {
			return nil, fmt.Errorf("prepare session %q for upgrade: persisted PID identity does not match live PID %d", id, pid)
		}

		startTime, err := grpty.ProcessStartTime(pid)
		if err != nil {
			return nil, fmt.Errorf("prepare session %q for upgrade: capture process identity for PID %d: %w", id, pid, err)
		}

		if state.PIDStartTime != 0 && state.PIDStartTime != startTime {
			return nil, fmt.Errorf("prepare session %q for upgrade: PID %d identity changed", id, pid)
		}

		state.PIDStartTime = startTime

		entry := UpgradeSession{ID: id, Fd: -1, PID: pid, PIDStartTime: startTime}
		if _, ok := sess.(*grpty.Session); ok {
			fd := int(sess.Fd())
			if err := clearCloseOnExec(fd); err != nil {
				// The process must still be represented in the manifest. The new
				// daemon cannot adopt this PTY, so it will identity-check and
				// terminate the process rather than leave it unmanaged.
				sm.log.Warn("session PTY unavailable for upgrade handoff; process will be terminated", "id", id, "err", err)
			} else {
				entry.Fd = fd
				entry.HasPTY = true
			}
		} else {
			// Pipe-backed/headless drivers cannot transfer their I/O handles,
			// but recording their process identity lets every replacement-daemon
			// failure path terminate them safely after exec.
			sm.log.Info("recording non-PTY session for upgrade cleanup", "id", id)
		}

		manifest.Sessions = append(manifest.Sessions, entry)
	}

	// Persist the exact identities recorded in the manifest before exec. The
	// replacement daemon requires state and manifest to agree before adoption;
	// failing here leaves the current daemon owning every live process.
	if err := sm.saveState(); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
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

// cleanupUpgradeManifest is the replacement daemon's last-resort ownership
// path. Run installs it immediately after reading the manifest, so failures
// that happen before state load or AdoptSessions cannot strand a headless or
// otherwise unadopted agent after the old daemon has exec'd away.
func (sm *SessionManager) cleanupUpgradeManifest(manifest *UpgradeManifest) error {
	return cleanupUpgradeProcesses(manifest, sm.Config().Lifecycle.ProcessKillGraceDuration())
}

func cleanupUpgradeProcesses(manifest *UpgradeManifest, grace time.Duration) error {
	var errs []error

	killedPIDs := make(map[int]struct{})

	for _, session := range manifest.Sessions {
		if _, handled := killedPIDs[session.PID]; handled {
			continue
		}

		killedPIDs[session.PID] = struct{}{}
		if session.PID <= 1 {
			errs = append(errs, fmt.Errorf("session %q has invalid upgrade PID %d", session.ID, session.PID))
			continue
		}

		if session.PIDStartTime == 0 {
			errs = append(errs, fmt.Errorf("session %q has no upgrade process identity for PID %d", session.ID, session.PID))
			continue
		}

		if !isProcessAlive(session.PID) {
			continue
		}

		current, err := grpty.ProcessStartTime(session.PID)
		if err != nil {
			errs = append(errs, fmt.Errorf("verify session %q process identity: %w", session.ID, err))
			continue
		}

		if current != session.PIDStartTime {
			errs = append(errs, fmt.Errorf("session %q PID %d identity mismatch (recorded=%d, current=%d)", session.ID, session.PID, session.PIDStartTime, current))
			continue
		}

		if err := killProcessGroup(session.PID, grace); err != nil {
			errs = append(errs, fmt.Errorf("terminate session %q after replacement-daemon failure: %w", session.ID, err))
		}
	}

	return errors.Join(errs...)
}

type preparedExecUpgrade struct {
	execPath   string
	definition daemonservice.Definition
	rollback   func() error
	managed    bool
}

var (
	prepareManagedUpgradeForExec = daemonservice.PrepareManagedUpgrade
	execProcessForUpgrade        = syscall.Exec
)

func prepareExecUpgrade(profile, clientExecPath string) (preparedExecUpgrade, error) {
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
			return preparedExecUpgrade{}, fmt.Errorf("resolve executable: %w", err)
		}
	}

	definition, rollback, managed, err := prepareManagedUpgradeForExec(profile, execPath)
	if err != nil {
		return preparedExecUpgrade{}, fmt.Errorf("validate managed upgrade: %w", err)
	}

	return preparedExecUpgrade{execPath: execPath, definition: definition, rollback: rollback, managed: managed}, nil
}

func (prepared preparedExecUpgrade) rollbackError(cause error) error {
	if prepared.rollback == nil {
		return cause
	}

	return errors.Join(cause, prepared.rollback())
}

func execPreparedUpgrade(manifestPath, configFile string, prepared preparedExecUpgrade) error {
	execPath := prepared.execPath

	args := []string{execPath, "daemon", "start", "--adopt-from", manifestPath}
	if prepared.managed {
		args = append(args,
			"--internal-service-label", prepared.definition.Label,
			"--internal-service-slot", prepared.definition.Slot,
		)
	}

	if configFile != "" {
		args = append(args, "--config", configFile)
	}

	err := execProcessForUpgrade(execPath, args, os.Environ())
	if err != nil {
		return prepared.rollbackError(err)
	}

	return err
}

func ExecUpgrade(manifestPath, configFile, profile, clientExecPath string) error {
	prepared, err := prepareExecUpgrade(profile, clientExecPath)
	if err != nil {
		return err
	}

	return execPreparedUpgrade(manifestPath, configFile, prepared)
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
			return errors.New("daemon not running (no pid file)")
		}

		return err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(pidFile)
		return fmt.Errorf("invalid pid file: %w", err)
	}

	if pid <= 1 {
		_ = os.Remove(pidFile)
		return fmt.Errorf("refusing to signal invalid pid %d", pid)
	}

	if !IsGraithDaemon(pid) {
		_ = os.Remove(pidFile)
		return fmt.Errorf("pid %d is not a graith daemon, removing stale pid file", pid)
	}

	return stopVerifiedDaemonPID(pid)
}

// StopDaemonPID stops one previously authenticated daemon peer identity. The
// caller obtains pid from Unix peer credentials, not from a mutable PID file.
func StopDaemonPID(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal invalid pid %d", pid)
	}

	if !IsGraithDaemon(pid) {
		return fmt.Errorf("pid %d is not a graith daemon", pid)
	}

	return stopVerifiedDaemonPID(pid)
}

func stopVerifiedDaemonPID(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
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
