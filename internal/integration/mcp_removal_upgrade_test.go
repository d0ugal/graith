//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const preMCPRemovalRevision = "3fdb037103f6f32ef9d35210a7d920d44d2d18b7"

// TestMCPRemovalUpgradeFromExactMain exercises the real exec handoff from the
// exact pre-removal main commit. It proves cleanup is performed by the old
// daemon before the replacement runs, rather than relying on deleted manager
// code in the new binary.
func TestMCPRemovalUpgradeFromExactMain(t *testing.T) {
	repoRoot := integrationRepoRoot(t)
	oldBinary := buildRevisionBinary(t, repoRoot, preMCPRemovalRevision, "old-gr")
	newBinary := buildCurrentBinary(t, repoRoot, "new-gr")

	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")

	runtimeBase := os.Getenv("GRAITH_TMPDIR")
	if runtimeBase == "" {
		runtimeBase = "/tmp"
	}

	runtimeHome, err := os.MkdirTemp(runtimeBase, "u")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = os.RemoveAll(runtimeHome) //nolint:gosec // os.MkdirTemp created this exact directory.
	})

	home := filepath.Join(root, "home")
	profile := "mcpup"
	appName := "graith-" + profile
	configDir := filepath.Join(configHome, appName)
	configPath := filepath.Join(configDir, "config.toml")
	statePath := filepath.Join(dataHome, appName, "state.json")
	socketPath := filepath.Join(runtimeHome, appName, "graith.sock")
	recordPath := filepath.Join(root, "agent-argv.txt")
	childPIDPath := filepath.Join(root, "managed-child.pid")

	for _, dir := range []string{configDir, dataHome, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	env := append(os.Environ(),
		"HOME="+home,
		"GRAITH_PROFILE="+profile,
		"XDG_CONFIG_HOME="+configHome,
		"XDG_DATA_HOME="+dataHome,
		"XDG_RUNTIME_DIR="+runtimeHome,
	)

	oldConfig := removalUpgradeConfig(recordPath, childPIDPath, true)
	if err := os.WriteFile(configPath, []byte(oldConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	// Run the exact old daemon directly so the fixture does not depend on the
	// host's packaged-service bootstrap policy.
	oldDaemon := exec.Command(oldBinary, "daemon", "start")
	oldDaemon.Env = env

	var daemonOutput bytes.Buffer

	oldDaemon.Stdout = &daemonOutput
	oldDaemon.Stderr = &daemonOutput

	if err := oldDaemon.Start(); err != nil {
		t.Fatal(err)
	}

	daemonDone := make(chan error, 1)
	go func() { daemonDone <- oldDaemon.Wait() }()

	waitForSocket(t, socketPath, daemonDone, &daemonOutput)
	runBinary(t, env, oldBinary, "--json", "list")
	t.Cleanup(func() { _ = runBinaryError(env, newBinary, "daemon", "stop") })

	runBinary(t, env, oldBinary, "--json", "new", "canny", "--agent", "claude", "--no-repo", "--background", "--skip-model-validation")

	oldArgs := waitForFile(t, recordPath)
	if !strings.Contains(oldArgs, "--mcp-config") {
		t.Fatalf("pre-removal launch did not contain the expected MCP injection:\n%s", oldArgs)
	}

	oldState := readRemovalUpgradeState(t, statePath)

	oldSessionPID := removalUpgradeSessionPID(t, oldState, "canny")
	if oldSessionPID <= 0 {
		t.Fatalf("pre-removal session PID = %d", oldSessionPID)
	}

	proxy := exec.Command(oldBinary, "mcp-proxy", "croft")
	proxy.Env = env

	proxyInput, err := proxy.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	var proxyOutput bytes.Buffer

	proxy.Stdout = &proxyOutput
	proxy.Stderr = &proxyOutput

	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = proxyInput.Close()

		if proxy.Process != nil {
			_ = proxy.Process.Kill()
		}
	})

	managedPID, err := strconv.Atoi(strings.TrimSpace(waitForFile(t, childPIDPath)))
	if err != nil || managedPID <= 0 {
		t.Fatalf("managed child PID = %q: %v", waitForFile(t, childPIDPath), err)
	}

	mcpLogPath := waitForMCPLog(t, filepath.Join(dataHome, appName, "logs", "mcp"))

	// Replace the file without reloading the old daemon. The target preflight
	// sees valid post-removal config while the old in-memory manager still owns
	// the live child and must drain it before exec.
	if err := os.WriteFile(configPath, []byte(removalUpgradeConfig(recordPath, childPIDPath, false)), 0o600); err != nil {
		t.Fatal(err)
	}

	runBinary(t, env, newBinary, "daemon", "restart")
	waitForProcessExit(t, managedPID)
	waitForCommandExit(t, proxy, &proxyOutput)

	logAfterDrain, err := os.ReadFile(mcpLogPath)
	if err != nil {
		t.Fatalf("read drained MCP stderr log: %v", err)
	}

	if !bytes.Contains(logAfterDrain, []byte("managed-stderr")) {
		t.Fatalf("drained MCP stderr log did not contain child output: %q", logAfterDrain)
	}

	time.Sleep(100 * time.Millisecond)

	logAfterWait, err := os.ReadFile(mcpLogPath)
	if err != nil {
		t.Fatalf("reread drained MCP stderr log: %v", err)
	}

	if !bytes.Equal(logAfterWait, logAfterDrain) {
		t.Fatal("MCP stderr log changed after its child and pipe were drained")
	}

	newState := readRemovalUpgradeState(t, statePath)
	if newState.Version != 27 {
		t.Fatalf("migrated state version = %d, want 27", newState.Version)
	}

	if got := removalUpgradeSessionPID(t, newState, "canny"); got != oldSessionPID {
		t.Fatalf("adopted session PID = %d, want unchanged %d", got, oldSessionPID)
	}

	backup, err := os.ReadFile(statePath + ".v26.bak")
	if err != nil {
		t.Fatalf("read v26 backup: %v", err)
	}

	if !bytes.Contains(backup, []byte("mcp_servers")) {
		t.Fatal("v26 backup lost the pre-removal per-agent MCP field")
	}

	migrated, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(migrated, []byte("mcp_servers")) {
		t.Fatal("v27 state retained the removed per-agent MCP field")
	}

	// An injected proxy from the adopted process can no longer reconnect: even
	// the old proxy binary reaches only the new daemon's generic unsupported path.
	staleProxyOutput, staleProxyErr := runBinaryOutput(env, oldBinary, "mcp-proxy", "graith")
	if staleProxyErr == nil || !strings.Contains(staleProxyOutput, "unsupported control message") {
		t.Fatalf("stale proxy result = %v, output %q; want unsupported message", staleProxyErr, staleProxyOutput)
	}

	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}

	runBinary(t, env, newBinary, "--json", "restart", "canny", "--background")

	newArgs := waitForFile(t, recordPath)
	for _, obsolete := range []string{"--mcp-config", "mcp_servers.", "mcp-proxy"} {
		if strings.Contains(newArgs, obsolete) {
			t.Fatalf("relaunched process retained %q:\n%s", obsolete, newArgs)
		}
	}

	if !strings.Contains(newArgs, "--native-runtime-setting") {
		t.Fatalf("relaunched process lost unrelated native agent config:\n%s", newArgs)
	}

	runBinary(t, env, newBinary, "daemon", "stop")

	downgradeOutput, downgradeErr := runBinaryOutput(env, oldBinary, "daemon", "start")
	if downgradeErr == nil || !strings.Contains(downgradeOutput, "state file version 27 is newer than this binary supports (26)") {
		t.Fatalf("old daemon downgrade result = %v, output %q", downgradeErr, downgradeOutput)
	}
}

func removalUpgradeConfig(recordPath, childPIDPath string, includeMCP bool) string {
	agentScript := `printf '%s\n' "$0" "$@" > "$GRAITH_ARGS_RECORD"; exec cat`

	configText := fmt.Sprintf(`fetch_on_create = false

[sandbox]
enabled = false

[agents.claude]
command = "sh"
args = ["-c", %s, "--native-runtime-setting", "canny"]
resume_args = ["-c", %s, "--native-runtime-setting", "canny"]
fork_args = ["-c", %s, "--native-runtime-setting", "canny"]
non_interactive_args = []
env = { GRAITH_ARGS_RECORD = %s }
`, strconv.Quote(agentScript), strconv.Quote(agentScript), strconv.Quote(agentScript), strconv.Quote(recordPath))
	if !includeMCP {
		return configText
	}

	childScript := `printf '%s' "$$" > "$1"; echo managed-stderr >&2; trap 'exit 0' TERM INT; while :; do sleep 1; done`

	return configText + fmt.Sprintf(`
[agents.claude.mcp_servers.croft]
args = ["per-agent-legacy-field"]

[[mcp_servers]]
name = "croft"
command = "sh"
args = ["-c", %s, "sh", %s]
`, strconv.Quote(childScript), strconv.Quote(childPIDPath))
}

type removalUpgradeState struct {
	Version  int `json:"version"`
	Sessions map[string]struct {
		Name string `json:"name"`
		PID  int    `json:"pid"`
	} `json:"sessions"`
}

func readRemovalUpgradeState(t *testing.T, path string) removalUpgradeState {
	t.Helper()
	data := []byte(waitForFile(t, path))

	var state removalUpgradeState

	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	return state
}

func removalUpgradeSessionPID(t *testing.T, state removalUpgradeState, name string) int {
	t.Helper()

	for _, session := range state.Sessions {
		if session.Name == name {
			return session.PID
		}
	}

	t.Fatalf("state has no session named %q", name)

	return 0
}

func integrationRepoRoot(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")

	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	return strings.TrimSpace(string(out))
}

func buildRevisionBinary(t *testing.T, repoRoot, revision, name string) string {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	cmd := exec.Command("git", "worktree", "add", "--detach", source, revision)

	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create exact-revision worktree: %v\n%s", err, out)
	}

	t.Cleanup(func() {
		cleanup := exec.Command("git", "worktree", "remove", "--force", source)
		cleanup.Dir = repoRoot
		_ = cleanup.Run()
	})

	return buildGoBinary(t, source, filepath.Join(parent, name))
}

func buildCurrentBinary(t *testing.T, repoRoot, name string) string {
	t.Helper()
	return buildGoBinary(t, repoRoot, filepath.Join(t.TempDir(), name))
}

func buildGoBinary(t *testing.T, source, output string) string {
	t.Helper()

	cmd := exec.Command("go", "build", "-o", output, "./cmd/graith")

	cmd.Dir = source
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", source, err, out)
	}

	return output
}

func runBinary(t *testing.T, env []string, binary string, args ...string) string {
	t.Helper()

	out, err := runBinaryOutput(env, binary, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, out)
	}

	return out
}

func runBinaryError(env []string, binary string, args ...string) error {
	_, err := runBinaryOutput(env, binary, args...)
	return err
}

func runBinaryOutput(env []string, binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}

	return string(out), err
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for non-empty file %s", path)

	return ""
}

func waitForMCPLog(t *testing.T, dir string) string {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		paths, err := filepath.Glob(filepath.Join(dir, "*.log"))
		if err != nil {
			t.Fatal(err)
		}

		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err == nil && bytes.Contains(data, []byte("managed-stderr")) {
				return path
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for MCP stderr log in %s", dir)

	return ""
}

func waitForSocket(t *testing.T, path string, done <-chan error, output *bytes.Buffer) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil { //nolint:gosec // path is the exact fixture socket.
			return
		}

		select {
		case err := <-done:
			t.Fatalf("old daemon exited before creating its socket: %v\n%s", err, output.String())
		default:
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for daemon socket %s; output:\n%s", path, output.String())
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("managed child PID %d survived the daemon upgrade", pid)
}

func waitForCommandExit(t *testing.T, cmd *exec.Cmd, output *bytes.Buffer) {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("old proxy survived daemon upgrade; output: %s", output.String())
	}
}
