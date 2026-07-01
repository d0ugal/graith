package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	statusSummaryClear bool
	statusSummaryTTL   string
)

var statusSummaryCmd = &cobra.Command{
	Use:   "status [session] <message>",
	Short: "Set a status summary for a session",
	Long: `Set a short status summary displayed in the session picker overlay.

When run inside a graith session, the session is auto-detected.
When run outside, provide the session name or ID as the first argument.

Examples:
  gr status "Exploring code"
  gr status --ttl 10m "Waiting for CI"
  gr status --clear
  gr status my-session "Reviewing PR"`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		sessionID, text, err := resolveStatusArgs(c, args)
		if err != nil {
			return err
		}

		var ttlSeconds int

		if statusSummaryTTL != "" {
			d, err := parseTTL(statusSummaryTTL)
			if err != nil {
				return err
			}

			ttlSeconds = int(d.Seconds())
		}

		if statusSummaryClear {
			text = ""
		}

		msg := protocol.SetStatusMsg{
			SessionID:  sessionID,
			Text:       text,
			TTLSeconds: ttlSeconds,
			Clear:      statusSummaryClear,
		}

		c.SendControl("set_status", msg)

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		if statusSummaryClear {
			out.Printf("Status cleared\n")
		} else {
			out.Printf("Status set\n")
		}

		return nil
	},
}

func resolveStatusArgs(c *client.Client, args []string) (sessionID, text string, err error) {
	envID := os.Getenv("GRAITH_SESSION_ID")

	if statusSummaryClear {
		if len(args) == 0 && envID != "" {
			return envID, "", nil
		}

		if len(args) == 1 {
			id, err := resolveSession(c, args[0])
			return id, "", err
		}

		if envID == "" {
			return "", "", fmt.Errorf("session name required when not running inside a graith session")
		}

		return envID, "", nil
	}

	if len(args) == 0 {
		return "", "", fmt.Errorf("message required")
	}

	if envID != "" {
		return envID, strings.Join(args, " "), nil
	}

	if len(args) < 2 {
		return "", "", fmt.Errorf("session name and message required when not running inside a graith session")
	}

	id, err := resolveSession(c, args[0])
	if err != nil {
		return "", "", err
	}

	return id, strings.Join(args[1:], " "), nil
}

func parseTTL(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid TTL %q: %w", s, err)
	}

	if d <= 0 {
		return 0, fmt.Errorf("TTL must be positive")
	}

	return d, nil
}

// registerStatusSummaryCmd registers this command on rootCmd. Called from registerCommands.
func registerStatusSummaryCmd() {
	statusSummaryCmd.Flags().BoolVar(&statusSummaryClear, "clear", false, "clear the status summary")
	statusSummaryCmd.Flags().StringVar(&statusSummaryTTL, "ttl", "", "override TTL for this status update (e.g. 10m)")
	rootCmd.AddCommand(statusSummaryCmd)
}
