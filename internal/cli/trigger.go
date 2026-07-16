package cli

import (
	"fmt"
	"io"
	"os"
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
		if t.Source == "watch" {
			when = t.WatchScope
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
			t.Name, t.Source, t.Action, when, triggerStateLabel(t, false), t.RunCount)
	}

	_ = tw.Flush()
}

// renderTriggerStatus writes the human-readable detail for one trigger.
func renderTriggerStatus(w io.Writer, t protocol.TriggerRecord) {
	_, _ = fmt.Fprintf(w, "Trigger: %s (%s → %s)\n", t.Name, t.Source, t.Action)

	if t.Source == "schedule" {
		_, _ = fmt.Fprintf(w, "Schedule: %s\n", t.Schedule)

		if t.NextFire != "" {
			_, _ = fmt.Fprintf(w, "Next fire: %s\n", t.NextFire)
		}
	} else {
		_, _ = fmt.Fprintf(w, "Watch: %s (%d live binding(s))\n", t.WatchScope, t.Bindings)

		if t.Degraded != "" {
			_, _ = fmt.Fprintf(w, "Degraded: %s\n", t.Degraded)

			if t.DegradedRetryAt != "" {
				_, _ = fmt.Fprintf(w, "Next retry: %s (after %d attempt(s); recovers automatically when the watch limit clears)\n", t.DegradedRetryAt, t.DegradedRetryCount)
			}
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

func registerTriggerCmd() {
	triggerCmd.AddCommand(triggerListCmd)
	triggerCmd.AddCommand(triggerStatusCmd)
	triggerCmd.AddCommand(triggerRunCmd)
	triggerCmd.AddCommand(triggerPauseCmd)
	triggerCmd.AddCommand(triggerResumeCmd)
	rootCmd.AddCommand(triggerCmd)
}
