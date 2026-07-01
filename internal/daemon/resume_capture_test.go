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
