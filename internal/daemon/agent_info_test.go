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
		Info: config.AgentInfoCommands{
			"model": config.AgentInfoCommand{Args: []string{"--list-models"}},
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
		Info: config.AgentInfoCommands{
			"version": config.AgentInfoCommand{Args: []string{"-v"}},
			"model":   config.AgentInfoCommand{Args: []string{"--list-models"}},
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

func TestAgentInfoCachesSuccessfulResultsAndRefreshes(t *testing.T) {
	sm := newTestSessionManager(t)
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	script := writeScript(t, `#!/bin/sh
count=$(cat "$COUNT_PATH" 2>/dev/null || echo 0)
count=$((count + 1))
printf '%s\n' "$count" > "$COUNT_PATH"
printf 'model-%s - Braw Model\n' "$count"
`)

	sm.cfg.Agents["couthy"] = config.Agent{
		Command: script,
		Env:     map[string]string{"COUNT_PATH": countPath},
		Info: config.AgentInfoCommands{
			"model": config.AgentInfoCommand{Args: []string{"--models"}, Format: config.AgentInfoFormatModelList},
		},
	}

	first, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "couthy", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo first: %v", err)
	}

	firstResult := first.Results[0]
	if got := strings.TrimSpace(firstResult.Stdout); got != "model-1 - Braw Model" {
		t.Fatalf("first stdout = %q, want model-1", got)
	}

	if firstResult.Cache == nil || !firstResult.Cache.Enabled || firstResult.Cache.Hit || firstResult.Cache.FetchedAt == "" || firstResult.Cache.ExpiresAt == "" {
		t.Fatalf("first cache metadata = %+v, want fresh stored miss", firstResult.Cache)
	}

	if len(firstResult.Models) != 1 || firstResult.Models[0].ID != "model-1" || firstResult.Models[0].Description != "Braw Model" {
		t.Fatalf("first models = %+v, want parsed model record", firstResult.Models)
	}

	second, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "couthy", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo second: %v", err)
	}

	secondResult := second.Results[0]
	if got := strings.TrimSpace(secondResult.Stdout); got != "model-1 - Braw Model" {
		t.Fatalf("second stdout = %q, want cached model-1", got)
	}

	if secondResult.Cache == nil || !secondResult.Cache.Hit {
		t.Fatalf("second cache metadata = %+v, want cache hit", secondResult.Cache)
	}

	refreshed, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "couthy", Key: "model", Refresh: true})
	if err != nil {
		t.Fatalf("AgentInfo refresh: %v", err)
	}

	refreshedResult := refreshed.Results[0]
	if got := strings.TrimSpace(refreshedResult.Stdout); got != "model-2 - Braw Model" {
		t.Fatalf("refresh stdout = %q, want model-2", got)
	}

	if refreshedResult.Cache == nil || refreshedResult.Cache.Hit || !refreshedResult.Cache.Bypassed {
		t.Fatalf("refresh cache metadata = %+v, want bypassed miss", refreshedResult.Cache)
	}

	noCache, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "couthy", Key: "model", NoCache: true})
	if err != nil {
		t.Fatalf("AgentInfo no-cache: %v", err)
	}

	noCacheResult := noCache.Results[0]
	if got := strings.TrimSpace(noCacheResult.Stdout); got != "model-3 - Braw Model" {
		t.Fatalf("no-cache stdout = %q, want model-3", got)
	}

	if noCacheResult.Cache == nil || noCacheResult.Cache.Hit || !noCacheResult.Cache.Bypassed {
		t.Fatalf("no-cache metadata = %+v, want bypassed miss", noCacheResult.Cache)
	}

	cachedAfterNoCache, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "couthy", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo cached after no-cache: %v", err)
	}

	if got := strings.TrimSpace(cachedAfterNoCache.Results[0].Stdout); got != "model-2 - Braw Model" {
		t.Fatalf("cached after no-cache stdout = %q, want refresh result model-2", got)
	}
}

func TestAgentInfoGlobalCacheDisabledOverridesPerKeyTTL(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.AgentInfo.CacheTTL = "0"
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	script := writeScript(t, `#!/bin/sh
count=$(cat "$COUNT_PATH" 2>/dev/null || echo 0)
count=$((count + 1))
printf '%s\n' "$count" > "$COUNT_PATH"
printf 'version-%s\n' "$count"
`)

	sm.cfg.Agents["thrawn"] = config.Agent{
		Command: script,
		Env:     map[string]string{"COUNT_PATH": countPath},
		Info: config.AgentInfoCommands{
			"version": config.AgentInfoCommand{Args: []string{"--version"}, CacheTTL: "1h"},
		},
	}

	first, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "thrawn", Key: "version"})
	if err != nil {
		t.Fatalf("AgentInfo first: %v", err)
	}

	second, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "thrawn", Key: "version"})
	if err != nil {
		t.Fatalf("AgentInfo second: %v", err)
	}

	if got := strings.TrimSpace(first.Results[0].Stdout); got != "version-1" {
		t.Fatalf("first stdout = %q, want version-1", got)
	}

	if got := strings.TrimSpace(second.Results[0].Stdout); got != "version-2" {
		t.Fatalf("second stdout = %q, want uncached version-2", got)
	}

	if second.Results[0].Cache == nil || second.Results[0].Cache.Enabled || second.Results[0].Cache.Bypassed {
		t.Fatalf("cache metadata = %+v, want disabled", second.Results[0].Cache)
	}

	refreshed, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "thrawn", Key: "version", Refresh: true})
	if err != nil {
		t.Fatalf("AgentInfo refresh: %v", err)
	}

	if got := strings.TrimSpace(refreshed.Results[0].Stdout); got != "version-3" {
		t.Fatalf("refresh stdout = %q, want uncached version-3", got)
	}

	if refreshed.Results[0].Cache == nil || refreshed.Results[0].Cache.Enabled || refreshed.Results[0].Cache.Bypassed {
		t.Fatalf("refresh cache metadata = %+v, want disabled without bypass", refreshed.Results[0].Cache)
	}
}

func TestAgentInfoCacheExpiryReturnsFreshFailureNotStale(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.AgentInfo.CacheTTL = "20ms"
	dir := t.TempDir()
	failPath := filepath.Join(dir, "fail")
	script := writeScript(t, `#!/bin/sh
if [ -f "$FAIL_PATH" ]; then
  echo "provider dreich" >&2
  exit 6
fi
echo "model-a"
`)

	sm.cfg.Agents["dreich"] = config.Agent{
		Command: script,
		Env:     map[string]string{"FAIL_PATH": failPath},
		Info: config.AgentInfoCommands{
			"model": config.AgentInfoCommand{Args: []string{"--models"}},
		},
	}

	first, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "dreich", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo first: %v", err)
	}

	if got := strings.TrimSpace(first.Results[0].Stdout); got != "model-a" {
		t.Fatalf("first stdout = %q, want model-a", got)
	}

	time.Sleep(40 * time.Millisecond)

	if err := os.WriteFile(failPath, []byte("fail"), 0o600); err != nil {
		t.Fatalf("write fail marker: %v", err)
	}

	second, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "dreich", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo second: %v", err)
	}

	result := second.Results[0]
	if result.ExitCode != 6 || result.Error == "" || !strings.Contains(result.Stderr, "provider dreich") {
		t.Fatalf("expired refresh result = %+v, want fresh provider failure", result)
	}

	if strings.Contains(result.Stdout, "model-a") {
		t.Fatalf("expired refresh returned stale stdout: %+v", result)
	}

	if result.Cache == nil || result.Cache.Hit || result.Cache.ExpiresAt != "" {
		t.Fatalf("failed refresh cache metadata = %+v, want uncached miss", result.Cache)
	}
}

func TestAgentInfoCacheInvalidatesOnGlobalSandboxChange(t *testing.T) {
	sm := newTestSessionManager(t)
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	script := writeScript(t, `#!/bin/sh
count=$(cat "$COUNT_PATH" 2>/dev/null || echo 0)
count=$((count + 1))
printf '%s\n' "$count" > "$COUNT_PATH"
printf 'model-%s\n' "$count"
`)

	sm.cfg.Sandbox.ReadDirs = []string{"/auld"}
	sm.cfg.Agents["bothy"] = config.Agent{
		Command: script,
		Env:     map[string]string{"COUNT_PATH": countPath},
		Info: config.AgentInfoCommands{
			"model": config.AgentInfoCommand{Args: []string{"--models"}},
		},
	}

	first, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "bothy", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo first: %v", err)
	}

	if got := strings.TrimSpace(first.Results[0].Stdout); got != "model-1" {
		t.Fatalf("first stdout = %q, want model-1", got)
	}

	second, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "bothy", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo second: %v", err)
	}

	if got := strings.TrimSpace(second.Results[0].Stdout); got != "model-1" {
		t.Fatalf("second stdout = %q, want cached model-1", got)
	}

	sm.cfg.Sandbox.ReadDirs = []string{"/braw"}

	third, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "bothy", Key: "model"})
	if err != nil {
		t.Fatalf("AgentInfo after sandbox change: %v", err)
	}

	if got := strings.TrimSpace(third.Results[0].Stdout); got != "model-2" {
		t.Fatalf("sandbox-change stdout = %q, want fresh model-2", got)
	}

	sm.agentInfoCacheMu.Lock()
	defer sm.agentInfoCacheMu.Unlock()

	entries := 0

	for key := range sm.agentInfoCache {
		if key.Agent == "bothy" && key.Key == "model" {
			entries++
		}
	}

	if entries != 1 {
		t.Fatalf("cache entries for bothy.model = %d, want stale signature evicted", entries)
	}
}

func TestAgentInfoCacheIsNotSharedAcrossManagers(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	script := writeScript(t, `#!/bin/sh
count=$(cat "$COUNT_PATH" 2>/dev/null || echo 0)
count=$((count + 1))
printf '%s\n' "$count" > "$COUNT_PATH"
printf 'version-%s\n' "$count"
`)

	cfg := config.Default()
	cfg.Agents["bothy"] = config.Agent{
		Command: script,
		Env:     map[string]string{"COUNT_PATH": countPath},
		Info: config.AgentInfoCommands{
			"version": config.AgentInfoCommand{Args: []string{"--version"}},
		},
	}

	first := newSMWithConfig(t, cfg)

	resp, err := first.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "bothy", Key: "version"})
	if err != nil {
		t.Fatalf("first AgentInfo: %v", err)
	}

	if got := strings.TrimSpace(resp.Results[0].Stdout); got != "version-1" {
		t.Fatalf("first stdout = %q, want version-1", got)
	}

	restarted := newSMWithConfig(t, cfg)

	resp, err = restarted.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "bothy", Key: "version"})
	if err != nil {
		t.Fatalf("restarted AgentInfo: %v", err)
	}

	if got := strings.TrimSpace(resp.Results[0].Stdout); got != "version-2" {
		t.Fatalf("restarted stdout = %q, want cache miss version-2", got)
	}
}

func TestAgentInfoConcurrentRequestsShareRefresh(t *testing.T) {
	sm := newTestSessionManager(t)
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	startedPath := filepath.Join(dir, "started")
	releasePath := filepath.Join(dir, "release")
	script := writeScript(t, `#!/bin/sh
count=$(cat "$COUNT_PATH" 2>/dev/null || echo 0)
count=$((count + 1))
printf '%s\n' "$count" > "$COUNT_PATH"
printf started > "$STARTED_PATH"
while [ ! -f "$RELEASE_PATH" ]; do
  sleep 0.02
done
printf 'model-%s\n' "$count"
`)

	sm.cfg.Agents["strath"] = config.Agent{
		Command: script,
		Env: map[string]string{
			"COUNT_PATH":   countPath,
			"STARTED_PATH": startedPath,
			"RELEASE_PATH": releasePath,
		},
		Info: config.AgentInfoCommands{
			"model": config.AgentInfoCommand{Args: []string{"--models"}},
		},
	}

	const callers = 8

	start := make(chan struct{})
	results := make(chan protocol.AgentInfoResponseMsg, callers)
	errs := make(chan error, callers)

	for i := 0; i < callers; i++ {
		go func() {
			<-start

			resp, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "strath", Key: "model"})
			if err != nil {
				errs <- err

				return
			}

			results <- resp
		}()
	}

	close(start)
	waitForAgentInfoMarker(t, startedPath, 5*time.Second)

	if err := os.WriteFile(releasePath, []byte("done"), 0o600); err != nil {
		t.Fatalf("write release marker: %v", err)
	}

	for i := 0; i < callers; i++ {
		select {
		case err := <-errs:
			t.Fatalf("AgentInfo concurrent: %v", err)
		case resp := <-results:
			if got := strings.TrimSpace(resp.Results[0].Stdout); got != "model-1" {
				t.Fatalf("concurrent stdout = %q, want shared model-1", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent AgentInfo")
		}
	}

	countBytes, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("read count: %v", err)
	}

	if got := strings.TrimSpace(string(countBytes)); got != "1" {
		t.Fatalf("provider executions = %s, want 1", got)
	}
}

func TestAgentInfoLeaderCancellationDoesNotCancelJoinedWaiter(t *testing.T) {
	sm := newTestSessionManager(t)
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	releasePath := filepath.Join(dir, "release")
	script := writeScript(t, `#!/bin/sh
printf started > "$STARTED_PATH"
while [ ! -f "$RELEASE_PATH" ]; do
  sleep 0.02
done
printf 'model-a\n'
`)

	sm.cfg.Agents["canny"] = config.Agent{
		Command: script,
		Env: map[string]string{
			"STARTED_PATH": startedPath,
			"RELEASE_PATH": releasePath,
		},
		Info: config.AgentInfoCommands{
			"model": config.AgentInfoCommand{Args: []string{"--models"}},
		},
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan protocol.AgentInfoResponseMsg, 1)
	leaderErr := make(chan error, 1)

	go func() {
		resp, err := sm.AgentInfo(leaderCtx, protocol.AgentInfoMsg{Agent: "canny", Key: "model"})
		if err != nil {
			leaderErr <- err

			return
		}

		leaderDone <- resp
	}()

	waitForAgentInfoMarker(t, startedPath, 5*time.Second)

	waiterDone := make(chan protocol.AgentInfoResponseMsg, 1)
	waiterErr := make(chan error, 1)

	go func() {
		resp, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "canny", Key: "model"})
		if err != nil {
			waiterErr <- err

			return
		}

		waiterDone <- resp
	}()

	cancelLeader()
	time.Sleep(40 * time.Millisecond)

	if err := os.WriteFile(releasePath, []byte("done"), 0o600); err != nil {
		t.Fatalf("write release marker: %v", err)
	}

	for name, done := range map[string]chan protocol.AgentInfoResponseMsg{
		"leader": leaderDone,
		"waiter": waiterDone,
	} {
		select {
		case err := <-leaderErr:
			t.Fatalf("leader AgentInfo: %v", err)
		case err := <-waiterErr:
			t.Fatalf("waiter AgentInfo: %v", err)
		case resp := <-done:
			if got := strings.TrimSpace(resp.Results[0].Stdout); got != "model-a" {
				t.Fatalf("%s stdout = %q, want model-a", name, got)
			}

			if resp.Results[0].Error != "" {
				t.Fatalf("%s result error = %q, want empty", name, resp.Results[0].Error)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s AgentInfo", name)
		}
	}
}

func TestAgentInfoNormalizesLineOutput(t *testing.T) {
	sm := newTestSessionManager(t)
	script := writeScript(t, "#!/bin/sh\nprintf 'braw\\ncanny\\n'\n")
	sm.cfg.Agents["blether"] = config.Agent{
		Command: script,
		Info: config.AgentInfoCommands{
			"status": config.AgentInfoCommand{Args: []string{"--status"}, Format: config.AgentInfoFormatLines},
		},
	}

	resp, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "blether", Key: "status"})
	if err != nil {
		t.Fatalf("AgentInfo: %v", err)
	}

	if got := resp.Results[0].Lines; !reflect.DeepEqual(got, []string{"braw", "canny"}) {
		t.Fatalf("lines = %v, want [braw canny]", got)
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
		sm.cfg.Agents["braw"] = config.Agent{Command: "braw", Info: config.AgentInfoCommands{"model": config.AgentInfoCommand{Args: []string{"--models"}}}}
		_, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "braw", Key: "version"})
		assertErrContains(t, err, `unknown info key "version"`)
	})

	t.Run("refresh and no-cache conflict", func(t *testing.T) {
		sm := newTestSessionManager(t)
		_, err := sm.AgentInfo(context.Background(), protocol.AgentInfoMsg{Agent: "braw", Refresh: true, NoCache: true})
		assertErrContains(t, err, `mutually exclusive`)
	})

	t.Run("failed command", func(t *testing.T) {
		sm := newTestSessionManager(t)
		script := writeScript(t, "#!/bin/sh\necho 'dreich failure' >&2\nexit 3\n")
		sm.cfg.Agents["bide"] = config.Agent{Command: script, Info: config.AgentInfoCommands{"model": config.AgentInfoCommand{Args: []string{"--models"}}}}

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

	result, err := sm.runAgentInfoCommand(context.Background(), sm.cfg, "bairn", agent, "model", config.AgentInfoCommand{Args: []string{"--models"}}, 10*time.Millisecond)
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

	result, err := sm.runAgentInfoCommand(context.Background(), sm.cfg, "bothy", agent, "model", config.AgentInfoCommand{Args: []string{"--models"}}, 500*time.Millisecond)
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

	result, err := sm.runAgentInfoCommand(context.Background(), sm.cfg, "bothy", agent, "model", config.AgentInfoCommand{Args: []string{"--models"}}, 5*time.Second)
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
		config.AgentInfoCommand{Args: []string{"--version"}},
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

func TestAgentInfoCleanupFailureWarnsWithoutFailingSuccessfulCommand(t *testing.T) {
	sm := newTestSessionManager(t)
	script := writeScript(t, "#!/bin/sh\necho 'version braw'\n")
	agent := config.Agent{Command: script}

	oldCleanup := cleanupAgentInfoProcessGroupFunc
	cleanupAgentInfoProcessGroupFunc = func(int) error {
		return errors.New("SIGTERM to pgid -84899: operation not permitted")
	}

	t.Cleanup(func() { cleanupAgentInfoProcessGroupFunc = oldCleanup })

	result, err := sm.runAgentInfoCommand(context.Background(), sm.cfg, "canny", agent, "version", config.AgentInfoCommand{Args: []string{"--version"}}, time.Second)
	if err != nil {
		t.Fatalf("runAgentInfoCommand: %v", err)
	}

	if result.Error != "" {
		t.Fatalf("result error = %q, want successful command despite cleanup failure", result.Error)
	}

	if got := strings.TrimSpace(result.Stdout); got != "version braw" {
		t.Fatalf("stdout = %q, want provider output preserved", got)
	}

	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "operation not permitted") {
		t.Fatalf("warnings = %v, want cleanup warning", result.Warnings)
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
		Info: config.AgentInfoCommands{
			"version": config.AgentInfoCommand{Args: []string{"version"}},
			"model":   config.AgentInfoCommand{Args: []string{"model"}},
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
		Info: config.AgentInfoCommands{
			"version": config.AgentInfoCommand{Args: []string{"--version"}},
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
	cfg.Agents["blether"] = config.Agent{Command: script, Info: config.AgentInfoCommands{"model": config.AgentInfoCommand{Args: []string{"--models"}}}}

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
