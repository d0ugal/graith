package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/adrg/xdg"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemonservice"
)

const (
	cliIsolationWorkerEnv   = "GRAITH_TEST_CLI_ISOLATION_WORKER"
	cliIsolationHostRootEnv = "GRAITH_TEST_CLI_ISOLATION_HOST_ROOT"
	cliIsolationSocketEnv   = "GRAITH_TEST_CLI_ISOLATION_HOST_SOCKET"
)

func TestCLISuiteIsolationKeepsLifecycleOffHostSocket(t *testing.T) {
	hostRoot := t.TempDir()
	hostProfile := "sentinel-host"
	// Unix socket addresses are short on macOS, while t.TempDir is deeply
	// nested in Graith worktrees. Use a short fixture name in the session's
	// writable temp root (or the system temp root outside a Graith session).
	socketFixtureRoot := os.Getenv("GRAITH_TMPDIR")
	if socketFixtureRoot == "" {
		socketFixtureRoot = os.TempDir()
	}

	hostRuntime, err := os.MkdirTemp(socketFixtureRoot, "g")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = os.RemoveAll(hostRuntime) //nolint:gosec // G703: hostRuntime was created by os.MkdirTemp for this test.
	})

	hostSocket := filepath.Join(hostRuntime, "graith-"+hostProfile, "graith.sock")
	if err := os.MkdirAll(filepath.Dir(hostSocket), 0o700); err != nil { //nolint:gosec // G703: hostSocket is beneath the test-owned temp directory.
		t.Fatal(err)
	}

	listener, err := net.Listen("unix", hostSocket)
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("cannot bind sentinel Unix socket in this sandbox: %v", err)
		}

		t.Fatal(err)
	}

	contacted := make(chan bool, 1)

	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			contacted <- false

			return
		}

		_ = connection.Close()

		contacted <- true
	}()

	//nolint:gosec // G702: the executable is this test process and every argument is a fixed test-runner flag.
	command := exec.Command(os.Args[0], "-test.run=^TestCLISuiteIsolationWorker$", "-test.count=1")

	command.Env = append(os.Environ(),
		cliIsolationWorkerEnv+"=1",
		cliIsolationHostRootEnv+"="+hostRoot,
		cliIsolationSocketEnv+"="+hostSocket,
		"XDG_CONFIG_HOME="+filepath.Join(hostRoot, "config"),
		"XDG_DATA_HOME="+filepath.Join(hostRoot, "data"),
		"XDG_RUNTIME_DIR="+hostRuntime,
		"GRAITH_PROFILE="+hostProfile,
		"GRAITH_TOKEN=sentinel-host-token",
	)

	output, commandErr := command.CombinedOutput()
	_ = listener.Close()

	if commandErr != nil {
		t.Fatalf("isolated CLI worker failed: %v\n%s", commandErr, output)
	}

	if <-contacted {
		t.Fatal("CLI lifecycle command contacted the sentinel host socket")
	}
}

func TestCLISuiteIsolationWorker(t *testing.T) {
	if os.Getenv(cliIsolationWorkerEnv) == "" {
		return
	}

	hostRoot := os.Getenv(cliIsolationHostRootEnv)

	hostSocket := os.Getenv(cliIsolationSocketEnv)
	if hostRoot == "" || hostSocket == "" {
		t.Fatal("missing sentinel host paths")
	}

	originalCfg, originalCfgFile, originalPaths := cfg, cfgFile, paths
	originalOut, originalJSON, originalAgentMode := out, jsonOutput, agentMode

	t.Cleanup(func() {
		cfg, cfgFile, paths = originalCfg, originalCfgFile, originalPaths
		out, jsonOutput, agentMode = originalOut, originalJSON, originalAgentMode
	})

	if err := executeWithArgs([]string{"version"}); err != nil {
		t.Fatalf("execute full CLI command: %v", err)
	}

	if pathWithinRoot(hostRoot, paths.ConfigFile) || paths.SocketPath == hostSocket || pathWithinRoot(hostRoot, paths.HumanTokenFile) {
		t.Fatalf("full CLI command retained sentinel host paths: %#v", paths)
	}

	if token := os.Getenv("GRAITH_TOKEN"); token != "" {
		t.Fatalf("GRAITH_TOKEN = %q, want isolated empty credential", token)
	}

	if err := daemonStopCmd.RunE(daemonStopCmd, nil); err == nil {
		t.Fatal("daemon stop unexpectedly found an isolated daemon")
	}
}

func pathWithinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}

	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func TestServiceBootstrapConfigSurvivesFlagRegistration(t *testing.T) {
	original := cfgFile

	t.Cleanup(func() { cfgFile = original })

	cfgFile = ""

	applyServiceBootstrapConfig(&daemonservice.Bootstrap{Request: daemonservice.StartupRequest{ConfigFile: "/bothy/graith.toml"}})

	if cfgFile != "/bothy/graith.toml" {
		t.Fatalf("bootstrap config = %q, want canonical request config", cfgFile)
	}
}

func TestExecuteJSONErrorFormat(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	os.Stderr = w

	execErr := executeWithArgs([]string{"--json", "nonexistent-command"})

	_ = w.Close()

	os.Stderr = oldStderr

	if execErr == nil {
		t.Fatal("expected error for unknown command")
	}

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, r)

	var jsonErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &jsonErr); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	if jsonErr.Error == "" {
		t.Error("JSON error message is empty")
	}
}

func TestExecutePlainTextErrorFormat(t *testing.T) {
	origOut := out
	origJSON := jsonOutput
	origAgentMode := agentMode

	defer func() {
		out = origOut
		jsonOutput = origJSON
		agentMode = origAgentMode
	}()

	t.Setenv("GR_AGENT_MODE", "0")

	agentMode = false

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	os.Stderr = w

	execErr := executeWithArgs([]string{"nonexistent-command"})

	_ = w.Close()

	os.Stderr = oldStderr

	if execErr == nil {
		t.Fatal("expected error for unknown command")
	}

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, r)

	got := buf.String()
	if !strings.HasPrefix(got, "error: ") {
		t.Errorf("expected plain text error starting with 'error: ', got %q", got)
	}

	var jsonErr struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(buf.Bytes(), &jsonErr) == nil {
		t.Error("plain text error should not be valid JSON")
	}
}

func restoreConfigFlag(t *testing.T) {
	t.Helper()
	registerCommands()

	configFlag := rootCmd.PersistentFlags().Lookup("config")
	originalValue := configFlag.Value.String()
	originalChanged := configFlag.Changed

	t.Cleanup(func() {
		_ = configFlag.Value.Set(originalValue)
		configFlag.Changed = originalChanged
	})
}

func TestConfigFlagBlockedInsideSession(t *testing.T) {
	restoreConfigFlag(t)

	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	t.Setenv("GRAITH_SESSION_ID", "test-session-123")
	t.Setenv("GR_AGENT_MODE", "0")

	err := executeWithArgs([]string{"--config", "/tmp/evil.toml", "list"})
	if err == nil {
		t.Fatal("expected error when --config is used inside a session")
	}

	if !strings.Contains(err.Error(), "not allowed inside a graith session") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestConfigFlagAllowedOutsideSession(t *testing.T) {
	restoreConfigFlag(t)

	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	if v, ok := os.LookupEnv("GRAITH_SESSION_ID"); ok {
		t.Cleanup(func() { _ = os.Setenv("GRAITH_SESSION_ID", v) })
	}

	_ = os.Unsetenv("GRAITH_SESSION_ID")

	t.Setenv("GR_AGENT_MODE", "0")

	origRuntimeDir := xdg.RuntimeDir
	xdg.RuntimeDir = t.TempDir()
	t.Cleanup(func() { xdg.RuntimeDir = origRuntimeDir })

	configFile := filepath.Join(t.TempDir(), "dreich.toml")
	connectErr := errors.New("dreich daemon unavailable")
	origListConnectFn := listConnectFn
	listConnectFn = func(*config.Config, config.Paths, string) (listConn, error) {
		return nil, connectErr
	}

	t.Cleanup(func() { listConnectFn = origListConnectFn })

	// The stubbed connection fails (expected), but the command should reach it
	// rather than fail with the "not allowed inside a session" error. The
	// temporary runtime directory also keeps the resolved socket isolated.
	err := executeWithArgs([]string{"--config", configFile, "list"})
	if !errors.Is(err, connectErr) {
		t.Fatalf("expected isolated list connection error, got %v", err)
	}
}

func TestConfigFlagBlockedForConfigSubcommand(t *testing.T) {
	restoreConfigFlag(t)

	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	t.Setenv("GRAITH_SESSION_ID", "test-session-123")
	t.Setenv("GR_AGENT_MODE", "0")

	err := executeWithArgs([]string{"--config", "/tmp/evil.toml", "config", "show"})
	if err == nil {
		t.Fatal("expected error when --config is used with config subcommand inside a session")
	}

	if !strings.Contains(err.Error(), "not allowed inside a graith session") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestConfigFlagBlockedWhenSetEmpty(t *testing.T) {
	restoreConfigFlag(t)

	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	t.Setenv("GRAITH_SESSION_ID", "")
	t.Setenv("GR_AGENT_MODE", "0")

	// GRAITH_SESSION_ID="" (set but empty) should still be treated as inside
	// a session — prevents bypass via GRAITH_SESSION_ID= gr --config ...
	err := executeWithArgs([]string{"--config", "/tmp/evil.toml", "list"})
	if err == nil {
		t.Fatal("expected error when --config is used with GRAITH_SESSION_ID set to empty")
	}

	if !strings.Contains(err.Error(), "not allowed inside a graith session") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestCanonicalConfigFileMakesRelativePathLaunchdStable(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	got, err := canonicalConfigFile(filepath.Join("bothy", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "bothy", "config.toml")
	if got != want || !filepath.IsAbs(got) {
		t.Fatalf("canonicalConfigFile() = %q, want %q", got, want)
	}
}

func TestExecuteCobraSilencesOwnErrors(t *testing.T) {
	origOut := out
	origJSON := jsonOutput

	defer func() {
		out = origOut
		jsonOutput = origJSON
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	os.Stdout = w

	_ = executeWithArgs([]string{"nonexistent-command"})

	_ = w.Close()

	os.Stdout = oldStdout

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, r)

	if strings.Contains(buf.String(), "Error:") {
		t.Errorf("Cobra's default error message appeared on stdout: %s", buf.String())
	}
}

// TestRegisterCommandsIdempotent verifies that command registration (moved out
// of per-file init() functions into registerCommands) wires subcommands onto
// rootCmd and can be invoked repeatedly without duplicating them — the
// sync.Once guard makes executeWithArgs safe to call more than once.
func TestRegisterCommandsIdempotent(t *testing.T) {
	registerCommands()
	registerCommands()

	want := []string{"new", "list", "update", "msg", "scenario", "sandbox", "store", "daemon", "config"}
	for _, name := range want {
		found := false

		for _, c := range rootCmd.Commands() {
			if c.Name() == name {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("subcommand %q not registered on rootCmd", name)
		}
	}

	for _, c := range rootCmd.Commands() {
		if c.Name() == "rename" {
			t.Fatal("removed rename command is still registered")
		}
	}

	// Persistent flags added during registration must be present exactly once.
	if rootCmd.PersistentFlags().Lookup("json") == nil {
		t.Error("--json persistent flag not registered")
	}

	// No duplicate command names (a second registration must not re-add them).
	seen := map[string]int{}
	for _, c := range rootCmd.Commands() {
		seen[c.Name()]++
	}

	for name, n := range seen {
		if n > 1 {
			t.Errorf("subcommand %q registered %d times, want 1", name, n)
		}
	}

	if seen["tokens"] != 0 {
		t.Error("obsolete tokens subcommand is still registered")
	}

	if listCmd.Flags().Lookup("tokens") == nil {
		t.Error("list --tokens flag is not registered")
	}
}

func TestCompletionOmitsTokensCommandAndIncludesListFlag(t *testing.T) {
	registerCommands()

	var buf bytes.Buffer
	if err := rootCmd.GenBashCompletion(&buf); err != nil {
		t.Fatalf("generate bash completion: %v", err)
	}

	script := buf.String()
	if strings.Contains(script, "_gr_tokens()") {
		t.Error("completion still contains the obsolete standalone token command")
	}

	if !strings.Contains(script, "--tokens") {
		t.Error("completion does not contain gr list --tokens flag")
	}
}

func TestListTokenProjectionFlagsAreExclusive(t *testing.T) {
	registerCommands()

	tokens := listCmd.Flags().Lookup("tokens")
	quiet := listCmd.Flags().Lookup("quiet")
	wide := listCmd.Flags().Lookup("wide")

	origTokensChanged := tokens.Changed
	origQuietChanged := quiet.Changed
	origWideChanged := wide.Changed

	t.Cleanup(func() {
		tokens.Changed = origTokensChanged
		quiet.Changed = origQuietChanged
		wide.Changed = origWideChanged
	})

	tokens.Changed = true
	quiet.Changed = true
	wide.Changed = false

	if err := listCmd.ValidateFlagGroups(); err == nil {
		t.Error("--tokens and --quiet should be mutually exclusive")
	}

	quiet.Changed = false
	wide.Changed = true

	if err := listCmd.ValidateFlagGroups(); err == nil {
		t.Error("--tokens and --wide should be mutually exclusive")
	}
}
