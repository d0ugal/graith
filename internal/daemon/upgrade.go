package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
	"github.com/d0ugal/graith/internal/processidentity"
	grpty "github.com/d0ugal/graith/internal/pty"
	"golang.org/x/sys/unix"
)

type UpgradeManifest struct {
	Version        int                     `json:"version,omitempty"`
	ListenerFd     int                     `json:"listener_fd"`
	ConfigFile     string                  `json:"config_file"`
	Profile        string                  `json:"profile,omitempty"`
	Sessions       []UpgradeSession        `json:"sessions"`
	Helpers        []UpgradeHelper         `json:"helpers,omitempty"`
	Target         UpgradeTargetDescriptor `json:"target,omitempty"`
	Paths          UpgradePathDescriptor   `json:"paths,omitempty"`
	StateSnapshot  []byte                  `json:"state_snapshot,omitempty"`
	ConfigSnapshot []byte                  `json:"config_snapshot,omitempty"`
	ConfigPresent  bool                    `json:"config_present,omitempty"`
	JournalID      string                  `json:"journal_id,omitempty"`

	descriptorFlags  map[int]int
	ownedDescriptors map[int]struct{}
	planSessions     map[string]*grpty.Session
	planDrivers      map[string]SessionDriver
	ownershipFD      int
	ownershipCapsule string
	adoptionDeadline time.Time
	journalDev       uint64
	journalIno       uint64
	journalSHA256    [sha256.Size]byte
}

type UpgradeSession struct {
	ID           string `json:"id"`
	Fd           int    `json:"fd"`
	HasPTY       bool   `json:"has_pty"`
	ScrollbackFd int    `json:"scrollback_fd,omitempty"`
	PID          int    `json:"pid"`
	PIDStartTime int64  `json:"pid_start_time"`
}

func upgradeSessionHasPTY(session UpgradeSession) bool {
	// Version 2 writers set HasPTY explicitly. Treat a valid transferred PTY
	// descriptor as authoritative as well so manifests written before this
	// field was introduced remain adoptable.
	return session.HasPTY || session.Fd > 2
}

func plannedUpgradeDriver(manifest *UpgradeManifest, id string) SessionDriver {
	if manifest.planDrivers != nil {
		if driver := manifest.planDrivers[id]; driver != nil {
			return driver
		}
	}

	if manifest.planSessions != nil {
		return manifest.planSessions[id]
	}

	return nil
}

type UpgradeHelper struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start_time"`
}

type UpgradeTargetDescriptor struct {
	ResolvedPath string `json:"resolved_path"`
	ExecPath     string `json:"exec_path,omitempty"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
	ModTimeNanos int64  `json:"mod_time_nanos"`
	SHA256       string `json:"sha256"`
}

type UpgradePathDescriptor struct {
	ConfigFile string `json:"config_file"`
	DataDir    string `json:"data_dir"`
	StateFile  string `json:"state_file"`
	RuntimeDir string `json:"runtime_dir"`
	SocketPath string `json:"socket_path"`
}

const (
	upgradeCapacityProbeVersion  = 2
	upgradeManifestVersion       = 2
	upgradeHelperHandoffVersion  = 2
	upgradeCapacityProbeMaxBytes = 1024
	upgradeCapacityProbeTimeout  = 5 * time.Second
	upgradeManifestMaxBytes      = 4 * 1024 * 1024
	upgradeManifestMaxSessions   = 4096
	upgradeManifestMaxHelpers    = 256
	upgradeCapacityProbeMarker   = "graith-private-upgrade-probe-v2"
	upgradeOwnershipFDEnv        = "GRAITH_PRIVATE_UPGRADE_OWNERSHIP_FD"
	upgradeOwnershipCapsuleEnv   = "GRAITH_PRIVATE_UPGRADE_CAPSULE"
	upgradeOwnershipHeaderMax    = 2 * 1024 * 1024
	upgradeOwnershipCapsuleMax   = 128 * 1024
	upgradeJournalMarkerMaxBytes = 128
	upgradeExecEnvironmentMax    = 128 * 1024
	upgradeExecEnvironmentSlack  = 16 * 1024
	upgradeDescriptorHeadroom    = 16
	upgradeConfigSnapshotMax     = 1024 * 1024
	upgradeAdoptionTimeout       = 15 * time.Second
	upgradeManifestReadTimeout   = 3 * time.Second
)

var upgradeOwnershipMagic = []byte("graith-upgrade-ownership-v2\n")
var upgradeOwnershipCapsuleMagic = []byte("GRCAP002")

type upgradeOwnershipHeader struct {
	Version    int              `json:"version"`
	ListenerFD int              `json:"listener_fd"`
	Sessions   []UpgradeSession `json:"sessions"`
	Helpers    []UpgradeHelper  `json:"helpers,omitempty"`
}

type UpgradeCapacityProbe struct {
	Version              int                   `json:"version"`
	Backend              string                `json:"backend"`
	MaxSessions          int                   `json:"max_sessions"`
	HelperHandoffVersion int                   `json:"helper_handoff_version"`
	StateVersion         int                   `json:"state_version"`
	ManifestVersion      int                   `json:"manifest_version"`
	AdoptionVersion      int                   `json:"adoption_version"`
	Profile              string                `json:"profile,omitempty"`
	Paths                UpgradePathDescriptor `json:"paths,omitempty"`
	ConfigSource         UpgradeConfigSource   `json:"config_source"`
}

type UpgradeConfigSource struct {
	Path    string `json:"path"`
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
}

func IsUpgradeCapacityProbeMarker(marker string) bool {
	return marker == upgradeCapacityProbeMarker
}

func CurrentUpgradeCapacityProbe() UpgradeCapacityProbe {
	defer func() { _ = grpty.ReleasePinnedTerminalExecutablePathForExec() }()

	maxSessions, available := grpty.ProbeTerminalAdoption()
	switch {
	case !available:
		return UpgradeCapacityProbe{
			Version: upgradeCapacityProbeVersion, Backend: "unavailable",
			HelperHandoffVersion: upgradeHelperHandoffVersion,
			StateVersion:         CurrentStateVersion, ManifestVersion: upgradeManifestVersion,
			AdoptionVersion: upgradeManifestVersion,
		}
	case maxSessions == 0:
		return UpgradeCapacityProbe{
			Version: upgradeCapacityProbeVersion, Backend: "unlimited",
			HelperHandoffVersion: upgradeHelperHandoffVersion,
			StateVersion:         CurrentStateVersion, ManifestVersion: upgradeManifestVersion,
			AdoptionVersion: upgradeManifestVersion,
		}
	default:
		return UpgradeCapacityProbe{
			Version: upgradeCapacityProbeVersion, Backend: "limited", MaxSessions: maxSessions,
			HelperHandoffVersion: upgradeHelperHandoffVersion,
			StateVersion:         CurrentStateVersion, ManifestVersion: upgradeManifestVersion,
			AdoptionVersion: upgradeManifestVersion,
		}
	}
}

func CurrentUpgradeCapacityProbeForConfig(configFile string) (UpgradeCapacityProbe, error) {
	probe := CurrentUpgradeCapacityProbe()

	effectiveConfig, _, err := config.ResolveConfigPath(configFile)
	if err != nil {
		return UpgradeCapacityProbe{}, err
	}

	configSnapshot, configPresent, err := captureUpgradeConfigSnapshot(effectiveConfig)
	if err != nil {
		return UpgradeCapacityProbe{}, err
	}

	cfg, err := config.LoadOrDefault(effectiveConfig)
	if err != nil {
		return UpgradeCapacityProbe{}, err
	}

	paths, err := config.ResolvePaths()
	if err != nil {
		return UpgradeCapacityProbe{}, err
	}

	if cfg.DataDir != "" {
		paths = paths.WithDataDir(cfg.DataDir)
	}

	probe.Profile = paths.Profile
	probe.Paths = makeUpgradePathDescriptor(paths, effectiveConfig)
	probe.ConfigSource = makeUpgradeConfigSource(effectiveConfig, configSnapshot, configPresent)

	return probe, nil
}

type upgradeRefusalError struct {
	reason string
}

func (e *upgradeRefusalError) Error() string { return e.reason }

func refuseUpgrade(reason string) error {
	return &upgradeRefusalError{reason: reason}
}

type upgradeTarget struct {
	path                 string
	pin                  *upgradeTargetPin
	capacity             int
	fileInfo             os.FileInfo
	fileSize             int64
	fileMode             os.FileMode
	fileModNanos         int64
	sha256               string
	helperHandoffVersion int
}

// preparedExecUpgrade records the managed-service generation staged before
// any session state or descriptor is mutated. Direct-source installs remain
// unmanaged and carry no marker or rollback.
type preparedExecUpgrade struct {
	execPath   string
	definition daemonservice.Definition
	rollback   *preparedUpgradeRollback
	managed    bool
}

type preparedUpgradeRollback struct {
	once sync.Once
	fn   func() error
	err  error
}

// retainedManagedUpgradeOrigin is authenticated during post-exec adoption and
// kept only for the lifetime of that daemon generation. It lets a daemon whose
// Darwin image is a private retained copy stage the next managed generation
// without weakening path-strict validation for fresh service processes.
type retainedManagedUpgradeOrigin struct {
	label                string
	slot                 string
	currentCandidatePath string
}

var (
	prepareManagedUpgradeForExec         = daemonservice.PrepareManagedUpgrade
	prepareRetainedManagedUpgradeForExec = daemonservice.PrepareRetainedManagedUpgrade
	execProcessForUpgrade                = syscall.Exec
)

func prepareExecUpgrade(profile, clientExecPath string) (preparedExecUpgrade, error) {
	return prepareExecUpgradeWithOrigin(profile, clientExecPath, nil)
}

func prepareExecUpgradeWithOrigin(
	profile, clientExecPath string,
	origin *retainedManagedUpgradeOrigin,
) (preparedExecUpgrade, error) {
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

	var (
		definition daemonservice.Definition
		rollback   func() error
		managed    bool
		err        error
	)

	if origin == nil {
		definition, rollback, managed, err = prepareManagedUpgradeForExec(profile, execPath)
	} else {
		definition, rollback, err = prepareRetainedManagedUpgradeForExec(
			origin.label, origin.slot, profile, origin.currentCandidatePath, execPath,
		)
		managed = true
	}

	if err != nil {
		return preparedExecUpgrade{}, fmt.Errorf("validate managed upgrade: %w", err)
	}

	if managed {
		if rollback == nil {
			return preparedExecUpgrade{}, errors.New("validate managed upgrade: rollback is missing")
		}

		if _, err := daemonservice.ValidateMarker(definition.Label, definition.Slot); err != nil {
			return preparedExecUpgrade{}, errors.Join(
				fmt.Errorf("validate managed upgrade marker: %w", err), rollback(),
			)
		}
	}

	prepared := preparedExecUpgrade{execPath: execPath, definition: definition, managed: managed}
	if rollback != nil {
		prepared.rollback = &preparedUpgradeRollback{fn: rollback}
	}

	return prepared, nil
}

func (prepared preparedExecUpgrade) rollbackError(cause error) error {
	if prepared.rollback == nil {
		return cause
	}

	prepared.rollback.once.Do(func() {
		prepared.rollback.err = prepared.rollback.fn()
	})

	return errors.Join(cause, prepared.rollback.err)
}

func (prepared preparedExecUpgrade) validateTarget(target *upgradeTarget) error {
	if !prepared.managed {
		return nil
	}

	if target == nil || canonicalUpgradePath(prepared.execPath) != canonicalUpgradePath(target.path) {
		return refuseUpgrade("managed upgrade target changed after service preparation")
	}

	return nil
}

func upgradeExecArgs(targetPath, manifestPath, configFile string, prepared preparedExecUpgrade) []string {
	args := []string{targetPath, "daemon", "start", "--adopt-from", manifestPath}
	if prepared.managed {
		args = append(args,
			"--internal-service-label", prepared.definition.Label,
			"--internal-service-slot", prepared.definition.Slot,
		)
	}

	if configFile != "" {
		args = append(args, "--config", configFile)
	}

	return args
}

type upgradeProbeExpectation struct {
	profile      string
	paths        UpgradePathDescriptor
	configSource UpgradeConfigSource
}

func probeUpgradeTarget(clientExecPath string, expectations ...upgradeProbeExpectation) (*upgradeTarget, error) {
	path, err := resolveUpgradeExecutable(clientExecPath)
	if err != nil {
		return nil, refuseUpgrade("upgrade target could not be resolved")
	}

	pin, err := pinUpgradeTarget(path)
	if err != nil {
		return nil, refuseUpgrade("upgrade target is not an executable file")
	}

	keepPin := false
	defer func() {
		if !keepPin {
			_ = pin.close()
		}
	}()

	if err := pin.validate(); err != nil {
		return nil, refuseUpgrade("upgrade target content could not be verified")
	}

	var expectation upgradeProbeExpectation
	if len(expectations) > 0 {
		expectation = expectations[0]
	}

	data, err := runUpgradeCapacityProbe(pin, expectation.configSource.Path, expectation.profile)
	if err != nil {
		return nil, err
	}

	var probe UpgradeCapacityProbe

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&probe); err != nil {
		return nil, refuseUpgrade("upgrade target returned an invalid capacity probe")
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, refuseUpgrade("upgrade target returned trailing capacity data")
	}

	if probe.Version != upgradeCapacityProbeVersion {
		return nil, refuseUpgrade("upgrade target capacity probe version is unsupported")
	}

	if probe.StateVersion != CurrentStateVersion || probe.ManifestVersion != upgradeManifestVersion ||
		probe.AdoptionVersion != upgradeManifestVersion {
		return nil, refuseUpgrade("upgrade target state or adoption protocol is incompatible")
	}

	if len(expectations) > 0 && (probe.Profile != expectation.profile || probe.Paths != expectation.paths) {
		return nil, refuseUpgrade("upgrade target effective paths do not match the running daemon")
	}

	if len(expectations) > 0 && probe.ConfigSource != expectation.configSource {
		return nil, refuseUpgrade("upgrade target config source does not match the captured daemon config")
	}

	capacity := 0

	switch probe.Backend {
	case "unlimited":
		if probe.MaxSessions != 0 {
			return nil, refuseUpgrade("upgrade target returned an invalid unlimited capacity")
		}
	case "limited":
		if probe.MaxSessions <= 0 {
			return nil, refuseUpgrade("upgrade target returned an invalid terminal capacity")
		}

		capacity = probe.MaxSessions
	case "unavailable":
		return nil, refuseUpgrade("upgrade target terminal backend is unavailable")
	default:
		return nil, refuseUpgrade("upgrade target terminal backend is unknown")
	}

	target := &upgradeTarget{
		path:                 path,
		pin:                  pin,
		capacity:             capacity,
		fileInfo:             pin.info,
		fileSize:             pin.info.Size(),
		fileMode:             pin.info.Mode(),
		fileModNanos:         pin.info.ModTime().UnixNano(),
		sha256:               pin.digest,
		helperHandoffVersion: probe.HelperHandoffVersion,
	}
	keepPin = true

	return target, nil
}

func runUpgradeCapacityProbe(pin *upgradeTargetPin, configFile, profile string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), upgradeCapacityProbeTimeout)
	defer cancel()

	args := []string{"daemon", "adoption-capacity", upgradeCapacityProbeMarker}
	if configFile != "" {
		args = append(args, "--config", configFile)
	}

	cmd := pin.probeCommand(ctx, args...)
	// The upgrade target is user-selectable code. Do not expose the daemon's
	// environment to it during capability discovery. Only values which affect
	// the effective config/data/runtime paths are forwarded so the probe sees
	// the same layout as the running daemon.
	cmd.Env = upgradeProbeEnvironment(profile)
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}

		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()

		return nil
	}

	var stdout boundedProbeOutput

	cmd.Stdout = &stdout
	waitErr := cmd.Run()

	if ctx.Err() != nil {
		return nil, refuseUpgrade("upgrade target capacity probe timed out")
	}

	if waitErr != nil {
		return nil, refuseUpgrade("upgrade target capacity probe failed")
	}

	if len(stdout.data) > upgradeCapacityProbeMaxBytes {
		return nil, refuseUpgrade("upgrade target capacity probe exceeded its output limit")
	}

	return stdout.data, nil
}

func upgradeProbeEnvironment(profile string) []string {
	const pathEnvironment = "HOME XDG_CONFIG_HOME XDG_DATA_HOME XDG_RUNTIME_DIR XDG_STATE_HOME"

	env := make([]string, 0, 6)

	for _, name := range strings.Fields(pathEnvironment) {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}

	if profile != "" {
		env = append(env, "GRAITH_PROFILE="+profile)
	}

	return env
}

type boundedProbeOutput struct {
	data []byte
}

func (w *boundedProbeOutput) Write(p []byte) (int, error) {
	remaining := upgradeCapacityProbeMaxBytes + 1 - len(w.data)
	if remaining > 0 {
		w.data = append(w.data, p[:min(len(p), remaining)]...)
	}

	return len(p), nil
}

func (t *upgradeTarget) validateFileIdentity() error {
	if t.pin == nil || t.pin.validate() != nil {
		return refuseUpgrade("upgrade target changed after capacity preflight")
	}

	return nil
}

func (t *upgradeTarget) validateFinalFileIdentity() error {
	if t.pin == nil || t.pin.validateFinal() != nil {
		return refuseUpgrade("upgrade target changed at final exec boundary")
	}

	return nil
}

func (t *upgradeTarget) descriptor() UpgradeTargetDescriptor {
	return UpgradeTargetDescriptor{
		ResolvedPath: t.path,
		ExecPath: func() string {
			if t.pin != nil && t.pin.retainedDir != "" {
				return t.pin.execPath
			}

			return ""
		}(),
		Size:         t.fileSize,
		Mode:         uint32(t.fileMode),
		ModTimeNanos: t.fileModNanos,
		SHA256:       t.sha256,
	}
}

func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateUpgradeTargetDescriptor(target UpgradeTargetDescriptor) (returnErr error) {
	if target.ResolvedPath == "" || !filepath.IsAbs(target.ResolvedPath) {
		return errors.New("upgrade manifest target identity is missing")
	}

	executablePath := "/proc/self/exe"

	if runtime.GOOS != "linux" {
		var err error

		executablePath, err = os.Executable()
		if err != nil {
			return errors.New("running executable cannot be verified")
		}
	}

	executable, err := os.Open(executablePath)
	if err != nil {
		return errors.New("running executable cannot be verified")
	}

	defer func() {
		returnErr = errors.Join(returnErr, executable.Close())
	}()

	currentInfo, err := executable.Stat()
	if err != nil {
		return errors.New("running executable cannot be verified")
	}

	digest, digestErr := digestUpgradeTargetFile(executable, currentInfo.Size())
	if !currentInfo.Mode().IsRegular() || currentInfo.Size() != target.Size ||
		digestErr != nil || digest != target.SHA256 {
		return errors.New("running executable does not match upgrade manifest target")
	}

	if target.ExecPath != "" && canonicalUpgradePath(executablePath) != canonicalUpgradePath(target.ExecPath) {
		return errors.New("running executable path does not match upgrade manifest target")
	}

	return nil
}

func descriptorFlags(fd int) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if errno != 0 {
		return 0, errno
	}

	return int(flags), nil
}

func setDescriptorFlags(fd, flags int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, uintptr(flags))
	if errno != 0 {
		return errno
	}

	return nil
}

var adoptSetDescriptorFlags = setDescriptorFlags
var adoptCloseDescriptor = syscall.Close

// verifyTransferredDescriptorClosed converts close(2)'s platform-ambiguous
// error result into a fail-closed ownership decision. A close error is never
// retried: the kernel may already have released the number and another thread
// may have reused it. EBADF from a subsequent non-mutating flags query proves
// the descriptor is gone; any live or indeterminate result forces bootstrap to
// abort so process exit resolves the remaining secured descriptor.
func verifyTransferredDescriptorClosed(fd int, closeErr error) error {
	if closeErr == nil || errors.Is(closeErr, syscall.EBADF) {
		return nil
	}

	_, verifyErr := descriptorFlags(fd)
	if errors.Is(verifyErr, syscall.EBADF) {
		return nil
	}

	return errors.New("transferred descriptor close could not be proven")
}

func secureTransferredDescriptor(fd int) error {
	flags, err := descriptorFlags(fd)
	if err != nil {
		return err
	}

	if flags&syscall.FD_CLOEXEC != 0 {
		return nil
	}

	return adoptSetDescriptorFlags(fd, flags|syscall.FD_CLOEXEC)
}

func secureUpgradeManifestDescriptors(manifest *UpgradeManifest) error {
	if err := secureTransferredDescriptor(manifest.ListenerFd); err != nil {
		return fmt.Errorf("secure inherited listener descriptor: %w", err)
	}

	for _, session := range manifest.Sessions {
		if !upgradeSessionHasPTY(session) {
			continue
		}

		if err := secureTransferredDescriptor(session.Fd); err != nil {
			return fmt.Errorf("secure inherited session descriptor: %w", err)
		}

		if session.ScrollbackFd > 2 {
			if err := secureTransferredDescriptor(session.ScrollbackFd); err != nil {
				return fmt.Errorf("secure inherited scrollback descriptor: %w", err)
			}
		}
	}

	return nil
}

var (
	rollbackGetDescriptorFlags = descriptorFlags
	rollbackSetDescriptorFlags = setDescriptorFlags
	rollbackCloseDescriptor    = syscall.Close
	unsafeUpgradeRollbackExit  = func() { syscall.Exit(1) }
)

type upgradeDescriptorSafetyError struct {
	err error
}

func (e *upgradeDescriptorSafetyError) Error() string { return e.err.Error() }
func (e *upgradeDescriptorSafetyError) Unwrap() error { return e.err }

func unsafeUpgradeDescriptor(err error) error {
	return &upgradeDescriptorSafetyError{err: err}
}

func hasUnsafeUpgradeDescriptor(err error) bool {
	var unsafeErr *upgradeDescriptorSafetyError
	return errors.As(err, &unsafeErr)
}

func (sm *SessionManager) PrepareUpgrade(listenerFd uintptr, configFile string) (*UpgradeManifest, error) {
	return sm.prepareUpgrade(listenerFd, configFile, 0, nil, false)
}

func (sm *SessionManager) prepareUpgrade(
	listenerFd uintptr,
	configFile string,
	maxSessions int,
	helpers []grpty.HelperProcessIdentity,
	requireReservation bool,
) (result *UpgradeManifest, returnErr error) {
	if configFile == "" {
		configFile = sm.paths.ConfigFile
	}

	type candidate struct {
		id        string
		driver    SessionDriver
		session   *grpty.Session
		statePID  int
		startTime int64
	}

	// Reserve/snapshot only manager-owned references under sm.mu. Session locks,
	// process identity reads, filesystem work, and fcntl calls all stay outside.
	sm.mu.RLock()

	candidates := make([]candidate, 0, len(sm.sessions))
	for id, driver := range sm.sessions {
		state := sm.state.Sessions[id]
		if state == nil {
			sm.mu.RUnlock()

			return nil, refuseUpgrade("session state is missing during upgrade")
		}

		candidates = append(candidates, candidate{
			id: id, driver: driver, statePID: state.PID, startTime: state.PIDStartTime,
		})
		candidates[len(candidates)-1].session, _ = driver.(*grpty.Session)
	}

	sm.mu.RUnlock()
	slices.SortFunc(candidates, func(a, b candidate) int { return strings.Compare(a.id, b.id) })
	manifest := &UpgradeManifest{
		Version:          upgradeManifestVersion,
		ListenerFd:       int(listenerFd),
		ConfigFile:       canonicalUpgradePath(configFile),
		Profile:          sm.paths.Profile,
		Paths:            makeUpgradePathDescriptor(sm.paths, configFile),
		descriptorFlags:  make(map[int]int, len(candidates)+1),
		ownedDescriptors: make(map[int]struct{}, len(candidates)),
		planSessions:     make(map[string]*grpty.Session, len(candidates)),
		planDrivers:      make(map[string]SessionDriver, len(candidates)),
	}
	// The caller retains the *os.File returned by net.UnixListener.File. Keep
	// that wrapper as the listener duplicate's sole closer: a raw close here
	// followed by the wrapper's deferred Close can otherwise close a reused FD.
	// Rollback still restores its exact descriptor flags before the wrapper is
	// closed, including while syscall.ForkLock is held.
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, rollbackUpgradeDescriptors(manifest))
		}
	}()

	for _, helper := range helpers {
		if helper.PID <= 0 || helper.StartTime <= 0 {
			return nil, refuseUpgrade("terminal helper identity is incomplete")
		}

		manifest.Helpers = append(manifest.Helpers, UpgradeHelper{
			PID: helper.PID, StartTime: helper.StartTime,
		})
	}

	slices.SortFunc(manifest.Helpers, func(a, b UpgradeHelper) int { return a.PID - b.PID })

	for i := 1; i < len(manifest.Helpers); i++ {
		if manifest.Helpers[i-1].PID == manifest.Helpers[i].PID {
			return nil, refuseUpgrade("terminal helper handoff contains duplicate identities")
		}
	}

	activeTerminals := 0

	for _, item := range candidates {
		if item.driver.Exited() {
			continue
		}

		pid := item.driver.ProcessPID()
		if item.startTime <= 0 || item.statePID != pid {
			return nil, refuseUpgrade("session process identity is incomplete")
		}

		currentStart, err := grpty.ProcessStartTime(pid)
		if err != nil || currentStart != item.startTime {
			return nil, refuseUpgrade("session process identity changed during upgrade preparation")
		}

		manifest.planDrivers[item.id] = item.driver
		if item.session == nil {
			manifest.Sessions = append(manifest.Sessions, UpgradeSession{
				ID: item.id, Fd: -1, PID: pid, PIDStartTime: item.startTime,
			})

			continue
		}

		if item.session.Scrollback == nil || item.session.Scrollback.ValidatePathIdentity() != nil {
			return nil, refuseUpgrade("session scrollback identity changed during upgrade preparation")
		}

		fd, err := item.session.DuplicateFD()
		if err != nil {
			return nil, errors.Join(
				refuseUpgrade("session descriptor could not be duplicated"),
				rollbackUpgradeDescriptors(manifest),
			)
		}

		manifest.ownedDescriptors[fd] = struct{}{}

		scrollbackFD, err := item.session.Scrollback.DuplicateFD()
		if err != nil {
			return nil, errors.Join(
				refuseUpgrade("session scrollback descriptor could not be duplicated"),
				rollbackUpgradeDescriptors(manifest),
			)
		}

		manifest.ownedDescriptors[scrollbackFD] = struct{}{}
		manifest.Sessions = append(manifest.Sessions, UpgradeSession{
			ID: item.id, Fd: fd, HasPTY: true, ScrollbackFd: scrollbackFD, PID: pid, PIDStartTime: item.startTime,
		})
		manifest.planSessions[item.id] = item.session
		activeTerminals++
	}

	if maxSessions > 0 && activeTerminals > maxSessions {
		return nil, refuseUpgrade("upgrade target terminal capacity is too small")
	}

	fds := make([]int, 0, len(manifest.Sessions)*2+1)

	fds = append(fds, int(listenerFd))

	for _, session := range manifest.Sessions {
		if !upgradeSessionHasPTY(session) {
			continue
		}

		fds = append(fds, session.Fd, session.ScrollbackFd)
	}

	for _, fd := range fds {
		if _, duplicate := manifest.descriptorFlags[fd]; duplicate {
			return nil, refuseUpgrade("upgrade descriptor set contains duplicates")
		}

		flags, err := descriptorFlags(fd)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("read upgrade descriptor flags: %w", err),
				rollbackUpgradeDescriptors(manifest),
			)
		}

		manifest.descriptorFlags[fd] = flags
	}

	// Commit validation: no selected session may have been removed, replaced, or
	// rebound while the off-lock work ran. On failure restore every exact flag.
	sm.mu.RLock()

	unchanged := !requireReservation || sm.upgradePending

	for _, item := range candidates {
		if item.driver.Exited() {
			continue
		}

		state := sm.state.Sessions[item.id]
		if sm.sessions[item.id] != item.driver || state == nil ||
			state.PID != item.statePID || state.PIDStartTime != item.startTime {
			unchanged = false
			break
		}
	}

	sm.mu.RUnlock()

	if !unchanged {
		return nil, errors.Join(
			refuseUpgrade("session lifecycle changed during upgrade preparation"),
			rollbackUpgradeDescriptors(manifest),
		)
	}

	return manifest, nil
}

func canonicalUpgradePath(path string) string {
	if path == "" {
		return ""
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}

	return filepath.Clean(absolute)
}

func makeUpgradePathDescriptor(paths config.Paths, configFile string) UpgradePathDescriptor {
	if configFile == "" {
		configFile = paths.ConfigFile
	}

	return UpgradePathDescriptor{
		ConfigFile: canonicalUpgradePath(configFile),
		DataDir:    canonicalUpgradePath(paths.DataDir),
		StateFile:  canonicalUpgradePath(paths.StateFile),
		RuntimeDir: canonicalUpgradePath(paths.RuntimeDir),
		SocketPath: canonicalUpgradePath(paths.SocketPath),
	}
}

func validateUpgradePathDescriptor(manifest *UpgradeManifest, paths config.Paths, configFile string) error {
	want := makeUpgradePathDescriptor(paths, configFile)
	if manifest.Paths != want || manifest.ConfigFile != want.ConfigFile {
		return errors.New("upgrade manifest effective paths do not match daemon configuration")
	}

	return nil
}

func (sm *SessionManager) upgradePTYSessionIDsLocked() []string {
	ids := make([]string, 0, len(sm.sessions))
	for id, driver := range sm.sessions {
		if _, ok := driver.(*grpty.Session); !ok {
			continue
		}

		ids = append(ids, id)
	}

	slices.Sort(ids)

	return ids
}

func (sm *SessionManager) preflightUpgradeSessions(maxSessions int) error {
	type candidate struct {
		id        string
		driver    SessionDriver
		hasPTY    bool
		statePID  int
		startTime int64
	}

	sm.mu.RLock()

	for _, state := range sm.state.Sessions {
		if state.Status == StatusCreating || state.Status == StatusDeleting {
			sm.mu.RUnlock()

			return refuseUpgrade("session lifecycle work is still in progress")
		}
	}

	candidates := make([]candidate, 0, len(sm.sessions))
	for id, driver := range sm.sessions {
		state := sm.state.Sessions[id]
		if state == nil {
			sm.mu.RUnlock()

			return refuseUpgrade("session state is missing during upgrade")
		}

		candidates = append(candidates, candidate{
			id: id, driver: driver, statePID: state.PID, startTime: state.PIDStartTime,
		})
		_, candidates[len(candidates)-1].hasPTY = driver.(*grpty.Session)
	}

	sm.mu.RUnlock()

	identities := make([]UpgradeSession, 0, len(candidates))
	terminalCount := 0

	for _, item := range candidates {
		if item.driver.Exited() {
			continue
		}

		identities = append(identities, UpgradeSession{
			ID: item.id, HasPTY: item.hasPTY, PID: item.driver.ProcessPID(), PIDStartTime: item.startTime,
		})
		if item.hasPTY {
			terminalCount++
		}
	}

	if maxSessions > 0 && terminalCount > maxSessions {
		return refuseUpgrade("upgrade target terminal capacity is too small")
	}

	for _, identity := range identities {
		if identity.PID <= 0 || identity.PIDStartTime <= 0 {
			return refuseUpgrade("session process identity is incomplete")
		}

		startTime, err := grpty.ProcessStartTime(identity.PID)
		if err != nil || startTime != identity.PIDStartTime {
			return refuseUpgrade("session process identity changed during upgrade preflight")
		}
	}

	return nil
}

func (sm *SessionManager) beginUpgradeReservation() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.upgradePending {
		return refuseUpgrade("upgrade already in progress")
	}

	if sm.shutdownPending {
		return refuseUpgrade("daemon shutdown is in progress")
	}

	if len(sm.state.UpgradeCleanup) > 0 {
		return refuseUpgrade("session process cleanup from a prior upgrade is still pending")
	}

	sm.upgradePending = true

	return nil
}

func (sm *SessionManager) beginLifecycleOperation() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.upgradePending {
		return errors.New("daemon upgrade is pending")
	}

	if sm.shutdownPending {
		return errors.New("daemon shutdown is pending")
	}

	sm.lifecycleInFlight++

	return nil
}

func (sm *SessionManager) beginMutationRequest() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.upgradePending {
		return errors.New("daemon upgrade is pending")
	}

	if sm.shutdownPending {
		return errors.New("daemon shutdown is pending")
	}

	sm.mutationInFlight++

	return nil
}

func (sm *SessionManager) endMutationRequest() {
	sm.mu.Lock()
	if sm.mutationInFlight > 0 {
		sm.mutationInFlight--
	}
	sm.mu.Unlock()
}

func (sm *SessionManager) endLifecycleOperation() {
	sm.mu.Lock()
	sm.lifecycleInFlight--
	sm.mu.Unlock()
}

func (sm *SessionManager) beginShutdownBarrier() {
	sm.mu.Lock()
	sm.shutdownPending = true
	sm.upgradePending = true
	sm.mu.Unlock()
}

func (sm *SessionManager) waitMutationIdle(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		sm.mu.RLock()
		busy := sm.mutationInFlight > 0
		sm.mu.RUnlock()

		if !busy {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (sm *SessionManager) waitLifecycleIdle(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		sm.mu.RLock()

		busy := sm.lifecycleInFlight > 0
		if !busy {
			for _, state := range sm.state.Sessions {
				if state.Status == StatusCreating || state.Status == StatusDeleting {
					busy = true
					break
				}
			}
		}

		sm.mu.RUnlock()

		if !busy {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (sm *SessionManager) beginUpgradeAttempt() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.upgradeAttempt {
		return false
	}

	sm.upgradeAttempt = true

	return true
}

func (sm *SessionManager) endUpgradeAttempt() {
	sm.mu.Lock()
	sm.upgradeAttempt = false
	sm.mu.Unlock()
}

func (sm *SessionManager) endUpgradeReservation() {
	sm.mu.Lock()
	if !sm.shutdownPending {
		sm.upgradePending = false
	}
	sm.mu.Unlock()
}

func (sm *SessionManager) rejectLaunchDuringUpgradeLocked() error {
	if sm.upgradePending || sm.shutdownPending {
		return errors.New("daemon upgrade or shutdown is pending")
	}

	return nil
}

func (sm *SessionManager) lifecyclePreSpawnBarrier() error {
	if hook := sm.beforeLifecycleSpawn; hook != nil {
		hook()
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.rejectLaunchDuringUpgradeLocked()
}

func (sm *SessionManager) recoverTerminalScreensAfterUpgrade(ctx context.Context) {
	sm.mu.RLock()

	sessions := make([]*grpty.Session, 0, len(sm.sessions))
	for _, driver := range sm.sessions {
		if session, ok := driver.(*grpty.Session); ok {
			sessions = append(sessions, session)
		}
	}

	sm.mu.RUnlock()

	results := grpty.RecoverTerminalSessionsAfterUpgrade(ctx, sessions)
	for i, err := range results {
		if err != nil && ctx.Err() == nil {
			sm.log.Warn("terminal recovery after failed upgrade was incomplete", "session", sessions[i].ID, "err", err)
		}
	}
}

func (sm *SessionManager) quiesceSessionIO(ctx context.Context) (func(), error) {
	sm.mu.RLock()
	ids := sm.upgradePTYSessionIDsLocked()

	sessions := make([]*grpty.Session, 0, len(ids))
	for _, id := range ids {
		sessions = append(sessions, sm.sessions[id].(*grpty.Session))
	}

	sm.mu.RUnlock()

	releases := make([]func(), 0, len(sessions))
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}

	for _, session := range sessions {
		release, err := session.QuiesceIOForUpgrade(ctx)
		if err != nil {
			releaseAll()
			return nil, err
		}

		releases = append(releases, release)
	}

	return releaseAll, nil
}

func (sm *SessionManager) snapshotUpgradeStateLocked() ([]byte, error) {
	sm.state.Version = CurrentStateVersion

	return json.MarshalIndent(sm.state, "", "  ")
}

func (sm *SessionManager) persistUpgradeStateSnapshot(data []byte) error {
	sm.statePersistMu.Lock()
	defer sm.statePersistMu.Unlock()

	return sm.persistUpgradeStateSnapshotLocked(data)
}

func (sm *SessionManager) persistUpgradeStateSnapshotAtGeneration(data []byte, generation uint64) (bool, error) {
	if sm.persistUpgradeBeforeLock != nil {
		sm.persistUpgradeBeforeLock()
	}

	sm.statePersistMu.Lock()
	defer sm.statePersistMu.Unlock()

	if sm.statePersistGen.Load() != generation {
		return false, nil
	}

	return true, sm.persistUpgradeStateSnapshotLocked(data)
}

func (sm *SessionManager) persistUpgradeStateSnapshotLocked(data []byte) error {
	if sm.saveStateFault != nil {
		if err := sm.saveStateFault(); err != nil {
			return err
		}
	}

	err := writeFileAtomic(sm.paths.StateFile, data)
	if err == nil {
		sm.statePersistGen.Add(1)
	}

	return err
}

func (sm *SessionManager) persistLatestUpgradeState() error {
	for {
		generation := sm.statePersistGen.Load()
		sm.mu.Lock()
		data, err := sm.snapshotUpgradeStateLocked()
		sm.mu.Unlock()

		if err != nil {
			return err
		}

		if sm.persistLatestStateBeforeLock != nil {
			sm.persistLatestStateBeforeLock()
		}

		sm.statePersistMu.Lock()
		if sm.statePersistGen.Load() != generation {
			sm.statePersistMu.Unlock()
			continue
		}

		err = sm.persistUpgradeStateSnapshotLocked(data)
		sm.statePersistMu.Unlock()

		return err
	}
}

func (sm *SessionManager) persistFrozenUpgradeState(manifest *UpgradeManifest) error {
	if err := sm.validateUpgradePlan(manifest); err != nil {
		return err
	}

	sm.mu.Lock()
	if !sm.upgradePending {
		sm.mu.Unlock()

		return refuseUpgrade("upgrade reservation was lost before state persistence")
	}

	for _, session := range manifest.Sessions {
		state := sm.state.Sessions[session.ID]
		if sm.sessions[session.ID] != plannedUpgradeDriver(manifest, session.ID) || state == nil ||
			state.PID != session.PID || state.PIDStartTime != session.PIDStartTime {
			sm.mu.Unlock()

			return refuseUpgrade("upgrade plan changed before state persistence")
		}
	}

	data, err := sm.snapshotUpgradeStateLocked()
	generation := sm.statePersistGen.Load()
	sm.mu.Unlock()

	if err != nil {
		return fmt.Errorf("snapshot upgrade state: %w", err)
	}

	written, err := sm.persistUpgradeStateSnapshotAtGeneration(data, generation)
	if err != nil {
		return fmt.Errorf("persist upgrade state: %w", err)
	}

	if !written {
		return refuseUpgrade("state was persisted concurrently during upgrade preparation")
	}

	sm.mu.Lock()
	current, err := sm.snapshotUpgradeStateLocked()

	unchanged := sm.upgradePending && err == nil && bytes.Equal(data, current)
	for _, session := range manifest.Sessions {
		state := sm.state.Sessions[session.ID]
		if sm.sessions[session.ID] != plannedUpgradeDriver(manifest, session.ID) || state == nil ||
			state.PID != session.PID || state.PIDStartTime != session.PIDStartTime {
			unchanged = false
			break
		}
	}
	sm.mu.Unlock()

	if err != nil {
		return fmt.Errorf("revalidate upgrade state: %w", err)
	}

	if !unchanged {
		refusal := refuseUpgrade("session state changed while the upgrade snapshot was persisted")
		if restoreErr := sm.persistLatestUpgradeState(); restoreErr != nil {
			return errors.Join(refusal, fmt.Errorf("persist current state after refused upgrade: %w", restoreErr))
		}

		return refusal
	}

	manifest.StateSnapshot = slices.Clone(data)

	return sm.validateUpgradePlan(manifest)
}

func (sm *SessionManager) validateUpgradePlan(manifest *UpgradeManifest) error {
	type candidate struct {
		id        string
		driver    SessionDriver
		statePID  int
		startTime int64
	}

	sm.mu.RLock()

	candidates := make([]candidate, 0, len(sm.sessions))
	for id, driver := range sm.sessions {
		state := sm.state.Sessions[id]
		if state == nil {
			sm.mu.RUnlock()

			return refuseUpgrade("session state is missing during upgrade plan validation")
		}

		candidates = append(candidates, candidate{
			id: id, driver: driver, statePID: state.PID, startTime: state.PIDStartTime,
		})
	}

	sm.mu.RUnlock()

	planned := make(map[string]UpgradeSession, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		planned[session.ID] = session
	}

	active := 0

	for _, item := range candidates {
		exited := item.driver.Exited()

		entry, exists := planned[item.id]
		if exited {
			if exists {
				return refuseUpgrade("planned session exited before daemon exec")
			}

			continue
		}

		active++

		if !exists || plannedUpgradeDriver(manifest, item.id) != item.driver ||
			entry.PID != item.driver.ProcessPID() || entry.PID != item.statePID ||
			entry.PIDStartTime != item.startTime {
			return refuseUpgrade("session lifecycle changed after upgrade planning")
		}

		startTime, err := grpty.ProcessStartTime(entry.PID)
		if err != nil || startTime != entry.PIDStartTime {
			return refuseUpgrade("session process identity changed after upgrade planning")
		}
	}

	if active != len(manifest.Sessions) {
		return refuseUpgrade("upgrade plan no longer matches live sessions")
	}

	return nil
}

func makeUpgradeDescriptorsInheritable(manifest *UpgradeManifest) error {
	fds := make([]int, 0, len(manifest.descriptorFlags))
	for fd := range manifest.descriptorFlags {
		fds = append(fds, fd)
	}

	slices.Sort(fds)

	for _, fd := range fds {
		if err := setDescriptorFlags(fd, manifest.descriptorFlags[fd]&^syscall.FD_CLOEXEC); err != nil {
			return fmt.Errorf("make upgrade descriptor inheritable: %w", err)
		}
	}

	return nil
}

func rollbackUpgradeDescriptors(manifest *UpgradeManifest) error {
	if manifest == nil {
		return nil
	}

	fdSet := make(map[int]struct{}, len(manifest.descriptorFlags)+len(manifest.ownedDescriptors))
	for fd := range manifest.descriptorFlags {
		fdSet[fd] = struct{}{}
	}

	for fd := range manifest.ownedDescriptors {
		fdSet[fd] = struct{}{}
	}

	fds := make([]int, 0, len(fdSet))
	for fd := range fdSet {
		fds = append(fds, fd)
	}

	slices.Sort(fds)

	var rollbackErr error

	for _, fd := range fds {
		if _, owned := manifest.ownedDescriptors[fd]; owned {
			flags, err := rollbackGetDescriptorFlags(fd)
			if errors.Is(err, syscall.EBADF) {
				delete(manifest.ownedDescriptors, fd)
				delete(manifest.descriptorFlags, fd)

				continue
			}

			if err != nil {
				rollbackErr = errors.Join(rollbackErr, unsafeUpgradeDescriptor(
					fmt.Errorf("verify owned upgrade descriptor %d flags: %w", fd, err),
				))

				continue
			}

			if flags&syscall.FD_CLOEXEC == 0 {
				if err := rollbackSetDescriptorFlags(fd, flags|syscall.FD_CLOEXEC); err != nil {
					if errors.Is(err, syscall.EBADF) {
						delete(manifest.ownedDescriptors, fd)
						delete(manifest.descriptorFlags, fd)

						continue
					}

					closeErr := rollbackCloseDescriptor(fd)
					if closeErr == nil || errors.Is(closeErr, syscall.EBADF) {
						delete(manifest.ownedDescriptors, fd)
						delete(manifest.descriptorFlags, fd)
						rollbackErr = errors.Join(rollbackErr,
							fmt.Errorf("secure owned upgrade descriptor %d before close: %w", fd, err))

						continue
					}

					verifiedFlags, verifyErr := rollbackGetDescriptorFlags(fd)
					switch {
					case errors.Is(verifyErr, syscall.EBADF):
						delete(manifest.ownedDescriptors, fd)
						delete(manifest.descriptorFlags, fd)

						rollbackErr = errors.Join(rollbackErr, err, closeErr)
					case verifyErr == nil && verifiedFlags&syscall.FD_CLOEXEC != 0:
						delete(manifest.ownedDescriptors, fd)
						delete(manifest.descriptorFlags, fd)

						rollbackErr = errors.Join(rollbackErr, err, closeErr)
					default:
						rollbackErr = errors.Join(rollbackErr, unsafeUpgradeDescriptor(errors.Join(
							fmt.Errorf("secure owned upgrade descriptor %d: %w", fd, err),
							fmt.Errorf("close unsecured upgrade descriptor %d: %w", fd, closeErr),
						)))
					}

					continue
				}
			}

			if err := rollbackCloseDescriptor(fd); err != nil && !errors.Is(err, syscall.EBADF) {
				// Never retry close after an error: close(2) may already have
				// released the descriptor, and its number can be reused. CLOEXEC
				// was established first, so an ambiguous close cannot leak the
				// PTY into a later child even when it remains open.
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("close secured upgrade descriptor %d: %w", fd, err))
			}

			delete(manifest.ownedDescriptors, fd)
			delete(manifest.descriptorFlags, fd)

			continue
		}

		originalFlags := manifest.descriptorFlags[fd]
		if err := rollbackSetDescriptorFlags(fd, originalFlags); err == nil {
			delete(manifest.descriptorFlags, fd)
			continue
		} else if errors.Is(err, syscall.EBADF) {
			delete(manifest.descriptorFlags, fd)
			continue
		} else {
			flags, verifyErr := rollbackGetDescriptorFlags(fd)
			switch {
			case verifyErr == nil && flags == originalFlags:
				delete(manifest.descriptorFlags, fd)
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore upgrade descriptor %d flags reported failure: %w", fd, err))
			case errors.Is(verifyErr, syscall.EBADF):
				delete(manifest.descriptorFlags, fd)
			case verifyErr != nil:
				rollbackErr = errors.Join(rollbackErr, unsafeUpgradeDescriptor(errors.Join(
					fmt.Errorf("restore upgrade descriptor %d flags: %w", fd, err),
					fmt.Errorf("verify upgrade descriptor %d flags: %w", fd, verifyErr),
				)))
			default:
				rollbackErr = errors.Join(rollbackErr, unsafeUpgradeDescriptor(
					fmt.Errorf("restore upgrade descriptor %d flags: %w", fd, err),
				))
			}
		}
	}

	if len(manifest.descriptorFlags) == 0 {
		manifest.descriptorFlags = nil
	}

	if len(manifest.ownedDescriptors) == 0 {
		manifest.ownedDescriptors = nil
	}

	return rollbackErr
}

// rollbackUpgradeDescriptorsBeforeForkUnlock resolves every inheritable
// descriptor while syscall.ForkLock is still held. A genuinely unsafe result
// terminates the production process without running defers; the callback is a
// private test seam and must make the descriptor set safe before returning.
func rollbackUpgradeDescriptorsBeforeForkUnlock(manifest *UpgradeManifest, cause error) error {
	rollbackErr := rollbackUpgradeDescriptors(manifest)

	result := errors.Join(cause, rollbackErr)
	for hasUnsafeUpgradeDescriptor(rollbackErr) {
		unsafeUpgradeRollbackExit()

		rollbackErr = rollbackUpgradeDescriptors(manifest)
		result = errors.Join(cause, errors.New("fail-closed upgrade rollback exit returned"), rollbackErr)
	}

	return result
}

func WriteManifest(dir string, m *UpgradeManifest) (string, error) {
	unlock, err := lockUpgradeJournalPublication(dir)
	if err != nil {
		return "", err
	}
	defer unlock()

	if err := ensureUpgradeJournalID(m); err != nil {
		return "", err
	}

	if pending, err := upgradeJournalPaths(dir); err != nil {
		return "", err
	} else if len(pending) > 0 {
		return "", errors.New("an unresolved upgrade adoption journal already exists")
	}

	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}

	if len(data) > upgradeManifestMaxBytes {
		return "", errors.New("upgrade manifest exceeds size limit")
	}

	headerData, err := json.Marshal(upgradeOwnershipHeader{
		Version: m.Version, ListenerFD: m.ListenerFd,
		Sessions: m.Sessions,
		Helpers:  m.Helpers,
	})
	if err != nil || len(headerData) > upgradeOwnershipHeaderMax {
		return "", errors.New("upgrade ownership header exceeds size limit")
	}

	fileData := make([]byte, 0, len(upgradeOwnershipMagic)+4+len(headerData)+len(data))
	fileData = append(fileData, upgradeOwnershipMagic...)

	var headerLength [4]byte
	// upgradeOwnershipHeaderMax is 2 MiB, safely below uint32's limit.
	binary.BigEndian.PutUint32(headerLength[:], uint32(len(headerData))) //nolint:gosec // G115: bounded immediately above
	fileData = append(fileData, headerLength[:]...)
	fileData = append(fileData, headerData...)
	fileData = append(fileData, data...)
	m.journalSHA256 = sha256.Sum256(fileData)

	path := filepath.Join(dir, "upgrade-adoption-"+m.JournalID+".pending")

	tmp, err := os.CreateTemp(dir, ".upgrade-manifest-*")
	if err != nil {
		return "", err
	}

	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()

		return "", err
	}

	if _, err := tmp.Write(fileData); err != nil {
		_ = tmp.Close()

		return "", err
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return "", err
	}

	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Link is the portable no-replace publication primitive on Darwin/Linux.
	// The directory publication lock also prevents distinct JournalIDs racing
	// past the global unresolved-artifact scan above.
	if err := os.Link(tmpPath, path); err != nil {
		return "", err
	}

	if err := os.Remove(tmpPath); err != nil {
		return path, err
	}

	var committed unix.Stat_t
	if err := unix.Lstat(path, &committed); err != nil {
		return path, err
	}
	// Dev is retained only as an opaque kernel identity and compared using the
	// same bit-preserving conversion after adoption.
	m.journalDev = uint64(committed.Dev) //nolint:gosec,unconvert,nolintlint // G115/unconvert are platform-exclusive because Dev is signed on Darwin and unsigned on Linux.
	m.journalIno = committed.Ino

	if err := syncUpgradeManifestDirectory(dir); err != nil {
		removeErr := removeUpgradePublishedPath(path)
		// Best effort makes a failed commit removable as well as non-executable.
		// Join both sync results so a caller never mistakes uncertain directory
		// durability for a usable handoff.
		cleanupSyncErr := syncUpgradeManifestDirectory(dir)

		if removeErr == nil && cleanupSyncErr == nil {
			return "", err
		}

		return path, errors.Join(err, removeErr, cleanupSyncErr)
	}

	return path, nil
}

func lockUpgradeJournalPublication(dir string) (func(), error) {
	dirFD, err := openUpgradeArtifactDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()

	fd, err := unix.Openat(dirFD, ".upgrade-adoption.lock",
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) { //nolint:gosec // G115: geteuid returns the kernel's non-negative uid_t value
		_ = unix.Close(fd)

		return nil, errors.New("upgrade adoption publication lock is not a private regular file")
	}

	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)

		return nil, errors.New("another upgrade adoption publication is in progress")
	}

	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

func openUpgradeArtifactDirectory(dir string) (int, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 { //nolint:gosec // G115: geteuid returns the kernel's non-negative uid_t value
		_ = unix.Close(fd)

		return -1, errors.New("upgrade artifact directory is not owner-controlled")
	}

	return fd, nil
}

func openUpgradeArtifact(path string, flags int) (int, error) {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return -1, errors.New("upgrade artifact path is invalid")
	}

	dirFD, err := openUpgradeArtifactDirectory(filepath.Dir(path))
	if err != nil {
		return -1, err
	}

	defer func() { _ = unix.Close(dirFD) }()

	return unix.Openat(dirFD, base, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func ensureUpgradeJournalID(manifest *UpgradeManifest) error {
	if manifest.JournalID == "" {
		id, err := randomHex(16)
		if err != nil {
			return err
		}

		manifest.JournalID = id
	}

	decoded, err := hex.DecodeString(manifest.JournalID)
	if err != nil || len(decoded) != 16 {
		return errors.New("upgrade adoption journal ID is invalid")
	}

	return nil
}

func upgradeJournalPaths(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var paths []string

	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, "upgrade-adoption-") &&
			(strings.HasSuffix(name, ".pending") || strings.HasSuffix(name, ".quarantine") ||
				strings.HasSuffix(name, ".pending.committed") || strings.HasSuffix(name, ".pending.rolledback")) {
			paths = append(paths, filepath.Join(dir, name))
		}
	}

	slices.Sort(paths)

	return paths, nil
}

const (
	upgradeJournalCommitted  = "committed"
	upgradeJournalRolledBack = "rolledback"
)

func upgradeJournalMarkerPath(path, phase string) string { return path + "." + phase }

func writeUpgradeJournalMarker(path string, manifest *UpgradeManifest, phase string) error {
	if phase != upgradeJournalCommitted && phase != upgradeJournalRolledBack {
		return errors.New("upgrade adoption journal phase is invalid")
	}

	if manifest == nil || filepath.Base(path) != "upgrade-adoption-"+manifest.JournalID+".pending" {
		return errors.New("upgrade adoption journal marker identity is invalid")
	}

	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".upgrade-marker-*")
	if err != nil {
		return err
	}

	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()

		return err
	}

	if manifest.journalSHA256 == ([sha256.Size]byte{}) {
		_ = tmp.Close()

		return errors.New("upgrade adoption journal digest is unavailable")
	}

	data := []byte(phase + "\n" + manifest.JournalID + "\n" + hex.EncodeToString(manifest.journalSHA256[:]) + "\n")
	if len(data) > upgradeJournalMarkerMaxBytes {
		_ = tmp.Close()

		return errors.New("upgrade adoption journal marker exceeds size limit")
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	markerPath := upgradeJournalMarkerPath(path, phase)
	if err := os.Link(tmpPath, markerPath); err != nil {
		return err
	}

	if err := os.Remove(tmpPath); err != nil {
		return err
	}

	return syncUpgradeManifestDirectory(dir)
}

func readUpgradeJournalMarker(path string, manifest *UpgradeManifest) (string, error) {
	if manifest == nil || manifest.journalSHA256 == ([sha256.Size]byte{}) {
		return "", errors.New("upgrade adoption journal marker digest is unavailable")
	}

	wantDigest := hex.EncodeToString(manifest.journalSHA256[:])
	found := ""

	for _, phase := range []string{upgradeJournalCommitted, upgradeJournalRolledBack} {
		marker := upgradeJournalMarkerPath(path, phase)

		fd, err := openUpgradeArtifact(marker, unix.O_RDONLY)
		if errors.Is(err, syscall.ENOENT) {
			continue
		}

		if err != nil {
			return "", err
		}

		var stat unix.Stat_t

		statErr := unix.Fstat(fd, &stat)
		if statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 ||
			stat.Uid != uint32(os.Geteuid()) || stat.Size <= 0 || stat.Size > upgradeJournalMarkerMaxBytes { //nolint:gosec // G115: geteuid returns the kernel's non-negative uid_t value
			_ = unix.Close(fd)

			return "", errors.New("upgrade adoption journal marker is not a private bounded regular file")
		}

		data := make([]byte, stat.Size)
		readErr := preadFull(fd, data, 0)

		closeErr := unix.Close(fd)
		if readErr != nil || closeErr != nil {
			return "", errors.New("upgrade adoption journal marker could not be read")
		}

		if string(data) != phase+"\n"+manifest.JournalID+"\n"+wantDigest+"\n" {
			return "", errors.New("upgrade adoption journal marker is invalid")
		}

		if found != "" {
			return "", errors.New("upgrade adoption journal has conflicting phase markers")
		}

		found = phase
	}

	return found, nil
}

func removeUpgradeJournalMarker(path, phase string) error {
	if phase == "" {
		return nil
	}

	if err := os.Remove(upgradeJournalMarkerPath(path, phase)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return syncUpgradeManifestDirectory(filepath.Dir(path))
}

func removeUpgradeJournal(path string, manifest *UpgradeManifest) error {
	if manifest == nil || manifest.JournalID == "" ||
		filepath.Base(path) != "upgrade-adoption-"+manifest.JournalID+".pending" {
		return errors.New("upgrade adoption journal path identity is invalid")
	}

	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		uint64(stat.Dev) != manifest.journalDev || stat.Ino != manifest.journalIno { //nolint:gosec,unconvert,nolintlint // G115/unconvert are platform-exclusive because Dev is signed on Darwin and unsigned on Linux.
		return errors.New("upgrade adoption journal pathname was replaced")
	}

	if err := os.Remove(path); err != nil {
		return err
	}

	return syncUpgradeManifestDirectory(filepath.Dir(path))
}

func recoverPendingUpgradeJournals(dir string) error {
	paths, err := upgradeJournalPaths(dir)
	if err != nil || len(paths) == 0 {
		return err
	}

	var pending, markers []string

	for _, path := range paths {
		switch {
		case strings.HasSuffix(path, ".quarantine"):
			return errors.New("a quarantined upgrade adoption journal requires manual recovery")
		case strings.HasSuffix(path, ".pending"):
			pending = append(pending, path)
		case strings.HasSuffix(path, ".pending.committed"), strings.HasSuffix(path, ".pending.rolledback"):
			markers = append(markers, path)
		}
	}

	if len(pending) > 1 {
		return errors.New("multiple pending upgrade adoption journals require manual recovery")
	}

	if len(pending) == 0 {
		for _, marker := range markers {
			if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}

		if len(markers) > 0 {
			return syncUpgradeManifestDirectory(dir)
		}

		return nil
	}

	path := pending[0]

	for _, marker := range markers {
		base := strings.TrimSuffix(strings.TrimSuffix(marker, ".committed"), ".rolledback")
		if base != path {
			return errors.New("upgrade adoption journal artifacts are not one coherent set")
		}
	}

	manifest, readErr := ReadManifest(path)
	if readErr != nil {
		if quarantineErr := quarantineUpgradeJournal(path); quarantineErr != nil {
			return errors.Join(errors.New("malformed upgrade adoption journal"), quarantineErr)
		}

		return errors.New("malformed upgrade adoption journal was quarantined")
	}

	if filepath.Base(path) != "upgrade-adoption-"+manifest.JournalID+".pending" ||
		manifest.Version != upgradeManifestVersion || manifest.Paths.RuntimeDir != canonicalUpgradePath(dir) {
		if quarantineErr := quarantineUpgradeJournal(path); quarantineErr != nil {
			return errors.Join(errors.New("misplaced upgrade adoption journal"), quarantineErr)
		}

		return errors.New("misplaced upgrade adoption journal was quarantined")
	}

	phase, markerErr := readUpgradeJournalMarker(path, manifest)
	if markerErr != nil {
		return markerErr
	}

	if phase != "" {
		if err := removeUpgradeJournal(path, manifest); err != nil {
			// A durable committed/rolled-back marker is authoritative: never
			// signal these identities even when artifact cleanup must retry.
			return err
		}

		if err := removeUpgradeJournalMarker(path, phase); err != nil {
			return err
		}

		return nil
	}

	deadline := time.Now().Add(upgradeAdoptionTimeout)

	type cleanupResult struct {
		ok  bool
		err error
	}

	count := len(manifest.Sessions) + len(manifest.Helpers)
	results := make(chan cleanupResult, count)

	for _, session := range manifest.Sessions {
		go func() {
			ok, cleanupErr := terminateFailedUpgradeSessionUntil(session, deadline)
			results <- cleanupResult{ok: ok, err: cleanupErr}
		}()
	}

	for _, helper := range manifest.Helpers {
		go func() {
			ok, cleanupErr := terminateFailedUpgradeSessionUntil(UpgradeSession{
				ID: "helper", PID: helper.PID, PIDStartTime: helper.StartTime,
			}, deadline)
			results <- cleanupResult{ok: ok, err: cleanupErr}
		}()
	}

	var cleanupErr error

	for i := 0; i < count; i++ {
		result := <-results
		if !result.ok || result.err != nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("pending upgrade ownership cleanup is unresolved"))
		}
	}

	if cleanupErr != nil {
		return cleanupErr
	}

	if err := removeUpgradeJournal(path, manifest); err != nil {
		return err
	}

	return nil
}

func quarantineUpgradeJournal(path string) error {
	if !strings.HasSuffix(path, ".pending") {
		return errors.New("upgrade adoption journal quarantine path is invalid")
	}

	quarantine := strings.TrimSuffix(path, ".pending") + ".quarantine"
	if err := os.Link(path, quarantine); err != nil {
		return err
	}

	if err := os.Remove(path); err != nil {
		return err
	}

	if err := syncUpgradeManifestDirectory(filepath.Dir(path)); err != nil {
		return err
	}

	return nil
}

var syncUpgradeManifestDirectory = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}

	syncErr := d.Sync()
	closeErr := d.Close()

	return errors.Join(syncErr, closeErr)
}

var removeUpgradePublishedPath = os.Remove

func ReadManifest(path string) (*UpgradeManifest, error) {
	fd, err := openUpgradeArtifact(path, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}

	f := os.NewFile(uintptr(fd), "upgrade-manifest")
	if f == nil {
		_ = unix.Close(fd)

		return nil, errors.New("open upgrade manifest")
	}

	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() > upgradeManifestMaxBytes+upgradeOwnershipHeaderMax+int64(len(upgradeOwnershipMagic)+4) {
		return nil, errors.New("upgrade manifest is not a private bounded regular file")
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) { //nolint:gosec // G115: geteuid returns the kernel's non-negative uid_t value
		return nil, errors.New("upgrade manifest owner is invalid")
	}

	data, err := io.ReadAll(io.LimitReader(f, upgradeManifestMaxBytes+upgradeOwnershipHeaderMax+int64(len(upgradeOwnershipMagic)+5)))
	if err != nil {
		return nil, errors.New("upgrade manifest could not be read within its limit")
	}

	header, body, err := splitUpgradeManifestData(data)
	if err != nil {
		return nil, err
	}

	manifest, err := decodeUpgradeManifest(body)
	if err != nil {
		return nil, err
	}

	if header != nil && !upgradeOwnershipMatches(header, manifest) {
		return nil, errors.New("upgrade manifest ownership differs from cleanup header")
	}

	manifest.journalDev = uint64(stat.Dev) //nolint:gosec,unconvert,nolintlint // G115/unconvert are platform-exclusive because Dev is signed on Darwin and unsigned on Linux.
	manifest.journalIno = stat.Ino
	manifest.journalSHA256 = sha256.Sum256(data)

	return manifest, nil
}

func decodeUpgradeManifest(data []byte) (*UpgradeManifest, error) {
	var m UpgradeManifest

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&m); err != nil {
		return nil, err
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("upgrade manifest has trailing data")
	}

	if err := validateUpgradeManifestStructure(&m); err != nil {
		return nil, err
	}

	return &m, nil
}

func splitUpgradeManifestData(data []byte) (*upgradeOwnershipHeader, []byte, error) {
	if len(data) > 0 && data[0] == '{' {
		if len(data) > upgradeManifestMaxBytes {
			return nil, nil, errors.New("upgrade manifest exceeds size limit")
		}

		return nil, data, nil // legacy path-only manifest
	}

	minimum := len(upgradeOwnershipMagic) + 4
	if len(data) < minimum || !bytes.Equal(data[:len(upgradeOwnershipMagic)], upgradeOwnershipMagic) {
		return nil, nil, errors.New("upgrade ownership header magic is invalid")
	}

	headerLength := int(binary.BigEndian.Uint32(data[len(upgradeOwnershipMagic):minimum]))
	if headerLength <= 0 || headerLength > upgradeOwnershipHeaderMax || minimum+headerLength > len(data) {
		return nil, nil, errors.New("upgrade ownership header length is invalid")
	}

	var header upgradeOwnershipHeader

	decoder := json.NewDecoder(bytes.NewReader(data[minimum : minimum+headerLength]))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&header); err != nil {
		return nil, nil, errors.New("upgrade ownership header is invalid")
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("upgrade ownership header has trailing data")
	}

	if err := validateUpgradeOwnershipHeader(&header); err != nil {
		return nil, nil, err
	}

	body := data[minimum+headerLength:]
	if len(body) == 0 || len(body) > upgradeManifestMaxBytes {
		return nil, nil, errors.New("upgrade manifest body size is invalid")
	}

	return &header, body, nil
}

func validateUpgradeOwnershipHeader(header *upgradeOwnershipHeader) error {
	if header == nil || header.ListenerFD <= 2 || header.ListenerFD > math.MaxInt32 ||
		len(header.Sessions) > upgradeManifestMaxSessions || len(header.Helpers) > upgradeManifestMaxHelpers {
		return errors.New("upgrade ownership header resource bounds are invalid")
	}

	if header.Version != 0 && header.Version != upgradeManifestVersion {
		return errors.New("upgrade ownership header version is unsupported")
	}

	fds := map[int]struct{}{header.ListenerFD: {}}
	pids := make(map[int]struct{}, len(header.Sessions)+len(header.Helpers))

	ids := make(map[string]struct{}, len(header.Sessions))
	for _, session := range header.Sessions {
		hasPTY := upgradeSessionHasPTY(session)
		if session.ID == "" || session.PID <= 1 || session.PID > math.MaxInt32 ||
			((header.Version == upgradeManifestVersion || !hasPTY) && session.PIDStartTime <= 0) ||
			(hasPTY && (session.Fd <= 2 || session.Fd > math.MaxInt32 || session.ScrollbackFd > math.MaxInt32 ||
				(header.Version == upgradeManifestVersion && session.ScrollbackFd <= 2))) ||
			(!hasPTY && (session.Fd != -1 || session.ScrollbackFd != 0)) {
			return errors.New("upgrade ownership header session identity is invalid")
		}

		if hasPTY {
			if _, exists := fds[session.Fd]; exists {
				return errors.New("upgrade ownership header descriptors are not unique")
			}

			fds[session.Fd] = struct{}{}
			if session.ScrollbackFd > 2 {
				if _, exists := fds[session.ScrollbackFd]; exists {
					return errors.New("upgrade ownership header descriptors are not unique")
				}

				fds[session.ScrollbackFd] = struct{}{}
			}
		}

		if _, exists := pids[session.PID]; exists {
			return errors.New("upgrade ownership header process IDs are not unique")
		}

		if _, exists := ids[session.ID]; exists {
			return errors.New("upgrade ownership header session IDs are not unique")
		}

		pids[session.PID] = struct{}{}
		ids[session.ID] = struct{}{}
	}

	for _, helper := range header.Helpers {
		if header.Version != upgradeManifestVersion || helper.PID <= 1 ||
			helper.PID > math.MaxInt32 || helper.StartTime <= 0 {
			return errors.New("upgrade ownership header helper identity is invalid")
		}

		if _, exists := pids[helper.PID]; exists {
			return errors.New("upgrade ownership header process IDs are not unique")
		}

		pids[helper.PID] = struct{}{}
	}

	return nil
}

func upgradeOwnershipMatches(header *upgradeOwnershipHeader, manifest *UpgradeManifest) bool {
	if header == nil || manifest == nil || header.Version != manifest.Version || header.ListenerFD != manifest.ListenerFd ||
		len(header.Sessions) != len(manifest.Sessions) || len(header.Helpers) != len(manifest.Helpers) {
		return false
	}

	for i := range header.Sessions {
		if header.Sessions[i] != manifest.Sessions[i] {
			return false
		}
	}

	for i := range header.Helpers {
		if header.Helpers[i] != manifest.Helpers[i] {
			return false
		}
	}

	return true
}

func prepareManifestHandoff(path string, manifest *UpgradeManifest) error {
	fd, err := openUpgradeArtifact(path, unix.O_RDONLY)
	if err != nil {
		return err
	}

	keep := false
	defer func() {
		if !keep {
			_ = unix.Close(fd)
		}
	}()

	_, decoded, err := readManifestHandoffDescriptor(fd)
	if err != nil {
		return err
	}

	if !upgradeOwnershipMatches(&upgradeOwnershipHeader{
		Version: manifest.Version, ListenerFD: manifest.ListenerFd, Sessions: manifest.Sessions, Helpers: manifest.Helpers,
	}, decoded) {
		return errors.New("upgrade manifest handoff identity changed")
	}

	manifest.journalDev = decoded.journalDev

	manifest.journalIno = decoded.journalIno
	if manifest.journalSHA256 == ([sha256.Size]byte{}) || manifest.journalSHA256 != decoded.journalSHA256 {
		return errors.New("upgrade manifest handoff digest changed")
	}

	flags, err := descriptorFlags(fd)
	if err != nil {
		return err
	}

	if manifest.descriptorFlags == nil {
		manifest.descriptorFlags = make(map[int]int)
	}

	if manifest.ownedDescriptors == nil {
		manifest.ownedDescriptors = make(map[int]struct{})
	}

	manifest.ownershipFD = fd
	manifest.descriptorFlags[fd] = flags
	manifest.ownedDescriptors[fd] = struct{}{}
	keep = true

	return nil
}

// prepareOwnershipCapsule serializes the minimum cleanup identity into the
// exec environment. The kernel copies this bounded value into the replacement
// image, so bootstrap can arm cleanup without filesystem I/O or a blocking
// descriptor read. Session IDs are deliberately omitted from process-visible
// environment state; the full manifest supplies and cross-checks them later.
func prepareOwnershipCapsule(manifest *UpgradeManifest) error {
	if err := ensureUpgradeJournalID(manifest); err != nil {
		return err
	}

	journalID, _ := hex.DecodeString(manifest.JournalID)
	if err := validateUpgradeOwnershipHeader(&upgradeOwnershipHeader{
		Version: manifest.Version, ListenerFD: manifest.ListenerFd,
		Sessions: manifest.Sessions, Helpers: manifest.Helpers,
	}); err != nil {
		return err
	}

	if manifest.journalSHA256 == ([sha256.Size]byte{}) {
		return errors.New("upgrade ownership capsule journal digest is unavailable")
	}

	data := make([]byte, 0, 68+len(manifest.Sessions)*20+len(manifest.Helpers)*12+sha256.Size)
	data = append(data, upgradeOwnershipCapsuleMagic...)
	// Header validation constrains the version to a small protocol constant and
	// counts to 4096 sessions and 256 helpers, all within uint16.
	data = binary.BigEndian.AppendUint16(data, uint16(manifest.Version))       //nolint:gosec // G115: validated protocol constant
	data = binary.BigEndian.AppendUint16(data, uint16(len(manifest.Sessions))) //nolint:gosec // G115: validated maximum 4096
	data = binary.BigEndian.AppendUint16(data, uint16(len(manifest.Helpers)))  //nolint:gosec // G115: validated maximum 256
	data = binary.BigEndian.AppendUint16(data, 0)
	data = binary.BigEndian.AppendUint32(data, uint32(manifest.ListenerFd)) //nolint:gosec // G115: header validation bounds descriptors to uint32
	data = append(data, journalID...)

	data = append(data, manifest.journalSHA256[:]...)
	for _, session := range manifest.Sessions {
		fd := uint32(math.MaxUint32)
		if upgradeSessionHasPTY(session) {
			fd = uint32(session.Fd) //nolint:gosec // G115: header validation bounds descriptors to uint32
		}

		data = binary.BigEndian.AppendUint32(data, fd)
		data = binary.BigEndian.AppendUint32(data, uint32(session.ScrollbackFd)) //nolint:gosec // G115: header validation bounds descriptors to uint32
		data = binary.BigEndian.AppendUint32(data, uint32(session.PID))          //nolint:gosec // G115: header validation bounds process IDs to uint32
		data = binary.BigEndian.AppendUint64(data, uint64(session.PIDStartTime)) //nolint:gosec // G115: header validation requires a positive int64
	}

	for _, helper := range manifest.Helpers {
		data = binary.BigEndian.AppendUint32(data, uint32(helper.PID))       //nolint:gosec // G115: header validation bounds process IDs to uint32
		data = binary.BigEndian.AppendUint64(data, uint64(helper.StartTime)) //nolint:gosec // G115: header validation requires a positive int64
	}

	digest := sha256.Sum256(data)

	data = append(data, digest[:]...)
	if len(data) == 0 || len(data) > upgradeOwnershipCapsuleMax {
		return errors.New("upgrade ownership capsule exceeds exec environment limit")
	}

	manifest.ownershipCapsule = base64.RawURLEncoding.EncodeToString(data)
	if len(manifest.ownershipCapsule) > upgradeOwnershipCapsuleMax*2 {
		return errors.New("upgrade ownership capsule exceeds encoded limit")
	}

	return nil
}

func captureUpgradeConfigSnapshot(path string) ([]byte, bool, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	defer func() { _ = unix.Close(fd) }()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 || stat.Size < 0 || stat.Size > upgradeConfigSnapshotMax { //nolint:gosec // G115: geteuid returns the kernel's non-negative uid_t value
		return nil, false, errors.New("upgrade config snapshot is not a bounded owner-controlled regular file")
	}

	data := make([]byte, stat.Size)
	if err := preadFull(fd, data, 0); err != nil {
		return nil, false, errors.New("upgrade config snapshot could not be read")
	}

	return data, true, nil
}

func makeUpgradeConfigSource(path string, snapshot []byte, present bool) UpgradeConfigSource {
	source := UpgradeConfigSource{Path: canonicalUpgradePath(path), Present: present}
	if present {
		digest := sha256.Sum256(snapshot)
		source.SHA256 = hex.EncodeToString(digest[:])
	}

	return source
}

func captureUpgradeBootstrapEnvironment() (capsule, ownershipFD string) {
	capsule = os.Getenv(upgradeOwnershipCapsuleEnv)
	ownershipFD = os.Getenv(upgradeOwnershipFDEnv)
	_ = os.Unsetenv(upgradeOwnershipCapsuleEnv)
	_ = os.Unsetenv(upgradeOwnershipFDEnv)

	return capsule, ownershipFD
}

func readInheritedOwnershipCapsule(raw string) (*UpgradeManifest, error) {
	if raw == "" || len(raw) > upgradeOwnershipCapsuleMax*2 {
		return nil, errors.New("inherited upgrade ownership capsule is missing or oversized")
	}

	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) == 0 || len(data) > upgradeOwnershipCapsuleMax {
		return nil, errors.New("inherited upgrade ownership capsule encoding is invalid")
	}

	const fixed = 68
	if len(data) < fixed+sha256.Size || !bytes.Equal(data[:len(upgradeOwnershipCapsuleMagic)], upgradeOwnershipCapsuleMagic) {
		return nil, errors.New("inherited upgrade ownership capsule header is invalid")
	}

	version := int(binary.BigEndian.Uint16(data[8:10]))
	sessionCount := int(binary.BigEndian.Uint16(data[10:12]))

	helperCount := int(binary.BigEndian.Uint16(data[12:14]))
	if binary.BigEndian.Uint16(data[14:16]) != 0 || sessionCount > upgradeManifestMaxSessions ||
		helperCount > upgradeManifestMaxHelpers {
		return nil, errors.New("inherited upgrade ownership capsule bounds are invalid")
	}

	wantSize := fixed + sessionCount*20 + helperCount*12 + sha256.Size
	if len(data) != wantSize {
		return nil, errors.New("inherited upgrade ownership capsule length is invalid")
	}

	wantDigest := sha256.Sum256(data[:len(data)-sha256.Size])
	if !bytes.Equal(wantDigest[:], data[len(data)-sha256.Size:]) {
		return nil, errors.New("inherited upgrade ownership capsule digest is invalid")
	}

	owned := &UpgradeManifest{
		Version: version, ListenerFd: int(binary.BigEndian.Uint32(data[16:20])),
		JournalID: hex.EncodeToString(data[20:36]),
		Helpers:   make([]UpgradeHelper, 0, helperCount),
		Sessions:  make([]UpgradeSession, 0, sessionCount),
	}
	copy(owned.journalSHA256[:], data[36:68])

	offset := fixed
	for i := 0; i < sessionCount; i++ {
		pidStartTime, ok := decodePositiveInt64(data[offset+12 : offset+20])
		if !ok {
			return nil, errors.New("inherited upgrade ownership capsule session identity is invalid")
		}

		fdWord := binary.BigEndian.Uint32(data[offset : offset+4])
		fd := -1

		hasPTY := fdWord != math.MaxUint32
		if hasPTY {
			fd = int(fdWord)
		}

		owned.Sessions = append(owned.Sessions, UpgradeSession{
			ID:           "capsule-" + strconv.Itoa(i),
			Fd:           fd,
			HasPTY:       hasPTY,
			ScrollbackFd: int(binary.BigEndian.Uint32(data[offset+4 : offset+8])),
			PID:          int(binary.BigEndian.Uint32(data[offset+8 : offset+12])),
			PIDStartTime: pidStartTime,
		})
		offset += 20
	}

	for i := 0; i < helperCount; i++ {
		startTime, ok := decodePositiveInt64(data[offset+4 : offset+12])
		if !ok {
			return nil, errors.New("inherited upgrade ownership capsule helper identity is invalid")
		}

		owned.Helpers = append(owned.Helpers, UpgradeHelper{
			PID:       int(binary.BigEndian.Uint32(data[offset : offset+4])),
			StartTime: startTime,
		})
		offset += 12
	}

	if err := validateUpgradeOwnershipHeader(&upgradeOwnershipHeader{
		Version: owned.Version, ListenerFD: owned.ListenerFd,
		Sessions: owned.Sessions, Helpers: owned.Helpers,
	}); err != nil {
		return nil, err
	}

	return owned, nil
}

func decodePositiveInt64(data []byte) (int64, bool) {
	value := binary.BigEndian.Uint64(data)
	if value == 0 || value > uint64(math.MaxInt64) {
		return 0, false
	}

	return int64(value), true
}

func upgradeOwnershipResourcesMatch(a, b *UpgradeManifest) bool {
	if a == nil || b == nil || a.Version != b.Version || a.ListenerFd != b.ListenerFd ||
		(a.JournalID != "" && b.JournalID != "" && a.JournalID != b.JournalID) ||
		len(a.Sessions) != len(b.Sessions) || len(a.Helpers) != len(b.Helpers) {
		return false
	}

	for i := range a.Sessions {
		left, right := a.Sessions[i], b.Sessions[i]
		if upgradeSessionHasPTY(left) != upgradeSessionHasPTY(right) ||
			left.Fd != right.Fd || left.ScrollbackFd != right.ScrollbackFd || left.PID != right.PID ||
			left.PIDStartTime != right.PIDStartTime {
			return false
		}
	}

	for i := range a.Helpers {
		if a.Helpers[i] != b.Helpers[i] {
			return false
		}
	}

	return true
}

func validateInheritedManifestBinding(capsule, header, manifest *UpgradeManifest) error {
	if !upgradeOwnershipResourcesMatch(capsule, header) {
		return errors.New("upgrade manifest ownership differs from immutable cleanup capsule")
	}

	if capsule.journalSHA256 == ([sha256.Size]byte{}) || capsule.journalSHA256 != header.journalSHA256 {
		return errors.New("upgrade manifest digest differs from immutable cleanup capsule")
	}

	if manifest == nil || capsule.JournalID == "" || capsule.JournalID != manifest.JournalID {
		return errors.New("upgrade manifest journal differs from immutable cleanup capsule")
	}

	return nil
}

// readManifestHandoffDescriptor decodes the independently bounded cleanup
// header first. The returned ownership manifest is usable even if the full
// protocol body is malformed, so callers can arm cleanup before handling the
// body error.
func readManifestHandoffDescriptor(fd int) (*UpgradeManifest, *UpgradeManifest, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || //nolint:gosec // G115: geteuid returns the kernel's non-negative uid_t value
		stat.Size < int64(len(upgradeOwnershipMagic)+4) ||
		stat.Size > upgradeManifestMaxBytes+upgradeOwnershipHeaderMax+int64(len(upgradeOwnershipMagic)+4) {
		return nil, nil, errors.New("upgrade manifest descriptor is not a private bounded regular file")
	}

	prefix := make([]byte, len(upgradeOwnershipMagic)+4)
	if err := preadFull(fd, prefix, 0); err != nil || !bytes.Equal(prefix[:len(upgradeOwnershipMagic)], upgradeOwnershipMagic) {
		return nil, nil, errors.New("upgrade ownership descriptor header is invalid")
	}

	headerLength := int(binary.BigEndian.Uint32(prefix[len(upgradeOwnershipMagic):]))
	if headerLength <= 0 || headerLength > upgradeOwnershipHeaderMax || int64(len(prefix)+headerLength) >= stat.Size {
		return nil, nil, errors.New("upgrade ownership descriptor length is invalid")
	}

	headerData := make([]byte, headerLength)
	if err := preadFull(fd, headerData, int64(len(prefix))); err != nil {
		return nil, nil, errors.New("upgrade ownership descriptor could not be read")
	}

	var header upgradeOwnershipHeader

	decoder := json.NewDecoder(bytes.NewReader(headerData))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&header); err != nil {
		return nil, nil, errors.New("upgrade ownership descriptor could not be decoded")
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("upgrade ownership descriptor has trailing data")
	}

	if err := validateUpgradeOwnershipHeader(&header); err != nil {
		return nil, nil, err
	}

	owned := &UpgradeManifest{
		Version: header.Version, ListenerFd: header.ListenerFD,
		Sessions: header.Sessions, Helpers: header.Helpers,
	}

	bodyOffset := int64(len(prefix) + headerLength)

	bodySize := stat.Size - bodyOffset
	if bodySize <= 0 || bodySize > upgradeManifestMaxBytes {
		return owned, nil, errors.New("upgrade manifest body size is invalid")
	}

	body := make([]byte, bodySize)
	if err := preadFull(fd, body, bodyOffset); err != nil {
		return owned, nil, errors.New("upgrade manifest body could not be read")
	}

	manifest, err := decodeUpgradeManifest(body)
	if err != nil {
		return owned, nil, err
	}

	if !upgradeOwnershipMatches(&header, manifest) {
		return owned, nil, errors.New("upgrade manifest ownership differs from cleanup header")
	}

	digest := sha256.New()
	_, _ = digest.Write(prefix)
	_, _ = digest.Write(headerData)
	_, _ = digest.Write(body)
	copy(owned.journalSHA256[:], digest.Sum(nil))
	manifest.journalSHA256 = owned.journalSHA256
	manifest.journalDev = uint64(stat.Dev) //nolint:gosec,unconvert,nolintlint // G115/unconvert are platform-exclusive because Dev is signed on Darwin and unsigned on Linux.
	manifest.journalIno = stat.Ino

	return owned, manifest, nil
}

func preadFull(fd int, data []byte, offset int64) error {
	for len(data) > 0 {
		n, err := upgradePread(fd, data, offset)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}

			return err
		}

		if n == 0 {
			return io.ErrUnexpectedEOF
		}

		data = data[n:]
		offset += int64(n)
	}

	return nil
}

var upgradePread = unix.Pread

func readInheritedManifestHandoff(ctx context.Context, raw string) (*UpgradeManifest, *UpgradeManifest, error) {
	fd, err := strconv.Atoi(raw)
	if err != nil || fd <= 2 {
		return nil, nil, errors.New("inherited upgrade ownership descriptor is missing")
	}

	if err := secureTransferredDescriptor(fd); err != nil {
		return nil, nil, errors.New("inherited upgrade ownership descriptor could not be secured")
	}

	workerFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, nil, errors.New("inherited upgrade ownership descriptor could not be duplicated")
	}

	if err := verifyTransferredDescriptorClosed(fd, adoptCloseDescriptor(fd)); err != nil {
		_ = syscall.Close(workerFD)

		return nil, nil, errors.New("inherited upgrade ownership descriptor close could not be proven")
	}

	type result struct {
		owned, manifest *UpgradeManifest
		err             error
	}

	done := make(chan result, 1)

	go func() {
		owned, manifest, readErr := readManifestHandoffDescriptor(workerFD)
		_ = syscall.Close(workerFD)

		done <- result{owned: owned, manifest: manifest, err: readErr}
	}()

	select {
	case result := <-done:
		return result.owned, result.manifest, result.err
	case <-ctx.Done():
		// The worker retains the dedicated duplicate until its read returns or
		// process exit, so cleanup can never close/reuse the number beneath a
		// stalled FUSE read.
		return nil, nil, errors.New("upgrade manifest read exceeded adoption deadline")
	}
}

func validateUpgradeManifestStructure(m *UpgradeManifest) error {
	if m.Version != 0 && m.Version != upgradeManifestVersion {
		return errors.New("upgrade manifest version is unsupported")
	}

	if len(m.Helpers) > 0 && m.Version != upgradeManifestVersion {
		return errors.New("legacy upgrade manifest cannot hand off helpers")
	}

	if m.ListenerFd <= 2 || m.ListenerFd > math.MaxInt32 ||
		len(m.Sessions) > upgradeManifestMaxSessions || len(m.Helpers) > upgradeManifestMaxHelpers {
		return errors.New("upgrade manifest resource bounds are invalid")
	}

	if m.Version == upgradeManifestVersion && (m.Target.ResolvedPath == "" || len(m.Target.SHA256) != sha256.Size*2) {
		return errors.New("upgrade manifest target identity is incomplete")
	}

	if m.Version == upgradeManifestVersion {
		decoded, err := hex.DecodeString(m.JournalID)
		if err != nil || len(decoded) != 16 {
			return errors.New("upgrade manifest journal identity is invalid")
		}
	}

	if m.Version == upgradeManifestVersion && (len(m.StateSnapshot) == 0 ||
		len(m.ConfigSnapshot) > upgradeConfigSnapshotMax || (!m.ConfigPresent && len(m.ConfigSnapshot) != 0)) {
		return errors.New("upgrade manifest exact snapshots are incomplete")
	}

	if m.Version == upgradeManifestVersion {
		paths := []string{
			m.ConfigFile, m.Paths.ConfigFile, m.Paths.DataDir, m.Paths.StateFile,
			m.Paths.RuntimeDir, m.Paths.SocketPath,
		}
		for _, path := range paths {
			if path == "" || !filepath.IsAbs(path) || canonicalUpgradePath(path) != path {
				return errors.New("upgrade manifest effective paths are incomplete")
			}
		}

		if m.ConfigFile != m.Paths.ConfigFile {
			return errors.New("upgrade manifest config path is inconsistent")
		}
	}

	ids := make(map[string]struct{}, len(m.Sessions))
	fds := map[int]struct{}{m.ListenerFd: {}}

	pids := make(map[int]struct{}, len(m.Sessions)+len(m.Helpers))
	for _, session := range m.Sessions {
		hasPTY := upgradeSessionHasPTY(session)
		if session.ID == "" || session.PID <= 1 || session.PID > math.MaxInt32 || session.PID == os.Getpid() ||
			(hasPTY && (session.Fd <= 2 || session.Fd > math.MaxInt32 || session.ScrollbackFd > math.MaxInt32 ||
				(m.Version == upgradeManifestVersion && session.ScrollbackFd <= 2))) ||
			(!hasPTY && (session.Fd != -1 || session.ScrollbackFd != 0)) {
			return errors.New("upgrade manifest session identity is invalid")
		}

		if (m.Version == upgradeManifestVersion || !hasPTY) && session.PIDStartTime <= 0 {
			return errors.New("upgrade manifest session process identity is incomplete")
		}

		if _, exists := ids[session.ID]; exists {
			return errors.New("upgrade manifest session IDs are not unique")
		}

		if hasPTY {
			if _, exists := fds[session.Fd]; exists {
				return errors.New("upgrade manifest descriptors are not unique")
			}

			fds[session.Fd] = struct{}{}
			if session.ScrollbackFd > 2 {
				if _, exists := fds[session.ScrollbackFd]; exists {
					return errors.New("upgrade manifest descriptors are not unique")
				}

				fds[session.ScrollbackFd] = struct{}{}
			}
		}

		if _, exists := pids[session.PID]; exists {
			return errors.New("upgrade manifest process IDs are not unique")
		}

		ids[session.ID] = struct{}{}
		pids[session.PID] = struct{}{}
	}

	for _, helper := range m.Helpers {
		if helper.PID <= 1 || helper.PID > math.MaxInt32 || helper.PID == os.Getpid() || helper.StartTime <= 0 {
			return errors.New("upgrade manifest helper identity is invalid")
		}

		if _, exists := pids[helper.PID]; exists {
			return errors.New("upgrade manifest process IDs are not unique")
		}

		pids[helper.PID] = struct{}{}
	}

	return nil
}

// upgradeOwnershipGuard owns every resource transferred across syscall.Exec
// from the instant structural validation succeeds. Entries are removed only
// after ownership is transferred to a live daemon object or exact cleanup is
// verified. This makes every post-exec startup return path fail closed.
type upgradeOwnershipGuard struct {
	mu                   sync.Mutex
	listenerFD           int
	sessionFDs           map[string]int
	scrollbackFDs        map[string]int
	ambiguousDescriptors map[string]int
	sessions             map[string]UpgradeSession
	helpers              map[int]UpgradeHelper
	deadline             time.Time
}

func newUpgradeOwnershipGuard(manifest *UpgradeManifest, deadlines ...time.Time) *upgradeOwnershipGuard {
	guard := &upgradeOwnershipGuard{
		listenerFD:           manifest.ListenerFd,
		sessionFDs:           make(map[string]int, len(manifest.Sessions)),
		scrollbackFDs:        make(map[string]int, len(manifest.Sessions)),
		ambiguousDescriptors: make(map[string]int),
		sessions:             make(map[string]UpgradeSession, len(manifest.Sessions)),
		helpers:              make(map[int]UpgradeHelper, len(manifest.Helpers)),
	}
	if len(deadlines) > 0 {
		guard.deadline = deadlines[0]
	}

	for _, session := range manifest.Sessions {
		if upgradeSessionHasPTY(session) {
			guard.sessionFDs[session.ID] = session.Fd
			if session.ScrollbackFd > 2 {
				guard.scrollbackFDs[session.ID] = session.ScrollbackFd
			}
		}

		guard.sessions[upgradeCleanupKey(session)] = session
	}

	for _, helper := range manifest.Helpers {
		guard.helpers[helper.PID] = helper
	}

	return guard
}

func (g *upgradeOwnershipGuard) refine(manifest *UpgradeManifest) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if manifest == nil {
		return errors.New("upgrade ownership guard refinement differs from cleanup capsule")
	}

	terminalCount := 0
	scrollbackCount := 0

	for _, session := range manifest.Sessions {
		if upgradeSessionHasPTY(session) {
			terminalCount++

			if session.ScrollbackFd > 2 {
				scrollbackCount++
			}
		}
	}

	if g.listenerFD != manifest.ListenerFd ||
		len(g.sessionFDs) != terminalCount || len(g.scrollbackFDs) != scrollbackCount ||
		len(g.sessions) != len(manifest.Sessions) ||
		len(g.helpers) != len(manifest.Helpers) {
		return errors.New("upgrade ownership guard refinement differs from cleanup capsule")
	}

	currentByPID := make(map[int]UpgradeSession, len(g.sessions))
	for _, session := range g.sessions {
		currentByPID[session.PID] = session
	}

	nextFDs := make(map[string]int, terminalCount)

	nextSessions := make(map[string]UpgradeSession, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		current, ok := currentByPID[session.PID]
		if !ok || upgradeSessionHasPTY(current) != upgradeSessionHasPTY(session) ||
			current.Fd != session.Fd || current.ScrollbackFd != session.ScrollbackFd ||
			current.PIDStartTime != session.PIDStartTime {
			return errors.New("upgrade ownership guard session refinement differs from cleanup capsule")
		}

		if upgradeSessionHasPTY(session) {
			nextFDs[session.ID] = session.Fd
		}

		nextSessions[upgradeCleanupKey(session)] = session
	}

	g.sessionFDs = nextFDs

	nextScrollbackFDs := make(map[string]int, terminalCount)

	for _, session := range manifest.Sessions {
		if upgradeSessionHasPTY(session) && session.ScrollbackFd > 2 {
			nextScrollbackFDs[session.ID] = session.ScrollbackFd
		}
	}

	g.scrollbackFDs = nextScrollbackFDs
	g.sessions = nextSessions

	return nil
}

func (g *upgradeOwnershipGuard) consumeListener(closeErr error) error {
	g.mu.Lock()

	fd := g.listenerFD
	if err := verifyTransferredDescriptorClosed(fd, closeErr); err != nil {
		g.listenerFD = -1
		g.ambiguousDescriptors["listener"] = fd
		g.mu.Unlock()

		return err
	}

	g.listenerFD = -1
	g.mu.Unlock()

	return nil
}

func (g *upgradeOwnershipGuard) consumeSessionFD(id string, closeErr error) error {
	return g.consumeTransferredFD(g.sessionFDs, id, "session", closeErr)
}

func (g *upgradeOwnershipGuard) consumeScrollbackFD(id string, closeErr error) error {
	return g.consumeTransferredFD(g.scrollbackFDs, id, "scrollback", closeErr)
}

func (g *upgradeOwnershipGuard) consumeTransferredFD(
	descriptors map[string]int,
	id, kind string,
	closeErr error,
) error {
	g.mu.Lock()

	fd, exists := descriptors[id]
	if !exists {
		g.mu.Unlock()

		return fmt.Errorf("transferred %s descriptor ownership is missing", kind)
	}

	if err := verifyTransferredDescriptorClosed(fd, closeErr); err != nil {
		delete(descriptors, id)
		g.ambiguousDescriptors[kind+":"+id] = fd
		g.mu.Unlock()

		return err
	}

	delete(descriptors, id)
	g.mu.Unlock()

	return nil
}

// moveSessionDescriptors atomically transfers the exact PTY and scrollback
// descriptors from the bootstrap guard to AdoptSession. No dup is involved,
// so post-exec descriptor pressure cannot turn a recoverable live handoff into
// a partial close. The recipient owns both descriptors on success.
func (g *upgradeOwnershipGuard) moveSessionDescriptors(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.sessionFDs[id]; !exists {
		return errors.New("transferred session descriptor ownership is missing")
	}

	if _, exists := g.scrollbackFDs[id]; !exists {
		return errors.New("transferred scrollback descriptor ownership is missing")
	}

	delete(g.sessionFDs, id)
	delete(g.scrollbackFDs, id)

	return nil
}

func (g *upgradeOwnershipGuard) armSession(session UpgradeSession) {
	g.mu.Lock()
	g.sessions[upgradeCleanupKey(session)] = session
	g.mu.Unlock()
}

func (g *upgradeOwnershipGuard) disarmSession(session UpgradeSession) {
	g.mu.Lock()
	delete(g.sessions, upgradeCleanupKey(session))
	g.mu.Unlock()
}

func (g *upgradeOwnershipGuard) disarmHelper(pid int) {
	g.mu.Lock()
	delete(g.helpers, pid)
	g.mu.Unlock()
}

func (g *upgradeOwnershipGuard) reapHelpers() error {
	g.mu.Lock()

	helpers := make(map[int]UpgradeHelper, len(g.helpers))
	for pid, helper := range g.helpers {
		helpers[pid] = helper
	}
	g.mu.Unlock()

	pids := make([]int, 0, len(helpers))
	for pid := range helpers {
		pids = append(pids, pid)
	}

	slices.Sort(pids)

	deadline := g.deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(3 * time.Second)
	}

	type helperResult struct {
		pid int
		err error
	}

	results := make(chan helperResult, len(pids))
	for _, pid := range pids {
		helper := helpers[pid]
		go func() {
			results <- helperResult{pid: helper.PID, err: reapInheritedHelperUntil(helper, deadline)}
		}()
	}

	var result error

	for range pids {
		item := <-results
		if item.err != nil {
			result = errors.Join(result, errors.New("inherited terminal helper could not be reaped"))
			continue
		}

		g.disarmHelper(item.pid)
	}

	return result
}

func (g *upgradeOwnershipGuard) Cleanup() error {
	g.mu.Lock()
	listenerFD := g.listenerFD
	g.listenerFD = -1

	sessions := make(map[string]UpgradeSession, len(g.sessions))
	for id, session := range g.sessions {
		sessions[id] = session
	}

	sessionFDs := make(map[string]int, len(g.sessionFDs))
	for id, fd := range g.sessionFDs {
		sessionFDs[id] = fd
	}

	g.sessionFDs = make(map[string]int)

	scrollbackFDs := make(map[string]int, len(g.scrollbackFDs))
	for id, fd := range g.scrollbackFDs {
		scrollbackFDs[id] = fd
	}

	g.scrollbackFDs = make(map[string]int)

	ambiguousDescriptors := make(map[string]int, len(g.ambiguousDescriptors))
	for id, fd := range g.ambiguousDescriptors {
		ambiguousDescriptors[id] = fd
	}
	g.mu.Unlock()

	var result error

	if listenerFD >= 0 {
		if err := syscall.Close(listenerFD); err != nil && !errors.Is(err, syscall.EBADF) {
			result = errors.Join(result, errors.New("transferred listener cleanup failed"))
		}
	}

	fdIDs := make([]string, 0, len(sessionFDs))
	for id := range sessionFDs {
		fdIDs = append(fdIDs, id)
	}

	slices.Sort(fdIDs)

	for _, id := range fdIDs {
		ptyFD := sessionFDs[id]

		scrollbackFD, paired := scrollbackFDs[id]
		if !paired {
			if err := syscall.Close(ptyFD); err != nil && !errors.Is(err, syscall.EBADF) {
				result = errors.Join(result, errors.New("transferred session descriptor cleanup failed"))
			}

			continue
		}

		delete(scrollbackFDs, id)

		ptmx := os.NewFile(uintptr(ptyFD), "cleanup-upgrade-pty")

		scrollback, scrollbackErr := grpty.AdoptScrollback(uintptr(scrollbackFD), "", 0)
		if ptmx != nil && scrollbackErr == nil {
			result = errors.Join(result, grpty.DrainTransferredPTY(ptmx, scrollback))
			if err := ptmx.Close(); err != nil && !errors.Is(err, syscall.EBADF) {
				result = errors.Join(result, errors.New("transferred session descriptor cleanup failed"))
			}

			if err := scrollback.Close(); err != nil && !errors.Is(err, syscall.EBADF) {
				result = errors.Join(result, errors.New("transferred scrollback descriptor cleanup failed"))
			}

			continue
		}

		if ptmx != nil {
			_ = ptmx.Close()
		} else {
			_ = syscall.Close(ptyFD)
		}

		if scrollback != nil {
			_ = scrollback.Close()
		} else {
			_ = syscall.Close(scrollbackFD)
		}

		result = errors.Join(result, errors.New("transferred descriptor pair could not be drained"))
	}

	scrollbackIDs := make([]string, 0, len(scrollbackFDs))
	for id := range scrollbackFDs {
		scrollbackIDs = append(scrollbackIDs, id)
	}

	slices.Sort(scrollbackIDs)

	for _, id := range scrollbackIDs {
		if err := syscall.Close(scrollbackFDs[id]); err != nil && !errors.Is(err, syscall.EBADF) {
			result = errors.Join(result, errors.New("transferred scrollback descriptor cleanup failed"))
		}
	}

	for _, fd := range ambiguousDescriptors {
		if _, err := descriptorFlags(fd); !errors.Is(err, syscall.EBADF) {
			// Never retry an ambiguous close: the descriptor number may have
			// been reused. Returning an error aborts bootstrap and process exit
			// closes the secured descriptor table.
			result = errors.Join(result, errors.New("ambiguous transferred descriptor remains unresolved"))
		}
	}

	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	deadline := g.deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(3 * time.Second)
	}

	type sessionResult struct {
		session UpgradeSession
		cleaned bool
		err     error
	}

	results := make(chan sessionResult, len(ids))
	for _, id := range ids {
		session := sessions[id]
		go func() {
			cleaned, err := terminateFailedUpgradeSessionUntil(session, deadline)
			results <- sessionResult{session: session, cleaned: cleaned, err: err}
		}()
	}

	for range ids {
		item := <-results
		if item.err != nil || !item.cleaned {
			result = errors.Join(result, errors.New("transferred session process cleanup failed"))
			continue
		}

		g.disarmSession(item.session)
	}

	result = errors.Join(result, g.reapHelpers())

	return result
}

type upgradeCleanupEntry struct {
	session    UpgradeSession
	onResolved func()
}

func upgradeCleanupKey(session UpgradeSession) string {
	return fmt.Sprintf("%s:%d:%d", session.ID, session.PID, session.PIDStartTime)
}

func (sm *SessionManager) registerUpgradeCleanup(session UpgradeSession, onResolved func()) {
	key := upgradeCleanupKey(session)

	sm.upgradeCleanupMu.Lock()
	sm.upgradeCleanup[key] = upgradeCleanupEntry{
		session: session, onResolved: onResolved,
	}
	sm.upgradeCleanupMu.Unlock()
	sm.mu.Lock()
	if sm.state.UpgradeCleanup == nil {
		sm.state.UpgradeCleanup = make(map[string]UpgradeCleanupState)
	}

	sm.state.UpgradeCleanup[key] = UpgradeCleanupState{
		ID: session.ID, PID: session.PID, PIDStartTime: session.PIDStartTime,
	}
	sm.mu.Unlock()
}

func (sm *SessionManager) restoreUpgradeCleanup() error {
	sm.mu.Lock()

	entries := make([]UpgradeSession, 0, len(sm.state.UpgradeCleanup))
	for key, entry := range sm.state.UpgradeCleanup {
		session := UpgradeSession{
			ID: entry.ID, PID: entry.PID, PIDStartTime: entry.PIDStartTime,
		}
		if !validPersistedUpgradeCleanup(key, session) {
			sm.mu.Unlock()
			return errors.New("persisted upgrade cleanup ownership is malformed")
		}

		entries = append(entries, session)
	}
	sm.mu.Unlock()

	for _, entry := range entries {
		sm.upgradeCleanupMu.Lock()
		sm.upgradeCleanup[upgradeCleanupKey(entry)] = upgradeCleanupEntry{session: entry}
		sm.upgradeCleanupMu.Unlock()
	}

	return nil
}

func validPersistedUpgradeCleanup(key string, session UpgradeSession) bool {
	if session.ID == "" || len(session.ID) > 128 || strings.ContainsAny(session.ID, "/\\\x00\r\n") ||
		session.PID <= 1 || session.PID == os.Getpid() || session.PIDStartTime <= 0 {
		return false
	}

	return key == upgradeCleanupKey(session)
}

func (sm *SessionManager) rejectPendingUpgradeCleanupLocked(id string) error {
	for _, entry := range sm.state.UpgradeCleanup {
		if entry.ID == id {
			return errors.New("session process cleanup from daemon upgrade is still pending")
		}
	}

	return nil
}

func (sm *SessionManager) reconcileUpgradeCleanup() {
	sm.upgradeCleanupMu.Lock()

	entries := make(map[string]upgradeCleanupEntry, len(sm.upgradeCleanup))
	for key, entry := range sm.upgradeCleanup {
		entries[key] = entry
	}

	attempt := sm.upgradeCleanupTry
	sm.upgradeCleanupMu.Unlock()

	if attempt == nil {
		attempt = terminateFailedUpgradeSession
	}

	for key, entry := range entries {
		resolved, alreadyReaped, err := resolveUpgradeCleanupIdentity(entry.session)
		if err != nil {
			continue
		}

		if resolved != entry.session {
			// Persist the exact generation before signalling it. If the daemon
			// crashes between cleanup attempts, restart recovery can therefore
			// distinguish the inherited child from a reused PID.
			sm.mu.Lock()
			delete(sm.state.UpgradeCleanup, key)
			sm.state.UpgradeCleanup[upgradeCleanupKey(resolved)] = UpgradeCleanupState{
				ID: resolved.ID, PID: resolved.PID, PIDStartTime: resolved.PIDStartTime,
			}

			state := sm.state.Sessions[resolved.ID]
			if state != nil && state.PID == resolved.PID && state.PIDStartTime == 0 {
				state.PIDStartTime = resolved.PIDStartTime
				state.Status = StatusErrored
				state.StatusChangedAt = time.Now()
				applyLifecycleSummaryLocked(state, "Daemon upgrade cleanup identity was recovered")
			}
			sm.mu.Unlock()

			if sm.persistLatestUpgradeState() != nil {
				continue
			}

			newKey := upgradeCleanupKey(resolved)

			sm.upgradeCleanupMu.Lock()
			current, exists := sm.upgradeCleanup[key]

			promoted := exists && current.session == entry.session
			if promoted {
				delete(sm.upgradeCleanup, key)

				entry.session = resolved
				sm.upgradeCleanup[newKey] = entry
			}
			sm.upgradeCleanupMu.Unlock()

			if !promoted {
				continue
			}

			key = newKey
		}

		cleaned := alreadyReaped
		if !cleaned {
			cleaned, _ = attempt(resolved)
		}

		if !cleaned {
			continue
		}

		sm.mu.Lock()
		state := sm.state.Sessions[resolved.ID]

		identityMatches := state != nil && state.PID == resolved.PID &&
			state.PIDStartTime == resolved.PIDStartTime
		if resolved.PIDStartTime == 0 {
			identityMatches = state != nil && state.PID == resolved.PID && state.PIDStartTime == 0 && alreadyReaped
		}

		if identityMatches {
			state.Status = StatusStopped
			state.StatusChangedAt = time.Now()
			state.PID = 0
			state.PIDStartTime = 0
			applyLifecycleSummaryLocked(state, "Stopped after retrying daemon upgrade cleanup")
		}

		delete(sm.state.UpgradeCleanup, key)
		sm.mu.Unlock()

		if sm.persistLatestUpgradeState() != nil {
			continue
		}

		sm.upgradeCleanupMu.Lock()

		current, exists := sm.upgradeCleanup[key]
		if exists && current.session == resolved {
			delete(sm.upgradeCleanup, key)
		}
		sm.upgradeCleanupMu.Unlock()

		if !exists || current.session != resolved {
			continue
		}

		if entry.onResolved != nil {
			entry.onResolved()
		}
	}
}

func (sm *SessionManager) RunUpgradeCleanupLoop(ctx context.Context) {
	ticker := sm.loopTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			sm.reconcileUpgradeCleanup()
		}
	}
}

var (
	upgradeProcessStartTime = grpty.ProcessStartTime
	upgradePreSignalWait4   = syscall.Wait4
	upgradeSessionSignal    = syscall.Kill
)

func waitForExactChild(pid int, expectedStartTime int64, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)

	for {
		var status syscall.WaitStatus

		waited, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		switch {
		case waited == pid:
			return true, nil
		case err == nil:
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.ECHILD):
			startTime, startErr := upgradeProcessStartTime(pid)
			if startErr == nil && expectedStartTime > 0 && startTime > 0 && startTime != expectedStartTime {
				return true, nil
			}

			if startErr != nil && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				return true, nil
			}

			if startErr != nil {
				return false, errors.New("handoff process identity could not be verified")
			}

			return false, errors.New("handoff process is not an exact child")
		default:
			return false, errors.New("wait for handoff process failed")
		}

		if time.Now().After(deadline) {
			return false, nil
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func remainingCleanupDuration(deadline time.Time, maximum time.Duration) time.Duration {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}

	if maximum > 0 && remaining > maximum {
		return maximum
	}

	return remaining
}

func waitForProcessGroupGoneUntil(
	pgid int,
	deadline time.Time,
	processGroupGone func(int) bool,
) bool {
	for {
		if processGroupGone(pgid) {
			return true
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}

		time.Sleep(min(10*time.Millisecond, remaining))
	}
}

func reapInheritedHelperUntil(helper UpgradeHelper, deadline time.Time) error {
	startTime, startErr := upgradeProcessStartTime(helper.PID)
	if startErr != nil || startTime != helper.StartTime {
		if errors.Is(syscall.Kill(helper.PID, 0), syscall.ESRCH) {
			return nil
		}

		return errors.New("inherited terminal helper identity changed")
	}
	// Signal the group while the exact leader generation still reserves both
	// PID and PGID. No group signal occurs after Wait4 can release that identity.
	_ = syscall.Kill(-helper.PID, syscall.SIGKILL)
	_ = syscall.Kill(helper.PID, syscall.SIGKILL)

	reaped, err := waitForExactChild(helper.PID, helper.StartTime, remainingCleanupDuration(deadline, 0))
	if err != nil || !reaped {
		return errors.New("inherited terminal helper did not exit within cleanup deadline")
	}

	// Reaping releases the leader identity, so only read-only absence checks are
	// safe while a killed group member is still briefly visible to the kernel.
	if !waitForProcessGroupGoneUntil(helper.PID, deadline, exactProcessGroupGone) {
		return errors.New("inherited terminal helper did not exit within cleanup deadline")
	}

	return nil
}

func terminateFailedUpgradeSession(session UpgradeSession) (bool, error) {
	return terminateFailedUpgradeSessionUntil(session, time.Now().Add(3*time.Second))
}

func terminateFailedUpgradeSessionUntil(session UpgradeSession, deadline time.Time) (bool, error) {
	resolved, alreadyReaped, err := resolveUpgradeCleanupIdentity(session)
	if err != nil || alreadyReaped {
		return alreadyReaped, err
	}

	session = resolved

	startTime, err := grpty.ProcessStartTime(session.PID)
	if err != nil {
		reaped, waitErr := waitForExactChild(session.PID, session.PIDStartTime, 0)
		if waitErr != nil || !reaped {
			return false, errors.New("failed adoption process identity could not be verified")
		}

		// The leader identity has already been released. A group signal here
		// could target a reused PGID; only read-only absence is safe.
		return exactProcessGroupGone(session.PID), nil
	}

	if startTime != session.PIDStartTime {
		// The recorded process is already gone and this PID belongs to a new
		// generation. Never signal the replacement.
		return true, nil
	}

	var status syscall.WaitStatus

	waited, waitErr := upgradePreSignalWait4(session.PID, &status, syscall.WNOHANG, nil)
	switch {
	case waited == session.PID:
		// Reaping released the leader identity. Never signal its former group;
		// only an already-absent group is safely resolved.
		return exactProcessGroupGone(session.PID), nil
	case waitErr == nil:
		// Exact WNOHANG ownership proves this live generation remains our child.
	case errors.Is(waitErr, syscall.EINTR):
		return terminateFailedUpgradeSessionUntil(session, deadline)
	case errors.Is(waitErr, syscall.ECHILD):
		// Cold-restart cleanup has no atomic reservation over this non-child PID
		// and PGID. A start-time check followed by kill(-pid) can race leader exit
		// and unrelated group reuse, so portable cleanup stays unresolved/manual.
		return false, errors.New("failed adoption process is not an exact child; group cleanup unresolved")
	default:
		return false, errors.New("failed adoption child ownership could not be verified")
	}

	_ = upgradeSessionSignal(-session.PID, syscall.SIGTERM)
	_ = upgradeSessionSignal(session.PID, syscall.SIGTERM)

	graceDeadline := time.Now().Add(500 * time.Millisecond)
	if graceDeadline.After(deadline) {
		graceDeadline = deadline
	}

	for time.Now().Before(graceDeadline) {
		current, currentErr := grpty.ProcessStartTime(session.PID)
		if currentErr != nil || current != session.PIDStartTime {
			// The leader is gone or changed before escalation. Never signal the
			// old PGID after its generation is no longer reserved.
			return exactProcessGroupGone(session.PID), nil
		}

		if exactProcessGroupGone(session.PID) {
			reaped, waitErr := waitForExactChild(session.PID, session.PIDStartTime, 0)
			return reaped, waitErr
		}

		time.Sleep(min(10*time.Millisecond, time.Until(graceDeadline)))
	}

	startTime, err = grpty.ProcessStartTime(session.PID)
	if err != nil || startTime != session.PIDStartTime {
		return exactProcessGroupGone(session.PID), nil
	}

	_ = upgradeSessionSignal(-session.PID, syscall.SIGKILL)
	_ = upgradeSessionSignal(session.PID, syscall.SIGKILL)

	for {
		reaped, waitErr := waitForExactChild(session.PID, session.PIDStartTime, 0)
		if waitErr == nil && reaped && exactProcessGroupGone(session.PID) {
			return true, nil
		}

		if time.Now().After(deadline) {
			return false, errors.New("failed adoption process group exceeded cleanup deadline")
		}

		time.Sleep(min(10*time.Millisecond, time.Until(deadline)))
	}
}

func resolveUpgradeCleanupIdentity(session UpgradeSession) (UpgradeSession, bool, error) {
	if session.PID <= 1 || session.PID == os.Getpid() || session.PIDStartTime < 0 {
		return session, false, errors.New("upgrade cleanup process identity is invalid")
	}

	if session.PIDStartTime == 0 {
		var status syscall.WaitStatus

		waited, waitErr := syscall.Wait4(session.PID, &status, syscall.WNOHANG, nil)
		switch {
		case waited == session.PID:
			if exactProcessGroupGone(session.PID) {
				return session, true, nil
			}
			// Wait4 released the legacy PID/PGID generation. Signalling it now
			// could kill an unrelated reused group, so retain unresolved state.
			return session, false, errors.New("reaped legacy upgrade leader left a live process group")
		case waitErr == nil:
			// A zero result for wait4(exact PID, WNOHANG) proves this running
			// PID is still our inherited child. Capture its current generation
			// before signalling; ECHILD below is deliberately not equivalent.
			startTime, err := grpty.ProcessStartTime(session.PID)
			if err != nil || startTime <= 0 {
				return session, false, errors.New("legacy upgrade child identity could not be hydrated")
			}

			session.PIDStartTime = startTime
		case errors.Is(waitErr, syscall.EINTR):
			return resolveUpgradeCleanupIdentity(session)
		case errors.Is(waitErr, syscall.ECHILD):
			return session, false, errors.New("legacy upgrade process is not an exact child")
		default:
			return session, false, errors.New("legacy upgrade child ownership could not be verified")
		}
	}

	return session, false, nil
}

func exactProcessGroupGone(pgid int) bool {
	err := syscall.Kill(-pgid, 0)

	return errors.Is(err, syscall.ESRCH)
}

func validateAdoptionCapacity(manifest *UpgradeManifest) error {
	maxSessions, available := grpty.TerminalAdoptionCapacity()
	if !available {
		return errors.New("terminal backend is unavailable for upgrade adoption")
	}

	terminalCount := 0

	for _, session := range manifest.Sessions {
		if upgradeSessionHasPTY(session) {
			terminalCount++
		}
	}

	if maxSessions > 0 && terminalCount > maxSessions {
		return errors.New("upgrade manifest exceeds terminal adoption capacity")
	}

	return nil
}

func validateUpgradeHelperHandoff(target *upgradeTarget, helpers []grpty.HelperProcessIdentity) error {
	if len(helpers) > 0 && target.helperHandoffVersion != upgradeHelperHandoffVersion {
		return refuseUpgrade("upgrade target does not support the terminal helper handoff")
	}

	return nil
}

func ExecUpgrade(manifestPath, configFile, clientExecPath string) error {
	return errors.New("direct upgrade exec is unsupported; use the negotiated daemon upgrade protocol")
}

func (sm *SessionManager) execPreparedUpgrade(
	target *upgradeTarget,
	manifest *UpgradeManifest,
	manifestPath string,
	configFile string,
	preparedService ...preparedExecUpgrade,
) error {
	var prepared preparedExecUpgrade
	if len(preparedService) > 0 {
		prepared = preparedService[0]
	}

	fail := func(err error) error {
		return prepared.rollbackError(err)
	}

	// Slow process and executable validation happens before the commit barrier.
	// The barrier below then prevents an accepted state mutation/save from being
	// stranded when syscall.Exec replaces the process image.
	if err := sm.validateUpgradePlan(manifest); err != nil {
		return fail(err)
	}

	if err := prepared.validateTarget(target); err != nil {
		return fail(err)
	}

	if err := target.validateFileIdentity(); err != nil {
		return fail(err)
	}

	if err := validateUpgradeExecBudget(target, manifestPath, configFile, manifest.ownershipFD, manifest.ownershipCapsule, prepared); err != nil {
		return fail(err)
	}

	if err := grpty.ReleasePinnedTerminalExecutablePathForExec(); err != nil {
		return fail(refuseUpgrade("terminal helper executable pin could not be released for exec"))
	}

	err := sm.withFinalUpgradeBarrier(manifest, func() error {
		// os/exec takes ForkLock for descriptor-sensitive forks. Holding it
		// across the only inheritable window prevents unrelated background
		// children from receiving listener or PTY handoff copies.
		syscall.ForkLock.Lock()
		defer syscall.ForkLock.Unlock()

		if err := target.validateFinalFileIdentity(); err != nil {
			return err
		}

		if err := makeUpgradeDescriptorsInheritable(manifest); err != nil {
			return rollbackUpgradeDescriptorsBeforeForkUnlock(manifest, err)
		}

		if err := execUpgradeTarget(target, manifestPath, configFile, manifest.ownershipFD, manifest.ownershipCapsule, prepared); err != nil {
			return rollbackUpgradeDescriptorsBeforeForkUnlock(manifest, err)
		}

		return nil
	})
	if err != nil {
		if restoreErr := grpty.RestorePinnedTerminalExecutableAfterExec(); restoreErr != nil {
			return fail(errors.Join(err, fmt.Errorf("restore terminal helper executable pin: %w", restoreErr)))
		}

		return fail(err)
	}

	return nil
}

func (sm *SessionManager) withFinalUpgradeBarrier(manifest *UpgradeManifest, commit func() error) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.statePersistMu.Lock()
	defer sm.statePersistMu.Unlock()

	if !sm.upgradePending {
		return refuseUpgrade("upgrade reservation was lost at the exec boundary")
	}

	current, err := sm.snapshotUpgradeStateLocked()
	if err != nil {
		return fmt.Errorf("snapshot state at upgrade exec boundary: %w", err)
	}

	if len(manifest.StateSnapshot) == 0 || !bytes.Equal(current, manifest.StateSnapshot) {
		return refuseUpgrade("session state changed at the upgrade exec boundary")
	}

	for _, planned := range manifest.Sessions {
		state := sm.state.Sessions[planned.ID]
		if sm.sessions[planned.ID] != plannedUpgradeDriver(manifest, planned.ID) || state == nil ||
			state.PID != planned.PID || state.PIDStartTime != planned.PIDStartTime {
			return refuseUpgrade("upgrade plan changed at the exec boundary")
		}
	}

	return commit()
}

func execUpgradeTarget(
	target *upgradeTarget,
	manifestPath, configFile string,
	ownershipFD int,
	ownershipCapsule string,
	preparedService ...preparedExecUpgrade,
) error {
	var prepared preparedExecUpgrade
	if len(preparedService) > 0 {
		prepared = preparedService[0]
	}

	args := upgradeExecArgs(target.path, manifestPath, configFile, prepared)

	env := os.Environ()
	ownershipPrefix := upgradeOwnershipFDEnv + "="
	capsulePrefix := upgradeOwnershipCapsuleEnv + "="

	filtered := env[:0]
	for _, item := range env {
		if !strings.HasPrefix(item, ownershipPrefix) && !strings.HasPrefix(item, capsulePrefix) {
			filtered = append(filtered, item)
		}
	}

	env = filtered
	if ownershipFD > 2 {
		env = append(env, ownershipPrefix+strconv.Itoa(ownershipFD))
	}

	if ownershipCapsule != "" {
		env = append(env, capsulePrefix+ownershipCapsule)
	}

	return execProcessForUpgrade(target.pin.execPath, args, env)
}

func validateUpgradeExecBudget(
	target *upgradeTarget,
	manifestPath, configFile string,
	ownershipFD int,
	ownershipCapsule string,
	preparedService ...preparedExecUpgrade,
) error {
	var prepared preparedExecUpgrade
	if len(preparedService) > 0 {
		prepared = preparedService[0]
	}

	args := upgradeExecArgs(target.path, manifestPath, configFile, prepared)

	ownershipPrefix := upgradeOwnershipFDEnv + "="
	capsulePrefix := upgradeOwnershipCapsuleEnv + "="
	count := len(args)

	bytesNeeded := 0
	for _, arg := range args {
		bytesNeeded += len(arg) + 1
	}

	for _, item := range os.Environ() {
		if strings.HasPrefix(item, ownershipPrefix) || strings.HasPrefix(item, capsulePrefix) {
			continue
		}

		bytesNeeded += len(item) + 1
		count++
	}

	if ownershipFD > 2 {
		bytesNeeded += len(ownershipPrefix) + 20 + 1
		count++
	}

	if ownershipCapsule != "" {
		bytesNeeded += len(capsulePrefix) + len(ownershipCapsule) + 1
		count++
	}
	// Account for argv/env pointer tables as well as strings. The conservative
	// 128 KiB plan is below Darwin's 256 KiB ARG_MAX and far below Linux's
	// normal limit, so a successful preflight cannot fail at exec due to size.
	bytesNeeded += (count + 2) * 8
	if bytesNeeded > upgradeExecEnvironmentMax-upgradeExecEnvironmentSlack {
		return refuseUpgrade("upgrade ownership capsule does not fit the safe exec environment budget")
	}

	return nil
}

func currentOpenDescriptorCount(limit uint64) (int, error) {
	return currentOpenDescriptorCountWith(limit, openDescriptorDirectoryCount, descriptorFlags)
}

func openDescriptorDirectoryCount(path string) (int, error) {
	directory, err := os.Open(path)
	if err != nil {
		return 0, err
	}

	names, readErr := directory.Readdirnames(-1)
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return 0, err
	}

	return len(names), nil
}

func currentOpenDescriptorCountWith(
	limit uint64,
	directoryCount func(string) (int, error),
	flags func(int) (int, error),
) (int, error) {
	for _, path := range []string{"/dev/fd", "/proc/self/fd"} {
		count, err := directoryCount(path)
		if err == nil {
			if count < 0 {
				return 0, errors.New("cannot inspect open descriptor budget")
			}

			return count, nil
		}
	}

	maxInt := uint64(^uint(0) >> 1)
	if limit == 0 || limit == unix.RLIM_INFINITY || limit > maxInt {
		return 0, errors.New("cannot inspect open descriptor budget")
	}

	return countOpenDescriptorsByFlags(int(limit), flags)
}

func countOpenDescriptorsByFlags(limit int, flags func(int) (int, error)) (int, error) {
	if limit <= 0 || flags == nil {
		return 0, errors.New("cannot inspect open descriptor budget")
	}

	count := 0

	for fd := 0; fd < limit; fd++ {
		if _, err := flags(fd); err == nil {
			count++
		} else if !errors.Is(err, syscall.EBADF) {
			return 0, errors.New("cannot inspect open descriptor budget")
		}
	}

	return count, nil
}

func validateUpgradeDescriptorBudget(sessionCount int) error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return refuseUpgrade("upgrade descriptor limit could not be inspected")
	}

	openCount, err := currentOpenDescriptorCount(limit.Cur)
	if err != nil {
		return refuseUpgrade("upgrade open descriptor count could not be inspected")
	}

	return validateUpgradeDescriptorBudgetValues(limit.Cur, openCount, sessionCount)
}

func validateUpgradeDescriptorBudgetValues(limit uint64, openCount, sessionCount int) error {
	if openCount < 0 || sessionCount < 0 {
		return refuseUpgrade("upgrade descriptor budget is invalid")
	}

	required := uint64(openCount) + uint64(sessionCount)*2 + upgradeDescriptorHeadroom
	if required < uint64(openCount) || required > limit {
		return refuseUpgrade("upgrade requires more descriptor headroom than is available")
	}

	return nil
}

func resolveUpgradeExecutable(clientExecPath string) (string, error) {
	path := clientExecPath
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
	}

	if path == "" {
		var err error

		path, err = resolveExecutable()
		if err != nil {
			return "", err
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	return filepath.EvalSymlinks(absPath)
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

	if !processidentity.IsGraithDaemon(pid) {
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

	if !processidentity.IsGraithDaemon(pid) {
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
