package cli

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type batchFlags struct {
	repo    string
	stopped bool
	stale   string
	force   bool
}

func addBatchFlags(cmd *cobra.Command, bf *batchFlags) {
	cmd.Flags().StringVar(&bf.repo, "repo", "", "filter by repo name or path")
	cmd.Flags().BoolVar(&bf.stopped, "stopped", false, "match stopped and errored sessions")
	cmd.Flags().StringVar(&bf.stale, "stale", "", "match sessions not attached for this duration (e.g. 7d, 24h)")
	cmd.Flags().BoolVarP(&bf.force, "force", "f", false, "skip confirmation prompt")
}

func (bf *batchFlags) active() bool {
	return bf.repo != "" || bf.stopped || bf.stale != ""
}

// selfSessionRef returns a name-or-id reference for the current session from the
// environment, backing the `--self` flag on gr delete/stop/purge. It prefers
// the canonical GRAITH_SESSION_ID and falls back to GRAITH_SESSION_NAME. It
// errors when neither is set — i.e. `--self` was used outside a graith session.
func selfSessionRef() (string, error) {
	if id := os.Getenv("GRAITH_SESSION_ID"); id != "" {
		return id, nil
	}

	if name := os.Getenv("GRAITH_SESSION_NAME"); name != "" {
		return name, nil
	}

	return "", errors.New("--self requires GRAITH_SESSION_ID or GRAITH_SESSION_NAME to be set; run it from inside a graith session")
}

// selfArgs resolves the positional args for a `--self` invocation. When self is
// set it discards any positional args (the Args validator has already rejected
// them) and substitutes the current session reference from the environment, so
// the caller's normal name-or-id resolution targets the calling session. When
// self is unset it returns args unchanged. Shared by gr delete/stop/purge so
// the substitution — and its outside-a-session error — lives in one tested spot.
func selfArgs(self bool, args []string) ([]string, error) {
	if !self {
		return args, nil
	}

	ref, err := selfSessionRef()
	if err != nil {
		return nil, err
	}

	return []string{ref}, nil
}

// selfChildrenBatchArgs builds the shared positional-args validator for the
// destructive session verbs (delete/stop/purge), which all expose the same
// --self / --children / batch-filter surface. The flag values are read through
// pointers so the returned validator reflects them at parse time. Precedence:
//   - --self is exclusive with --children and batch filters, and takes no arg;
//   - --children with a batch filter is rejected;
//   - a batch filter takes no positional arg;
//   - --children allows zero or one arg (zero auto-resolves the current session);
//   - otherwise exactly one name-or-id is required.
func selfChildrenBatchArgs(self, children *bool, bf *batchFlags) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if *self {
			if *children || bf.active() {
				return errors.New("--self cannot be combined with --children or batch filters")
			}

			return cobra.NoArgs(cmd, args)
		}

		if *children && bf.active() {
			return errors.New("--children cannot be combined with batch filters")
		}

		if bf.active() {
			return cobra.NoArgs(cmd, args)
		}

		if *children {
			return cobra.MaximumNArgs(1)(cmd, args)
		}

		return cobra.ExactArgs(1)(cmd, args)
	}
}

var dayPattern = regexp.MustCompile(`(\d+)d`)

func parseStaleDuration(s string) (time.Duration, error) {
	var convErr error

	expanded := dayPattern.ReplaceAllStringFunc(s, func(match string) string {
		numStr := match[:len(match)-1]

		n, err := strconv.Atoi(numStr)
		if err != nil {
			convErr = fmt.Errorf("invalid day count %q: %w", numStr, err)

			return match
		}

		if n <= 0 {
			convErr = fmt.Errorf("day count must be positive, got %d", n)

			return match
		}

		if n > math.MaxInt/24 {
			convErr = fmt.Errorf("day count %d is too large", n)

			return match
		}

		return fmt.Sprintf("%dh", n*24)
	})

	if convErr != nil {
		return 0, convErr
	}

	d, err := time.ParseDuration(expanded)
	if err != nil {
		return 0, err
	}
	// A non-positive duration (e.g. "-1d", "-6h", "0h") would make every
	// session match in filterSessions, the same match-everything hazard as
	// an overflowing day count. Reject it. The day-count guard above only
	// catches unsigned day values because the regex matches digits only, so
	// signed and non-day units are handled here.
	if d <= 0 {
		return 0, fmt.Errorf("stale duration must be positive, got %q", s)
	}

	return d, nil
}

func filterSessions(sessions []protocol.SessionInfo, bf *batchFlags) ([]protocol.SessionInfo, error) {
	var result []protocol.SessionInfo

	var staleDuration time.Duration

	if bf.stale != "" {
		var err error

		staleDuration, err = parseStaleDuration(bf.stale)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now()

	for _, s := range sessions {
		if bf.repo != "" && !matchesRepo(s, bf.repo) {
			continue
		}

		if bf.stopped && s.Status != "stopped" && s.Status != "errored" {
			continue
		}

		if bf.stale != "" {
			ts := s.LastAttachedAt
			if ts == "" {
				ts = s.CreatedAt
			}

			if ts == "" {
				continue
			}

			t, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				continue
			}

			if now.Sub(t) <= staleDuration {
				continue
			}
		}

		result = append(result, s)
	}

	return result, nil
}

// noOpSkip reports whether a matched session should be treated as a no-op for
// the operation (e.g. stopping an already-stopped session) and, if so, a short
// human-readable reason. Returning ("", false) means the session is actionable.
// A nil noOpSkip means every matched session is actionable.
type noOpSkip func(protocol.SessionInfo) (reason string, skip bool)

// stopNoOpSkip treats non-running sessions as no-ops for batch stop: stopping a
// session that is already stopped/errored (or otherwise not running) is a
// success by intent, so skip it with a note rather than erroring (#203).
func stopNoOpSkip(s protocol.SessionInfo) (string, bool) {
	if s.Status == "running" {
		return "", false
	}

	if s.Status == "" {
		return "not running", true
	}

	return fmt.Sprintf("not running (%s)", s.Status), true
}

// partitionNoOp splits matched sessions into the actionable ones and a list of
// "<name>: <reason>" notes for the no-ops, using noOp to classify each. A nil
// noOp leaves every session actionable and returns no notes.
func partitionNoOp(matched []protocol.SessionInfo, noOp noOpSkip) ([]protocol.SessionInfo, []string) {
	if noOp == nil {
		return matched, nil
	}

	var (
		actionable []protocol.SessionInfo
		skips      []string
	)

	for _, s := range matched {
		if reason, skip := noOp(s); skip {
			skips = append(skips, fmt.Sprintf("%s: %s", s.Name, reason))
			continue
		}

		actionable = append(actionable, s)
	}

	return actionable, skips
}

// runBatch performs a batch operation (stop or delete) over the sessions that
// match bf's filters. verb/pastTense/gerund provide the human-readable words
// ("stop"/"stopped"/"stopping"), controlType is the control message name and
// payload builds the per-session message to send. Starred sessions are skipped.
// noOp, when non-nil, marks matched sessions the operation cannot meaningfully
// act on (e.g. already-stopped sessions for a stop); they are skipped with a
// note instead of being sent to the daemon.
func runBatch(cmd *cobra.Command, bf *batchFlags, verb, pastTense, gerund, controlType string, payload func(sessionID string) any, noOp noOpSkip) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	_ = c.SendControl("list", struct{}{})

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return err
	}

	matched, err := filterSessions(list.Sessions, bf)
	if err != nil {
		return err
	}

	// Split matched sessions into the ones the operation can act on and the
	// no-ops (e.g. already-stopped sessions for a stop). No-ops are reported as
	// skipped notes rather than sent to the daemon, so a mixed filter still acts
	// on the actionable sessions instead of aborting on the first no-op (#203).
	matched, noOpSkips := partitionNoOp(matched, noOp)

	if len(matched) == 0 {
		if len(noOpSkips) > 0 {
			out.Printf("No sessions to %s\n", verb)

			for _, note := range noOpSkips {
				out.Printf("Skipped %s\n", note)
			}

			return nil
		}

		out.Printf("No sessions match the given filters\n")

		return nil
	}

	if !bf.force {
		confirmed, err := confirmBatch(cmd, verb, pastTense, matched)
		if err != nil {
			return err
		}

		if !confirmed {
			return nil
		}
	}

	res, transportErr := executeBatch(c, matched, controlType, payload)
	res.noOps = noOpSkips

	printBatchSummary(pastTense, gerund, res)

	// A transport-level failure desyncs the stream, so no further sessions
	// could be processed — surface it after reporting what did complete.
	if transportErr != nil {
		return transportErr
	}

	// Partial failure must still exit non-zero so scripts and orchestrators
	// can detect that not every session was affected.
	if len(res.failed) > 0 {
		attempted := len(res.succeeded) + len(res.failed)

		return fmt.Errorf("%d of %d sessions could not be %s", len(res.failed), attempted, pastTense)
	}

	return nil
}

// batchConn is the subset of *client.Client that the batch execution loop
// needs. It is an interface so the loop can be unit-tested without a live
// daemon connection.
type batchConn interface {
	SendControl(msgType string, payload any) error
	ReadControlResponse() (protocol.Envelope, error)
}

// batchFailure records a session whose per-session operation the daemon
// rejected, along with the daemon's error message.
type batchFailure struct {
	name string
	msg  string
}

// batchResults collects the per-session outcomes of a batch operation.
type batchResults struct {
	succeeded []string
	failed    []batchFailure
	skipped   []string
	// noOps holds "<name>: <reason>" notes for matched sessions the operation
	// could not meaningfully act on and skipped without contacting the daemon
	// (e.g. already-stopped sessions for a stop — issue #203).
	noOps []string
}

// executeBatch runs controlType against every matched session, collecting a
// per-session result instead of aborting on the first daemon error. Starred
// sessions are skipped without contacting the daemon. A daemon "error"
// response is recorded as a failure and processing continues, so earlier
// successes are never hidden by a later failure (issue #201).
//
// A transport-level error (SendControl / ReadControlResponse failing) is
// returned separately: it means the connection or framing is broken, so the
// stream is desynced and no further sessions can be processed reliably. The
// results gathered before that point are still returned so the caller can
// report them.
func executeBatch(c batchConn, matched []protocol.SessionInfo, controlType string, payload func(sessionID string) any) (batchResults, error) {
	var res batchResults

	for _, s := range matched {
		if s.Starred {
			res.skipped = append(res.skipped, s.Name)
			continue
		}

		if err := c.SendControl(controlType, payload(s.ID)); err != nil {
			return res, fmt.Errorf("sending request for %s: %w", s.Name, err)
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return res, fmt.Errorf("reading response for %s: %w", s.Name, err)
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			res.failed = append(res.failed, batchFailure{name: s.Name, msg: e.Message})

			continue
		}

		res.succeeded = append(res.succeeded, s.Name)
	}

	return res, nil
}

// printBatchSummary reports how many sessions succeeded, then lists the
// starred sessions that were skipped and the sessions that failed (with the
// daemon's error). pastTense is e.g. "deleted"/"stopped"; gerund is e.g.
// "deleting"/"stopping".
func printBatchSummary(pastTense, gerund string, res batchResults) {
	out.Printf("%s %d sessions\n", strings.ToUpper(pastTense[:1])+pastTense[1:], len(res.succeeded))

	for _, name := range res.skipped {
		out.Printf("Skipped starred session: %s\n", name)
	}

	for _, note := range res.noOps {
		out.Printf("Skipped %s\n", note)
	}

	for _, f := range res.failed {
		out.Printf("Failed %s %s: %s\n", gerund, f.name, f.msg)
	}
}

func confirmBatch(cmd *cobra.Command, verb string, pastTense string, sessions []protocol.SessionInfo) (bool, error) {
	n := len(sessions)

	if out.IsJSON() {
		return false, fmt.Errorf("use --force to %s %d sessions", verb, n)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("use --force to %s %d sessions", verb, n)
	}

	out.Printf("The following %d sessions will be %s:\n\n", n, pastTense)

	// The daemon's background refresh loop skips non-running sessions, so the
	// Dirty/UnpushedCount fields on SessionInfo are stale for stopped sessions
	// (#209). Recompute them with live git checks so the table reflects the
	// real deletion risk.
	now := time.Now()
	anyGitFailed := false
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tAGENT\tREPO\tSTATUS\tLAST ATTACHED\tDIRTY\tUNPUSHED")

	for _, s := range sessions {
		lastAttached := "never"

		if s.LastAttachedAt != "" {
			if t, err := time.Parse(time.RFC3339, s.LastAttachedAt); err == nil {
				lastAttached = client.ShortDuration(now.Sub(t)) + " ago"
			}
		}

		st := liveSessionStatus(s)

		// A known-dirty result survives a partial git failure: if any repo's
		// check confirmed dirt we still say "yes". Only genuinely-unknown
		// columns render as "?".
		dirty := "no"
		if st.dirty {
			dirty = "yes"
		} else if st.gitFailed {
			dirty = "?"
		}

		unpushed := "—"
		if st.gitFailed {
			// The count is incomplete when a check failed, so don't imply a
			// precise figure.
			unpushed = "?"
		} else if st.unpushed > 0 {
			unpushed = fmt.Sprintf("%d commits", st.unpushed)
		}

		if st.gitFailed {
			anyGitFailed = true
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, s.Agent, s.RepoName, s.Status, lastAttached, dirty, unpushed)
	}

	_ = tw.Flush()

	if anyGitFailed {
		out.Printf("\nWarning: could not check git status for sessions marked \"?\" — dirty/unpushed state is unknown\n")
	}

	prompt := strings.ToUpper(verb[:1]) + verb[1:]
	out.Printf("\n%s %d sessions? [y/N] ", prompt, n)

	reader := bufio.NewReader(os.Stdin)

	answer, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		out.Printf("Aborted\n")
		return false, nil
	}

	return true, nil
}
