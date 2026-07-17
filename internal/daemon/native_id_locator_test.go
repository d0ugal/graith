package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// TestWouldCrossAssignUsesPersistedLocator is the cross-generation regression
// (issue #1236): sibling detection compares each session's PERSISTED launch
// locator, not current config. An older running session launched under a prior
// generation (its locator persisted) must still be recognised as a same-store
// sibling even after a reload changes/removes that alias's config locator, so a
// newly started built-in never cross-assigns the older session's rollout.
func TestWouldCrossAssignUsesPersistedLocator(t *testing.T) {
	cases := []struct {
		name            string
		siblingLocator  string
		siblingStatus   SessionStatus
		wantCrossAssign bool
	}{
		{"same persisted locator, running", config.NativeIDLocatorCodex, StatusRunning, true},
		{"same persisted locator, creating", config.NativeIDLocatorCodex, StatusCreating, true},
		{"different persisted locator, running", config.NativeIDLocatorClaude, StatusRunning, false},
		{"empty persisted locator (pre-#1236), running → conservative", "", StatusRunning, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sm := newMigrateTestManager(t)
			cwd := t.TempDir()

			// The older-generation sibling: its locator is persisted on state. Even if
			// we now blank its agent's config locator, the persisted value governs.
			sm.state.Sessions["older"] = &SessionState{
				ID: "older", Name: "older", Agent: "mycodex-alias",
				NativeIDLocator: c.siblingLocator,
				Status:          c.siblingStatus, WorktreePath: cwd,
			}

			// Simulate a reload that removed the alias entirely from config.
			delete(sm.cfg.Agents, "mycodex-alias")

			sm.mu.Lock()
			got := sm.wouldCrossAssign("newcodex", config.NativeIDLocatorCodex, cwd, "scraped-sid", time.Now().Add(-time.Minute))
			sm.mu.Unlock()

			if got != c.wantCrossAssign {
				t.Errorf("wouldCrossAssign = %v, want %v", got, c.wantCrossAssign)
			}
		})
	}
}

// TestWouldCrossAssignGloballyUniqueID confirms a claimed AgentSessionID is never
// reused regardless of locator or worktree.
func TestWouldCrossAssignGloballyUniqueID(t *testing.T) {
	sm := newMigrateTestManager(t)

	sm.state.Sessions["owner"] = &SessionState{
		ID: "owner", Agent: "codex", AgentSessionID: "claimed-sid",
		NativeIDLocator: config.NativeIDLocatorClaude, // different locator + worktree
		Status:          StatusStopped, WorktreePath: t.TempDir(),
	}

	sm.mu.Lock()
	got := sm.wouldCrossAssign("newcodex", config.NativeIDLocatorCodex, t.TempDir(), "claimed-sid", time.Now())
	sm.mu.Unlock()

	if !got {
		t.Error("wouldCrossAssign should refuse an id already claimed by another session")
	}
}

// TestForcedAliasArgvLifecycle is the end-to-end custom-alias regression (issue
// #1236): a custom force=true alias with id-templated Args/ResumeArgs/ForkArgs
// must, without any built-in name match, receive a minted id at create, replay it
// on resume, and carry both the source and the new id on fork.
func TestForcedAliasArgvLifecycle(t *testing.T) {
	alias := config.Agent{
		Command:    "my-claude",
		Args:       []string{"--session-id", "{agent_session_id}"},
		ResumeArgs: []string{"--resume", "{agent_session_id}"},
		ForkArgs:   []string{"--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"},
		NativeID:   &config.AgentNativeIDConfig{Force: true, Locator: config.NativeIDLocatorClaude},
	}

	// The alias must be a legal config (force + id-passing args + locator).
	cfg := config.Default()
	cfg.Agents["myclaude"] = alias

	if err := cfg.Validate(); err != nil {
		t.Fatalf("custom forced alias rejected by Validate: %v", err)
	}

	if !alias.ForcesNativeID() {
		t.Fatal("alias should force its native id from config alone")
	}

	// create: graith mints an id and passes it via the alias's own Args.
	created := newAgentSessionID()

	createArgv, err := config.ExpandSlice(alias.Args, config.TemplateVars{AgentSessionID: created})
	if err != nil {
		t.Fatalf("expand create args: %v", err)
	}

	if !slices.Contains(createArgv, created) {
		t.Errorf("create argv %v does not carry the minted id %q", createArgv, created)
	}

	// resume: the same id is replayed via resume_args (not a fresh mint).
	resumeRaw, note := resolveResumeArgs(alias, "myclaude", created, false)
	if note != "" {
		t.Errorf("resume fell back unexpectedly: %q", note)
	}

	resumeArgv, err := config.ExpandSlice(resumeRaw, config.TemplateVars{AgentSessionID: created})
	if err != nil {
		t.Fatalf("expand resume args: %v", err)
	}

	if len(resumeArgv) != 2 || resumeArgv[0] != "--resume" || resumeArgv[1] != created {
		t.Errorf("resume argv = %v, want [--resume %s]", resumeArgv, created)
	}

	// fork: a new id is minted and both the source and new id reach the argv.
	forked := newAgentSessionID()
	if forked == created {
		t.Fatal("fork should mint a distinct id")
	}

	forkArgv, err := config.ExpandSlice(alias.ForkArgs, config.TemplateVars{
		AgentSessionID:           forked,
		ForkSourceAgentSessionID: created,
	})
	if err != nil {
		t.Fatalf("expand fork args: %v", err)
	}

	if !slices.Contains(forkArgv, created) || !slices.Contains(forkArgv, forked) {
		t.Errorf("fork argv %v must carry both source %q and new %q ids", forkArgv, created, forked)
	}
}

// TestForcedAliasCreateResumeForkArgvEndToEnd drives a real recorder-backed
// SessionManager through Create → Restart → Fork for a CUSTOM forced alias (no
// built-in name), asserting the minted id reaches the launch argv, is reused on
// resume, and that a fork carries both the source and a new id. Unlike a pure
// expansion test, this would fail if Create/Fork retained a built-in-name check,
// because "myclaude" only forces via its config (issue #1236).
func TestForcedAliasCreateResumeForkArgvEndToEnd(t *testing.T) {
	repoDir := initTempGitRepo(t)
	dir := t.TempDir()
	recordPath := filepath.Join(dir, "argv.txt")

	// $0/$@ are exactly the id-templated flags graith launches the alias with.
	script := `printf '%s\n' "$0" "$@" > "$GRAITH_ARGS_RECORD"; exec cat`

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Sandbox = config.SandboxConfig{Enabled: false}
	cfg.Agents["myclaude"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", script, "myclaude", "--session-id", "{agent_session_id}"},
		ResumeArgs: []string{"-c", script, "myclaude", "--resume", "{agent_session_id}"},
		ForkArgs:   []string{"-c", script, "myclaude", "--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"},
		Env:        map[string]string{"GRAITH_ARGS_RECORD": recordPath},
		NativeID:   &config.AgentNativeIDConfig{Force: true, Locator: config.NativeIDLocatorClaude},
	}
	cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		TmpDir:     filepath.Join(dir, "tmp"),
	}, slog.Default())

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "myclaude", RepoPath: repoDir, BaseBranch: "main",
		Prompt: "hae", Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	mintedID := created.AgentSessionID
	if mintedID == "" {
		t.Fatal("forced alias should mint a native id at create (config-driven force)")
	}

	if argv := waitForRecordedArgv(t, recordPath, "--session-id"); !slices.Contains(argv, mintedID) {
		t.Errorf("create argv %v does not carry the minted id %q", argv, mintedID)
	}

	// Make a conversation exist on disk so resume reuses the id (a forced agent
	// with no transcript would fresh-fallback to a new id). Sets CLAUDE_CONFIG_DIR.
	writeClaudeTranscript(t, mintedID, braeLine)

	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before resume: %v", err)
	}

	if err := sm.Stop(id); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	waitForStatus(t, sm, id, StatusStopped)

	if _, err := sm.Restart(id, 24, 80); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if argv := waitForRecordedArgv(t, recordPath, "--resume"); !slices.Contains(argv, mintedID) {
		t.Errorf("resume argv %v did not reuse the minted id %q", argv, mintedID)
	}

	// Fork: a new id is minted, the source id carries over.
	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before fork: %v", err)
	}

	forked, err := sm.Fork("bairn", id, 24, 80)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, forked.ID) })

	newID := forked.AgentSessionID
	if newID == "" || newID == mintedID {
		t.Fatalf("fork should mint a distinct id, got %q (source %q)", newID, mintedID)
	}

	argv := waitForRecordedArgv(t, recordPath, "--fork-session")
	if !slices.Contains(argv, mintedID) || !slices.Contains(argv, newID) {
		t.Errorf("fork argv %v must carry both source %q and new %q ids", argv, mintedID, newID)
	}
}

// TestCaptureNativeSessionIDCustomAliasLocator proves a custom alias (not named
// "codex") whose config declares the codex locator scrapes its self-minted id
// exactly like the built-in — the capture is locator-driven, not name-driven
// (issue #1236).
func TestCaptureNativeSessionIDCustomAliasLocator(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()
	writeCodexRollout(t, root, "alias-native-id", cwd, time.Now())

	sm.state.Sessions["bide"] = &SessionState{
		ID: "bide", Name: "alias", Agent: "mycodex-alias",
		NativeIDLocator: config.NativeIDLocatorCodex,
		Status:          StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("bide", "mycodex-alias", config.NativeIDLocatorCodex, cwd, root, time.Now().Add(-time.Minute), 0, 0)

	if got := sm.state.Sessions["bide"].AgentSessionID; got != "alias-native-id" {
		t.Fatalf("AgentSessionID = %q; want alias-native-id (scraped via codex locator)", got)
	}
}

// TestTranscriptKind covers the locator/agent fallback used to dispatch transcript
// operations for custom aliases (issue #1236).
func TestTranscriptKind(t *testing.T) {
	if got := transcriptKind(config.NativeIDLocatorCodex, "mycodex"); got != config.NativeIDLocatorCodex {
		t.Errorf("transcriptKind(codex, mycodex) = %q, want codex", got)
	}

	if got := transcriptKind("", "codex"); got != "codex" {
		t.Errorf("transcriptKind(\"\", codex) = %q, want codex (legacy fallback)", got)
	}
}
