//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/daemon"
)

func TestDaemonRestartPreservesRunningSessionAcrossExecHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and exec-restarts an isolated native daemon")
	}

	if commandPolicyFixtureBlockedByParentSandbox() {
		t.Skip("host-level daemon fixture cannot bind a Unix socket inside a parent Graith sandbox (EPERM); CI is explicitly never skipped")
	}

	repoRoot := integrationRepoRoot(t)
	binary := buildCurrentBinary(t, repoRoot, "gr")

	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")

	runtimeBase := os.Getenv("GRAITH_TMPDIR")
	if runtimeBase == "" {
		runtimeBase = "/tmp"
	}

	runtimeHome, err := os.MkdirTemp(runtimeBase, "handoff")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(runtimeHome) }) //nolint:gosec // os.MkdirTemp created this exact directory.

	home := filepath.Join(root, "home")
	profile := "handoff"
	appName := "graith-" + profile
	configDir := filepath.Join(configHome, appName)
	configPath := filepath.Join(configDir, "config.toml")
	statePath := filepath.Join(dataHome, appName, "state.json")
	socketPath := filepath.Join(runtimeHome, appName, "graith.sock")
	daemonLogPath := filepath.Join(dataHome, appName, "daemon.log")
	sessionPIDPath := filepath.Join(root, "session.pid")

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

	if err := os.WriteFile(configPath, []byte(restartHandoffConfig(sessionPIDPath)), 0o600); err != nil {
		t.Fatal(err)
	}

	daemonCmd := exec.Command(binary, "daemon", "start")
	daemonCmd.Env = env

	var daemonOutput lockedBuffer

	daemonCmd.Stdout = &daemonOutput
	daemonCmd.Stderr = &daemonOutput

	if err := daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}

	daemonDone := make(chan error, 1)
	go func() { daemonDone <- daemonCmd.Wait() }()

	t.Cleanup(func() {
		_ = runBinaryError(env, binary, "daemon", "stop")

		if daemonCmd.Process != nil {
			_ = daemonCmd.Process.Kill()
		}

		select {
		case <-daemonDone:
		case <-time.After(5 * time.Second):
		}
	})

	waitForSocket(t, socketPath, daemonDone, &daemonOutput)
	runBinary(t, env, binary, "--json", "new", "canny", "--agent", "claude", "--no-repo", "--background", "--skip-model-validation")

	oldState, oldSessionID, oldSession := waitForRestartHandoffSession(t, statePath, "canny", 0)
	if oldState.Version != daemon.CurrentStateVersion {
		t.Fatalf("initial state version = %d, want current %d", oldState.Version, daemon.CurrentStateVersion)
	}

	if pidText := strings.TrimSpace(waitForFile(t, sessionPIDPath)); pidText != strconv.Itoa(oldSession.PID) {
		t.Fatalf("agent pid marker = %q, state PID = %d", pidText, oldSession.PID)
	}

	output := runBinary(t, env, binary, "daemon", "restart")
	if !strings.Contains(output, "sessions preserved") {
		t.Fatalf("restart output = %q, want preserved-session restart", output)
	}

	logText := waitForRestartHandoffLog(t, daemonLogPath)

	listOutput := runBinary(t, env, binary, "--json", "list")
	if !json.Valid([]byte(listOutput)) || !strings.Contains(listOutput, oldSessionID) {
		t.Fatalf("restarted daemon list output = %q, want valid JSON containing session %q", listOutput, oldSessionID)
	}

	newState, newSessionID, newSession := waitForRestartHandoffSession(t, statePath, "canny", oldSession.PID)
	if newState.Version != daemon.CurrentStateVersion {
		t.Fatalf("restarted state version = %d, want current %d", newState.Version, daemon.CurrentStateVersion)
	}

	if newSessionID != oldSessionID {
		t.Fatalf("session ID changed across daemon restart: %q -> %q", oldSessionID, newSessionID)
	}

	if newSession.PID != oldSession.PID {
		t.Fatalf("session PID changed across daemon restart: %d -> %d", oldSession.PID, newSession.PID)
	}

	if newSession.CWD != oldSession.CWD || newSession.WorktreePath != oldSession.WorktreePath {
		t.Fatalf("session paths changed across daemon restart: cwd %q -> %q, worktree %q -> %q",
			oldSession.CWD, newSession.CWD, oldSession.WorktreePath, newSession.WorktreePath)
	}

	if starts := strings.Count(logText, `"msg":"daemon started"`); starts != 1 {
		t.Fatalf("daemon start log count = %d, want only the initial cold start", starts)
	}
}

func waitForRestartHandoffLog(t *testing.T, path string) string {
	t.Helper()

	var logText string

	waitFor(t, 20*time.Second, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}

		logText = string(data)
		positions := restartHandoffLogPositions(logText)

		return positions["prepare-exec-boundary"] >= 0 &&
			positions["awaiting-ack"] >= 0 &&
			positions["exec-started"] >= 0 &&
			positions["daemon-upgraded"] >= 0
	}, "restart handoff adoption logs")

	positions := restartHandoffLogPositions(logText)
	for _, key := range []string{"prepare-exec-boundary", "awaiting-ack", "exec-started", "daemon-upgraded"} {
		if positions[key] < 0 {
			t.Fatalf("restart handoff log is missing %s:\n%s", key, logText)
		}
	}

	if positions["prepare-exec-boundary"] >= positions["awaiting-ack"] ||
		positions["awaiting-ack"] >= positions["exec-started"] ||
		positions["exec-started"] >= positions["daemon-upgraded"] {
		t.Fatalf("restart handoff log order = %+v, want prepare boundary before ack before exec before adoption", positions)
	}

	return logText
}

func restartHandoffLogPositions(logText string) map[string]int {
	positions := map[string]int{
		"prepare-exec-boundary": -1,
		"awaiting-ack":          -1,
		"exec-started":          -1,
		"daemon-upgraded":       -1,
	}

	for lineNumber, line := range strings.Split(logText, "\n") {
		var event struct {
			Message string `json:"msg"`
			Stage   string `json:"stage"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}

		var key string

		switch {
		case event.Message == "upgrade stage completed" && event.Stage == "prepare-exec-boundary":
			key = "prepare-exec-boundary"
		case event.Message == "upgrade awaiting client acknowledgement":
			key = "awaiting-ack"
		case event.Message == "exec-ing new binary":
			key = "exec-started"
		case event.Message == "daemon upgraded":
			key = "daemon-upgraded"
		default:
			continue
		}

		if positions[key] < 0 {
			positions[key] = lineNumber
		}
	}

	return positions
}

func restartHandoffConfig(sessionPIDPath string) string {
	agentScript := `printf '%s' "$$" > "$GRAITH_FIXTURE_SESSION_PID"; echo braw-ready; trap 'exit 0' TERM INT; while :; do sleep 1; done`

	return fmt.Sprintf(`fetch_on_create = false

[sandbox]
enabled = false

[agents.claude]
command = "sh"
args = ["-c", %s]
resume_args = ["-c", %s]
fork_args = ["-c", %s]
non_interactive_args = []
env = { GRAITH_FIXTURE_SESSION_PID = %s }
`, strconv.Quote(agentScript), strconv.Quote(agentScript), strconv.Quote(agentScript), strconv.Quote(sessionPIDPath))
}

func waitForRestartHandoffSession(
	t *testing.T,
	path string,
	name string,
	wantPID int,
) (removalUpgradeState, string, removalUpgradeSession) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var state removalUpgradeState

			if json.Unmarshal(data, &state) == nil {
				id, session, ok := lookupRemovalUpgradeSession(state, name)

				if ok && session.Status == "running" && session.PID > 0 && (wantPID == 0 || session.PID == wantPID) {
					return state, id, session
				}
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for running session %q with PID %d", name, wantPID)

	return removalUpgradeState{}, "", removalUpgradeSession{}
}
