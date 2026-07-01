package cli

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	reportEvent string
	reportTool  string
)

// hookStdin represents common fields from Claude/Codex hook JSON payloads.
type hookStdin struct {
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
}

var reportStatusCmd = &cobra.Command{
	Use:    "report-status",
	Short:  "Report agent status to daemon (used by hooks)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		sessionID := os.Getenv("GRAITH_SESSION_ID")
		if sessionID == "" {
			return nil
		}

		event := reportEvent
		if event == "" {
			return nil
		}

		// Try to parse stdin for tool/notification metadata (non-blocking, channel-safe)
		type stdinResult struct {
			data   hookStdin
			parsed bool
		}

		ch := make(chan stdinResult, 1)

		go func() {
			data, err := io.ReadAll(os.Stdin)
			if err == nil && len(data) > 0 {
				var parsed hookStdin
				if json.Unmarshal(data, &parsed) == nil {
					ch <- stdinResult{data: parsed, parsed: true}
					return
				}
			}

			ch <- stdinResult{}
		}()

		var stdin stdinResult
		select {
		case stdin = <-ch:
		case <-time.After(100 * time.Millisecond):
		}

		// Filter Notification events: only permission_prompt maps to approval
		if event == "Notification" && stdin.parsed && stdin.data.NotificationType != "permission_prompt" {
			return nil
		}

		msg := protocol.StatusReportMsg{
			SessionID: sessionID,
			Event:     event,
			ToolName:  reportTool,
		}

		if stdin.parsed {
			if stdin.data.ToolName != "" && msg.ToolName == "" {
				msg.ToolName = stdin.data.ToolName
			}
		}

		hookPaths, err := config.ResolvePaths()
		if err != nil {
			return nil
		}

		c, err := client.ConnectFast(hookPaths)
		if err != nil {
			return nil
		}
		defer c.Close()

		c.SendControl("status_report", msg)
		c.ReadControlResponse()

		return nil
	},
}

// registerReportStatusCmd registers this command on rootCmd. Called from registerCommands.
func registerReportStatusCmd() {
	rootCmd.AddCommand(reportStatusCmd)
	reportStatusCmd.Flags().StringVar(&reportEvent, "event", "", "hook event name")
	reportStatusCmd.Flags().StringVar(&reportTool, "tool", "", "tool name")
}
