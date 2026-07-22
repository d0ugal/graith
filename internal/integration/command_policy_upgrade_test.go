//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
)

const commandPolicyBaselineCommit = "3fdb037103f6f32ef9d35210a7d920d44d2d18b7"

type commandPolicyUpgradeSession struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Token        string `json:"token"`
	Status       string `json:"status"`
	PID          int    `json:"pid"`
	PIDStartTime int64  `json:"pid_start_time"`
}

type commandPolicyUpgradeState struct {
	Version  int                                    `json:"version"`
	Sessions map[string]commandPolicyUpgradeSession `json:"sessions"`
}

// TestCommandPolicyRemovalUpgradeFromExactBaseline exercises the real
// 3fdb037 binary, its v26 state, and its generated Claude hook. The replacement
// must stop that exact live process instead of adopting it, remove the old
// Graith-owned hook, bound an attempted old-hook call with a native deny, and
// leave the session resumable with lifecycle-only hooks.
func TestCommandPolicyRemovalUpgradeFromExactBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and exec-upgrades the exact removal baseline")
	}

	if commandPolicyFixtureBlockedByParentSandbox() {
		t.Skip("host-level daemon fixture cannot bind a Unix socket inside a parent Graith sandbox (EPERM); CI is explicitly never skipped")
	}

	repoRoot := gitOutput(t, "rev-parse", "--show-toplevel")

	root, err := os.MkdirTemp("/tmp", "graith-policy-upgrade-")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(root) })

	oldSource := filepath.Join(root, "old-source")

	if err := ensureGitRevision(repoRoot, exactRevisionFetchURL, commandPolicyBaselineCommit); err != nil {
		t.Fatal(err)
	}

	runGit(t, repoRoot, "worktree", "add", "--detach", oldSource, commandPolicyBaselineCommit)
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", oldSource)
		cmd.Dir = repoRoot
		_ = cmd.Run()
	})

	oldBinary := filepath.Join(root, "gr-old")
	currentBinary := filepath.Join(root, "gr-current")

	buildGraithBinary(t, oldSource, oldBinary)
	buildGraithBinary(t, repoRoot, currentBinary)

	configHome := filepath.Join(root, "config")
	configDir := filepath.Join(configHome, "graith")
	dataDir := filepath.Join(root, "data")

	runtimeDir, err := os.MkdirTemp(repoRoot, "r-")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	for _, dir := range []string{configDir, dataDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	configPath := filepath.Join(configDir, "config.toml")
	argsPath := filepath.Join(root, "agent-args")
	// The exact baseline deliberately refuses command-policy enforcement for an
	// executable whose basename is not the supported agent name. Give the
	// fixture its real CLI basename while keeping the implementation test-owned.
	agentScript := filepath.Join(root, "claude")
	//nolint:gosec // The test-owned fixture script must be owner-executable.
	if err := os.WriteFile(agentScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GRAITH_FIXTURE_ARGS\"\nprintf 'braw-ready\\n'\nexec sleep 600\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("GRAITH_FIXTURE_ARGS", argsPath)

	var env []string

	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GRAITH_") {
			env = append(env, value)
		}
	}

	env = append(env, "GRAITH_FIXTURE_ARGS="+argsPath)

	defaultPaths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}

	if defaultPaths.ConfigFile != configPath {
		t.Fatalf("fixture config path = %q, generated hook default = %q", configPath, defaultPaths.ConfigFile)
	}

	writeCommandPolicyFixtureConfig(t, configPath, dataDir, agentScript, true)

	daemonLog, err := os.Create(filepath.Join(root, "daemon-process.log"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = daemonLog.Close() })

	daemonCmd := exec.Command(oldBinary, "--config", configPath, "daemon", "start")
	daemonCmd.Env = env
	daemonCmd.Stdout = daemonLog

	daemonCmd.Stderr = daemonLog
	if err := daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}

	daemonDone := make(chan error, 1)
	go func() { daemonDone <- daemonCmd.Wait() }()

	t.Cleanup(func() {
		if daemonCmd.Process != nil {
			_ = daemonCmd.Process.Signal(syscall.SIGKILL)
		}

		select {
		case <-daemonDone:
		case <-time.After(5 * time.Second):
		}
	})

	paths := resolveFixturePaths(t, configPath, dataDir)
	waitForDaemonStart(t, paths.SocketPath, filepath.Join(root, "daemon-process.log"), daemonDone, 20*time.Second)
	runFixtureCLI(t, env, oldBinary, "--config", configPath, "new", "braw", "--agent", "claude", "--no-repo", "--background")

	statePath := paths.StateFile

	var oldState commandPolicyUpgradeState

	waitFor(t, 15*time.Second, func() bool {
		oldState = readCommandPolicyUpgradeState(t, statePath)
		return len(oldState.Sessions) == 1
	}, "old daemon to persist the live session")

	var oldSession commandPolicyUpgradeSession
	for _, session := range oldState.Sessions {
		oldSession = session
	}

	if oldState.Version != 26 || oldSession.PID <= 1 || oldSession.PIDStartTime == 0 {
		t.Fatalf("old state = version %d session %+v, want live v26 exact identity", oldState.Version, oldSession)
	}

	settingsPath := filepath.Join(dataDir, "hooks", oldSession.ID, "settings.json")

	oldSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(oldSettings, []byte("command-policy-check")) {
		t.Fatalf("exact baseline did not generate the expected policy hook: %s", oldSettings)
	}

	// Archive/remove the user-facing key before upgrading. The old daemon reloads
	// the now-disabled policy, but the live session's creation snapshot and hook
	// still prove it was launched under policy.
	writeCommandPolicyFixtureConfig(t, configPath, dataDir, agentScript, false)
	runFixtureCLI(t, env, oldBinary, "--config", configPath, "daemon", "reload")

	preUpgradeState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	requestFixtureUpgrade(t, configPath, dataDir, currentBinary)
	waitFor(t, 20*time.Second, func() bool {
		state := readCommandPolicyUpgradeState(t, statePath)
		session, ok := state.Sessions[oldSession.ID]

		return state.Version == daemon.CurrentStateVersion && ok && session.PID == 0 && session.Status == string(daemon.StatusStopped)
	}, "replacement daemon to stop and migrate the policy-enabled session")

	if processExists(oldSession.PID) {
		t.Fatalf("old policy-enabled process PID %d still exists after migration", oldSession.PID)
	}

	if _, err := os.Stat(settingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old generated policy settings still exist: %v", err)
	}

	backup, err := os.ReadFile(daemon.StateBackupPath(statePath, 26))
	if err != nil {
		t.Fatalf("read exact v26 backup: %v", err)
	}

	if !bytes.Equal(backup, preUpgradeState) {
		t.Fatal("upgrade backup is not the exact pre-upgrade v26 state")
	}

	migrated, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(migrated, []byte(`"command_policy"`)) {
		t.Fatalf("migrated state retained removed creation config: %s", migrated)
	}

	// An already-generated old hook is bounded and denies when pointed at the
	// replacement daemon; it cannot silently allow, hang, or reach a handler.
	hookCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	// Invoke the same command the generated hook contains. The hook inherits the
	// session's config environment; adding --config would be rejected inside a
	// Graith session and would not exercise the removed daemon message.
	hook := exec.CommandContext(hookCtx, oldBinary, "command-policy-check")

	hook.Env = append(env,
		"GRAITH_SESSION_ID="+oldSession.ID,
		"GRAITH_SESSION_NAME="+oldSession.Name,
		"GRAITH_AGENT_TYPE=claude",
		"GRAITH_TOKEN="+oldSession.Token,
	)
	hook.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"echo canny"}}`)

	hookOutput, err := hook.CombinedOutput()
	if err != nil {
		t.Fatalf("old hook did not return a bounded native decision: %v: %s", err, hookOutput)
	}

	if hookCtx.Err() != nil || !bytes.Contains(hookOutput, []byte(`"permissionDecision":"deny"`)) ||
		!bytes.Contains(hookOutput, []byte("unexpected response error")) {
		t.Fatalf("old hook response = %s, want bounded deny from unsupported removed handler", hookOutput)
	}

	runFixtureCLI(t, env, currentBinary, "--config", configPath, "restart", oldSession.Name)
	waitFor(t, 15*time.Second, func() bool {
		state := readCommandPolicyUpgradeState(t, statePath)
		session := state.Sessions[oldSession.ID]

		return session.PID > 1 && session.PID != oldSession.PID && session.Status == string(daemon.StatusRunning)
	}, "migrated session to resume with a fresh process")

	newSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read regenerated lifecycle settings: %v", err)
	}

	if bytes.Contains(newSettings, []byte("command-policy-check")) ||
		!bytes.Contains(newSettings, []byte("report-status")) || !bytes.Contains(newSettings, []byte("check-inbox")) {
		t.Fatalf("regenerated settings did not preserve lifecycle-only hooks: %s", newSettings)
	}

	runFixtureCLI(t, env, currentBinary, "--config", configPath, "daemon", "stop")

	select {
	case <-daemonDone:
	case <-time.After(15 * time.Second):
		t.Fatal("replacement daemon did not stop")
	}

	// Restoring only the old binary must not reactivate policy from the migrated
	// state. A downgrade requires restoring both the v26 backup and old config.
	downgradeCtx, downgradeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer downgradeCancel()

	downgrade := exec.CommandContext(downgradeCtx, oldBinary, "--config", configPath, "daemon", "start")
	downgrade.Env = env

	downgradeOutput, err := downgrade.CombinedOutput()
	if err == nil || downgradeCtx.Err() != nil || !bytes.Contains(downgradeOutput, []byte("newer than this binary supports")) {
		t.Fatalf("old binary downgrade result err=%v context=%v output=%s, want immediate newer-state refusal", err, downgradeCtx.Err(), downgradeOutput)
	}
}

func TestCommandPolicyUpgradeFixtureNeverSkipsCI(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "strath")
	t.Setenv("CI", "true")

	if commandPolicyFixtureBlockedByParentSandbox() {
		t.Fatal("the exact-baseline upgrade fixture must not be skipped in CI")
	}
}

func commandPolicyFixtureBlockedByParentSandbox() bool {
	return os.Getenv("GRAITH_SESSION_ID") != "" && os.Getenv("CI") == ""
}

func buildGraithBinary(t *testing.T, sourceDir, output string) {
	t.Helper()

	cmd := exec.Command("go", "build", "-o", output, "./cmd/graith")
	cmd.Dir = sourceDir

	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Graith from %s: %v\n%s", sourceDir, err, output)
	}
}

func gitOutput(t *testing.T, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}

	return strings.TrimSpace(string(output))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)

	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func writeCommandPolicyFixtureConfig(t *testing.T, path, dataDir, agentScript string, policy bool) {
	t.Helper()

	contents := fmt.Sprintf(`default_agent = "claude"
data_dir = %q
fetch_on_create = false

[sandbox]
enabled = false

[agents.claude]
command = %q
args = []
resume_args = []
non_interactive_args = []
inject_prompt = false
prompt_injection = "none"
`, dataDir, agentScript)
	if policy {
		contents += `
[command_policy]
backend = "builtin"
timeout = "5s"

[command_policy.builtin]
allow = ["echo @*"]
deny = ["rm @*"]
`
	}

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resolveFixturePaths(t *testing.T, configPath, dataDir string) config.Paths {
	t.Helper()

	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}

	paths.ConfigFile = configPath

	return paths.WithDataDir(dataDir)
}

func runFixtureCLI(t *testing.T, env []string, binary string, args ...string) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", filepath.Base(binary), strings.Join(args, " "), err, output)
	}

	return output
}

func requestFixtureUpgrade(t *testing.T, configPath, dataDir, currentBinary string) {
	t.Helper()

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}

	paths := resolveFixturePaths(t, configPath, dataDir)

	c, err := client.ConnectExisting(cfg, paths)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	msg := protocol.UpgradeMsg{ExecPath: currentBinary, ClientVersion: version.Version}
	if msg.ClientVersion == "" {
		msg.ClientVersion = "integration"
	}

	if err := c.SendControl("upgrade_preflight", msg); err != nil {
		t.Fatal(err)
	}

	if response, err := c.ReadControlResponse(); err != nil || response.Type != "upgrade_preflight_ok" {
		t.Fatalf("upgrade preflight response = %q err=%v", response.Type, err)
	}

	if err := c.SendControl("upgrade", msg); err != nil {
		t.Fatal(err)
	}

	if response, err := c.ReadControlResponse(); err != nil || response.Type != "upgrading" {
		t.Fatalf("upgrade response = %q err=%v", response.Type, err)
	}
}

func readCommandPolicyUpgradeState(t *testing.T, path string) commandPolicyUpgradeState {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return commandPolicyUpgradeState{}
		}

		t.Fatal(err)
	}

	var state commandPolicyUpgradeState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}

	return state
}

func waitForDaemonStart(t *testing.T, path, logPath string, done <-chan error, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			output, _ := os.ReadFile(logPath)
			t.Fatalf("baseline daemon exited before creating its socket: %v\n%s", err, output)
		default:
		}

		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	output, _ := os.ReadFile(logPath)
	t.Fatalf("timed out waiting for daemon socket %s\n%s", path, output)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", description)
}

func processExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return process.Signal(syscall.Signal(0)) == nil
}
