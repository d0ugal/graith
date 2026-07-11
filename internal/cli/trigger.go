package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/client"
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
		resp, err := triggerRequest("trigger_list", protocol.TriggerListMsg{})
		if err != nil {
			return err
		}
		var listResp protocol.TriggerListResponse
		_ = protocol.DecodePayload(resp, &listResp)

		if jsonOutput {
			return out.JSON(listResp)
		}
		if len(listResp.Triggers) == 0 {
			out.Printf("No triggers configured.\n")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSOURCE\tACTION\tWHEN\tSTATE\tRUNS")
		for _, t := range listResp.Triggers {
			when := t.Schedule
			if t.Source == "watch" {
				when = t.WatchScope
			}
			state := "enabled"
			switch {
			case !t.Enabled:
				state = "disabled"
			case t.Paused:
				state = "paused"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n", t.Name, t.Source, t.Action, when, state, t.RunCount)
		}
		return w.Flush()
	},
}

var triggerStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show detail for one trigger",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := triggerRequest("trigger_status", protocol.TriggerStatusMsg{Name: args[0]})
		if err != nil {
			return err
		}
		var statusResp protocol.TriggerStatusResponse
		_ = protocol.DecodePayload(resp, &statusResp)
		t := statusResp.Trigger

		if jsonOutput {
			return out.JSON(statusResp)
		}
		out.Printf("Trigger: %s (%s → %s)\n", t.Name, t.Source, t.Action)
		if t.Source == "schedule" {
			out.Printf("Schedule: %s\n", t.Schedule)
			if t.NextFire != "" {
				out.Printf("Next fire: %s\n", t.NextFire)
			}
		} else {
			out.Printf("Watch: %s (%d live binding(s))\n", t.WatchScope, t.Bindings)
			if t.Degraded != "" {
				out.Printf("Degraded: %s\n", t.Degraded)
			}
		}
		state := "enabled"
		switch {
		case !t.Enabled:
			state = "disabled (config)"
		case t.Paused:
			state = "paused"
		}
		out.Printf("State: %s\n", state)
		out.Printf("Runs: %d\n", t.RunCount)
		if t.LastRun != "" {
			out.Printf("Last run: %s\n", t.LastRun)
		}
		if t.LastResult != "" {
			out.Printf("Last result: %s\n", t.LastResult)
		}
		if t.LastError != "" {
			out.Printf("Last error: %s\n", t.LastError)
		}
		return nil
	},
}

var triggerRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Fire a schedule trigger once, now (respects overlap)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := triggerRequest("trigger_run", protocol.TriggerRunMsg{Name: args[0]}); err != nil {
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
		if _, err := triggerRequest("trigger_pause", protocol.TriggerPauseMsg{Name: args[0], Pause: true}); err != nil {
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
		if _, err := triggerRequest("trigger_pause", protocol.TriggerPauseMsg{Name: args[0], Pause: false}); err != nil {
			return err
		}
		out.Printf("Resumed trigger %q.\n", args[0])
		return nil
	},
}

// triggerRequest sends a control message and returns the reply, surfacing daemon
// errors as Go errors.
func triggerRequest(msgType string, payload any) (protocol.Envelope, error) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return protocol.Envelope{}, err
	}
	defer c.Close()

	_ = c.SendControl(msgType, payload)
	resp, err := c.ReadControlResponse()
	if err != nil {
		return protocol.Envelope{}, err
	}
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		_ = protocol.DecodePayload(resp, &e)
		return protocol.Envelope{}, fmt.Errorf("%s", e.Message)
	}
	return resp, nil
}

func registerTriggerCmd() {
	triggerCmd.AddCommand(triggerListCmd)
	triggerCmd.AddCommand(triggerStatusCmd)
	triggerCmd.AddCommand(triggerRunCmd)
	triggerCmd.AddCommand(triggerPauseCmd)
	triggerCmd.AddCommand(triggerResumeCmd)
	rootCmd.AddCommand(triggerCmd)
}
