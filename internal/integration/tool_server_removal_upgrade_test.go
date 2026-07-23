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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/daemon"
)

const (
	preToolServerRemovalRevision = "3fdb037103f6f32ef9d35210a7d920d44d2d18b7"
	exactRevisionFetchURL        = "https://github.com/d0ugal/graith.git"
)

// TestToolServerRemovalUpgradeFromExactMain exercises the real exec handoff from the
// exact pre-removal main commit. It proves cleanup is performed by the old
// daemon before the replacement runs, rather than relying on deleted manager
// code in the new binary.
func TestToolServerRemovalUpgradeFromExactMain(t *testing.T) {
	repoRoot := integrationRepoRoot(t)
	oldBinary := buildRevisionBinary(t, repoRoot, preToolServerRemovalRevision, "old-gr")
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
	profile := "toolup"
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

	var daemonOutput lockedBuffer

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
		t.Fatalf("pre-removal launch did not contain the expected legacy tool-server injection:\n%s", oldArgs)
	}

	oldState := readRemovalUpgradeState(t, statePath)

	oldSessionID, oldSession := requireRemovalUpgradeSession(t, oldState, "canny")

	oldSessionPID := oldSession.PID
	if oldSessionPID <= 0 {
		t.Fatalf("pre-removal session PID = %d", oldSessionPID)
	}

	proxy := exec.Command(oldBinary, "mcp-proxy", "croft")
	proxy.Env = env

	proxyInput, err := proxy.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	var proxyOutput lockedBuffer

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

	legacyLogPath := waitForLegacyToolLog(t, filepath.Join(dataHome, appName, "logs", "mcp"))

	// Replace the file without reloading the old daemon. The target preflight
	// sees valid post-removal config while the old in-memory manager still owns
	// the live child and must drain it before exec.
	if err := os.WriteFile(configPath, []byte(removalUpgradeConfig(recordPath, childPIDPath, false)), 0o600); err != nil {
		t.Fatal(err)
	}

	runBinary(t, env, newBinary, "daemon", "restart")
	waitForRemovalUpgradeProcessExit(t, managedPID)

	// This wait is the stale-client regression sentinel: if the new daemon ever
	// accepted the removed connect request, the old proxy would enter its bridge
	// loop instead of reporting the generic unsupported-message response.
	waitForCommandOutput(t, &proxyOutput, "unsupported control message: mcp_connect")

	if err := proxyInput.Close(); err != nil {
		t.Fatalf("close stale proxy input: %v", err)
	}

	stopFixtureCommand(t, proxy)

	logAfterDrain, err := os.ReadFile(legacyLogPath)
	if err != nil {
		t.Fatalf("read drained legacy tool-server stderr log: %v", err)
	}

	if !bytes.Contains(logAfterDrain, []byte("managed-stderr")) {
		t.Fatalf("drained legacy tool-server stderr log did not contain child output: %q", logAfterDrain)
	}

	time.Sleep(100 * time.Millisecond)

	logAfterWait, err := os.ReadFile(legacyLogPath)
	if err != nil {
		t.Fatalf("reread drained legacy tool-server stderr log: %v", err)
	}

	if !bytes.Equal(logAfterWait, logAfterDrain) {
		t.Fatal("legacy tool-server stderr log changed after its child and pipe were drained")
	}

	newState := readRemovalUpgradeState(t, statePath)
	if newState.Version != daemon.CurrentStateVersion {
		t.Fatalf("migrated state version = %d, want current %d", newState.Version, daemon.CurrentStateVersion)
	}

	adoptedSessionID, adoptedSession := requireRemovalUpgradeSession(t, newState, "canny")
	if adoptedSessionID != oldSessionID {
		t.Fatalf("adopted session ID = %q, want unchanged %q", adoptedSessionID, oldSessionID)
	}

	if adoptedSession.PID != oldSessionPID {
		t.Fatalf("adopted session PID = %d, want unchanged %d", adoptedSession.PID, oldSessionPID)
	}

	if adoptedSession.CWD != oldSession.CWD || adoptedSession.WorktreePath != oldSession.WorktreePath {
		t.Fatalf("adopted session paths changed: cwd %q -> %q, worktree %q -> %q",
			oldSession.CWD, adoptedSession.CWD, oldSession.WorktreePath, adoptedSession.WorktreePath)
	}

	backup, err := os.ReadFile(statePath + ".v26.bak")
	if err != nil {
		t.Fatalf("read v26 backup: %v", err)
	}

	if !bytes.Contains(backup, []byte("mcp_servers")) {
		t.Fatal("v26 backup lost the pre-removal per-agent legacy tool-server field")
	}

	migrated, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(migrated, []byte("mcp_servers")) {
		t.Fatal("v27 state retained the removed per-agent legacy tool-server field")
	}

	// An injected proxy from the adopted process can no longer reconnect: even
	// the old proxy binary reaches only the new daemon's generic unsupported path.
	staleProxyOutput, staleProxyErr := runBinaryOutputWithin(3*time.Second, env, oldBinary, "mcp-proxy", "graith")
	if staleProxyErr == nil || !strings.Contains(staleProxyOutput, "unsupported control message") {
		t.Fatalf("stale proxy result = %v, output %q; want unsupported message", staleProxyErr, staleProxyOutput)
	}

	// Adoption preserves the running PTY. Stop it explicitly before exercising
	// the documented stopped-session restart path with the new agent arguments.
	runBinary(t, env, newBinary, "--json", "stop", "canny")
	waitForRemovalUpgradeProcessExit(t, oldSessionPID)

	stoppedState, stoppedSessionID, stoppedSession := waitForRemovalUpgradeSessionStop(t, statePath, "canny")
	if stoppedState.Version != daemon.CurrentStateVersion {
		t.Fatalf("stopped state version = %d, want current %d", stoppedState.Version, daemon.CurrentStateVersion)
	}

	if stoppedSessionID != oldSessionID {
		t.Fatalf("stopped session ID = %q, want preserved %q", stoppedSessionID, oldSessionID)
	}

	if stoppedSession.CWD != oldSession.CWD || stoppedSession.WorktreePath != oldSession.WorktreePath {
		t.Fatalf("stopped session paths changed: cwd %q -> %q, worktree %q -> %q",
			oldSession.CWD, stoppedSession.CWD, oldSession.WorktreePath, stoppedSession.WorktreePath)
	}

	logAfterStop, err := os.ReadFile(legacyLogPath)
	if err != nil {
		t.Fatalf("read legacy tool-server log after adopted session stop: %v", err)
	}

	if !bytes.Equal(logAfterStop, logAfterDrain) {
		t.Fatal("legacy tool-server log changed after adopted session stop")
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

	restartedState, restartedSessionID, restartedSession := waitForRemovalUpgradeSessionRelaunch(t, statePath, "canny", oldSessionPID)
	if restartedState.Version != daemon.CurrentStateVersion {
		t.Fatalf("restarted state version = %d, want current %d", restartedState.Version, daemon.CurrentStateVersion)
	}

	if restartedSessionID != oldSessionID {
		t.Fatalf("restarted session ID = %q, want preserved %q", restartedSessionID, oldSessionID)
	}

	if restartedSession.CWD != oldSession.CWD || restartedSession.WorktreePath != oldSession.WorktreePath {
		t.Fatalf("restarted session paths changed: cwd %q -> %q, worktree %q -> %q",
			oldSession.CWD, restartedSession.CWD, oldSession.WorktreePath, restartedSession.WorktreePath)
	}

	logAfterRelaunch, err := os.ReadFile(legacyLogPath)
	if err != nil {
		t.Fatalf("read legacy tool-server log after session relaunch: %v", err)
	}

	if !bytes.Equal(logAfterRelaunch, logAfterStop) {
		t.Fatal("legacy tool-server log changed after adopted session stop and relaunch")
	}

	runBinary(t, env, newBinary, "daemon", "stop")

	downgradeOutput, downgradeErr := runBinaryOutput(env, oldBinary, "daemon", "start")

	wantDowngrade := fmt.Sprintf("state file version %d is newer than this binary supports (26)", daemon.CurrentStateVersion)
	if downgradeErr == nil || !strings.Contains(downgradeOutput, wantDowngrade) {
		t.Fatalf("old daemon downgrade result = %v, output %q", downgradeErr, downgradeOutput)
	}
}

func TestEnsureGitRevisionFromShallowRepository(t *testing.T) {
	remote := t.TempDir()
	runFixtureGit(t, remote, "init", "--initial-branch=main")
	runFixtureGit(t, remote, "-c", "user.name=Graith Tests", "-c", "user.email=tests@graith.invalid", "commit", "--allow-empty", "-m", "braw")

	wanted := runFixtureGit(t, remote, "rev-parse", "HEAD")
	runFixtureGit(t, remote, "-c", "user.name=Graith Tests", "-c", "user.email=tests@graith.invalid", "commit", "--allow-empty", "-m", "canny")

	cloneParent := t.TempDir()
	shallow := filepath.Join(cloneParent, "shallow")

	clone := exec.Command("git", "clone", "--depth=1", "file://"+remote, shallow)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("create shallow fixture repository: %v\n%s", err, out)
	}

	if _, err := resolveGitCommit(shallow, wanted); err == nil {
		t.Fatalf("pinned commit %s unexpectedly present in shallow fixture", wanted)
	}

	if err := ensureGitRevision(shallow, "file://"+remote, wanted); err != nil {
		t.Fatalf("fetch pinned commit into shallow fixture: %v", err)
	}

	resolved, err := resolveGitCommit(shallow, wanted)
	if err != nil {
		t.Fatal(err)
	}

	if resolved != wanted {
		t.Fatalf("resolved commit = %s, want %s", resolved, wanted)
	}

	t.Run("identity mismatch", func(t *testing.T) {
		err := ensureGitRevision(shallow, "file://"+remote, strings.ToUpper(wanted))
		if err == nil || !strings.Contains(err.Error(), "exact revision resolved") {
			t.Fatalf("identity mismatch error = %v", err)
		}
	})

	t.Run("missing object", func(t *testing.T) {
		missing := strings.Repeat("0", 40)

		err := ensureGitRevision(shallow, "file://"+remote, missing)
		if err == nil || !strings.Contains(err.Error(), "fetch exact revision "+missing) {
			t.Fatalf("missing object error = %v", err)
		}
	})
}

func removalUpgradeConfig(recordPath, childPIDPath string, includeToolServer bool) string {
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
	if !includeToolServer {
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
	Version  int                              `json:"version"`
	Sessions map[string]removalUpgradeSession `json:"sessions"`
}

type removalUpgradeSession struct {
	Name         string `json:"name"`
	PID          int    `json:"pid"`
	Status       string `json:"status"`
	CWD          string `json:"cwd"`
	WorktreePath string `json:"worktree_path"`
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

func requireRemovalUpgradeSession(t *testing.T, state removalUpgradeState, name string) (string, removalUpgradeSession) {
	t.Helper()

	if id, session, ok := lookupRemovalUpgradeSession(state, name); ok {
		return id, session
	}

	t.Fatalf("state has no session named %q", name)

	return "", removalUpgradeSession{}
}

func lookupRemovalUpgradeSession(state removalUpgradeState, name string) (string, removalUpgradeSession, bool) {
	for id, session := range state.Sessions {
		if session.Name == name {
			return id, session, true
		}
	}

	return "", removalUpgradeSession{}, false
}

func waitForRemovalUpgradeSessionRelaunch(
	t *testing.T,
	path string,
	name string,
	previousPID int,
) (removalUpgradeState, string, removalUpgradeSession) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var state removalUpgradeState

			if json.Unmarshal(data, &state) == nil {
				id, session, ok := lookupRemovalUpgradeSession(state, name)

				if ok && session.PID > 0 && session.PID != previousPID {
					return state, id, session
				}
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for session %q to relaunch with a new PID after %d", name, previousPID)

	return removalUpgradeState{}, "", removalUpgradeSession{}
}

func waitForRemovalUpgradeSessionStop(
	t *testing.T,
	path string,
	name string,
) (removalUpgradeState, string, removalUpgradeSession) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var state removalUpgradeState

			if json.Unmarshal(data, &state) == nil {
				id, session, ok := lookupRemovalUpgradeSession(state, name)

				if ok && session.PID == 0 && session.Status == "stopped" {
					return state, id, session
				}
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for session %q to persist its stopped state", name)

	return removalUpgradeState{}, "", removalUpgradeSession{}
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

	if err := ensureGitRevision(repoRoot, exactRevisionFetchURL, revision); err != nil {
		t.Fatal(err)
	}

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

func ensureGitRevision(repoRoot, fetchURL, revision string) error {
	resolved, err := resolveGitCommit(repoRoot, revision)
	if err == nil {
		if resolved != revision {
			return fmt.Errorf("exact revision resolved to %s, want %s", resolved, revision)
		}

		return nil
	}

	// actions/checkout uses a shallow clone, so the pinned pre-removal commit
	// may not be present. Fetch that exact object without changing the user's
	// configured remotes or accepting a moving branch as the fixture source.
	fetch := exec.Command("git", "fetch", "--no-tags", "--depth=1", fetchURL, revision)

	fetch.Dir = repoRoot
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("fetch exact revision %s: %w\n%s", revision, err, out)
	}

	resolved, err = resolveGitCommit(repoRoot, revision)
	if err != nil {
		return fmt.Errorf("verify exact revision %s after fetch: %w", revision, err)
	}

	if resolved != revision {
		return fmt.Errorf("fetched revision resolved to %s, want %s", resolved, revision)
	}

	return nil
}

func resolveGitCommit(repoRoot, revision string) (string, error) {
	verify := exec.Command("git", "rev-parse", "--verify", revision+"^{commit}")
	verify.Dir = repoRoot

	out, err := verify.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve commit: %w: %s", err, out)
	}

	return strings.TrimSpace(string(out)), nil
}

func runFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	return strings.TrimSpace(string(out))
}

func buildCurrentBinary(t *testing.T, repoRoot, name string) string {
	t.Helper()
	return buildNativeGoBinary(t, repoRoot, filepath.Join(t.TempDir(), name))
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

func buildNativeGoBinary(t *testing.T, source, output string) string {
	t.Helper()

	cmd := exec.Command("go", "build", "-tags=libghostty", "-o", output, "./cmd/graith")
	cmd.Dir = source

	cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native %s: %v\n%s", source, err, out)
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
	return runBinaryOutputWithin(45*time.Second, env, binary, args...)
}

func runBinaryOutputWithin(timeout time.Duration, env []string, binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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

func waitForLegacyToolLog(t *testing.T, dir string) string {
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

	t.Fatalf("timed out waiting for legacy tool-server stderr log in %s", dir)

	return ""
}

func waitForSocket(t *testing.T, path string, done <-chan error, output *lockedBuffer) {
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

func waitForRemovalUpgradeProcessExit(t *testing.T, pid int) {
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

func waitForCommandOutput(t *testing.T, output *lockedBuffer, want string) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), want) {
			return
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for command output %q; output: %s", want, output.String())
}

func stopFixtureCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("stop fixture command: %v", err)
	}

	done := make(chan error, 1)

	go func() { done <- cmd.Wait() }()

	var err error

	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out reaping fixture command after kill")
	}

	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("reap fixture command: %v", err)
		}
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
