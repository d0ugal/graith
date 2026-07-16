package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/spf13/cobra"
)

var sandboxCmd = &cobra.Command{
	Use:   "sandbox",
	Short: "Inspect and debug the sandbox",
	Long: `Inspect and debug graith's OS sandbox.

Two questions, two subcommands:

  gr sandbox explain   Would a given access be allowed? (predictive)
                       Asks the backend's policy oracle against the profile
                       graith would generate. Needs an oracle → the "nono"
                       backend.

  gr sandbox watch     What did the sandbox actually deny? (retrospective)
                       Reads real denials from the OS. Needs an OS denial log
                       → macOS (Seatbelt), which covers both the "safehouse"
                       and "nono" backends on macOS.

Neither launches an agent or changes anything.`,
}

// --- gr sandbox explain (the policy oracle) ---

var (
	explainPath  string
	explainOp    string
	explainHost  string
	explainPort  int
	explainAgent string
)

var sandboxExplainCmd = &cobra.Command{
	Use:   "explain",
	Short: "Explain whether an access would be allowed (policy oracle)",
	Long: `Explain, predictively, whether an access would be allowed or denied.

Builds the profile graith would generate from your config (global merged with
an optional per-agent override) and asks the backend's policy oracle whether a
given filesystem or network access would be permitted, then explains why.

  gr sandbox explain --path ~/.ssh/id_rsa --op read
  gr sandbox explain --path ./src --op write
  gr sandbox explain --host github.com --port 443
  gr sandbox explain --agent codex --path /etc/hosts --op read

--op is one of read, write, or readwrite. Add --json for a machine-readable
decision.

This needs a policy oracle, which today only the "nono" backend provides. On a
"safehouse" config it errors and points you at "gr sandbox watch", which shows
what the Seatbelt sandbox actually denied.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runSandboxExplain()
	},
}

// --- gr sandbox watch (the OS denial log) ---

var (
	watchRecent bool
	watchFollow bool
	watchSince  string
	watchProc   string
)

var sandboxWatchCmd = &cobra.Command{
	Use:   "watch [session]",
	Short: "Watch what the sandbox actually denied (OS denial log)",
	Long: `Watch the sandbox denials the OS recorded — live, or over a recent window.

The macOS Seatbelt sandbox logs every denial to the unified log; this taps it
so you can see exactly which paths and operations were blocked. This is the way
to debug a confusing "permission denied", and the only way under the
"safehouse" backend (which has no policy oracle — see "gr sandbox explain").

  gr sandbox watch                   # live-tail denials (Ctrl-C to stop)
  gr sandbox watch --recent          # recent denials instead (default 5m)
  gr sandbox watch --recent --since 1h
  gr sandbox watch --follow          # force live-tail even when piped
  gr sandbox watch my-session        # scope to a session's process tree
  gr sandbox watch --proc node       # filter by process-name substring

--recent aggregates identical denials with a repeat count; live mode prints
each as it arrives. Passing --since implies --recent. Live-tail is the default
on a terminal; when output is piped or in --json (agent) mode it defaults to
--recent instead, so it can't hang forever with nowhere interactive to stop it
— pass --follow to force a live tail there. Add --json for machine-readable
output (one NDJSON object per denial when live, an aggregate when --recent).

This is macOS-only — it relies on Seatbelt and unified logging. Because
/usr/bin/log refuses to run inside a sandbox, run this from your normal shell,
not from within a sandboxed agent session.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		session := ""
		if len(args) == 1 {
			session = args[0]
		}

		recent, err := resolveWatchRecent(
			watchRecent, watchFollow, cmd.Flags().Changed("since"), stdoutIsTTY())
		if err != nil {
			return err
		}

		return runSandboxWatch(session, recent)
	},
}

// sandboxWhyCmd is a hidden tombstone for the removed `gr sandbox why` command.
// It disables flag parsing so an old invocation (e.g. `gr sandbox why --path
// …`) reaches RunE and gets a clear "renamed" pointer rather than an unknown-flag
// error or the parent help.
var sandboxWhyCmd = &cobra.Command{
	Use:                "why",
	Hidden:             true,
	DisableFlagParsing: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return errors.New("`gr sandbox why` has been split: use `gr sandbox explain` (would an access be allowed?) " +
			"or `gr sandbox watch` (what did the sandbox actually deny?)")
	},
}

// whyOutput is the JSON shape emitted by `gr sandbox explain --json`.
type whyOutput struct {
	Backend     string   `json:"backend"`
	Query       whyQuery `json:"query"`
	Allowed     bool     `json:"allowed"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
	Details     string   `json:"details,omitempty"`
	Source      string   `json:"source,omitempty"`
	Suggested   string   `json:"suggested_flag,omitempty"`
	Explanation string   `json:"explanation"`
	Warnings    []string `json:"warnings,omitempty"`
}

type whyQuery struct {
	Path string `json:"path,omitempty"`
	Op   string `json:"op,omitempty"`
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

func runSandboxExplain() error {
	// Expand ~/$HOME in the query path so it matches the absolute, expanded
	// paths in the profile the daemon generates. The --help examples use ~, so
	// an unexpanded ~ would otherwise misreport path_not_granted.
	queryPath := explainPath
	if queryPath != "" {
		queryPath = config.ExpandPath(queryPath)
	}

	query := sandbox.WhyQuery{Path: queryPath, Op: explainOp, Host: explainHost, Port: explainPort}
	if err := query.Validate(); err != nil {
		return err
	}

	merged := cfg.Sandbox
	if explainAgent != "" {
		agent, ok := cfg.Agents[explainAgent]
		if !ok {
			return fmt.Errorf(
				"unknown agent %q; configured agents are: %s",
				explainAgent, knownAgentNames(cfg.Agents))
		}

		merged = cfg.Sandbox.Merge(agent.Sandbox)
	}

	if merged.Backend != sandbox.BackendNono {
		backend := merged.Backend
		if backend == "" {
			backend = "(unset)"
		}

		return fmt.Errorf(
			"the %q backend has no policy oracle, so `gr sandbox explain` can't answer this; "+
				"use `gr sandbox watch` to see what it actually denied (configured backend is %q)",
			backend, backend)
	}

	// Confirm nono can be reached before building a profile, so the error is
	// about the backend, not a stray decision.
	req := sandbox.Requirements{Network: merged.Network.IsSet()}

	avail, err := sandbox.CheckAvailability(merged.Backend, merged.Command, req)
	if err != nil {
		return err
	}

	if !avail.CanEnforce {
		return fmt.Errorf("nono backend cannot enforce here: %s", avail.Detail)
	}

	opts := whyWrapOpts(merged)

	profilePath, warnings, err := sandbox.BuildQueryProfile(opts)
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(profilePath) }()

	res, err := sandbox.WhyForProfile(merged.Command, profilePath, query)
	if err != nil {
		return err
	}

	return renderWhy(merged.Backend, query, res, warnings)
}

// runSandboxWatch reads Seatbelt sandbox denials from the macOS unified log.
// When recent is true it shows an aggregated recent window; otherwise it
// live-tails. When session is set, denials are scoped to that session's process
// tree; watchProc narrows by process-name substring.
// Seams over the exec/daemon-bound calls, so runSandboxWatch's dispatch is
// testable without the macOS `log`/`ps` binaries or a live daemon.
var (
	denialLogSupportedFn = sandbox.DenialLogSupported
	resolveSessionPIDFn  = resolveSessionPID
	processTreeFn        = sandbox.ProcessTree
	watchRecentFn        = watchRecentDenials
	watchLiveFn          = watchLiveDenials
)

func runSandboxWatch(session string, recent bool) error {
	if err := denialLogSupportedFn(); err != nil {
		return err
	}

	if !recent {
		var matcher *sandbox.SessionMatcher

		if session != "" {
			pid, err := resolveSessionPIDFn(session)
			if err != nil {
				return err
			}

			matcher = sandbox.NewSessionMatcher(pid)
		}

		return watchLiveFn(matcher)
	}

	var pids map[int]bool

	if session != "" {
		pid, err := resolveSessionPIDFn(session)
		if err != nil {
			return err
		}

		tree, err := processTreeFn(pid)
		if err != nil {
			return err
		}

		pids = tree

		// The macOS log records no session identity, only (process, PID), so a
		// live process-tree snapshot can't faithfully reconstruct which past
		// denials belonged to the session: children that have since exited are
		// gone, and a recycled PID can be misattributed. Warn and steer toward
		// the more reliable scoping tools.
		if !jsonOutput {
			out.Printf("note: session scoping of past denials is best-effort — it matches only processes still running now, so denials from exited children may be missing and recycled PIDs misattributed. Use --proc, or a live `watch` (scopes more reliably), when precision matters.\n\n")
		}
	}

	return watchRecentFn(pids)
}

// resolveWatchRecent decides between the recent (windowed) and live-tail modes
// and validates the mode flags. --recent or a user-set --since selects recent;
// --follow forces live and conflicts with them. With no explicit mode, live is
// the default on a terminal but recent off one (a pipe or agent --json), so a
// non-interactive `gr sandbox watch` can't hang forever with nowhere to stop it.
func resolveWatchRecent(recentFlag, followFlag, sinceChanged, stdoutTTY bool) (bool, error) {
	recent := recentFlag || sinceChanged

	if followFlag && recent {
		return false, errors.New("--follow cannot be combined with --recent/--since")
	}

	if followFlag {
		return false, nil
	}

	if recent {
		return true, nil
	}

	return !stdoutTTY, nil
}

// stdoutIsTTY reports whether stdout is an interactive terminal (not a pipe,
// file, or agent capture).
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}

	return fi.Mode()&os.ModeCharDevice != 0
}

// resolveSessionPID resolves a session name/ID to its running PID via the
// daemon's diagnostics, so denials can be scoped to that session.
func resolveSessionPID(session string) (int, error) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return 0, err
	}
	defer c.Close()

	if err := c.SendControl("diagnostics", struct{}{}); err != nil {
		return 0, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return 0, err
	}

	var diag protocol.DiagnosticsMsg
	if err := protocol.DecodePayload(resp, &diag); err != nil {
		return 0, err
	}

	return pidFromDiagnostics(diag, session)
}

// pidFromDiagnostics finds the live PID for a session name/ID in a diagnostics
// snapshot. Split from the daemon roundtrip so the match/liveness logic is
// testable without a daemon.
func pidFromDiagnostics(diag protocol.DiagnosticsMsg, session string) (int, error) {
	for _, s := range diag.Sessions {
		if s.ID != session && s.Name != session {
			continue
		}

		if s.PID <= 0 || !s.PIDAlive {
			return 0, fmt.Errorf("session %q has no running process to scope denials to", session)
		}

		return s.PID, nil
	}

	return 0, fmt.Errorf("no session matched %q", session)
}

func watchRecentDenials(pids map[int]bool) error {
	denials, err := sandbox.RecentDenials(watchSince)
	if err != nil {
		return err
	}

	return renderRecentDenials(denials, pids)
}

// renderRecentDenials filters, aggregates, and prints a recent-window denial
// set (human table or --json aggregate). Split from the fetch so the rendering
// logic is testable without the macOS `log` binary.
func renderRecentDenials(denials []sandbox.Denial, pids map[int]bool) error {
	denials = sandbox.FilterByPIDs(denials, pids)
	denials = sandbox.FilterByProcess(denials, watchProc)

	groups := sandbox.AggregateDenials(denials)

	if jsonOutput {
		return out.JSON(denialsOutput(watchSince, groups))
	}

	if len(groups) == 0 {
		out.Printf("No sandbox denials in the last %s.\n", watchSince)
		return nil
	}

	out.Printf("Sandbox denials in the last %s (most frequent first):\n\n", watchSince)

	for _, g := range groups {
		out.Printf("%s\n", formatDenialGroup(g))
	}

	return nil
}

// watchLiveDenials live-tails denials. matcher (nil = no session scope) decides
// membership per denial against a live process tree, so a subprocess spawned
// after the stream starts is still attributed to the session. In --json mode
// each denial is emitted as one NDJSON line (plain Printf is suppressed under
// --json, which agents enable by default). A write failure (e.g. a broken
// output pipe) propagates out of the callback so the stream stops promptly.
func watchLiveDenials(matcher *sandbox.SessionMatcher) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Printf is a no-op under --json; only announce in human mode.
	out.Printf("Watching sandbox denials (Ctrl-C to stop)...\n")

	err := sandbox.StreamDenials(ctx, liveDenialHandler(matcher))

	// A cancelled context (Ctrl-C) is the normal way to stop; don't report it.
	if err != nil && ctx.Err() != nil {
		return nil
	}

	return err
}

// liveDenialHandler returns the per-denial callback for a live watch: it applies
// the session (matcher) and --proc filters, then writes one NDJSON line
// (--json) or a formatted human line. A write error propagates so StreamDenials
// stops the tail. Split out so the filter/format/write logic is testable without
// a live `log stream`.
func liveDenialHandler(matcher *sandbox.SessionMatcher) func(sandbox.Denial) error {
	return func(d sandbox.Denial) error {
		if matcher != nil && !matcher.Matches(d.PID) {
			return nil
		}

		if !sandbox.ProcessNameMatches(d.Process, watchProc) {
			return nil
		}

		if jsonOutput {
			return out.JSONLine(streamDenialJSON(d))
		}

		out.Printf("%s\n", formatDenial(d))

		return nil
	}
}

// formatDenial renders a single denial as one line: [time] process(pid) op path.
// The timestamp is included when present so a human tailing the feed can
// correlate a denial with what they just did.
func formatDenial(d sandbox.Denial) string {
	line := fmt.Sprintf("%s(%d) %s", d.Process, d.PID, d.Operation)
	if d.Path != "" {
		line += " " + d.Path
	}

	if d.Time != "" {
		line = d.Time + "  " + line
	}

	return line
}

// formatDenialGroup renders an aggregated denial with its repeat count.
func formatDenialGroup(g sandbox.DenialGroup) string {
	line := fmt.Sprintf("%4d× %s", g.Count, g.Operation)
	if g.Path != "" {
		line += " " + g.Path
	}

	line += fmt.Sprintf("  [%s]", g.Process)

	return line
}

// streamDenialLine is the --json (NDJSON) shape for a single live denial.
type streamDenialLine struct {
	Time      string `json:"time,omitempty"`
	Process   string `json:"process"`
	PID       int    `json:"pid"`
	Operation string `json:"operation"`
	Path      string `json:"path,omitempty"`
}

func streamDenialJSON(d sandbox.Denial) streamDenialLine {
	return streamDenialLine{
		Time:      d.Time,
		Process:   d.Process,
		PID:       d.PID,
		Operation: d.Operation,
		Path:      d.Path,
	}
}

// denialJSON is the --json shape for one aggregated denial group.
type denialJSON struct {
	Process   string `json:"process"`
	Operation string `json:"operation"`
	Path      string `json:"path,omitempty"`
	Count     int    `json:"count"`
	LastPID   int    `json:"last_pid,omitempty"`
	LastTime  string `json:"last_time,omitempty"`
}

// denialsReport is the `gr sandbox watch --recent --json` shape. Source is the
// denial feed (not a backend name) because the macOS log serves both the
// safehouse and nono-on-macOS backends.
type denialsReport struct {
	Source  string       `json:"source"`
	Since   string       `json:"since"`
	Denials []denialJSON `json:"denials"`
	Count   int          `json:"count"`
}

func denialsOutput(since string, groups []sandbox.DenialGroup) denialsReport {
	// Initialize Denials to a non-nil slice so an empty result marshals as
	// "denials": [] rather than "denials": null.
	rep := denialsReport{Source: "macos-unified-log", Since: since, Denials: []denialJSON{}}

	for _, g := range groups {
		rep.Denials = append(rep.Denials, denialJSON{
			Process:   g.Process,
			Operation: g.Operation,
			Path:      g.Path,
			Count:     g.Count,
			LastPID:   g.LastPID,
			LastTime:  g.LastTime,
		})
		rep.Count += g.Count
	}

	return rep
}

// whyWrapOpts builds the policy-relevant WrapOpts for an explain query. It
// mirrors the daemon's expansion (ExpandPath + globbing) but omits
// session-specific grants (hook dir, runtime dir, agent binary dir) that the
// daemon adds at launch — those are not part of the user-configured policy the
// user is reasoning about. PATH/HOME are added because the daemon always
// includes them and a query against the env would otherwise misreport.
// ReadFiles/WriteFiles (single-file grants) are included so the query matches
// the profile the daemon actually generates — omitting them produced false
// path_not_granted denials for granted files (e.g. ~/.claude.json).
func whyWrapOpts(merged config.SandboxConfig) sandbox.WrapOpts {
	envKeys := ensureWhyEnvKeys(nil, "PATH", "HOME")

	opts := sandbox.WrapOpts{
		Backend:        merged.Backend,
		ReadDirs:       expandWhyPaths(merged.ReadDirs),
		WriteDirs:      expandWhyPaths(merged.WriteDirs),
		ReadFiles:      expandWhyFilePaths(merged.ReadFiles),
		WriteFiles:     expandWhyFilePaths(merged.WriteFiles),
		Features:       merged.Features,
		EnvKeys:        envKeys,
		SignalMode:     merged.SignalMode,
		Profile:        merged.Profile,
		BackendCommand: merged.Command,
	}

	if merged.Network.IsSet() {
		opts.Network = &sandbox.NetworkPolicy{
			Block:        merged.Network.Block,
			AllowDomains: merged.Network.AllowDomains,
		}
	}

	return opts
}

// expandWhyPaths mirrors the daemon's expandPaths: ~-expand, make absolute,
// glob-expand, and drop entries that do not exist (matching the lenient run
// behaviour). It has no logger, so silent skips are fine for a read-only query.
func expandWhyPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	var out []string

	for _, p := range paths {
		expanded := config.ExpandPath(p)

		if strings.ContainsAny(expanded, "*?[") {
			if matches, err := filepath.Glob(expanded); err == nil {
				for _, m := range matches {
					if _, statErr := os.Stat(m); statErr == nil {
						out = append(out, m)
					}
				}
			}

			continue
		}

		if _, err := os.Stat(expanded); err == nil {
			out = append(out, expanded)
		}
	}

	return out
}

// expandWhyFilePaths mirrors the daemon's expandFilePaths: ~-expand, make
// absolute, and glob-expand, but — unlike expandWhyPaths — it keeps a literal
// path that does not exist on disk. A writable file grant is routinely for a
// file the agent creates at runtime (e.g. Claude's ~/.claude.json.lock), so
// stat-filtering would drop the grant and make the query misreport. Globs that
// match nothing are still skipped, since there is nothing to grant.
func expandWhyFilePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	var out []string

	for _, p := range paths {
		expanded := config.ExpandPath(p)

		if strings.ContainsAny(expanded, "*?[") {
			if matches, err := filepath.Glob(expanded); err == nil {
				out = append(out, matches...)
			}

			continue
		}

		out = append(out, expanded)
	}

	return out
}

func ensureWhyEnvKeys(keys []string, want ...string) []string {
	have := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		have[k] = struct{}{}
	}

	for _, w := range want {
		if _, ok := have[w]; !ok {
			keys = append(keys, w)
			have[w] = struct{}{}
		}
	}

	return keys
}

func renderWhy(backend string, q sandbox.WhyQuery, res sandbox.WhyResult, warnings []string) error {
	if jsonOutput {
		return out.JSON(whyOutput{
			Backend:     backend,
			Query:       whyQuery{Path: q.Path, Op: q.Op, Host: q.Host, Port: q.Port},
			Allowed:     res.Allowed(),
			Status:      res.Status,
			Reason:      res.Reason,
			Details:     res.Details,
			Source:      whySource(res),
			Suggested:   res.SuggestFlag,
			Explanation: res.Explanation(),
			Warnings:    warnings,
		})
	}

	for _, w := range warnings {
		out.Printf("warning: %s\n", w)
	}

	subject := q.Path
	if subject == "" {
		subject = q.Host
	}

	verb := q.Op
	if verb == "" {
		verb = "connect"
	}

	out.Printf("%s %s: %s\n", verb, subject, res.Explanation())

	return nil
}

// knownAgentNames returns the configured agent names, sorted, for use in the
// unknown-agent error. If none are configured it returns "(none)" so the error
// still reads sensibly.
func knownAgentNames(agents map[string]config.Agent) string {
	if len(agents) == 0 {
		return "(none)"
	}

	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}

	sort.Strings(names)

	return strings.Join(names, ", ")
}

func whySource(res sandbox.WhyResult) string {
	if res.Source != "" {
		return res.Source
	}

	return res.PolicySource
}

// registerSandboxCmd registers this command on rootCmd. Called from registerCommands.
func registerSandboxCmd() {
	sandboxExplainCmd.Flags().StringVar(&explainPath, "path", "", "filesystem path to check")
	sandboxExplainCmd.Flags().StringVar(&explainOp, "op", "", "operation for --path: read, write, or readwrite")
	sandboxExplainCmd.Flags().StringVar(&explainHost, "host", "", "network host to check (e.g. github.com)")
	sandboxExplainCmd.Flags().IntVar(&explainPort, "port", 0, "network port for --host (default 443)")
	sandboxExplainCmd.Flags().StringVar(&explainAgent, "agent", "", "resolve the policy for this agent's merged sandbox config")

	sandboxWatchCmd.Flags().BoolVar(&watchRecent, "recent", false, "show recent denials over a window instead of live-tailing")
	sandboxWatchCmd.Flags().BoolVarP(&watchFollow, "follow", "f", false, "force a live tail even when output is piped or in --json mode")
	sandboxWatchCmd.Flags().StringVar(&watchSince, "since", "5m", "recent window for --recent (a log show --last duration, e.g. 5m, 1h); implies --recent")
	sandboxWatchCmd.Flags().StringVar(&watchProc, "proc", "", "filter denials to processes whose name contains this substring")

	sandboxCmd.AddCommand(sandboxExplainCmd)
	sandboxCmd.AddCommand(sandboxWatchCmd)
	sandboxCmd.AddCommand(sandboxWhyCmd)
	rootCmd.AddCommand(sandboxCmd)
}
