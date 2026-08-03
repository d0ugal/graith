package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var triggerCmd = &cobra.Command{
	Use:   "trigger",
	Short: "Inspect and control daemon-fired triggers",
	Long: "Triggers fire daemon-side actions on a schedule (cron/interval) or on " +
		"file changes in a session worktree. Definitions live in config.toml; this " +
		"command lists, inspects, and controls them.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var triggerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured triggers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := cliRequest("trigger_list", protocol.TriggerListMsg{})
		if err != nil {
			return err
		}

		var listResp protocol.TriggerListResponse

		_ = protocol.DecodePayload(resp, &listResp)

		if jsonOutput {
			return out.JSON(listResp)
		}

		renderTriggerList(os.Stdout, listResp.Triggers)

		return nil
	},
}

var triggerStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show detail for one trigger",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := cliRequest("trigger_status", protocol.TriggerStatusMsg{Name: args[0]})
		if err != nil {
			return err
		}

		var statusResp protocol.TriggerStatusResponse

		_ = protocol.DecodePayload(resp, &statusResp)

		if jsonOutput {
			return out.JSON(statusResp)
		}

		renderTriggerStatus(os.Stdout, statusResp.Trigger)

		return nil
	},
}

var triggerRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Fire a schedule trigger once, now (respects overlap)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := cliRequest("trigger_run", protocol.TriggerRunMsg{Name: args[0]}); err != nil {
			return err
		}

		out.Printf("Fired trigger %q.\n", args[0])

		return nil
	},
}

var triggerPauseCmd = &cobra.Command{
	Use:   "pause <name>",
	Short: "Pause a trigger (persists across restart)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := cliRequest("trigger_pause", protocol.TriggerPauseMsg{Name: args[0], Pause: true}); err != nil {
			return err
		}

		out.Printf("Paused trigger %q.\n", args[0])

		return nil
	},
}

var triggerResumeCmd = &cobra.Command{
	Use:   "resume <name>",
	Short: "Resume a paused trigger",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := cliRequest("trigger_pause", protocol.TriggerPauseMsg{Name: args[0], Pause: false}); err != nil {
			return err
		}

		out.Printf("Resumed trigger %q.\n", args[0])

		return nil
	},
}

// triggerStateLabel renders the human-readable state label for a trigger.
func triggerStateLabel(t protocol.TriggerRecord, withConfig bool) string {
	switch {
	case !t.Enabled:
		if withConfig {
			return "disabled (config)"
		}

		return "disabled"
	case t.Paused:
		return "paused"
	default:
		return "enabled"
	}
}

// renderTriggerList writes the human-readable trigger table.
func renderTriggerList(w io.Writer, triggers []protocol.TriggerRecord) {
	if len(triggers) == 0 {
		_, _ = fmt.Fprintln(w, "No triggers configured.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSOURCE\tACTION\tWHEN\tSTATE\tRUNS")

	for _, t := range triggers {
		when := t.Schedule
		switch t.Source {
		case "watch":
			when = t.WatchScope
		case "gcx":
			when = t.GCXScope
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
			t.Name, t.Source, t.Action, when, triggerStateLabel(t, false), t.RunCount)
	}

	_ = tw.Flush()
}

// renderTriggerStatus writes the human-readable detail for one trigger.
func renderTriggerStatus(w io.Writer, t protocol.TriggerRecord) {
	_, _ = fmt.Fprintf(w, "Trigger: %s (%s → %s)\n", t.Name, t.Source, t.Action)

	switch t.Source {
	case "schedule":
		_, _ = fmt.Fprintf(w, "Schedule: %s\n", t.Schedule)

		if t.NextFire != "" {
			_, _ = fmt.Fprintf(w, "Next fire: %s\n", t.NextFire)
		}
	case "watch":
		_, _ = fmt.Fprintf(w, "Watch: %s (%d live binding(s))\n", t.WatchScope, t.Bindings)
		renderTriggerBindingDetails(w, t.BindingsDetail)

		if t.Degraded != "" {
			_, _ = fmt.Fprintf(w, "Degraded: %s\n", t.Degraded)

			if t.DegradedRetryAt != "" {
				_, _ = fmt.Fprintf(w, "Next retry: %s (after %d attempt(s); recovers automatically when the watch backend can be recreated)\n", t.DegradedRetryAt, t.DegradedRetryCount)
			}
		}
	case "gcx":
		_, _ = fmt.Fprintf(w, "GCX: %s\n", t.GCXScope)
		if t.NextPoll != "" {
			_, _ = fmt.Fprintf(w, "Next poll: %s\n", t.NextPoll)
		}
	}

	_, _ = fmt.Fprintf(w, "State: %s\n", triggerStateLabel(t, true))
	_, _ = fmt.Fprintf(w, "Runs: %d\n", t.RunCount)

	if t.LastRun != "" {
		_, _ = fmt.Fprintf(w, "Last run: %s\n", t.LastRun)
	}

	if t.LastResult != "" {
		_, _ = fmt.Fprintf(w, "Last result: %s\n", t.LastResult)
	}

	if t.LastError != "" {
		_, _ = fmt.Fprintf(w, "Last error: %s\n", t.LastError)
	}
}

func renderTriggerBindingDetails(w io.Writer, bindings []protocol.TriggerBindingDetail) {
	if len(bindings) == 0 {
		return
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SESSION\tWORKTREE\tSTATE\tWATCH DIRS\tLIVE\tSTALE\tWATCH COST\tLIVE COST\tSTALE COST\tBUDGET\tPENDING\tDEBOUNCE\tIN-FLIGHT\tLAST RUN\tRESULT\tRETRY")

	for _, b := range bindings {
		liveDirs, staleDirs, liveCost, staleCost := triggerBindingLiveStaleUsage(b)

		session := b.SessionName
		if session == "" {
			session = b.SessionID
		} else if b.SessionID != "" {
			session = fmt.Sprintf("%s (%s)", b.SessionName, b.SessionID)
		}

		debounce := b.DebounceUntil
		if debounce == "" {
			debounce = "-"
		}

		inFlight := "-"
		if b.ActionInFlight {
			inFlight = "yes"
		}

		lastRun := b.LastRun
		if lastRun == "" {
			lastRun = "-"
		}

		result := b.LastResult
		if b.LastError != "" {
			if result == "" {
				result = "failed"
			}

			result += ": " + b.LastError
		}

		if b.Degraded != "" {
			degraded := "degraded: " + b.Degraded
			if result == "" {
				result = degraded
			} else {
				result += "; " + degraded
			}
		}

		if result == "" {
			result = "-"
		}

		result = triggerBindingTableCell(result)

		retry := "-"
		if b.DegradedRetryAt != "" {
			retry = fmt.Sprintf("%s (%d)", b.DegradedRetryAt, b.DegradedRetryCount)
		}

		worktree := b.WorktreePath
		if worktree == "" {
			worktree = "-"
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%.2f%%\t%d\t%s\t%s\t%s\t%s\t%s\n",
			triggerBindingTableCell(session), triggerBindingTableCell(worktree), b.State,
			b.RegisteredWatchDirectories, liveDirs, staleDirs,
			b.EstimatedWatchDescriptorCost, liveCost, staleCost, b.WatchBudgetPercent,
			b.PendingChanges, debounce, inFlight, lastRun, result, retry)
	}

	_ = tw.Flush()
}

func triggerBindingLiveStaleUsage(b protocol.TriggerBindingDetail) (int, int, int, int) {
	liveDirs := b.LiveWatchDirectories
	staleDirs := b.StaleWatchDirectories
	liveCost := b.LiveEstimatedWatchCost
	staleCost := b.StaleEstimatedWatchCost

	// Older daemons did not send live/stale fields. Preserve sensible output
	// when talking to one by treating the legacy registered totals as live.
	if liveDirs == 0 && staleDirs == 0 && b.RegisteredWatchDirectories > 0 {
		liveDirs = b.RegisteredWatchDirectories
	}

	if liveCost == 0 && staleCost == 0 && b.EstimatedWatchDescriptorCost > 0 {
		liveCost = b.EstimatedWatchDescriptorCost
	}

	return liveDirs, staleDirs, liveCost, staleCost
}

func triggerBindingTableCell(s string) string {
	const maxLen = 160

	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")

	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}

	return s
}

func registerTriggerCmd() {
	triggerCmd.AddCommand(triggerListCmd)
	triggerCmd.AddCommand(triggerStatusCmd)
	triggerCmd.AddCommand(triggerRunCmd)
	triggerCmd.AddCommand(triggerPauseCmd)
	triggerCmd.AddCommand(triggerResumeCmd)
	rootCmd.AddCommand(triggerCmd)
}
