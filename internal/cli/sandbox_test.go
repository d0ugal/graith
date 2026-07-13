package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
)

func TestExpandWhyPathsDropsMissing(t *testing.T) {
	dir := t.TempDir()

	bothy := filepath.Join(dir, "bothy")
	if err := os.Mkdir(bothy, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	missing := filepath.Join(dir, "haar")

	got := expandWhyPaths([]string{bothy, missing})
	if len(got) != 1 || got[0] != bothy {
		t.Fatalf("expandWhyPaths() = %v, want [%s]", got, bothy)
	}
}

func TestExpandWhyPathsGlob(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"croft-a", "croft-b"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	got := expandWhyPaths([]string{filepath.Join(dir, "croft-*")})
	if len(got) != 2 {
		t.Fatalf("expandWhyPaths() glob = %v, want 2 matches", got)
	}
}

func TestExpandWhyFilePathsKeepsMissing(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(present, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// A writable file grant is routinely for a file the agent creates at
	// runtime (e.g. a lockfile), so a missing literal path must be kept — not
	// stat-dropped like a directory grant.
	missing := filepath.Join(dir, "claude.json.lock")

	got := expandWhyFilePaths([]string{present, missing})
	if len(got) != 2 || got[0] != present || got[1] != missing {
		t.Fatalf("expandWhyFilePaths() = %v, want [%s %s]", got, present, missing)
	}
}

func TestExpandWhyFilePathsGlobSkipsNoMatch(t *testing.T) {
	dir := t.TempDir()

	got := expandWhyFilePaths([]string{filepath.Join(dir, "haar-*.lock")})
	if len(got) != 0 {
		t.Fatalf("expandWhyFilePaths() glob no-match = %v, want empty", got)
	}
}

func TestWhyWrapOptsIncludesFileGrants(t *testing.T) {
	dir := t.TempDir()

	writeFile := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(writeFile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	readFile := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(readFile, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	opts := whyWrapOpts(config.SandboxConfig{
		Backend:    sandbox.BackendNono,
		ReadFiles:  []string{readFile},
		WriteFiles: []string{writeFile},
	})

	if !containsStr(opts.ReadFiles, readFile) {
		t.Fatalf("ReadFiles = %v, want %s", opts.ReadFiles, readFile)
	}

	if !containsStr(opts.WriteFiles, writeFile) {
		t.Fatalf("WriteFiles = %v, want %s", opts.WriteFiles, writeFile)
	}
}

func TestWhyWrapOptsIncludesBaseEnvKeys(t *testing.T) {
	opts := whyWrapOpts(config.SandboxConfig{Backend: sandbox.BackendNono})

	if !containsStr(opts.EnvKeys, "PATH") || !containsStr(opts.EnvKeys, "HOME") {
		t.Fatalf("EnvKeys = %v, want PATH and HOME", opts.EnvKeys)
	}

	if opts.Backend != sandbox.BackendNono {
		t.Fatalf("Backend = %q, want nono", opts.Backend)
	}
}

// saveExplainFlags snapshots the explain-command flag vars and restores them
// after the test, so mutating package globals doesn't leak between tests.
func saveExplainFlags(t *testing.T) {
	t.Helper()

	oldPath, oldOp, oldHost, oldPort, oldAgent := explainPath, explainOp, explainHost, explainPort, explainAgent
	oldCfg := cfg

	t.Cleanup(func() {
		explainPath, explainOp, explainHost, explainPort, explainAgent = oldPath, oldOp, oldHost, oldPort, oldAgent
		cfg = oldCfg
	})
}

func TestRunSandboxExplainRejectsNonNonoBackend(t *testing.T) {
	saveExplainFlags(t)

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendSafehouse}
	explainPath, explainOp, explainHost, explainPort, explainAgent = "/glen/bothy", "read", "", 0, ""

	err := runSandboxExplain()
	// The gate should name the oracle-less backend and point at `watch`.
	if err == nil || !strings.Contains(err.Error(), "oracle") || !strings.Contains(err.Error(), "watch") {
		t.Fatalf("runSandboxExplain() = %v, want backend-gate error pointing at watch", err)
	}

	if !strings.Contains(err.Error(), sandbox.BackendSafehouse) {
		t.Fatalf("runSandboxExplain() = %v, want it to name the safehouse backend", err)
	}
}

func TestRunSandboxExplainValidatesQuery(t *testing.T) {
	saveExplainFlags(t)

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendNono}
	// Invalid: path without op.
	explainPath, explainOp, explainHost, explainPort, explainAgent = "/glen/wynd", "", "", 0, ""

	err := runSandboxExplain()
	if err == nil || !strings.Contains(err.Error(), "--op is required") {
		t.Fatalf("runSandboxExplain() = %v, want validation error", err)
	}
}

func TestRunSandboxExplainRejectsUnknownAgent(t *testing.T) {
	saveExplainFlags(t)

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendNono}
	// "thrawn" is not a configured agent — a typo should error, not silently
	// fall back to the global policy.
	explainPath, explainOp, explainHost, explainPort, explainAgent = "/glen/bothy", "read", "", 0, "thrawn"

	err := runSandboxExplain()
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("runSandboxExplain() = %v, want unknown-agent error", err)
	}

	// The error should list the known agents so the user can correct the typo.
	if !strings.Contains(err.Error(), "claude") {
		t.Fatalf("runSandboxExplain() error = %v, want it to list known agents", err)
	}
}

func TestRunSandboxExplainMergesKnownAgentOverride(t *testing.T) {
	saveExplainFlags(t)

	cfg = config.Default()
	cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: sandbox.BackendNono}
	// A known agent that overrides the backend proves the known-agent branch
	// ran and merged the per-agent config: the backend gate should now report
	// the agent's safehouse override, not the global nono backend.
	cfg.Agents["braw"] = config.Agent{Sandbox: config.SandboxConfig{Backend: sandbox.BackendSafehouse}}
	explainPath, explainOp, explainHost, explainPort, explainAgent = "/glen/bothy", "read", "", 0, "braw"

	err := runSandboxExplain()
	if err == nil || !strings.Contains(err.Error(), sandbox.BackendSafehouse) {
		t.Fatalf("runSandboxExplain() = %v, want backend-gate error mentioning the merged safehouse override", err)
	}
}

func TestKnownAgentNamesSortedAndEmpty(t *testing.T) {
	if got := knownAgentNames(nil); got != "(none)" {
		t.Fatalf("knownAgentNames(nil) = %q, want %q", got, "(none)")
	}

	agents := map[string]config.Agent{"codex": {}, "braw": {}, "claude": {}}
	if got := knownAgentNames(agents); got != "braw, claude, codex" {
		t.Fatalf("knownAgentNames() = %q, want sorted list", got)
	}
}

func TestFormatDenial(t *testing.T) {
	got := formatDenial(sandbox.Denial{Time: "2024-06-01 12:00:00", Process: "node", PID: 42, Operation: "file-read-data", Path: "/hame/bothy"})
	if !strings.Contains(got, "node(42)") || !strings.Contains(got, "file-read-data") || !strings.Contains(got, "/hame/bothy") {
		t.Fatalf("formatDenial() = %q, want process, op and path", got)
	}

	// The timestamp is included (leads the line) so a human can correlate.
	if !strings.HasPrefix(got, "2024-06-01 12:00:00") {
		t.Fatalf("formatDenial() = %q, want it to lead with the timestamp", got)
	}

	// A pathless (e.g. network) denial with no time should not print a trailing
	// space+path.
	net := formatDenial(sandbox.Denial{Process: "claude", PID: 7, Operation: "network-outbound"})
	if strings.HasSuffix(net, " ") || net != "claude(7) network-outbound" {
		t.Fatalf("formatDenial() = %q, want %q", net, "claude(7) network-outbound")
	}
}

func TestResolveWatchRecent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                               string
		recentFlag, followFlag, since, tty bool
		want                               bool
		wantErr                            bool
	}{
		{name: "default on a tty is live", tty: true, want: false},
		{name: "default off a tty is recent (avoids infinite tail)", tty: false, want: true},
		{name: "--recent forces recent", recentFlag: true, tty: true, want: true},
		{name: "--since implies recent", since: true, tty: true, want: true},
		{name: "--follow forces live even off a tty", followFlag: true, tty: false, want: false},
		{name: "--follow with --recent conflicts", followFlag: true, recentFlag: true, wantErr: true},
		{name: "--follow with --since conflicts", followFlag: true, since: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveWatchRecent(tt.recentFlag, tt.followFlag, tt.since, tt.tty)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveWatchRecent() = nil error, want conflict error")
				}

				return
			}

			if err != nil {
				t.Fatalf("resolveWatchRecent() error = %v", err)
			}

			if got != tt.want {
				t.Fatalf("resolveWatchRecent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSandboxWhyIsRenamed: the removed `why` command survives as a hidden
// tombstone that errors with a pointer to the new commands, even with old flags.
func TestSandboxWhyIsRenamed(t *testing.T) {
	err := sandboxWhyCmd.RunE(sandboxWhyCmd, []string{"--path", "/x", "--op", "read"})
	if err == nil {
		t.Fatal("sandbox why = nil error, want a renamed pointer")
	}

	if !strings.Contains(err.Error(), "explain") || !strings.Contains(err.Error(), "watch") {
		t.Fatalf("sandbox why error = %v, want it to point at explain and watch", err)
	}

	if !sandboxWhyCmd.Hidden {
		t.Fatal("sandbox why should be Hidden")
	}
}

func TestFormatDenialGroup(t *testing.T) {
	got := formatDenialGroup(sandbox.DenialGroup{Process: "node", Operation: "file-read-data", Path: "/loch/shore", Count: 12})
	if !strings.Contains(got, "12×") || !strings.Contains(got, "/loch/shore") || !strings.Contains(got, "[node]") {
		t.Fatalf("formatDenialGroup() = %q, want count, path and process", got)
	}
}

func TestDenialsOutputAggregates(t *testing.T) {
	groups := []sandbox.DenialGroup{
		{Process: "node", Operation: "file-read-data", Path: "/hame/a", Count: 3, LastPID: 9},
		{Process: "bash", Operation: "network-outbound", Count: 1},
	}

	rep := denialsOutput("10m", groups)
	if rep.Source != "macos-unified-log" || rep.Since != "10m" {
		t.Fatalf("denialsOutput() meta = %+v, want macos-unified-log/10m", rep)
	}

	if rep.Count != 4 {
		t.Fatalf("denialsOutput() total count = %d, want 4", rep.Count)
	}

	if len(rep.Denials) != 2 || rep.Denials[0].Path != "/hame/a" {
		t.Fatalf("denialsOutput() denials = %+v, want 2 with /hame/a first", rep.Denials)
	}
}

func TestStreamDenialJSONShape(t *testing.T) {
	got := streamDenialJSON(sandbox.Denial{Time: "t", Process: "node", PID: 5, Operation: "file-read-data", Path: "/loch"})
	if got.Process != "node" || got.PID != 5 || got.Operation != "file-read-data" || got.Path != "/loch" || got.Time != "t" {
		t.Fatalf("streamDenialJSON() = %+v, want fields mapped through", got)
	}
}

func TestWhySource(t *testing.T) {
	if got := whySource(sandbox.WhyResult{Source: "profile"}); got != "profile" {
		t.Fatalf("whySource() = %q, want profile (Source preferred)", got)
	}

	if got := whySource(sandbox.WhyResult{PolicySource: "group:deny_credentials"}); got != "group:deny_credentials" {
		t.Fatalf("whySource() = %q, want the policy_source fallback", got)
	}
}

func TestRenderWhyHumanAndJSON(t *testing.T) {
	oldOut, oldJSON := out, jsonOutput

	t.Cleanup(func() { out, jsonOutput = oldOut, oldJSON })

	q := sandbox.WhyQuery{Path: "/hame/bothy", Op: "read"}
	res := sandbox.WhyResult{Status: "denied", Reason: "sensitive_path", PolicySource: "group:deny_credentials"}

	// Human mode: writes a line, no error.
	out, jsonOutput = output.NewWithWriter(false, io.Discard), false

	if err := renderWhy("nono", q, res, []string{"a warning"}); err != nil {
		t.Fatalf("renderWhy() human error = %v", err)
	}

	// JSON mode: encodes the decision, no error.
	out, jsonOutput = output.NewWithWriter(true, io.Discard), true

	if err := renderWhy("nono", q, res, nil); err != nil {
		t.Fatalf("renderWhy() json error = %v", err)
	}
}

func TestStdoutIsTTY(t *testing.T) {
	// Under `go test`, stdout is a pipe, not a terminal — this just exercises
	// the probe without asserting a brittle environment-dependent value.
	_ = stdoutIsTTY()
}

func TestRenderRecentDenials(t *testing.T) {
	oldOut, oldJSON, oldProc, oldSince := out, jsonOutput, watchProc, watchSince

	t.Cleanup(func() { out, jsonOutput, watchProc, watchSince = oldOut, oldJSON, oldProc, oldSince })

	watchProc, watchSince = "", "5m"

	denials := []sandbox.Denial{
		{Process: "node", PID: 1, Operation: "file-read-data", Path: "/hame/a"},
		{Process: "node", PID: 1, Operation: "file-read-data", Path: "/hame/a"},
		{Process: "bash", PID: 2, Operation: "network-outbound"},
	}

	// Human, non-empty.
	out, jsonOutput = output.NewWithWriter(false, io.Discard), false

	if err := renderRecentDenials(denials, nil); err != nil {
		t.Fatalf("renderRecentDenials() human = %v", err)
	}

	// Human, empty.
	out = output.NewWithWriter(false, io.Discard)

	if err := renderRecentDenials(nil, nil); err != nil {
		t.Fatalf("renderRecentDenials() empty = %v", err)
	}

	// JSON.
	out, jsonOutput = output.NewWithWriter(true, io.Discard), true

	if err := renderRecentDenials(denials, nil); err != nil {
		t.Fatalf("renderRecentDenials() json = %v", err)
	}
}

func TestRunSandboxWatchDispatch(t *testing.T) {
	oldSupported, oldResolve, oldTree := denialLogSupportedFn, resolveSessionPIDFn, processTreeFn
	oldRecent, oldLive, oldOut, oldJSON := watchRecentFn, watchLiveFn, out, jsonOutput

	t.Cleanup(func() {
		denialLogSupportedFn, resolveSessionPIDFn, processTreeFn = oldSupported, oldResolve, oldTree
		watchRecentFn, watchLiveFn, out, jsonOutput = oldRecent, oldLive, oldOut, oldJSON
	})

	out, jsonOutput = output.NewWithWriter(false, io.Discard), false

	// Unsupported platform → the gate error propagates, nothing else runs.
	t.Run("unsupported", func(t *testing.T) {
		denialLogSupportedFn = func() error { return errThrawn }
		watchRecentFn = func(map[int]bool) error { t.Fatal("recent should not run"); return nil }
		watchLiveFn = func(*sandbox.SessionMatcher) error { t.Fatal("live should not run"); return nil }

		if err := runSandboxWatch("", true); err != errThrawn {
			t.Fatalf("runSandboxWatch() = %v, want the unsupported gate error", err)
		}
	})

	denialLogSupportedFn = func() error { return nil }

	// Recent, no session → recent path with nil pids.
	t.Run("recent no session", func(t *testing.T) {
		called := false
		watchRecentFn = func(pids map[int]bool) error {
			called = true

			if pids != nil {
				t.Errorf("recent pids = %v, want nil", pids)
			}

			return nil
		}

		if err := runSandboxWatch("", true); err != nil || !called {
			t.Fatalf("runSandboxWatch() recent = %v, called=%v", err, called)
		}
	})

	// Recent + session → resolve PID, build tree, scope recent to it.
	t.Run("recent with session", func(t *testing.T) {
		resolveSessionPIDFn = func(string) (int, error) { return 100, nil }
		processTreeFn = func(int) (map[int]bool, error) { return map[int]bool{100: true, 200: true}, nil }

		var gotPids map[int]bool

		watchRecentFn = func(pids map[int]bool) error { gotPids = pids; return nil }

		if err := runSandboxWatch("braw", true); err != nil {
			t.Fatalf("runSandboxWatch() = %v", err)
		}

		if !gotPids[100] || !gotPids[200] {
			t.Fatalf("recent scoped pids = %v, want the resolved tree", gotPids)
		}
	})

	// Session resolution failure propagates.
	t.Run("session resolve error", func(t *testing.T) {
		resolveSessionPIDFn = func(string) (int, error) { return 0, errThrawn }
		watchRecentFn = func(map[int]bool) error { t.Fatal("recent should not run"); return nil }

		if err := runSandboxWatch("dreich", true); err != errThrawn {
			t.Fatalf("runSandboxWatch() = %v, want resolve error", err)
		}
	})

	// Live, no session → live path with a nil matcher.
	t.Run("live no session", func(t *testing.T) {
		called := false
		watchLiveFn = func(m *sandbox.SessionMatcher) error {
			called = true

			if m != nil {
				t.Errorf("live matcher = %v, want nil", m)
			}

			return nil
		}

		if err := runSandboxWatch("", false); err != nil || !called {
			t.Fatalf("runSandboxWatch() live = %v, called=%v", err, called)
		}
	})

	// Live + session → resolve PID, build a matcher.
	t.Run("live with session", func(t *testing.T) {
		resolveSessionPIDFn = func(string) (int, error) { return 100, nil }

		var gotMatcher *sandbox.SessionMatcher

		watchLiveFn = func(m *sandbox.SessionMatcher) error { gotMatcher = m; return nil }

		if err := runSandboxWatch("canny", false); err != nil {
			t.Fatalf("runSandboxWatch() = %v", err)
		}

		if gotMatcher == nil {
			t.Fatal("live with session: matcher = nil, want a session matcher")
		}
	})
}

func TestPIDFromDiagnostics(t *testing.T) {
	t.Parallel()

	diag := protocol.DiagnosticsMsg{Sessions: []protocol.SessionDiagnostic{
		{ID: "abc123", Name: "braw", PID: 4242, PIDAlive: true},
		{ID: "def456", Name: "dreich", PID: 0, PIDAlive: false},
		{ID: "ghi789", Name: "thrawn", PID: 99, PIDAlive: false},
	}}

	// Match by name.
	if pid, err := pidFromDiagnostics(diag, "braw"); err != nil || pid != 4242 {
		t.Fatalf("pidFromDiagnostics(name) = %d, %v, want 4242", pid, err)
	}

	// Match by ID.
	if pid, err := pidFromDiagnostics(diag, "abc123"); err != nil || pid != 4242 {
		t.Fatalf("pidFromDiagnostics(id) = %d, %v, want 4242", pid, err)
	}

	// Matched but not running.
	if _, err := pidFromDiagnostics(diag, "thrawn"); err == nil || !strings.Contains(err.Error(), "no running process") {
		t.Fatalf("pidFromDiagnostics(dead) = %v, want no-running-process error", err)
	}

	// No match.
	if _, err := pidFromDiagnostics(diag, "haar"); err == nil || !strings.Contains(err.Error(), "no session matched") {
		t.Fatalf("pidFromDiagnostics(missing) = %v, want no-match error", err)
	}
}

var errThrawn = errThrawnErr{}

type errThrawnErr struct{}

func (errThrawnErr) Error() string { return "thrawn: stubbed failure" }

// failWriter fails every write, standing in for a broken output pipe.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLiveDenialHandler(t *testing.T) {
	oldOut, oldJSON, oldProc := out, jsonOutput, watchProc

	t.Cleanup(func() { out, jsonOutput, watchProc = oldOut, oldJSON, oldProc })

	d := sandbox.Denial{Process: "node", PID: 5, Operation: "file-read-data", Path: "/loch"}

	// --proc that doesn't match is skipped (no write, no error).
	out, jsonOutput, watchProc = output.NewWithWriter(false, io.Discard), false, "chrome"

	if err := liveDenialHandler(nil)(d); err != nil {
		t.Fatalf("liveDenialHandler() filtered = %v", err)
	}

	// Matching --proc, human mode writes without error.
	out, watchProc = output.NewWithWriter(false, io.Discard), "node"

	if err := liveDenialHandler(nil)(d); err != nil {
		t.Fatalf("liveDenialHandler() human = %v", err)
	}

	// JSON mode with a broken pipe surfaces the write error (so the stream stops).
	out, jsonOutput, watchProc = output.NewWithWriter(true, failWriter{}), true, ""

	if err := liveDenialHandler(nil)(d); err == nil {
		t.Fatalf("liveDenialHandler() = nil, want the broken-pipe write error")
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}

	return false
}
