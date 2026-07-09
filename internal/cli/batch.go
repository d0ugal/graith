package cli

import (
	"bufio"
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
	cmd.Flags().StringVar(&bf.repo, "repo", "", "filter by repo name")
	cmd.Flags().BoolVar(&bf.stopped, "stopped", false, "match stopped and errored sessions")
	cmd.Flags().StringVar(&bf.stale, "stale", "", "match sessions not attached for this duration (e.g. 7d, 24h)")
	cmd.Flags().BoolVarP(&bf.force, "force", "f", false, "skip confirmation prompt")
}

func (bf *batchFlags) active() bool {
	return bf.repo != "" || bf.stopped || bf.stale != ""
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
		if bf.repo != "" && s.RepoName != bf.repo {
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

// runBatch performs a batch operation (stop or delete) over the sessions that
// match bf's filters. verb/pastTense/gerund provide the human-readable words
// ("stop"/"stopped"/"stopping"), controlType is the control message name and
// payload builds the per-session message to send. Starred sessions are skipped.
func runBatch(cmd *cobra.Command, bf *batchFlags, verb, pastTense, gerund, controlType string, payload func(sessionID string) any) error {
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

	if len(matched) == 0 {
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
