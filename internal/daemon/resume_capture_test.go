package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/config"
)

func TestResolveResumeArgs(t *testing.T) {
	codex := config.Agent{Args: nil, ResumeArgs: []string{"resume", "{agent_session_id}"}}
	opencode := config.Agent{Args: nil, ResumeArgs: []string{"--session", "{agent_session_id}"}}
	claude := config.Agent{
		Args:       []string{"--session-id", "{agent_session_id}"},
		ResumeArgs: []string{"--resume", "{agent_session_id}"},
	}
	cursor := config.Agent{Args: nil, ResumeArgs: []string{"resume"}}

	tests := []struct {
		name       string
		agent      config.Agent
		sessAgent  string
		sessID     string
		freshStart bool
		want       []string
		wantNote   bool
	}{
		{"codex with id pins to id", codex, "codex", "braw-id", false, []string{"resume", "{agent_session_id}"}, false},
		{"codex empty id falls back to --last", codex, "codex", "", false, []string{"resume", "--last"}, true},
		{"opencode empty id starts fresh", opencode, "opencode", "", false, nil, true},
		{"opencode with id pins to id", opencode, "opencode", "ses-1", false, []string{"--session", "{agent_session_id}"}, false},
		{"claude with id uses resume_args", claude, "claude", "bide-id", false, []string{"--resume", "{agent_session_id}"}, false},
		{"freshStart uses agent.Args, no guard", codex, "codex", "", true, nil, false},
		{"cursor no token, no guard", cursor, "cursor", "", false, []string{"resume"}, false},
		{
			"fresh fallback drops id-templated args",
			config.Agent{Args: []string{"--session", "{agent_session_id}"}, ResumeArgs: []string{"--session", "{agent_session_id}"}},
			"whin", "", false, nil, true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, note := resolveResumeArgs(tc.agent, tc.sessAgent, tc.sessID, tc.freshStart)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("args = %#v; want %#v", got, tc.want)
			}

			if (note != "") != tc.wantNote {
				t.Errorf("note = %q; wantNote=%v", note, tc.wantNote)
			}
		})
	}
}

// writeCodexRollout writes a minimal Codex rollout file (session_meta with id +
// cwd) under root's sessions dir, stamped with mtime, and returns its path.
func writeCodexRollout(t *testing.T, root, id, cwd string, mtime time.Time) string {
	t.Helper()

	dir := filepath.Join(root, "sessions", "2026", "06", "25")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	path := filepath.Join(dir, "rollout-2026-06-25T00-00-00-"+id+".jsonl")

	line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n", id, cwd)
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	return path
}

func TestForcesAndScrapesPredicates(t *testing.T) {
	if !forcesID("claude") {
		t.Error("claude should be forced")
	}

	for _, a := range []string{"codex", "opencode", "agy", "cursor", "haar"} {
		if forcesID(a) {
			t.Errorf("%q should not be forced (no client-supplied-id flag verified)", a)
		}
	}

	if !scrapesID("codex") {
		t.Error("codex should be scrapeable")
	}

	for _, a := range []string{"claude", "opencode", "agy", "cursor"} {
		if scrapesID(a) {
			t.Errorf("%q has no on-disk locator yet; should not scrape", a)
		}
	}
}

func TestArgsNeedAgentID(t *testing.T) {
	if !argsNeedAgentID([]string{"--session", "{agent_session_id}"}) {
		t.Error("expected token to be detected")
	}

	if !argsNeedAgentID([]string{"resume", "{agent_session_id}"}) {
		t.Error("expected token to be detected (codex form)")
	}

	if argsNeedAgentID([]string{"resume", "--last"}) {
		t.Error("no token present")
	}

	if argsNeedAgentID(nil) {
		t.Error("empty args need no id")
	}
}

func TestLocateCodexSinceInRootAndSince(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	now := time.Now()

	// A fresh rollout in the right cwd, after `since`.
	writeCodexRollout(t, root, "bide-id", cwd, now)

	// Explicit root is honored (not the daemon's CODEX_HOME).
	if path, ok := transcript.LocateCodexSinceIn(root, cwd, now.Add(-time.Minute)); !ok {
		t.Fatal("expected to locate the rollout under the explicit root")
	} else if id, ok := transcript.CodexRolloutID(path); !ok || id != "bide-id" {
		t.Fatalf("CodexRolloutID = %q, %v; want bide-id", id, ok)
	}

	// A `since` after the file's mtime must exclude it (stale-filter).
	if _, ok := transcript.LocateCodexSinceIn(root, cwd, now.Add(time.Hour)); ok {
		t.Error("rollout older than `since` should be excluded")
	}

	// A different cwd must not match.
	if _, ok := transcript.LocateCodexSinceIn(root, t.TempDir(), now.Add(-time.Minute)); ok {
		t.Error("rollout for a different cwd should not match")
	}
}

func TestCaptureNativeSessionIDCodex(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()
	since := time.Now().Add(-time.Minute)
	writeCodexRollout(t, root, "braw-native-id", cwd, time.Now())

	sm.state.Sessions["bide"] = &SessionState{
		ID: "bide", Name: "braw-bothy", Agent: "codex",
		AgentSessionID: "", PID: 4242, PIDStartTime: 111,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("bide", "codex", cwd, root, since, 4242, 111)

	if got := sm.state.Sessions["bide"].AgentSessionID; got != "braw-native-id" {
		t.Fatalf("AgentSessionID = %q; want braw-native-id", got)
	}
}

func TestCaptureNativeSessionIDStartTimeMismatch(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()
	writeCodexRollout(t, root, "skelf-id", cwd, time.Now())

	// Same PID but a different start time = a recycled PID from a later start.
	sm.state.Sessions["skelf"] = &SessionState{
		ID: "skelf", Name: "skelf-bothy", Agent: "codex",
		AgentSessionID: "", PID: 50, PIDStartTime: 999,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("skelf", "codex", cwd, root, time.Now().Add(-time.Minute), 50, 111)

	if got := sm.state.Sessions["skelf"].AgentSessionID; got != "" {
		t.Fatalf("AgentSessionID = %q; want empty (start-time mismatch must not write)", got)
	}
}

func TestCodexSessionIDSinceAmbiguous(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	since := time.Now().Add(-time.Minute)

	// Single rollout → resolves.
	writeCodexRollout(t, root, "lone-id", cwd, time.Now())

	if id, ok := transcript.CodexSessionIDSince(root, cwd, since); !ok || id != "lone-id" {
		t.Fatalf("CodexSessionIDSince = %q,%v; want lone-id,true", id, ok)
	}

	// A second rollout with a DIFFERENT id in the same cwd → ambiguous, refuse.
	writeCodexRollout(t, root, "thrawn-id", cwd, time.Now())

	if id, ok := transcript.CodexSessionIDSince(root, cwd, since); ok {
		t.Fatalf("CodexSessionIDSince = %q,%v; want refusal on ambiguous same-cwd match", id, ok)
	}
}

func TestCaptureNativeSessionIDSkipsSharedCwdSibling(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()

	// Session A (the sibling) started first and its rollout is already on disk.
	writeCodexRollout(t, root, "ben-native-id", cwd, time.Now())
	sm.state.Sessions["ben"] = &SessionState{
		ID: "ben", Name: "ben-bothy", Agent: "codex",
		AgentSessionID: "ben-native-id", PID: 100, PIDStartTime: 100,
		Status: StatusRunning, WorktreePath: cwd,
	}

	// Session B shares the same cwd and has not written its own rollout yet.
	sm.state.Sessions["bairn"] = &SessionState{
		ID: "bairn", Name: "bairn-bothy", Agent: "codex",
		AgentSessionID: "", PID: 200, PIDStartTime: 200,
		Status: StatusRunning, WorktreePath: cwd,
	}

	// B's capture runs while only A's rollout exists — it must not pin A's id.
	sm.captureNativeSessionID("bairn", "codex", cwd, root, time.Now().Add(-time.Minute), 200, 200)

	if got := sm.state.Sessions["bairn"].AgentSessionID; got != "" {
		t.Fatalf("AgentSessionID = %q; want empty (must not cross-assign sibling's id)", got)
	}
}

func TestCaptureNativeSessionIDCapturesWhenSiblingStopped(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()

	// A stopped sibling in the same cwd does not block capture — only a live
	// same-agent session in the cwd makes attribution ambiguous.
	sm.state.Sessions["auld"] = &SessionState{
		ID: "auld", Name: "auld-bothy", Agent: "codex",
		AgentSessionID: "auld-id", PID: 0,
		Status: StatusStopped, WorktreePath: cwd,
	}

	writeCodexRollout(t, root, "bonnie-native-id", cwd, time.Now())
	sm.state.Sessions["bonnie"] = &SessionState{
		ID: "bonnie", Name: "bonnie-bothy", Agent: "codex",
		AgentSessionID: "", PID: 300, PIDStartTime: 300,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("bonnie", "codex", cwd, root, time.Now().Add(-time.Minute), 300, 300)

	if got := sm.state.Sessions["bonnie"].AgentSessionID; got != "bonnie-native-id" {
		t.Fatalf("AgentSessionID = %q; want bonnie-native-id (stopped sibling must not block)", got)
	}
}

func TestCaptureNativeSessionIDSkipsSiblingStoppedAfterStart(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()
	since := time.Now().Add(-time.Minute)

	// Sibling A wrote its rollout, then exited *after* B's capture started
	// (StatusChangedAt after `since`) — a short-lived Codex start racing B. Its
	// rollout is the only one on disk and it has no recorded id yet, so only the
	// active-during-window check can stop the cross-assignment.
	writeCodexRollout(t, root, "haar-native-id", cwd, time.Now())
	sm.state.Sessions["haar"] = &SessionState{
		ID: "haar", Name: "haar-bothy", Agent: "codex",
		AgentSessionID: "", PID: 0,
		Status: StatusStopped, StatusChangedAt: time.Now(), WorktreePath: cwd,
	}

	sm.state.Sessions["fash"] = &SessionState{
		ID: "fash", Name: "fash-bothy", Agent: "codex",
		AgentSessionID: "", PID: 400, PIDStartTime: 400,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("fash", "codex", cwd, root, since, 400, 400)

	if got := sm.state.Sessions["fash"].AgentSessionID; got != "" {
		t.Fatalf("AgentSessionID = %q; want empty (sibling stopped mid-capture must not cross-assign)", got)
	}
}

func TestCaptureNativeSessionIDSkipsIDAlreadyClaimed(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()

	// The scraped id is already recorded by another session (any cwd/status) —
	// a native id backs exactly one conversation, so it must never be reused.
	writeCodexRollout(t, root, "thrawn-id", cwd, time.Now())
	sm.state.Sessions["ben"] = &SessionState{
		ID: "ben", Name: "ben-bothy", Agent: "codex",
		AgentSessionID: "thrawn-id", PID: 0,
		Status: StatusStopped, WorktreePath: t.TempDir(),
	}

	sm.state.Sessions["scunner"] = &SessionState{
		ID: "scunner", Name: "scunner-bothy", Agent: "codex",
		AgentSessionID: "", PID: 500, PIDStartTime: 500,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("scunner", "codex", cwd, root, time.Now().Add(-time.Minute), 500, 500)

	if got := sm.state.Sessions["scunner"].AgentSessionID; got != "" {
		t.Fatalf("AgentSessionID = %q; want empty (id already claimed must not be reused)", got)
	}
}

// claudeAgentConfig is the default Claude agent shape: a fresh start pins a
// client-supplied id (--session-id), a resume replays it (--resume).
func claudeAgentConfig() config.Agent {
	return config.Agent{
		Args:       []string{"--session-id", "{agent_session_id}"},
		ResumeArgs: []string{"--resume", "{agent_session_id}"},
	}
}

// braeLine is a minimal Claude user record used to stand up a transcript file.
const braeLine = `{"type":"user","message":{"role":"user","content":"hae"}}`

func TestForcedIDConversationExists(t *testing.T) {
	// No transcript on disk → no conversation. Point the locator at an empty dir
	// so the glob can't stray into the real ~/.claude.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	if forcedIDConversationExists("claude", "dreich-id", t.TempDir()) {
		t.Error("no transcript on disk should report no conversation")
	}

	// Transcript present → conversation exists.
	writeClaudeTranscript(t, "bide-id", braeLine)

	if !forcedIDConversationExists("claude", "bide-id", t.TempDir()) {
		t.Error("on-disk transcript should report a conversation")
	}
}

// TestResolveForcedIDFreshStart covers the #1091 fix: resuming a Claude session
// whose captured id never persisted a conversation must fall back to a fresh
// start instead of a doomed `claude --resume <id>`, and the following resume
// (once the conversation exists) must use native --resume again.
func TestResolveForcedIDFreshStart(t *testing.T) {
	const minted = "canny-fresh-id"

	mint := func() string { return minted }
	claude := claudeAgentConfig()

	freshArgs := []string{"--session-id", "{agent_session_id}"}
	resumeArgs := []string{"--resume", "{agent_session_id}"}

	t.Run("existing transcript resumes native", func(t *testing.T) {
		writeClaudeTranscript(t, "bide-id", braeLine)

		id, fresh, fellBack := resolveForcedIDFreshStart("claude", "bide-id", t.TempDir(), false, mint)
		if id != "bide-id" || fresh || fellBack {
			t.Fatalf("got (%q,%v,%v); want (bide-id,false,false)", id, fresh, fellBack)
		}

		if got, _ := resolveResumeArgs(claude, "claude", id, fresh); !reflect.DeepEqual(got, resumeArgs) {
			t.Fatalf("args = %#v; want %#v (--resume)", got, resumeArgs)
		}
	})

	t.Run("missing transcript mints fresh id", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // empty → no conversation

		id, fresh, fellBack := resolveForcedIDFreshStart("claude", "dreich-id", t.TempDir(), false, mint)
		if id != minted || !fresh || !fellBack {
			t.Fatalf("got (%q,%v,%v); want (%q,true,true)", id, fresh, fellBack, minted)
		}

		if got, _ := resolveResumeArgs(claude, "claude", id, fresh); !reflect.DeepEqual(got, freshArgs) {
			t.Fatalf("args = %#v; want %#v (fresh --session-id)", got, freshArgs)
		}
	})

	t.Run("after fallback next resume uses native --resume", func(t *testing.T) {
		// The freshly-minted conversation now has a transcript and FreshStart was
		// cleared on the successful start, so the next resume replays it natively.
		writeClaudeTranscript(t, minted, braeLine)

		id, fresh, fellBack := resolveForcedIDFreshStart("claude", minted, t.TempDir(), false, mint)
		if id != minted || fresh || fellBack {
			t.Fatalf("got (%q,%v,%v); want (%q,false,false)", id, fresh, fellBack, minted)
		}

		if got, _ := resolveResumeArgs(claude, "claude", id, fresh); !reflect.DeepEqual(got, resumeArgs) {
			t.Fatalf("args = %#v; want %#v (--resume)", got, resumeArgs)
		}
	})

	t.Run("freshStart already set is left alone", func(t *testing.T) {
		id, fresh, fellBack := resolveForcedIDFreshStart("claude", "bide-id", t.TempDir(), true, mint)
		if id != "bide-id" || !fresh || fellBack {
			t.Fatalf("got (%q,%v,%v); want (bide-id,true,false)", id, fresh, fellBack)
		}
	})

	t.Run("non-forced agent skips the check", func(t *testing.T) {
		id, fresh, fellBack := resolveForcedIDFreshStart("codex", "braw-id", t.TempDir(), false, mint)
		if id != "braw-id" || fresh || fellBack {
			t.Fatalf("got (%q,%v,%v); want (braw-id,false,false)", id, fresh, fellBack)
		}
	})

	t.Run("empty id skips the check", func(t *testing.T) {
		id, fresh, fellBack := resolveForcedIDFreshStart("claude", "", t.TempDir(), false, mint)
		if id != "" || fresh || fellBack {
			t.Fatalf("got (%q,%v,%v); want (\"\",false,false)", id, fresh, fellBack)
		}
	})
}

func TestArgsNeedForkSourceID(t *testing.T) {
	if !argsNeedForkSourceID([]string{"fork", "{fork_source_agent_session_id}"}) {
		t.Error("expected fork-source token to be detected")
	}

	if argsNeedForkSourceID([]string{"fork", "{agent_session_id}"}) {
		t.Error("agent_session_id is not the fork-source token")
	}

	if argsNeedForkSourceID(nil) {
		t.Error("empty args need no fork-source id")
	}
}

func TestCaptureNativeSessionIDNoOverwrite(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()
	writeCodexRollout(t, root, "scraped-id", cwd, time.Now())

	// Session already has a good id — capture must never clobber it.
	sm.state.Sessions["bonnie"] = &SessionState{
		ID: "bonnie", Name: "bonnie-bothy", Agent: "codex",
		AgentSessionID: "kept-id", PID: 7,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("bonnie", "codex", cwd, root, time.Now().Add(-time.Minute), 7, 0)

	if got := sm.state.Sessions["bonnie"].AgentSessionID; got != "kept-id" {
		t.Fatalf("AgentSessionID = %q; want kept-id (must not overwrite)", got)
	}
}

func TestCaptureNativeSessionIDGenerationMismatch(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()
	writeCodexRollout(t, root, "auld-gen-id", cwd, time.Now())

	// A later start replaced the process generation: stored PID != expectedPID.
	sm.state.Sessions["auld"] = &SessionState{
		ID: "auld", Name: "auld-bothy", Agent: "codex",
		AgentSessionID: "", PID: 999,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("auld", "codex", cwd, root, time.Now().Add(-time.Minute), 111, 0)

	if got := sm.state.Sessions["auld"].AgentSessionID; got != "" {
		t.Fatalf("AgentSessionID = %q; want empty (stale generation must not write)", got)
	}
}

func TestCaptureNativeSessionIDNonScrapeAgent(t *testing.T) {
	sm := newMigrateTestManager(t)
	root := t.TempDir()
	cwd := t.TempDir()
	writeCodexRollout(t, root, "neep-id", cwd, time.Now())

	// claude is forced, never scraped: capture is a no-op even if a file exists.
	sm.state.Sessions["neep"] = &SessionState{
		ID: "neep", Name: "neep-bothy", Agent: "claude",
		AgentSessionID: "", PID: 1,
		Status: StatusRunning, WorktreePath: cwd,
	}

	sm.captureNativeSessionID("neep", "claude", cwd, root, time.Now().Add(-time.Minute), 1, 0)

	if got := sm.state.Sessions["neep"].AgentSessionID; got != "" {
		t.Fatalf("AgentSessionID = %q; want empty (claude is not scraped)", got)
	}
}
