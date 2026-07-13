package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The safehouse backend (macOS Seatbelt / sandbox-exec) has no policy oracle
// like nono's `nono why`, so an allow/deny question can't be answered
// predictively. Instead graith taps the macOS unified log, where the kernel
// records every Seatbelt denial. `gr sandbox watch` uses this to show — live or
// retrospectively — exactly which filesystem paths or operations the sandbox
// denied, which is the practical way to debug a confusing "permission denied".
//
// The technique (streaming Sandbox deny records from `log`) comes from the
// agent-safehouse project; see
// https://github.com/eugene1g/agent-safehouse/issues/143.

// logCommand is the macOS unified-logging binary. It is a var so tests can
// point it at a stub instead of the real /usr/bin/log.
var logCommand = "/usr/bin/log"

// psCommand is the process-listing binary used to resolve a session's process
// tree. A var for the same test seam reason as logCommand.
var psCommand = "/bin/ps"

// denialPredicate selects Seatbelt sandbox-deny records in the unified log. A
// denial's eventMessage looks like:
//
//	Sandbox: node(1234) deny(1) file-read-data /Users/x/.ssh/id_rsa
//
// Filtering to "Sandbox:" + "deny(" keeps the stream to actual denials.
const denialPredicate = `eventMessage CONTAINS "Sandbox:" AND eventMessage CONTAINS "deny("`

// Denial is one parsed Seatbelt sandbox denial from the unified log.
type Denial struct {
	// Time is the log timestamp (best-effort: the leading date+time of the
	// compact log line), empty if it couldn't be located.
	Time string
	// Process is the name of the process whose access was denied.
	Process string
	// PID is that process's PID, used to filter by a session's process tree.
	PID int
	// Operation is the Seatbelt operation, e.g. "file-read-data",
	// "network-outbound", "mach-lookup".
	Operation string
	// Path is the target resource (a filesystem path for file-* operations),
	// possibly empty for operations that don't name one.
	Path string
	// Raw is the full log line, kept so nothing is lost if parsing is partial.
	Raw string
}

// denialRe extracts the parts of a Seatbelt denial message. It matches the
// message wherever it appears in the log line, so it is robust to the log
// line's leading metadata (timestamp, thread, process prefix) regardless of the
// exact `--style` formatting. The operation is a run of non-space characters
// (covers "file-read-data", "file-read*", "network-outbound", etc.).
var denialRe = regexp.MustCompile(`Sandbox:\s+(.+?)\((\d+)\)\s+deny\(\d+\)\s+(\S+)\s*(.*)`)

// parseDenialLine parses a single unified-log line into a Denial. It returns
// false when the line is not a Seatbelt denial (so non-denial noise the
// predicate can't exclude, and any stream preamble, is skipped).
func parseDenialLine(line string) (Denial, bool) {
	m := denialRe.FindStringSubmatch(line)
	if m == nil {
		return Denial{}, false
	}

	pid, _ := strconv.Atoi(m[2])

	d := Denial{
		Process:   strings.TrimSpace(m[1]),
		PID:       pid,
		Operation: m[3],
		Path:      strings.TrimSpace(m[4]),
		Raw:       strings.TrimSpace(line),
	}

	// The timestamp is the log line's leading date+time, before "Sandbox:".
	if idx := strings.Index(line, "Sandbox:"); idx > 0 {
		if fields := strings.Fields(line[:idx]); len(fields) >= 2 {
			d.Time = fields[0] + " " + fields[1]
		} else if len(fields) == 1 {
			d.Time = fields[0]
		}
	}

	return d, true
}

// logShowArgs builds the `log show` argv for denials over the past `since`
// window (a duration like "5m", "1h" accepted by `log show --last`).
func logShowArgs(since string) []string {
	return []string{
		"show",
		"--last", since,
		"--style", "compact",
		"--predicate", denialPredicate,
	}
}

// logStreamArgs builds the `log stream` argv for a live denial feed.
func logStreamArgs() []string {
	return []string{
		"stream",
		"--style", "compact",
		"--predicate", denialPredicate,
	}
}

// outputRunner runs a command and returns its stdout. It is a seam so the
// denial readers are unit-testable without the real `log` binary.
type outputRunner func(name string, args []string) (string, error)

func runOutput(name string, args []string) (string, error) {
	cmd := exec.Command(name, args...)

	var stdout, stderr strings.Builder

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	// Fold stderr into the error so a `log`/`ps` usage failure (e.g. the "Cannot
	// run while sandboxed" refusal when invoked from inside a sandbox) surfaces
	// its reason instead of a bare exit code.
	if err != nil && strings.TrimSpace(stderr.String()) != "" {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), err
}

// DenialLogSupported reports whether the unified-log denial feed can be used on
// this host: it is a macOS-only facility (Seatbelt + `log`). The returned error
// explains why when unsupported.
func DenialLogSupported() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf(
			"sandbox denial logging is macOS-only (Seatbelt + unified logging); this host is %s",
			runtime.GOOS)
	}

	if _, err := exec.LookPath(logCommand); err != nil {
		return fmt.Errorf("%q not found; unified logging is unavailable", logCommand)
	}

	return nil
}

// RecentDenials returns the Seatbelt sandbox denials the kernel logged over the
// past `since` window (a `log show --last` duration such as "5m" or "1h").
func RecentDenials(since string) ([]Denial, error) {
	return recentDenials(since, runOutput)
}

func recentDenials(since string, run outputRunner) ([]Denial, error) {
	out, runErr := run(logCommand, logShowArgs(since))

	// `log show` exits 0 on success, even with zero matches. A non-zero exit is
	// a real failure — a bad predicate, denied log access, or the "Cannot run
	// while sandboxed" refusal (whose reason runOutput folds into runErr).
	// Surface it rather than presenting a partial or empty read as a complete
	// result: for a diagnostic command, silently reporting incomplete data is
	// worse than reporting that the read failed.
	if runErr != nil {
		return nil, fmt.Errorf("log show: %w", runErr)
	}

	denials, scanErr := parseDenials(out)
	if scanErr != nil {
		return nil, fmt.Errorf("read log show output: %w", scanErr)
	}

	return denials, nil
}

// parseDenials extracts every denial from a block of log output. It returns a
// scan error (e.g. a line past the buffer cap) so a truncated read is not
// silently reported as a clean, short result.
func parseDenials(out string) ([]Denial, error) {
	var ds []Denial

	err := scanDenials(strings.NewReader(out), func(d Denial) error {
		ds = append(ds, d)
		return nil
	})

	return ds, err
}

// StreamDenials runs `log stream` and calls onDenial for each parsed Seatbelt
// denial until ctx is cancelled (or the stream ends). If onDenial returns an
// error (e.g. a broken output pipe), the stream is stopped and that error is
// returned — so `gr sandbox watch --json | head` terminates promptly instead of
// running on with nowhere to write. Cancelling ctx kills the `log` process; the
// resulting wait error is expected and folded into ctx.Err.
func StreamDenials(ctx context.Context, onDenial func(Denial) error) error {
	return streamDenials(ctx, onDenial, logCommand)
}

func streamDenials(parent context.Context, onDenial func(Denial) error, command string) error {
	// A child context lets us stop the (otherwise infinite) `log stream` if the
	// scanner fails mid-read, so cmd.Wait doesn't block forever.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, logStreamArgs()...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	var stderr strings.Builder

	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start log stream: %w", err)
	}

	// scanErr covers both a scanner read failure and an onDenial callback error
	// (e.g. a broken output pipe); either should stop the stream promptly.
	scanErr := scanDenials(stdout, onDenial)
	if scanErr != nil {
		cancel()
	}

	waitErr := cmd.Wait()

	// A cancelled *parent* context is the normal exit (the user hit Ctrl-C);
	// report it as ctx.Err rather than the process's kill signal. Check the
	// parent, not our child, so an internal cancel-on-scan-error isn't mistaken
	// for a user interrupt.
	if parentErr := parent.Err(); parentErr != nil {
		return parentErr
	}

	if scanErr != nil {
		return fmt.Errorf("log stream: %w", scanErr)
	}

	// Fold stderr into a wait failure so a `log` refusal keeps its reason.
	if waitErr != nil && strings.TrimSpace(stderr.String()) != "" {
		return fmt.Errorf("%w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}

	return waitErr
}

// scanDenials reads lines from r, parses denials, and invokes onDenial for each.
// It stops and returns the first onDenial error (so a downstream write failure
// terminates the stream), otherwise the scanner's error — so a truncated or
// failed read is observable rather than looking like a clean end of stream.
func scanDenials(r io.Reader, onDenial func(Denial) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		d, ok := parseDenialLine(sc.Text())
		if !ok {
			continue
		}

		if err := onDenial(d); err != nil {
			return err
		}
	}

	return sc.Err()
}

// FilterByPIDs keeps only denials whose PID is in pids. A nil/empty set keeps
// everything (no filter requested).
func FilterByPIDs(ds []Denial, pids map[int]bool) []Denial {
	if len(pids) == 0 {
		return ds
	}

	var out []Denial

	for _, d := range ds {
		if pids[d.PID] {
			out = append(out, d)
		}
	}

	return out
}

// ProcessNameMatches reports whether a process name contains substr
// (case-insensitive). An empty substr matches everything. It is the single
// predicate behind both FilterByProcess and the live --proc filter, so the
// recent and streaming paths stay in lockstep.
func ProcessNameMatches(process, substr string) bool {
	if substr == "" {
		return true
	}

	return strings.Contains(strings.ToLower(process), strings.ToLower(substr))
}

// FilterByProcess keeps only denials whose process name contains substr
// (case-insensitive). An empty substr keeps everything.
func FilterByProcess(ds []Denial, substr string) []Denial {
	if substr == "" {
		return ds
	}

	var out []Denial

	for _, d := range ds {
		if ProcessNameMatches(d.Process, substr) {
			out = append(out, d)
		}
	}

	return out
}

// DenialGroup is a set of identical denials (same process, operation, and path)
// collapsed into one row with a count — recent denials repeat heavily, so
// aggregation is what makes the output readable.
type DenialGroup struct {
	Process   string
	Operation string
	Path      string
	Count     int
	LastTime  string
	LastPID   int
}

// AggregateDenials collapses identical denials into groups, ordered by count
// (descending) then operation then path, so the noisiest denial is first.
func AggregateDenials(ds []Denial) []DenialGroup {
	type key struct{ proc, op, path string }

	order := make([]key, 0, len(ds))
	groups := make(map[key]*DenialGroup, len(ds))

	for _, d := range ds {
		k := key{d.Process, d.Operation, d.Path}

		g, ok := groups[k]
		if !ok {
			g = &DenialGroup{Process: d.Process, Operation: d.Operation, Path: d.Path}
			groups[k] = g
			order = append(order, k)
		}

		g.Count++
		g.LastTime = d.Time
		g.LastPID = d.PID
	}

	out := make([]DenialGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}

		if out[i].Operation != out[j].Operation {
			return out[i].Operation < out[j].Operation
		}

		return out[i].Path < out[j].Path
	})

	return out
}

// DescendantPIDs returns root plus all its transitive children, given a
// pid→ppid map. A session's agent spawns subprocesses (shells, tools), so
// filtering denials to one session means matching the whole process tree.
func DescendantPIDs(root int, parents map[int]int) map[int]bool {
	children := make(map[int][]int, len(parents))
	for pid, ppid := range parents {
		children[ppid] = append(children[ppid], pid)
	}

	result := map[int]bool{root: true}
	stack := []int{root}

	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, c := range children[p] {
			if !result[c] {
				result[c] = true
				stack = append(stack, c)
			}
		}
	}

	return result
}

// ProcessTree returns root plus all transitive child PIDs, by shelling out to
// `ps` for the live pid→ppid map. Used to scope denials to a graith session.
func ProcessTree(root int) (map[int]bool, error) {
	return processTree(root, runOutput)
}

func processTree(root int, run outputRunner) (map[int]bool, error) {
	out, err := run(psCommand, []string{"-axo", "pid=,ppid="})
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	return DescendantPIDs(root, parsePSPairs(out)), nil
}

// ancestorsIncludeRoot reports whether root is pid itself or one of its
// ancestors, walking the pid→ppid chain. A visited-set guards against a cycle
// or a self-parent so the walk always terminates.
func ancestorsIncludeRoot(pid, root int, parents map[int]int) bool {
	seen := make(map[int]bool)

	for pid > 0 {
		if pid == root {
			return true
		}

		if seen[pid] {
			return false
		}

		seen[pid] = true

		next, ok := parents[pid]
		if !ok {
			return false
		}

		pid = next
	}

	return false
}

// sessionMatcherTTL is how long a process-tree snapshot is reused before a
// fresh `ps`. Short enough to catch newly-spawned children quickly, long enough
// that a busy stream doesn't fork `ps` per denial.
const sessionMatcherTTL = time.Second

// SessionMatcher decides, live, whether a PID belongs to a session's process
// tree, for scoping a `watch` stream to one session. It keeps a short-TTL
// pid→ppid snapshot (refreshed via `ps`, shared across denials) and walks
// ancestry against it, so:
//
//   - a subprocess spawned *after* the stream starts is attributed once the
//     next snapshot sees it (rather than being cached out);
//   - one `ps` runs per TTL window, not per denial (bounds the fork cost);
//   - a transient `ps` failure is not turned into a permanent verdict — the
//     stale snapshot is reused and the next window retries.
//
// Confirmed-in-tree PIDs are remembered permanently (matched), so a short-lived
// child that has already exited still matches its late-arriving denials once it
// has been seen alive. A never-seen-alive exited child is inherently
// unattributable (the macOS log carries no session identity) — this is the
// documented best-effort limit, narrowed as far as `ps` allows.
type SessionMatcher struct {
	root     int
	matched  map[int]bool // PIDs confirmed in the tree (survives their exit)
	snapshot map[int]int  // last pid→ppid snapshot
	snapAt   time.Time    // when the snapshot was taken (zero = none yet)
	run      outputRunner
	now      func() time.Time
}

// NewSessionMatcher returns a matcher rooted at a session's PID.
func NewSessionMatcher(root int) *SessionMatcher {
	return newSessionMatcher(root, runOutput, time.Now)
}

func newSessionMatcher(root int, run outputRunner, now func() time.Time) *SessionMatcher {
	return &SessionMatcher{
		root:    root,
		matched: map[int]bool{root: true},
		run:     run,
		now:     now,
	}
}

// Matches reports whether pid is in the session's process tree.
func (m *SessionMatcher) Matches(pid int) bool {
	if m.matched[pid] {
		return true
	}

	m.refresh()

	if ancestorsIncludeRoot(pid, m.root, m.snapshot) {
		m.matched[pid] = true
		return true
	}

	// Not a match against the current snapshot. Don't cache the negative: the
	// PID may belong to a child that appears in a later snapshot, and a stale
	// snapshot (e.g. after a transient ps failure) shouldn't fix the verdict.
	return false
}

// refresh re-reads the process tree if the snapshot is missing or older than
// the TTL. A `ps` failure keeps the previous snapshot (best-effort) rather than
// clearing it, so a momentary hiccup doesn't blind the matcher.
func (m *SessionMatcher) refresh() {
	if m.snapshot != nil && m.now().Sub(m.snapAt) < sessionMatcherTTL {
		return
	}

	out, err := m.run(psCommand, []string{"-axo", "pid=,ppid="})
	if err != nil {
		return
	}

	m.snapshot = parsePSPairs(out)
	m.snapAt = m.now()
}

// parsePSPairs parses `ps -axo pid=,ppid=` output (two integer columns per
// line) into a pid→ppid map. Malformed lines are skipped.
func parsePSPairs(out string) map[int]int {
	parents := make(map[int]int)

	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		parents[pid] = ppid
	}

	return parents
}
