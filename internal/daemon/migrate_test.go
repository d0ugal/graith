package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/scenariofile"
)

// shAgent is a fake agent whose command stays alive and ignores extra
// positional args (the seed prompt), so migration's start step succeeds in
// tests without a real agent binary.
func shAgent() config.Agent {
	return config.Agent{NonInteractiveArgs: []string{}, Command: "/bin/sh", Args: []string{"-c", "sleep 30"}}
}

func newMigrateTestManager(t *testing.T) *SessionManager {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Sandbox.Enabled = false
	// Keep migration rollback tests fast while still giving /bin/sh enough time
	// to prove that a target which should stay alive did start successfully.
	cfg.Migration.HealthWindow = "300ms"
	cfg.Agents["claude"] = shAgent()
	cfg.Agents["codex"] = shAgent()

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
		TmpDir:    filepath.Join(tmpDir, "tmp"),
	}, slog.Default())
	sm.sandboxResolver = func(string) (bool, error) { return false, nil }

	return sm
}

func TestMigrateRejectsBadTargets(t *testing.T) {
	sm := newMigrateTestManager(t)
	sm.state.Sessions["s1"] = &SessionState{
		ID: "s1", Name: "braw-bothy", Agent: "claude",
		AgentSessionID: "sid-1", Status: StatusStopped,
		WorktreePath: t.TempDir(), RepoPath: t.TempDir(),
	}

	if _, err := sm.Migrate("s1", "claude", "", 24, 80); err == nil {
		t.Error("expected error migrating to the same agent")
	}

	if _, err := sm.Migrate("s1", "bogus-agent", "", 24, 80); err == nil {
		t.Error("expected error for unknown target agent")
	}

	if _, err := sm.Migrate("missing", "codex", "", 24, 80); err == nil {
		t.Error("expected error for missing session")
	}

	// Unsupported source agent (no transcript reader).
	sm.state.Sessions["s2"] = &SessionState{
		ID: "s2", Name: "thrawn", Agent: "cursor",
		Status: StatusStopped, WorktreePath: t.TempDir(), RepoPath: t.TempDir(),
	}
	if _, err := sm.Migrate("s2", "claude", "", 24, 80); err == nil {
		t.Error("expected error migrating from unsupported source agent")
	}
}

func TestMigrateInPlaceSwap(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	sm := newMigrateTestManager(t)
	recordPath := filepath.Join(t.TempDir(), "argv.txt")
	cfg := sm.Config()
	codex := cfg.Agents["codex"]
	codex.DefaultModel = "gpt-5.6-terra"
	codex.Args = []string{"-c", `printf '%s\n' "$@" > "$GRAITH_ARGS_RECORD"; exec sleep 30`, "braw"}
	codex.Env = map[string]string{"GRAITH_ARGS_RECORD": recordPath}
	codex.OptionArgs = []config.AgentOptionArg{{When: "model", Args: []string{"--model", "{model}"}}}
	cfg.Agents["codex"] = codex
	repo := initTempGitRepo(t)

	// Bound the async codex id-capture scan to an empty temp dir so it never
	// touches the real ~/.codex and finds nothing (no state writes).
	t.Setenv("CODEX_HOME", t.TempDir())

	// Stage a Claude transcript at the globbed location.
	claudeRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)

	sid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	projDir := filepath.Join(claudeRoot, "projects", "-some-proj")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}

	lines := `{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"fix the bothy"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"done, braw"}]}}
`
	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["m1"] = &SessionState{
		ID: "m1", Name: "braw-bothy", Agent: "claude",
		AgentSessionID: sid, Status: StatusStopped,
		WorktreePath: repo, RepoPath: repo, CreatedAt: time.Now(),
		Labels: []string{"Urgent", "release"},
	}

	res, err := sm.Migrate("m1", "codex", "", 24, 80)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Drain the spawned process and let watchSession persist the stop before
	// the test's temp dirs are torn down (avoids a cleanup write race).
	defer func() {
		if p, ok := sm.GetPTY("m1"); ok {
			_ = p.Kill()
			<-p.Done()
		}

		time.Sleep(250 * time.Millisecond)
	}()

	if res.Agent != "codex" {
		t.Errorf("result agent = %q, want codex", res.Agent)
	}

	if res.Model != "gpt-5.6-terra" {
		t.Errorf("result model = %q, want target default model", res.Model)
	}

	assertModelArgOccursOnce(t, waitForRecordedArgv(t, recordPath, "--model"), "gpt-5.6-terra")

	if !reflect.DeepEqual(res.Labels, []string{"Urgent", "release"}) {
		t.Fatalf("result labels = %#v, want labels preserved", res.Labels)
	}

	sm.mu.RLock()
	s := sm.state.Sessions["m1"]
	sm.mu.RUnlock()

	if s.Agent != "codex" {
		t.Errorf("session agent = %q, want codex", s.Agent)
	}

	if s.AgentSessionID != "" {
		t.Errorf("codex agent session id = %q, want empty (captured async)", s.AgentSessionID)
	}

	if s.WorktreePath != repo {
		t.Errorf("worktree changed: %q, want %q (in-place migrate retains worktree)", s.WorktreePath, repo)
	}

	if !reflect.DeepEqual(s.Labels, []string{"Urgent", "release"}) {
		t.Fatalf("session labels after migrate = %#v, want labels preserved", s.Labels)
	}

	if s.MigratedFrom == nil || s.MigratedFrom.Agent != "claude" {
		t.Fatalf("MigratedFrom = %+v, want agent claude", s.MigratedFrom)
	}

	if s.MigratedFrom.AgentSessionID != sid {
		t.Errorf("MigratedFrom session id = %q, want %q", s.MigratedFrom.AgentSessionID, sid)
	}

	if _, err := os.Stat(s.MigratedFrom.RenderedPath); err != nil {
		t.Errorf("rendered context file missing: %v", err)
	}

	if s.FreshStart {
		t.Error("FreshStart should be cleared after a successful seeded start")
	}
}

func TestMigrateLaunchGatePreventsScenarioRetryConsumingFreshStart(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	sm := newMigrateTestManager(t)
	repo := initTempGitRepo(t)

	t.Setenv("CODEX_HOME", t.TempDir())

	claudeRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)

	sid := "22222222-3333-4444-5555-666666666666"

	projDir := filepath.Join(claudeRoot, "projects", "-race-proj")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}

	lines := `{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"keep the canny context"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"context staged"}]}}
`
	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	deadline := now
	sm.state.Sessions["m3"] = &SessionState{
		ID: "m3", Name: "braw-worker", Agent: "claude",
		AgentSessionID: sid, Status: StatusStopped,
		WorktreePath: repo, RepoPath: repo, CreatedAt: started,
		ScenarioID: "sc-strath", LaunchGeneration: 1,
	}
	sm.state.Scenarios["sc-strath"] = &ScenarioState{
		ID: "sc-strath", Name: "strath", SessionIDs: []string{"m3"},
		Sessions: []ScenarioSession{{
			Name: "braw-worker",
			Policy: &ScenarioMemberPolicyState{
				Required: true, TimeoutNanos: int64(time.Minute), Retries: 1, Attempt: 1,
				AttemptStartedAt: &started, Deadline: &deadline,
			},
		}},
		Policy: &ScenarioPolicyState{
			Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedWait, Active: true,
		},
	}

	actions := sm.planScenarioPolicyActions(now)
	if len(actions) != 1 {
		t.Fatalf("actions = %v, want one retry", actions)
	}

	retryAttempted := false
	retryReachedLaunch := false
	sm.scenarioRestart = func(id string, _, _ uint16) error {
		retryReachedLaunch = true

		sm.mu.Lock()
		if s := sm.state.Sessions[id]; s != nil {
			s.FreshStart = false
			s.LaunchGeneration++
		}
		sm.mu.Unlock()

		return nil
	}
	sm.afterMigrationSwap = func() {
		retryAttempted = true

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		sm.executeScenarioRetry(ctx, actions[0], now)
	}

	res, err := sm.Migrate("m3", "codex", "", 24, 80)
	// Failure paths can still leave a restored PTY running, so cleanup must be
	// registered before checking the migration result.
	defer func() {
		if p, ok := sm.GetPTY("m3"); ok {
			_ = p.Kill()
			<-p.Done()
		}

		time.Sleep(250 * time.Millisecond)
	}()

	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if res.Agent != "codex" {
		t.Fatalf("result agent = %q, want codex", res.Agent)
	}

	if !retryAttempted {
		t.Fatal("migration test hook did not attempt the racing retry")
	}

	if retryReachedLaunch {
		t.Fatal("scenario retry reached launch while migration held the session gate")
	}

	sm.reconcileScenarioPolicies(context.Background(), now.Add(time.Second))

	sm.mu.RLock()
	s := sm.state.Sessions["m3"]
	drv := sm.sessions["m3"]
	policy := *sm.state.Scenarios["sc-strath"].Sessions[0].Policy
	sm.mu.RUnlock()

	if s.FreshStart {
		t.Fatal("FreshStart should be cleared by the seeded migration launch")
	}

	if s.LaunchGeneration != 2 {
		t.Fatalf("launch generation = %d, want exactly one target launch from generation 1", s.LaunchGeneration)
	}

	ptySess, ok := drv.(*grpty.Session)
	if !ok {
		t.Fatalf("session driver is %T, want *pty.Session", drv)
	}

	if got := countArgsContaining(ptySess.Cmd.Args, "migrated from claude"); got != 1 {
		t.Fatalf("migration seed args = %d, want exactly one; argv=%v", got, ptySess.Cmd.Args)
	}

	if policy.RetryPending || policy.RetryDispatched {
		t.Fatalf("stale retry claim was not cleared after migration generation advance: %+v", policy)
	}
}

func TestScenarioRetryAfterMigrationSwapRestartUsesPersistedSeed(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	sm := newMigrateTestManager(t)
	repo := initTempGitRepo(t)
	t.Setenv("CODEX_HOME", t.TempDir())

	contextDir, err := sm.repoTmpDir(repo)
	if err != nil {
		t.Fatal(err)
	}

	contextPath := filepath.Join(contextDir, "migrate-m4", "context.md")
	if err := os.MkdirAll(filepath.Dir(contextPath), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(contextPath, []byte("prior canny context"), 0o600); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 7, 29, 12, 30, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	deadline := now
	sm.state.Sessions["m4"] = &SessionState{
		ID: "m4", Name: "canny-worker", Agent: "codex",
		Status: StatusErrored, WorktreePath: repo, RepoPath: repo, CreatedAt: started,
		ScenarioID: "sc-croft", LaunchGeneration: 1, FreshStart: true,
		MigratedFrom: &MigrationInfo{
			Agent:          "claude",
			AgentSessionID: "33333333-4444-5555-6666-777777777777",
			RenderedPath:   contextPath,
			At:             started,
		},
	}
	sm.state.Scenarios["sc-croft"] = &ScenarioState{
		ID: "sc-croft", Name: "croft", SessionIDs: []string{"m4"},
		Sessions: []ScenarioSession{{
			Name: "canny-worker",
			Policy: &ScenarioMemberPolicyState{
				Required: true, TimeoutNanos: int64(time.Minute), Retries: 1, Attempt: 2,
				AttemptStartedAt: &started, Deadline: &deadline,
				RetryPending: true, RetryFromGeneration: 1,
			},
		}},
		Policy: &ScenarioPolicyState{
			Completion: scenariofile.CompletionAll, OnExhausted: scenariofile.OnExhaustedWait, Active: true,
		},
	}

	action := scenarioRetryAction{
		scenarioID: "sc-croft", sessionID: "m4", memberIndex: 0, attempt: 2, fromGeneration: 1,
	}

	sm.executeScenarioRetry(context.Background(), action, now)
	defer func() {
		if p, ok := sm.GetPTY("m4"); ok {
			_ = p.Kill()
			<-p.Done()
		}

		// Give watchSession time to persist the stop before temp dirs disappear.
		time.Sleep(250 * time.Millisecond)
	}()

	sm.mu.RLock()
	s := sm.state.Sessions["m4"]
	drv := sm.sessions["m4"]
	policy := *sm.state.Scenarios["sc-croft"].Sessions[0].Policy
	sm.mu.RUnlock()

	if s.FreshStart {
		t.Fatal("FreshStart should be cleared by the recovered migration launch")
	}

	if s.LaunchGeneration != 2 {
		t.Fatalf("launch generation = %d, want retry launch from generation 1", s.LaunchGeneration)
	}

	ptySess, ok := drv.(*grpty.Session)
	if !ok {
		t.Fatalf("session driver is %T, want *pty.Session", drv)
	}

	if got := countArgsContaining(ptySess.Cmd.Args, "migrated from claude"); got != 1 {
		t.Fatalf("recovered migration seed args = %d, want exactly one; argv=%v", got, ptySess.Cmd.Args)
	}

	if policy.RetryPending || policy.RetryDispatched || policy.ExhaustedAt != nil {
		t.Fatalf("retry policy after recovered launch = %+v, want completed retry claim", policy)
	}
}

// TestMigrateRestoresOnTargetFailure exercises the post-start health check: a
// target that exits immediately must trigger restore of the original agent.
func TestMigrateRestoresOnTargetFailure(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}

	sm := newMigrateTestManager(t)
	// Target codex exits immediately; source claude stays alive on restore.
	sm.cfg.Agents["codex"] = config.Agent{NonInteractiveArgs: []string{}, Command: "/bin/sh", Args: []string{"-c", "true"}}
	repo := initTempGitRepo(t)

	claudeRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)

	sid := "11111111-2222-3333-4444-555555555555"

	projDir := filepath.Join(claudeRoot, "projects", "-p")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(
		`{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"bide a wee"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["m2"] = &SessionState{
		ID: "m2", Name: "canny-croft", Agent: "claude",
		AgentSessionID: sid, Status: StatusStopped,
		WorktreePath: repo, RepoPath: repo, CreatedAt: time.Now(),
	}

	_, err := sm.Migrate("m2", "codex", "", 24, 80)
	defer func() {
		if p, ok := sm.GetPTY("m2"); ok {
			_ = p.Kill()
			<-p.Done()
		}

		time.Sleep(250 * time.Millisecond)
	}()

	if err == nil {
		t.Fatal("expected migrate to fail when target exits immediately")
	}

	sm.mu.RLock()
	s := sm.state.Sessions["m2"]
	sm.mu.RUnlock()

	if s.Agent != "claude" {
		t.Errorf("agent = %q after failed migrate, want claude (restored)", s.Agent)
	}

	if s.AgentSessionID != sid {
		t.Errorf("agent session id = %q, want restored %q", s.AgentSessionID, sid)
	}

	if s.MigratedFrom != nil {
		t.Errorf("MigratedFrom = %+v, want nil after successful restore", s.MigratedFrom)
	}

	if s.Status != StatusRunning {
		t.Errorf("status = %q, want running (original restored)", s.Status)
	}
	// The per-session migration context dir should be cleaned up after restore.
	tmpDir, derr := sm.repoTmpDir(repo)
	if derr != nil {
		t.Fatal(derr)
	}

	if _, statErr := os.Stat(filepath.Join(tmpDir, "migrate-m2")); statErr == nil {
		t.Error("migration context dir should be removed after successful restore")
	}
}

func countArgsContaining(args []string, sub string) int {
	count := 0

	for _, arg := range args {
		if strings.Contains(arg, sub) {
			count++
		}
	}

	return count
}
