package daemonservice

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestInstallRequestEnvironmentUsesOSIdentityAndProjection(t *testing.T) {
	originalLookup := lookupOSUserByID
	originalEnvironment := os.Environ()

	t.Cleanup(func() {
		lookupOSUserByID = originalLookup

		os.Clearenv()

		for _, entry := range originalEnvironment {
			name, value, found := strings.Cut(entry, "=")
			if found {
				_ = os.Setenv(name, value)
			}
		}
	})

	home := t.TempDir()
	lookupOSUserByID = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Geteuid()), Username: "canny", HomeDir: home}, nil
	}

	request := StartupRequest{
		UID: os.Geteuid(), Profile: "bothy",
		Environment: map[string]string{"PATH": "/usr/bin:/bin", "CANNY_TOKEN": "braw"},
	}
	if err := InstallRequestEnvironment(request); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"HOME": home, "USER": "canny", "LOGNAME": "canny", "GRAITH_PROFILE": "bothy",
		"PATH": "/usr/bin:/bin", "CANNY_TOKEN": "braw",
	} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func testControlRoot(t *testing.T) string {
	t.Helper()

	services := filepath.Join(t.TempDir(), "services")
	if err := os.Mkdir(services, 0o700); err != nil { // #nosec G301 -- owner-only test service root.
		t.Fatal(err)
	}

	return filepath.Join(services, "control", "bootstrap")
}

func TestStartupRequestIsOneUseAndBoundToIntent(t *testing.T) {
	t.Parallel()

	definition, err := DefinitionForSlot("07")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	paths := config.Paths{Profile: "canny", SocketPath: "/tmp/canny.sock", PIDFile: "/tmp/canny.pid"}

	request, err := NewStartupRequest(definition, "canny", "/bothy/config.toml", paths, "1-canny", map[string]string{"PATH": "/usr/bin:/bin"}, os.Getuid(), now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	root := testControlRoot(t)
	if err := WithStartLock(root, os.Getuid(), definition, func() error {
		return WriteStartupRequest(root, request)
	}); err != nil {
		t.Fatal(err)
	}

	if err := WriteStartupRequest(root, request); !errors.Is(err, ErrStartupRequestExists) {
		t.Fatalf("second WriteStartupRequest() = %v, want ErrStartupRequestExists", err)
	}

	expected := ExpectedStartup{Profile: "canny", Generation: "1-canny", Nonce: request.Nonce}

	consumed, err := ConsumeStartupRequest(root, os.Getuid(), definition, expected, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ConsumeStartupRequest() = %v", err)
	}

	if consumed.Profile != "canny" || consumed.Paths != paths || consumed.ConfigFile != "/bothy/config.toml" {
		t.Fatalf("consumed request = %#v", consumed)
	}

	if _, err := ConsumeStartupRequest(root, os.Getuid(), definition, expected, now.Add(time.Second)); err == nil {
		t.Fatal("startup request consumed twice")
	}
}

func TestStartupRequestRejectsRelativeConfigPath(t *testing.T) {
	definition := Definitions()[0]
	if _, err := NewStartupRequest(definition, "", filepath.Join("bothy", "config.toml"), config.Paths{}, "1-canny", map[string]string{}, os.Getuid(), time.Now(), time.Minute); err == nil {
		t.Fatal("relative config path was accepted for a launchd request")
	}
}

func TestWriteStartupRequestRejectsPathLikeNonce(t *testing.T) {
	definition := Definitions()[0]

	request := StartupRequest{Label: definition.Label, Slot: definition.Slot, UID: os.Getuid(), Nonce: "../../thrawn"}
	if err := WriteStartupRequest(testControlRoot(t), request); err == nil {
		t.Fatal("path-like startup nonce was accepted as an atomic staging name")
	}
}

func TestStartupRequestFailureRemovesRequest(t *testing.T) {
	t.Parallel()

	definition := Definitions()[0]
	now := time.Now().UTC()

	request, err := NewStartupRequest(definition, "", "", config.Paths{}, "braw", map[string]string{}, os.Getuid(), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	root := testControlRoot(t)
	if err := WriteStartupRequest(root, request); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, requestFilename(definition))
	if err := os.Chmod(path, 0o644); err != nil { // #nosec G302 -- intentionally unsafe negative fixture.
		t.Fatal(err)
	}

	_, err = ConsumeStartupRequest(root, os.Getuid(), definition, ExpectedStartup{Generation: "braw", Nonce: request.Nonce}, now)
	if err == nil || !strings.Contains(err.Error(), "ownership, mode, or type") {
		t.Fatalf("ConsumeStartupRequest() = %v, want mode error", err)
	}

	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed request remains at %s: %v", path, err)
	}
}

func TestStartupRequestRejectsExpiryAndMismatch(t *testing.T) {
	t.Parallel()

	definition, _ := DefinitionForSlot("03")
	now := time.Now().UTC()

	request, err := NewStartupRequest(definition, "dreich", "", config.Paths{}, "braw", map[string]string{}, os.Getuid(), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	root := testControlRoot(t)
	if err := WriteStartupRequest(root, request); err != nil {
		t.Fatal(err)
	}

	if _, err := ConsumeStartupRequest(root, os.Getuid(), definition, ExpectedStartup{Profile: "dreich", Generation: "braw", Nonce: request.Nonce}, now.Add(2*time.Second)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired ConsumeStartupRequest() = %v", err)
	}

	request, err = NewStartupRequest(definition, "dreich", "", config.Paths{}, "braw", map[string]string{}, os.Getuid(), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteStartupRequest(root, request); err != nil {
		t.Fatal(err)
	}

	if _, err := ConsumeStartupRequest(root, os.Getuid(), definition, ExpectedStartup{Profile: "strath", Generation: "braw", Nonce: request.Nonce}, now); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("mismatched ConsumeStartupRequest() = %v", err)
	}
}

func TestProjectEnvironment(t *testing.T) {
	t.Parallel()
	tempDir := filepath.Join(t.TempDir(), "canny")

	environ := []string{
		"PATH=/opt/homebrew/bin:/usr/bin:/bin",
		"SHELL=/bin/zsh",
		"TMPDIR=" + tempDir,
		"LANG=en_GB.UTF-8",
		"LC_TIME=en_GB.UTF-8",
		"XDG_CONFIG_HOME=/bothy/config",
		"SSH_AUTH_SOCK=/private/tmp/agent.sock",
		"ANTHROPIC_API_KEY=secret",
		"GRAITH_SESSION_ID=must-not-project",
	}

	got, err := ProjectEnvironment(environ, []string{"SSH_AUTH_SOCK", "ANTHROPIC_API_KEY"}, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"PATH", "SHELL", "TMPDIR", "LANG", "LC_TIME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "ANTHROPIC_API_KEY"} {
		if got[name] == "" {
			t.Errorf("projection missing %s", name)
		}
	}

	if _, ok := got["GRAITH_SESSION_ID"]; ok {
		t.Fatal("projection included reserved Graith session variable")
	}

	if _, err := ProjectEnvironment([]string{"PATH=bin:/usr/bin"}, nil, os.Getuid()); err == nil {
		t.Fatal("relative PATH entry accepted")
	}

	if _, err := ProjectEnvironment(environ, []string{"DYLD_INSERT_LIBRARIES"}, os.Getuid()); err == nil {
		t.Fatal("reserved opt-in accepted")
	}

	if _, err := ProjectEnvironment([]string{"LC_TIME=en_GB.UTF-8\nGRAITH_TOKEN=dreich"}, nil, os.Getuid()); err == nil {
		t.Fatal("multi-line locale projection accepted")
	}
}

func TestProjectEnvironmentRejectsTMPDIRExposingProtectedServices(t *testing.T) {
	root := t.TempDir()
	services := filepath.Join(root, "services")

	control := filepath.Join(services, "control")
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(root, "croft")
	if err := os.Symlink(services, alias); err != nil {
		t.Fatal(err)
	}

	cacheRoot := func(int) (string, error) { return services, nil }
	for _, tempDir := range []string{services, control, filepath.Dir(services), string(filepath.Separator), alias, filepath.Join(alias, "control")} {
		_, err := projectEnvironment([]string{"PATH=/usr/bin:/bin", "TMPDIR=" + tempDir}, nil, os.Getuid(), cacheRoot)
		if err == nil || !strings.Contains(err.Error(), "must not expose") {
			t.Errorf("TMPDIR %q error = %v", tempDir, err)
		}
	}
}

func TestStartIntentReceiptBinding(t *testing.T) {
	t.Parallel()

	receipt := NewReceipt()
	definition, _ := DefinitionForSlot("11")
	now := time.Now().UTC().Round(0)

	intent := StartIntent{
		Label: definition.Label, Slot: definition.Slot, Profile: "bothy", Generation: "braw", Nonce: strings.Repeat("ca", 24),
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := BeginStart(&receipt, intent); err != nil {
		t.Fatal(err)
	}

	if err := CompleteStart(&receipt, intent.Label, "wrong"); err == nil {
		t.Fatal("wrong start nonce accepted")
	}

	if err := CompleteStart(&receipt, intent.Label, intent.Nonce); err != nil {
		t.Fatal(err)
	}

	if len(receipt.Starts) != 0 {
		t.Fatalf("completed start remains in receipt: %#v", receipt.Starts)
	}
}

func TestBootstrapValidatesResolvedConfigAndPaths(t *testing.T) {
	t.Parallel()

	paths := config.Paths{Profile: "canny", DataDir: "/bothy/data", RuntimeDir: "/bothy/run", SocketPath: "/bothy/run/graith.sock"}

	bootstrap := Bootstrap{Request: StartupRequest{ConfigFile: "/bothy/config.toml", Paths: paths}}
	if err := bootstrap.ValidateResolvedConfig("/bothy/config.toml", paths); err != nil {
		t.Fatal(err)
	}

	if err := bootstrap.ValidateResolvedConfig("/strath/config.toml", paths); err == nil {
		t.Fatal("mismatched config path accepted")
	}

	changed := paths

	changed.DataDir = "/strath/data"
	if err := bootstrap.ValidateResolvedConfig("/bothy/config.toml", changed); err == nil {
		t.Fatal("mismatched data paths accepted")
	}
}
