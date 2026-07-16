package sandbox

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/tools"
)

func TestParseDenialLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		line    string
		want    Denial
		wantOK  bool
		wantErr string
	}{
		{
			name:   "file read denial",
			line:   `2024-06-01 12:00:00.123456+0100 0x1a Df kernel: Sandbox: node(12345) deny(1) file-read-data /Users/braw/.ssh/id_rsa`,
			wantOK: true,
			want: Denial{
				Time:      "2024-06-01 12:00:00.123456+0100",
				Process:   "node",
				PID:       12345,
				Operation: "file-read-data",
				Path:      "/Users/braw/.ssh/id_rsa",
			},
		},
		{
			name:   "network denial has no path",
			line:   `2024-06-01 12:00:01.000000+0100 0x1b Df kernel: Sandbox: claude(999) deny(1) network-outbound`,
			wantOK: true,
			want: Denial{
				Time:      "2024-06-01 12:00:01.000000+0100",
				Process:   "claude",
				PID:       999,
				Operation: "network-outbound",
				Path:      "",
			},
		},
		{
			name:   "not a denial line",
			line:   `2024-06-01 12:00:02.000000+0100 some other unrelated log line`,
			wantOK: false,
		},
		{
			name:   "sandbox mention without deny is skipped",
			line:   `Sandbox: node(1) starting up`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseDenialLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseDenialLine() ok = %v, want %v", ok, tt.wantOK)
			}

			if !tt.wantOK {
				return
			}

			// Raw is the trimmed line; check it separately then blank it for the
			// struct comparison.
			if got.Raw != strings.TrimSpace(tt.line) {
				t.Errorf("Raw = %q, want %q", got.Raw, strings.TrimSpace(tt.line))
			}

			got.Raw = ""
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDenialLine() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLogShowArgs(t *testing.T) {
	t.Parallel()

	got := logShowArgs("15m")
	want := []string{"show", "--last", "15m", "--style", "compact", "--predicate", denialPredicate}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logShowArgs() = %v, want %v", got, want)
	}
}

func TestLogStreamArgs(t *testing.T) {
	t.Parallel()

	got := logStreamArgs()
	want := []string{"stream", "--style", "compact", "--predicate", denialPredicate}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logStreamArgs() = %v, want %v", got, want)
	}
}

func TestRecentDenialsParses(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		`2024-06-01 12:00:00.000000+0100 Df kernel: Sandbox: node(1) deny(1) file-read-data /hame/bothy`,
		`noise line that should be ignored`,
		`2024-06-01 12:00:01.000000+0100 Df kernel: Sandbox: node(1) deny(1) file-write-create /glen/wynd`,
	}, "\n")

	run := func(name string, args []string) (string, error) {
		if name != logCommand {
			t.Errorf("run command = %q, want %q", name, logCommand)
		}

		if !containsArg(args, "--last") || !containsArg(args, "30m") {
			t.Errorf("args missing --last 30m: %v", args)
		}

		return out, nil
	}

	denials, err := recentDenials("30m", run)
	if err != nil {
		t.Fatalf("recentDenials() error = %v", err)
	}

	if len(denials) != 2 {
		t.Fatalf("recentDenials() = %d denials, want 2", len(denials))
	}
}

func TestRecentDenialsErrorWithNoOutput(t *testing.T) {
	t.Parallel()

	run := func(_ string, _ []string) (string, error) {
		return "", errors.New("log: command not found")
	}

	_, err := recentDenials("5m", run)
	if err == nil || !strings.Contains(err.Error(), "log show") {
		t.Fatalf("recentDenials() = %v, want log show error", err)
	}
}

func TestRecentDenialsErrorsOnNonZeroExitEvenWithOutput(t *testing.T) {
	t.Parallel()

	// A non-zero `log show` exit is a real failure (bad predicate, denied
	// access, sandbox refusal). Even if some output was emitted, a diagnostic
	// command must surface the failure rather than present a partial read as a
	// complete result.
	run := func(_ string, _ []string) (string, error) {
		return `2024-06-01 Df kernel: Sandbox: canny(7) deny(1) file-read-data /loch/shore`,
			errors.New("exit 1: log: some failure")
	}

	_, err := recentDenials("5m", run)
	if err == nil || !strings.Contains(err.Error(), "log show") {
		t.Fatalf("recentDenials() = %v, want a surfaced log show error", err)
	}
}

func TestFilterByPIDs(t *testing.T) {
	t.Parallel()

	ds := []Denial{
		{Process: "braw", PID: 1},
		{Process: "canny", PID: 2},
		{Process: "dreich", PID: 3},
	}

	got := FilterByPIDs(ds, map[int]bool{1: true, 3: true})
	if len(got) != 2 || got[0].PID != 1 || got[1].PID != 3 {
		t.Fatalf("FilterByPIDs() = %+v, want pids 1 and 3", got)
	}

	// Empty set keeps everything.
	if all := FilterByPIDs(ds, nil); len(all) != 3 {
		t.Fatalf("FilterByPIDs(nil) = %d, want 3 (no filter)", len(all))
	}
}

func TestFilterByProcess(t *testing.T) {
	t.Parallel()

	ds := []Denial{
		{Process: "node"},
		{Process: "Claude"},
		{Process: "bash"},
	}

	got := FilterByProcess(ds, "claude")
	if len(got) != 1 || got[0].Process != "Claude" {
		t.Fatalf("FilterByProcess() = %+v, want case-insensitive Claude match", got)
	}

	if all := FilterByProcess(ds, ""); len(all) != 3 {
		t.Fatalf("FilterByProcess(\"\") = %d, want 3 (no filter)", len(all))
	}
}

func TestAggregateDenials(t *testing.T) {
	t.Parallel()

	ds := []Denial{
		{Process: "node", Operation: "file-read-data", Path: "/hame/a", PID: 1, Time: "t1"},
		{Process: "node", Operation: "file-read-data", Path: "/hame/a", PID: 2, Time: "t2"},
		{Process: "node", Operation: "file-read-data", Path: "/hame/a", PID: 3, Time: "t3"},
		{Process: "bash", Operation: "network-outbound", Path: "", PID: 4, Time: "t4"},
	}

	groups := AggregateDenials(ds)
	if len(groups) != 2 {
		t.Fatalf("AggregateDenials() = %d groups, want 2", len(groups))
	}

	// The most frequent group sorts first.
	if groups[0].Count != 3 || groups[0].Path != "/hame/a" {
		t.Fatalf("groups[0] = %+v, want count 3 for /hame/a", groups[0])
	}

	// Last-seen metadata is carried from the final occurrence.
	if groups[0].LastPID != 3 || groups[0].LastTime != "t3" {
		t.Fatalf("groups[0] last = pid %d time %q, want 3/t3", groups[0].LastPID, groups[0].LastTime)
	}

	if groups[1].Count != 1 || groups[1].Operation != "network-outbound" {
		t.Fatalf("groups[1] = %+v, want the single network-outbound", groups[1])
	}
}

func TestDescendantPIDs(t *testing.T) {
	t.Parallel()

	// 100 -> 200 -> 400, 100 -> 300; 500 is unrelated.
	parents := map[int]int{
		200: 100,
		300: 100,
		400: 200,
		500: 999,
	}

	got := DescendantPIDs(100, parents)
	want := map[int]bool{100: true, 200: true, 300: true, 400: true}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DescendantPIDs() = %v, want %v", got, want)
	}

	if got[500] {
		t.Fatalf("DescendantPIDs() included unrelated pid 500")
	}
}

func TestParsePSPairs(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		"  100     1",
		"  200   100",
		"garbage line",
		"  abc   100",
		"  300   100",
	}, "\n")

	got := parsePSPairs(out)
	want := map[int]int{100: 1, 200: 100, 300: 100}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePSPairs() = %v, want %v", got, want)
	}
}

func TestProcessTree(t *testing.T) {
	t.Parallel()

	run := func(name string, args []string) (string, error) {
		if name != tools.PS() {
			t.Errorf("run command = %q, want %q", name, tools.PS())
		}

		if !containsArg(args, "-axo") {
			t.Errorf("args missing -axo: %v", args)
		}

		return "100 1\n200 100\n300 200\n400 999\n", nil
	}

	got, err := processTree(100, run)
	if err != nil {
		t.Fatalf("processTree() error = %v", err)
	}

	want := map[int]bool{100: true, 200: true, 300: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("processTree() = %v, want %v", got, want)
	}
}

// TestDenialProcessResolutionUsesReloadedToolsPS proves both denial process
// resolution paths consult the process-wide tools resolver at execution time.
// In particular, a long-lived SessionMatcher must not capture the ps path when
// it is constructed: the next TTL refresh observes a reloaded [tools].ps.
func TestDenialProcessResolutionUsesReloadedToolsPS(t *testing.T) {
	tools.Reset()
	t.Cleanup(tools.Reset)

	var commands []string
	run := func(name string, _ []string) (string, error) {
		commands = append(commands, name)

		return "100 1\n200 100\n300 200\n", nil
	}

	tools.Configure(tools.Config{PS: "/croft/bin/ps-once"})
	if _, err := processTree(100, run); err != nil {
		t.Fatalf("processTree() error = %v", err)
	}

	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := newSessionMatcher(100, run, clk.now)

	tools.Configure(tools.Config{PS: "/bothy/bin/ps-live"})
	if !m.Matches(200) {
		t.Fatal("Matches(200) = false, want true")
	}

	tools.Configure(tools.Config{PS: "/strath/bin/ps-reloaded"})
	clk.advance(2 * sessionMatcherTTL)
	if !m.Matches(300) {
		t.Fatal("Matches(300) = false after reload, want true")
	}

	want := []string{"/croft/bin/ps-once", "/bothy/bin/ps-live", "/strath/bin/ps-reloaded"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("ps commands = %v, want %v", commands, want)
	}
}

func TestProcessTreeError(t *testing.T) {
	t.Parallel()

	run := func(_ string, _ []string) (string, error) {
		return "", errors.New("ps: not found")
	}

	if _, err := processTree(1, run); err == nil {
		t.Fatalf("processTree() = nil error, want ps failure")
	}
}

func TestScanDenials(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`Df kernel: Sandbox: node(1) deny(1) file-read-data /hame/a`,
		`preamble noise`,
		`Df kernel: Sandbox: node(1) deny(1) file-read-data /hame/b`,
	}, "\n")

	var seen []Denial

	if err := scanDenials(strings.NewReader(input), func(d Denial) error {
		seen = append(seen, d)
		return nil
	}); err != nil {
		t.Fatalf("scanDenials() error = %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("scanDenials() saw %d denials, want 2", len(seen))
	}

	if seen[0].Path != "/hame/a" || seen[1].Path != "/hame/b" {
		t.Fatalf("scanDenials() paths = %q, %q", seen[0].Path, seen[1].Path)
	}
}

// TestScanDenialsStopsOnCallbackError: a callback error (e.g. a broken output
// pipe) must stop the scan and propagate, so a live stream terminates promptly
// instead of consuming with nowhere to write.
func TestScanDenialsStopsOnCallbackError(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`Df kernel: Sandbox: node(1) deny(1) file-read-data /hame/a`,
		`Df kernel: Sandbox: node(1) deny(1) file-read-data /hame/b`,
	}, "\n")

	boom := errors.New("dreich: broken pipe")
	seen := 0

	err := scanDenials(strings.NewReader(input), func(Denial) error {
		seen++
		return boom
	})

	if !errors.Is(err, boom) {
		t.Fatalf("scanDenials() = %v, want the callback error", err)
	}

	if seen != 1 {
		t.Fatalf("scanDenials() invoked callback %d times, want 1 (stop on first error)", seen)
	}
}

// TestStreamDenialsCancelledContext exercises the stream path against a stub
// that behaves like a blocking `log stream`: it emits nothing and stays alive
// until killed, so a pre-cancelled context returns promptly with ctx.Err.
func TestStreamDenialsCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// `sleep` stands in for a long-running `log stream`; the cancelled context
	// kills it immediately.
	err := streamDenials(ctx, func(Denial) error {
		t.Errorf("onDenial called for a silent stream")
		return nil
	}, "sleep")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("streamDenials() = %v, want context.Canceled", err)
	}
}

func TestAncestorsIncludeRoot(t *testing.T) {
	t.Parallel()

	// 400 -> 200 -> 100 (root); 500 -> 999 is unrelated.
	parents := map[int]int{200: 100, 400: 200, 500: 999, 100: 1}

	if !ancestorsIncludeRoot(400, 100, parents) {
		t.Errorf("ancestorsIncludeRoot(400, 100) = false, want true (400→200→100)")
	}

	if !ancestorsIncludeRoot(100, 100, parents) {
		t.Errorf("ancestorsIncludeRoot(100, 100) = false, want true (self)")
	}

	if ancestorsIncludeRoot(500, 100, parents) {
		t.Errorf("ancestorsIncludeRoot(500, 100) = true, want false (unrelated)")
	}

	// A cycle must terminate and report no match.
	cycle := map[int]int{10: 20, 20: 10}
	if ancestorsIncludeRoot(10, 999, cycle) {
		t.Errorf("ancestorsIncludeRoot() on a cycle = true, want false")
	}
}

// fakeClock is a controllable time source for SessionMatcher's TTL logic.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestSessionMatcher(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: time.Unix(1000, 0)}

	calls := 0
	// The tree grows between snapshots: child 300 (under 200 under root 100)
	// appears only on the second ps, modelling a subprocess spawned after the
	// stream started.
	run := func(_ string, _ []string) (string, error) {
		calls++
		if calls == 1 {
			return "100 1\n200 100\n", nil
		}

		return "100 1\n200 100\n300 200\n", nil
	}

	m := newSessionMatcher(100, run, clk.now)

	// Root is known without any ps call.
	if !m.Matches(100) {
		t.Fatalf("Matches(100 root) = false, want true")
	}

	if calls != 0 {
		t.Fatalf("root match ran ps %d times, want 0 (no snapshot needed)", calls)
	}

	// 200 is a descendant (triggers the first snapshot).
	if !m.Matches(200) {
		t.Fatalf("Matches(200) = false, want true")
	}

	if calls != 1 {
		t.Fatalf("Matches(200) ran ps %d times, want 1", calls)
	}

	// 300 isn't in the first snapshot; within the TTL window no fresh ps runs,
	// so it isn't matched yet — and ps was NOT re-forked (snapshot reused).
	if m.Matches(300) {
		t.Fatalf("Matches(300) = true within TTL, want false (not in snapshot yet)")
	}

	if calls != 1 {
		t.Fatalf("Matches(300) within TTL ran ps %d times, want 1 (reused snapshot)", calls)
	}

	// Advance past the TTL: the next check refreshes and now sees 300.
	clk.advance(2 * sessionMatcherTTL)

	if !m.Matches(300) {
		t.Fatalf("Matches(300) = false after refresh, want true (spawned after start)")
	}

	if calls != 2 {
		t.Fatalf("Matches(300) post-TTL ran ps %d times, want 2 (refreshed)", calls)
	}

	// Once confirmed, 300 stays matched even after it exits (drops from ps) and
	// the TTL lapses — a late-arriving denial from a short-lived child still
	// attributes.
	clk.advance(2 * sessionMatcherTTL)

	run = func(_ string, _ []string) (string, error) {
		calls++
		return "100 1\n200 100\n", nil // 300 has exited
	}
	m.run = run

	if !m.Matches(300) {
		t.Fatalf("Matches(300) after exit = false, want true (confirmed PIDs survive)")
	}

	if calls != 2 {
		t.Fatalf("confirmed Matches(300) ran ps %d times, want 2 (served from matched cache)", calls)
	}
}

// TestSessionMatcherRetriesAfterTransientPSFailure: a one-off `ps` failure must
// not become a permanent verdict — the next check retries and can match.
func TestSessionMatcherRetriesAfterTransientPSFailure(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: time.Unix(1000, 0)}

	fail := true
	run := func(_ string, _ []string) (string, error) {
		if fail {
			return "", errors.New("scunner: ps hiccup")
		}

		return "100 1\n200 100\n", nil
	}

	m := newSessionMatcher(100, run, clk.now)

	// First check hits the ps failure → not matched, but not cached as a verdict.
	if m.Matches(200) {
		t.Fatalf("Matches(200) during ps failure = true, want false")
	}

	// ps recovers; a later check (no snapshot cached yet, so it retries
	// regardless of TTL) now matches.
	fail = false

	if !m.Matches(200) {
		t.Fatalf("Matches(200) after ps recovered = false, want true (no poisoned negative)")
	}
}

func TestProcessNameMatches(t *testing.T) {
	t.Parallel()

	if !ProcessNameMatches("node", "") {
		t.Errorf("empty substr should match everything")
	}

	if !ProcessNameMatches("Google Chrome", "chrome") {
		t.Errorf("case-insensitive substring should match")
	}

	if ProcessNameMatches("bash", "node") {
		t.Errorf("non-substring should not match")
	}
}

func TestRunOutputSuccess(t *testing.T) {
	t.Parallel()

	out, err := runOutput("/bin/echo", []string{"braw"})
	if err != nil {
		t.Fatalf("runOutput() error = %v", err)
	}

	if strings.TrimSpace(out) != "braw" {
		t.Fatalf("runOutput() = %q, want braw", out)
	}
}

func TestRunOutputFoldsStderr(t *testing.T) {
	t.Parallel()

	// `ls` of a missing path writes its reason to stderr and exits non-zero;
	// runOutput must fold that reason into the error, not drop it.
	_, err := runOutput("/bin/ls", []string{"/no/such/dreich/path/xyz"})
	if err == nil {
		t.Fatalf("runOutput() = nil error, want failure")
	}

	if !strings.Contains(strings.ToLower(err.Error()), "no such") {
		t.Fatalf("runOutput() error = %v, want the stderr reason folded in", err)
	}
}

func TestDenialLogSupported(t *testing.T) {
	t.Parallel()

	err := DenialLogSupported()

	if runtime.GOOS == "darwin" {
		// /usr/bin/log ships with macOS, so support should be reported.
		if err != nil {
			t.Fatalf("DenialLogSupported() = %v on darwin, want nil", err)
		}

		return
	}

	if err == nil {
		t.Fatalf("DenialLogSupported() = nil on %s, want a macOS-only error", runtime.GOOS)
	}
}

func TestProcessTreeReal(t *testing.T) {
	t.Parallel()

	// A real `ps` must at least report the running test process's own PID.
	tree, err := ProcessTree(os.Getpid())
	if err != nil {
		// Some sandboxed dev environments forbid exec of /bin/ps; skip there
		// (CI runs unsandboxed and exercises this path).
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("ps exec not permitted in this environment: %v", err)
		}

		t.Fatalf("ProcessTree() error = %v", err)
	}

	if !tree[os.Getpid()] {
		t.Fatalf("ProcessTree(self) missing own PID %d: %v", os.Getpid(), tree)
	}
}

func TestNewSessionMatcherRootMatches(t *testing.T) {
	t.Parallel()

	m := NewSessionMatcher(os.Getpid())
	if !m.Matches(os.Getpid()) {
		t.Fatalf("Matches(root) = false, want true")
	}
}

// TestStreamDenialsSurfacesCommandFailure exercises the non-cancel exit path:
// a command that fails to run should surface an error (with its stderr folded
// in when present), not be swallowed.
func TestStreamDenialsSurfacesCommandFailure(t *testing.T) {
	t.Parallel()

	// `ls` gets the log-stream argv, which it can't satisfy → non-zero exit,
	// empty stdout, a reason on stderr.
	err := streamDenials(context.Background(), func(Denial) error {
		t.Errorf("onDenial called for a failing command")
		return nil
	}, "/bin/ls")
	if err == nil {
		t.Fatalf("streamDenials() = nil error, want a command failure")
	}
}

func containsArg(args []string, want string) bool {
	return slices.Contains(args, want)
}
