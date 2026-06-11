package cli

import (
	"bufio"
	"fmt"
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
	expanded := dayPattern.ReplaceAllStringFunc(s, func(match string) string {
		numStr := match[:len(match)-1]
		n, _ := strconv.Atoi(numStr)
		return fmt.Sprintf("%dh", n*24)
	})
	return time.ParseDuration(expanded)
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

func confirmBatch(cmd *cobra.Command, verb string, pastTense string, sessions []protocol.SessionInfo) (bool, error) {
	n := len(sessions)

	if out.IsJSON() {
		return false, fmt.Errorf("use --force to %s %d sessions", verb, n)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("use --force to %s %d sessions", verb, n)
	}

	out.Print("The following %d sessions will be %s:\n\n", n, pastTense)

	now := time.Now()
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tAGENT\tREPO\tSTATUS\tLAST ATTACHED\tDIRTY\tUNPUSHED")
	for _, s := range sessions {
		lastAttached := "never"
		if s.LastAttachedAt != "" {
			if t, err := time.Parse(time.RFC3339, s.LastAttachedAt); err == nil {
				lastAttached = client.ShortDuration(now.Sub(t)) + " ago"
			}
		}

		dirty := "no"
		if s.Dirty {
			dirty = "yes"
		}

		unpushed := "—"
		if s.UnpushedCount > 0 {
			unpushed = fmt.Sprintf("%d commits", s.UnpushedCount)
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, s.Agent, s.RepoName, s.Status, lastAttached, dirty, unpushed)
	}
	tw.Flush()

	prompt := strings.ToUpper(verb[:1]) + verb[1:]
	out.Print("\n%s %d sessions? [y/N] ", prompt, n)

	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		out.Print("Aborted\n")
		return false, nil
	}
	return true, nil
}
