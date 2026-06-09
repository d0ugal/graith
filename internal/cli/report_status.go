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
	Model            struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Cost struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	} `json:"cost"`
	ContextWindow struct {
		UsedPercentage float64 `json:"used_percentage"`
	} `json:"context_window"`
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

		// Try to parse stdin for enrichment data (non-blocking, channel-safe)
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
			if stdin.data.Model.DisplayName != "" {
				msg.Model = stdin.data.Model.DisplayName
			}
			if stdin.data.Cost.TotalCostUSD > 0 {
				cost := stdin.data.Cost.TotalCostUSD
				msg.Usage = &protocol.UsageReport{CostUSD: &cost}
			}
			if stdin.data.ContextWindow.UsedPercentage > 0 {
				pct := stdin.data.ContextWindow.UsedPercentage
				msg.Context = &protocol.ContextReport{Percent: &pct}
			}
		}

		c, err := client.ConnectFast(config.ResolvePaths())
		if err != nil {
			return nil
		}
		defer c.Close()

		c.SendControl("status_report", msg)
		c.ReadControlResponse()

		return nil
	},
}

func init() {
	rootCmd.AddCommand(reportStatusCmd)
	reportStatusCmd.Flags().StringVar(&reportEvent, "event", "", "hook event name")
	reportStatusCmd.Flags().StringVar(&reportTool, "tool", "", "tool name")
}
