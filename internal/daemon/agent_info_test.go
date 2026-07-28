package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestAgentInfoRunsConfiguredCommandWithInfoArgs(t *testing.T) {
	sm := newTestSessionManager(t)
	t.Setenv("GRAITH_TOKEN", "dreich-secret")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/dreich-agent.sock")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/dreich-runtime")

	script := writeScript(t, `#!/bin/sh
if [ -n "$GRAITH_TOKEN" ]; then
  echo "GRAITH_TOKEN leaked" >&2
  exit 7
fi
if [ -n "$SSH_AUTH_SOCK" ]; then
  echo "SSH_AUTH_SOCK leaked" >&2
  exit 8
fi
if [ -n "$XDG_RUNTIME_DIR" ]; then
  echo "XDG_RUNTIME_DIR leaked" >&2
  exit 9
fi
printf 'args:'
for arg in "$@"; do
  printf ' <%s>' "$arg"
done
printf '\nagent=%s\n' "$GRAITH_AGENT_TYPE"
printf 'session=%s\n' "$GRAITH_SESSION_NAME"
`)

	sm.cfg.Agents["thrawn"] = config.Agent{
		Command: script,
		Args:    []string{"--session-arg"},
		Info: map[string][]string{
			"model": {"--list-models"},
		},
	}

	resp, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "thrawn", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo: %v", err)
	}

	if resp.Agent != "thrawn" || len(resp.Results) != 1 {
		t.Fatalf("response = %+v, want one thrawn result", resp)
	}

	result := resp.Results[0]
	if result.Key != "model" {
		t.Errorf("result key = %q, want model", result.Key)
	}

	if result.Command != script {
		t.Errorf("result command = %q, want %q", result.Command, script)
	}

	if !reflect.DeepEqual(result.Args, []string{"--list-models"}) {
		t.Errorf("result args = %v, want [--list-models]", result.Args)
	}

	for _, want := range []string{"args: <--list-models>", "agent=thrawn", "session=agent-info-thrawn"} {
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("stdout = %q, want substring %q", result.Stdout, want)
		}
	}

	if strings.Contains(result.Stdout, "--session-arg") {
		t.Errorf("agent session args leaked into info command: %q", result.Stdout)
	}

	if result.Error != "" {
		t.Errorf("result error = %q, want empty", result.Error)
	}
}

func TestAgentInfoRunsAllKeysSorted(t *testing.T) {
	sm := newTestSessionManager(t)
	script := writeScript(t, `#!/bin/sh
printf '%s\n' "$1"
`)

	sm.cfg.Agents["canny"] = config.Agent{
		Command: script,
		Info: map[string][]string{
			"version": {"-v"},
			"model":   {"--list-models"},
		},
	}

	resp, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "canny"})
	if err != nil {
		t.Fatalf("AgentInfo: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("results = %+v, want two keys", resp.Results)
	}

	if got := []string{resp.Results[0].Key, resp.Results[1].Key}; !reflect.DeepEqual(got, []string{"model", "version"}) {
		t.Fatalf("result key order = %v, want [model version]", got)
	}
}

func TestAgentInfoFailureCases(t *testing.T) {
	t.Run("unknown agent", func(t *testing.T) {
		sm := newTestSessionManager(t)
		_, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "ghaist"})
		assertErrContains(t, err, `unknown agent "ghaist"`)
	})

	t.Run("missing configuration", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.cfg.Agents["dreich"] = config.Agent{Command: "dreich"}
		_, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "dreich"})
		assertErrContains(t, err, `agent "dreich" has no info commands configured`)
	})

	t.Run("unknown key", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.cfg.Agents["braw"] = config.Agent{Command: "braw", Info: map[string][]string{"model": {"--models"}}}
		_, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "braw", Key: "version"})
		assertErrContains(t, err, `unknown info key "version"`)
	})

	t.Run("failed command", func(t *testing.T) {
		sm := newTestSessionManager(t)
		script := writeScript(t, "#!/bin/sh\necho 'dreich failure' >&2\nexit 3\n")
		sm.cfg.Agents["bide"] = config.Agent{Command: script, Info: map[string][]string{"model": {"--models"}}}

		resp, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "bide", Key: "model"})
		if err != nil {
			t.Fatalf("AgentInfo: %v", err)
		}

		if len(resp.Results) != 1 {
			t.Fatalf("results = %+v, want one failed result", resp.Results)
		}

		result := resp.Results[0]
		if result.ExitCode != 3 {
			t.Fatalf("exit code = %d, want 3", result.ExitCode)
		}

		if !strings.Contains(result.Error, "exit code 3") {
			t.Errorf("result error = %q, want exit code", result.Error)
		}

		if !strings.Contains(result.Stderr, "dreich failure") {
			t.Errorf("stderr = %q, want provider error", result.Stderr)
		}
	})
}

func TestAgentInfoTimeout(t *testing.T) {
	sm := newTestSessionManager(t)
	script := writeScript(t, "#!/bin/sh\nsleep 2\n")
	agent := config.Agent{Command: script}

	result, err := sm.runAgentInfoCommand(context.Background(), sm.cfg, "bairn", agent, "model", []string{"--models"}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("runAgentInfoCommand: %v", err)
	}

	if !strings.Contains(result.Error, "timed out") {
		t.Fatalf("result error = %q, want timeout", result.Error)
	}
}

func TestAgentInfoTimeoutKillsInheritedOutputProcessGroup(t *testing.T) {
	sm := newTestSessionManager(t)
	dir := t.TempDir()
	groupPIDPath := filepath.Join(dir, "group-pid")
	childReadyPath := filepath.Join(dir, "child-ready")
	script := writeScript(t, `#!/bin/sh
printf '%s\n' "$$" > "$GROUP_PID_PATH"
(
  trap '' TERM
  while :; do
    sleep 1
  done
) &
printf ready > "$CHILD_READY_PATH"
trap '' TERM
while :; do
  sleep 1
done
`)
	agent := config.Agent{
		Command: script,
		Env: map[string]string{
			"GROUP_PID_PATH":   groupPIDPath,
			"CHILD_READY_PATH": childReadyPath,
		},
	}

	start := time.Now()

	result, err := sm.runAgentInfoCommand(context.Background(), sm.cfg, "bothy", agent, "model", []string{"--models"}, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("runAgentInfoCommand: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout returned after %v, want prompt process-group cleanup", elapsed)
	}

	if !strings.Contains(result.Error, "timed out") {
		t.Fatalf("result error = %q, want timeout", result.Error)
	}

	if _, err := os.Stat(childReadyPath); err != nil {
		t.Fatalf("background child did not start: %v", err)
	}

	groupPIDBytes, err := os.ReadFile(groupPIDPath)
	if err != nil {
		t.Fatalf("read process group pid: %v", err)
	}

	groupPID, err := strconv.Atoi(strings.TrimSpace(string(groupPIDBytes)))
	if err != nil {
		t.Fatalf("parse process group pid %q: %v", string(groupPIDBytes), err)
	}

	t.Cleanup(func() {
		_ = syscall.Kill(-groupPID, syscall.SIGKILL)
	})

	if !waitForAgentInfoProcessGroupGone(groupPID, 2*time.Second) {
		t.Fatalf("process group %d still alive after timed out info command", groupPID)
	}
}

func TestAgentInfoCleansInheritedOutputProcessGroupAfterExit(t *testing.T) {
	sm := newTestSessionManager(t)
	dir := t.TempDir()
	groupPIDPath := filepath.Join(dir, "group-pid")
	childReadyPath := filepath.Join(dir, "child-ready")
	script := writeScript(t, `#!/bin/sh
printf '%s\n' "$$" > "$GROUP_PID_PATH"
(
  trap '' TERM
  while :; do
    sleep 1
  done
) &
printf ready > "$CHILD_READY_PATH"
echo "braw model"
exit 0
`)
	agent := config.Agent{
		Command: script,
		Env: map[string]string{
			"GROUP_PID_PATH":   groupPIDPath,
			"CHILD_READY_PATH": childReadyPath,
		},
	}

	start := time.Now()

	result, err := sm.runAgentInfoCommand(context.Background(), sm.cfg, "bothy", agent, "model", []string{"--models"}, 5*time.Second)
	if err != nil {
		t.Fatalf("runAgentInfoCommand: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("post-exit inherited output returned after %v, want bounded wait", elapsed)
	}

	if !strings.Contains(result.Error, "left output streams open after exit") {
		t.Fatalf("result error = %q, want inherited output pipe error", result.Error)
	}

	if !strings.Contains(result.Stdout, "braw model") {
		t.Fatalf("stdout = %q, want provider output before pipe cleanup", result.Stdout)
	}

	if _, err := os.Stat(childReadyPath); err != nil {
		t.Fatalf("background child did not start: %v", err)
	}

	groupPIDBytes, err := os.ReadFile(groupPIDPath)
	if err != nil {
		t.Fatalf("read process group pid: %v", err)
	}

	groupPID, err := strconv.Atoi(strings.TrimSpace(string(groupPIDBytes)))
	if err != nil {
		t.Fatalf("parse process group pid %q: %v", string(groupPIDBytes), err)
	}

	t.Cleanup(func() {
		_ = syscall.Kill(-groupPID, syscall.SIGKILL)
	})

	if !waitForAgentInfoProcessGroupGone(groupPID, 2*time.Second) {
		t.Fatalf("process group %d still alive after inherited output cleanup", groupPID)
	}
}

func TestAgentInfoOutputIsCapped(t *testing.T) {
	sm := newTestSessionManager(t)
	script := writeScript(t, `#!/bin/sh
printf 'stdout-braw'
printf 'stderr-dreich' >&2
`)
	agent := config.Agent{Command: script}

	result, err := sm.runAgentInfoCommandWithOutputLimit(
		context.Background(),
		sm.cfg,
		"braw",
		agent,
		"version",
		[]string{"--version"},
		time.Second,
		6,
	)
	if err != nil {
		t.Fatalf("runAgentInfoCommandWithOutputLimit: %v", err)
	}

	if result.Stdout != "stdout" || !result.StdoutTruncated {
		t.Fatalf("stdout = %q truncated=%v, want capped stdout", result.Stdout, result.StdoutTruncated)
	}

	if result.Stderr != "stderr" || !result.StderrTruncated {
		t.Fatalf("stderr = %q truncated=%v, want capped stderr", result.Stderr, result.StderrTruncated)
	}

	if result.Error != "" {
		t.Fatalf("result error = %q, want successful command despite output cap", result.Error)
	}
}

func TestAgentInfoReturnsPerKeyFailuresWithSuccessfulSiblings(t *testing.T) {
	sm := newTestSessionManager(t)
	script := writeScript(t, `#!/bin/sh
case "$1" in
  model)
    echo "model-a"
    ;;
  version)
    echo "version dreich" >&2
    exit 5
    ;;
esac
`)

	sm.cfg.Agents["couthy"] = config.Agent{
		Command: script,
		Info: map[string][]string{
			"version": {"version"},
			"model":   {"model"},
		},
	}

	resp, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "couthy"})
	if err != nil {
		t.Fatalf("AgentInfo: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("results = %+v, want two keys", resp.Results)
	}

	model := resp.Results[0]
	if model.Key != "model" || strings.TrimSpace(model.Stdout) != "model-a" || model.Error != "" {
		t.Fatalf("model result = %+v, want successful model output", model)
	}

	version := resp.Results[1]
	if version.Key != "version" || version.ExitCode != 5 {
		t.Fatalf("version result = %+v, want failed version output", version)
	}

	if !strings.Contains(version.Error, "exit code 5") || !strings.Contains(version.Stderr, "version dreich") {
		t.Fatalf("version result = %+v, want error and stderr preserved", version)
	}
}

func TestAgentInfoLifecycleLeaseBlocksUpgradeDrain(t *testing.T) {
	sm := newTestSessionManager(t)
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	releasePath := filepath.Join(dir, "release")
	script := writeScript(t, `#!/bin/sh
printf started > "$MARKER_STARTED"
while [ ! -f "$MARKER_RELEASE" ]; do
  sleep 0.02
done
echo "braw version"
`)

	sm.cfg.Agents["strath"] = config.Agent{
		Command: script,
		Env: map[string]string{
			"MARKER_STARTED": startedPath,
			"MARKER_RELEASE": releasePath,
		},
		Info: map[string][]string{
			"version": {"--version"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := false

	go func() {
		_, err := sm.AgentInfo(ctx, protocol.AgentInfoMsg{Agent: "strath", Key: "version"})
		done <- err
	}()

	t.Cleanup(func() {
		if finished {
			return
		}

		cancel()

		_ = os.WriteFile(releasePath, []byte("done"), 0o600)

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("AgentInfo did not finish during cleanup")
		}
	})

	waitForAgentInfoMarker(t, startedPath, 5*time.Second)

	drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if err := sm.waitLifecycleIdle(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitLifecycleIdle while info command is running = %v, want deadline", err)
	}

	if err := os.WriteFile(releasePath, []byte("done"), 0o600); err != nil {
		t.Fatalf("write release marker: %v", err)
	}

	select {
	case err := <-done:
		finished = true

		if err != nil {
			t.Fatalf("AgentInfo: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AgentInfo did not finish after release marker")
	}

	if err := sm.waitLifecycleIdle(context.Background()); err != nil {
		t.Fatalf("waitLifecycleIdle after info command finished: %v", err)
	}
}

func TestAgentInfoRejectsDuringUpgrade(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.upgradePending = true

	_, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "missing"})
	assertErrContains(t, err, "upgrade is pending")

	if sm.lifecycleInFlight != 0 {
		t.Fatalf("refused entry leaked lifecycle reservations: %d", sm.lifecycleInFlight)
	}
}

func TestAgentInfoHandlerResponds(t *testing.T) {
	cfg := config.Default()
	script := writeScript(t, "#!/bin/sh\necho model-a\n")
	cfg.Agents["blether"] = config.Agent{Command: script, Info: map[string][]string{"model": {"--models"}}}

	h := newTestHarnessWithConfig(t, cfg)
	h.sendControl(t, "agent_info", protocol.AgentInfoMsg{Agent: "blether", Key: "model"})

	env := h.expectType(t, "agent_info_response")

	var resp protocol.AgentInfoResponseMsg
	if err := protocol.DecodePayload(env, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Agent != "blether" || len(resp.Results) != 1 || !strings.Contains(resp.Results[0].Stdout, "model-a") {
		t.Fatalf("handler response = %+v, want blether model output", resp)
	}
}

func waitForAgentInfoMarker(t *testing.T, path string, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for marker %s", path)
}

func waitForAgentInfoProcessGroupGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if exactProcessGroupGone(pid) {
			return true
		}

		time.Sleep(10 * time.Millisecond)
	}

	return exactProcessGroupGone(pid)
}
