package daemonservice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"golang.org/x/sys/unix"
)

const (
	startupRequestSchema = 1
	startupRequestLimit  = 1 << 20
)

var ErrStartupRequestExists = errors.New("daemon service startup request already exists")

var BuiltInEnvironmentNames = []string{
	"PATH", "SHELL", "TMPDIR", "LANG", "LC_*",
	"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
}

type StartupRequest struct {
	Schema      int               `json:"schema"`
	Profile     string            `json:"profile,omitempty"`
	Slot        string            `json:"slot"`
	Label       string            `json:"label"`
	ConfigFile  string            `json:"config_file,omitempty"`
	Paths       config.Paths      `json:"paths"`
	Generation  string            `json:"generation"`
	Environment map[string]string `json:"environment"`
	Nonce       string            `json:"nonce"`
	UID         int               `json:"uid"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Checksum    string            `json:"checksum"`
}

type ExpectedStartup struct {
	Profile    string
	Generation string
	Nonce      string
}

func NewStartupRequest(definition Definition, profile, configFile string, paths config.Paths, generation string, environment map[string]string, uid int, now time.Time, lifetime time.Duration) (StartupRequest, error) {
	if err := ProfileForDefinition(definition, profile); err != nil {
		return StartupRequest{}, err
	}

	if lifetime <= 0 {
		return StartupRequest{}, errors.New("daemon service startup request lifetime must be positive")
	}

	if configFile != "" && (!filepath.IsAbs(configFile) || filepath.Clean(configFile) != configFile) {
		return StartupRequest{}, errors.New("daemon service config path must be clean and absolute")
	}

	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return StartupRequest{}, fmt.Errorf("create daemon service startup nonce: %w", err)
	}

	request := StartupRequest{
		Schema:      startupRequestSchema,
		Profile:     profile,
		Slot:        definition.Slot,
		Label:       definition.Label,
		ConfigFile:  configFile,
		Paths:       paths,
		Generation:  generation,
		Environment: environment,
		Nonce:       hex.EncodeToString(nonceBytes),
		UID:         uid,
		CreatedAt:   now.UTC(),
		ExpiresAt:   now.Add(lifetime).UTC(),
	}
	if err := validateStartupRequest(request, definition, ExpectedStartup{Profile: profile, Generation: generation, Nonce: request.Nonce}, uid, now); err != nil {
		return StartupRequest{}, err
	}

	return request.withChecksum()
}

func (request StartupRequest) withChecksum() (StartupRequest, error) {
	request.Checksum = ""

	data, err := json.Marshal(request)
	if err != nil {
		return StartupRequest{}, err
	}

	sum := sha256.Sum256(data)
	request.Checksum = hex.EncodeToString(sum[:])

	return request, nil
}

func requestFilename(definition Definition) string   { return "start-" + definition.Slot + ".json" }
func startLockFilename(definition Definition) string { return "start-" + definition.Slot + ".lock" }

func openControlRoot(root string, uid int) (int, error) {
	parent := filepath.Dir(root)

	grandparent := filepath.Dir(parent)

	if filepath.Base(grandparent) != "services" || filepath.Base(parent) != "control" || filepath.Base(root) != "bootstrap" {
		return -1, errors.New("daemon service control root has unexpected fixed suffix")
	}

	if err := secureOwnedPath(grandparent, uid, true); err != nil {
		return -1, fmt.Errorf("validate daemon service root: %w", err)
	}

	grandFD, err := unix.Open(grandparent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}

	defer func() { _ = unix.Close(grandFD) }()

	if err := unix.Mkdirat(grandFD, "control", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err
	}

	parentFD, err := unix.Openat(grandFD, "control", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}

	defer func() { _ = unix.Close(parentFD) }()

	if err := validateOwnedDirectoryFD(parentFD, uid); err != nil {
		return -1, err
	}

	if err := unix.Mkdirat(parentFD, "bootstrap", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err
	}

	rootFD, err := unix.Openat(parentFD, "bootstrap", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}

	if err := validateOwnedDirectoryFD(rootFD, uid); err != nil {
		_ = unix.Close(rootFD)
		return -1, err
	}

	return rootFD, nil
}

func validateOwnedDirectoryFD(fd, uid int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}

	if int(stat.Uid) != uid || stat.Mode&0o777 != 0o700 || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("daemon service control directory ownership, mode, or type is invalid")
	}

	return nil
}

func WithStartLock(root string, uid int, definition Definition, fn func() error) error {
	dir, err := openControlRoot(root, uid)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()

	name := startLockFilename(definition)

	fd, err := unix.Openat(dir, name, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}

	defer func() { _ = unix.Close(fd) }()

	var stat unix.Stat_t

	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}

	if int(stat.Uid) != uid || stat.Mode&0o777 != 0o600 || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("daemon service start lock ownership, mode, or type is invalid")
	}

	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()

	return fn()
}

func WriteStartupRequest(root string, request StartupRequest) error {
	definition, err := ValidateMarker(request.Label, request.Slot)
	if err != nil {
		return err
	}

	if !validStartupNonce(request.Nonce) {
		return errors.New("daemon service startup request has an unsafe nonce")
	}

	dir, err := openControlRoot(root, request.UID)
	if err != nil {
		return err
	}

	defer func() { _ = unix.Close(dir) }()

	name := requestFilename(definition)

	checked, err := request.withChecksum()
	if err != nil {
		return err
	}

	data, err := json.Marshal(checked)
	if err != nil {
		return err
	}

	if len(data) > startupRequestLimit {
		return errors.New("daemon service startup request exceeds size limit")
	}

	tempName := "." + name + "." + request.Nonce + ".tmp"

	fd, err := unix.Openat(dir, tempName, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}

	file := os.NewFile(uintptr(fd), tempName)
	ok := false
	published := false

	defer func() {
		_ = file.Close()
		_ = unix.Unlinkat(dir, tempName, 0)

		if published && !ok {
			_ = unix.Unlinkat(dir, name, 0)
		}
	}()

	if _, err := file.Write(data); err != nil {
		return err
	}

	if err := file.Sync(); err != nil {
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	if err := unix.Linkat(dir, tempName, dir, name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrStartupRequestExists
		}

		return err
	}

	published = true

	if err := unix.Unlinkat(dir, tempName, 0); err != nil {
		return err
	}

	if err := unix.Fsync(dir); err != nil {
		return err
	}

	ok = true

	return nil
}

func RemoveStartupRequest(root string, uid int, definition Definition) error {
	dir, err := openControlRoot(root, uid)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()

	if err := unix.Unlinkat(dir, requestFilename(definition), 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}

	return nil
}

func ConsumeStartupRequest(root string, uid int, definition Definition, expected ExpectedStartup, now time.Time) (StartupRequest, error) {
	dir, err := openControlRoot(root, uid)
	if err != nil {
		return StartupRequest{}, err
	}
	defer func() { _ = unix.Close(dir) }()

	name := requestFilename(definition)

	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Unlinkat(dir, name, 0)
		return StartupRequest{}, err
	}

	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()

	if err := unix.Unlinkat(dir, name, 0); err != nil {
		return StartupRequest{}, fmt.Errorf("consume daemon service startup request: %w", err)
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return StartupRequest{}, err
	}

	if int(stat.Uid) != uid || stat.Mode&0o777 != 0o600 || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return StartupRequest{}, errors.New("daemon service startup request ownership, mode, or type is invalid")
	}

	data, err := io.ReadAll(io.LimitReader(file, startupRequestLimit+1))
	if err != nil {
		return StartupRequest{}, err
	}

	if len(data) > startupRequestLimit {
		return StartupRequest{}, errors.New("daemon service startup request exceeds size limit")
	}

	var request StartupRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return StartupRequest{}, err
	}

	wantChecksum := request.Checksum

	checked, err := request.withChecksum()
	if err != nil || wantChecksum == "" || checked.Checksum != wantChecksum {
		return StartupRequest{}, errors.New("daemon service startup request checksum mismatch")
	}

	if err := validateStartupRequest(request, definition, expected, uid, now); err != nil {
		return StartupRequest{}, err
	}

	return request, nil
}

func validateStartupRequest(request StartupRequest, definition Definition, expected ExpectedStartup, uid int, now time.Time) error {
	if request.Schema != startupRequestSchema || request.UID != uid {
		return errors.New("daemon service startup request schema or UID mismatch")
	}

	if _, err := ValidateMarker(request.Label, request.Slot); err != nil || request.Label != definition.Label || request.Slot != definition.Slot {
		return errors.New("daemon service startup request marker mismatch")
	}

	if err := ProfileForDefinition(definition, request.Profile); err != nil {
		return err
	}

	if request.Profile != expected.Profile || request.Generation != expected.Generation || request.Nonce != expected.Nonce || !validStartupNonce(request.Nonce) {
		return errors.New("daemon service startup request lease, generation, or nonce mismatch")
	}

	if request.CreatedAt.IsZero() || request.ExpiresAt.IsZero() || request.ExpiresAt.Before(request.CreatedAt) || now.Before(request.CreatedAt.Add(-time.Second)) || !now.Before(request.ExpiresAt) {
		return errors.New("daemon service startup request is expired or not yet valid")
	}

	for name, value := range request.Environment {
		if !validProjectedName(name) || isReservedProjectedName(name) || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("daemon service startup request contains unsafe environment variable %q", name)
		}
	}

	return nil
}

func validStartupNonce(nonce string) bool {
	if len(nonce) != 48 {
		return false
	}

	_, err := hex.DecodeString(nonce)

	return err == nil
}

func ProjectEnvironment(environ []string, inherit []string, uid int) (map[string]string, error) {
	return projectEnvironment(environ, inherit, uid, CacheRoot)
}

func projectEnvironment(environ []string, inherit []string, uid int, cacheRoot func(int) (string, error)) (map[string]string, error) {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}

	projected := make(map[string]string)

	for _, name := range []string{"PATH", "SHELL", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "LC_COLLATE", "LC_MESSAGES", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		if value, ok := values[name]; ok && value != "" {
			if err := validateBuiltInValue(name, value); err != nil {
				return nil, err
			}

			projected[name] = value
		}
	}

	for name, value := range values {
		if strings.HasPrefix(name, "LC_") && value != "" {
			if err := validateBuiltInValue(name, value); err != nil {
				return nil, err
			}

			projected[name] = value
		}
	}

	seen := make(map[string]bool)
	for _, name := range inherit {
		if seen[name] || !validProjectedName(name) || isReservedProjectedName(name) {
			return nil, fmt.Errorf("unsafe daemon service inherited environment variable %q", name)
		}

		seen[name] = true
		if value, ok := values[name]; ok {
			if strings.IndexByte(value, 0) >= 0 {
				return nil, fmt.Errorf("daemon service environment variable %q contains NUL", name)
			}

			projected[name] = value
		}
	}

	if _, err := lookupOSUser(uid); err != nil {
		return nil, err
	}

	servicesRoot, err := cacheRoot(uid)
	if err != nil {
		return nil, err
	}

	if tempDir := projected["TMPDIR"]; tempDir != "" {
		overlaps, err := CanonicalPathsOverlap(tempDir, servicesRoot)
		if err != nil {
			return nil, fmt.Errorf("validate daemon service TMPDIR: %w", err)
		}

		if overlaps {
			return nil, errors.New("daemon service TMPDIR must not expose the protected Graith services tree")
		}
	}

	return projected, nil
}

func validateBuiltInValue(name, value string) error {
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("daemon service environment variable %q contains NUL", name)
	}

	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("daemon service environment variable %q contains a line break", name)
	}

	switch {
	case name == "PATH":
		for _, component := range filepath.SplitList(value) {
			if component == "" || !filepath.IsAbs(component) {
				return errors.New("daemon service PATH must contain only absolute non-empty entries")
			}
		}
	case name == "SHELL" || name == "TMPDIR" || strings.HasPrefix(name, "XDG_"):
		if !filepath.IsAbs(value) {
			return fmt.Errorf("daemon service environment variable %s must be absolute", name)
		}
	}

	return nil
}

func validProjectedName(name string) bool {
	if name == "" || !validProjectedNameStart(name[0]) {
		return false
	}

	for index := 1; index < len(name); index++ {
		ch := name[index]
		if !validProjectedNameStart(ch) && (ch < '0' || ch > '9') {
			return false
		}
	}

	return true
}

func validProjectedNameStart(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch == '_'
}

func isReservedProjectedName(name string) bool {
	return config.DaemonServiceEnvironmentReserved(name)
}

func lookupOSUser(uid int) (*user.User, error) {
	account, err := lookupOSUserByID(strconv.Itoa(uid))
	if err != nil {
		return nil, fmt.Errorf("look up effective user %d: %w", uid, err)
	}

	parsed, err := strconv.Atoi(account.Uid)
	if err != nil || parsed != uid || account.HomeDir == "" || account.Username == "" || !filepath.IsAbs(account.HomeDir) {
		return nil, errors.New("OS user database returned inconsistent daemon service identity")
	}

	return account, nil
}

func InstallRequestEnvironment(request StartupRequest) error {
	account, err := lookupOSUser(request.UID)
	if err != nil {
		return err
	}

	for name := range request.Environment {
		if !validProjectedName(name) || isReservedProjectedName(name) {
			return fmt.Errorf("reserved environment variable %q survived request validation", name)
		}
	}

	os.Clearenv()

	for name, value := range map[string]string{"HOME": account.HomeDir, "USER": account.Username, "LOGNAME": account.Username} {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}

	for name, value := range request.Environment {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}

	if request.Profile != "" {
		if err := os.Setenv("GRAITH_PROFILE", request.Profile); err != nil {
			return err
		}
	}

	return nil
}
