package cli

import (
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	notifyTitle    string
	notifyPriority string
)

var notifyCmd = &cobra.Command{
	Use:   "notify <message>",
	Short: "Send a proactive desktop/push notification to the human",
	Long: `Send a proactive push notification via the configured [notifications] backend.

Unlike an inbox message (which waits to be read), a notification proactively
gets the human's attention — a morning briefing, a CI failure, a review needed.

Only the orchestrator session and the human may send notifications; plain agent
sessions are rejected to prevent notification spam.

Priority levels:
  low     quietest; subject to quiet hours and rate limiting
  normal  (default) subject to quiet hours and rate limiting
  high    plays a sound and bypasses quiet hours and the rate limit

Examples:
  gr notify "Morning briefing ready" --priority low
  gr notify "CI failing on main after 3 retries" --priority high
  gr notify "Review needed" --title "graith"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, ok := config.NormalizeNotifyPriority(notifyPriority); !ok {
			return fmt.Errorf("invalid --priority %q (want low, normal, or high)", notifyPriority)
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if err := c.SendControl("notify", protocol.NotifyMsg{
			Message:  strings.Join(args, " "),
			Title:    notifyTitle,
			Priority: notifyPriority,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		if resp.Type != "notify_response" {
			return fmt.Errorf("unexpected response %q from daemon", resp.Type)
		}

		var r protocol.NotifyResponse

		if err := protocol.DecodePayload(resp, &r); err != nil {
			return fmt.Errorf("decode notify response: %w", err)
		}

		if r.Delivered {
			out.Printf("Notification sent\n")
		} else {
			out.Printf("Notification not delivered: %s\n", r.Reason)
		}

		return nil
	},
}

// registerNotifyCmd registers this command on rootCmd. Called from registerCommands.
func registerNotifyCmd() {
	notifyCmd.Flags().StringVar(&notifyTitle, "title", "graith", "notification title")
	notifyCmd.Flags().StringVar(&notifyPriority, "priority", "normal", "priority: low, normal, or high")
	rootCmd.AddCommand(notifyCmd)
}
