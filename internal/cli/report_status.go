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
	ToolName string `json:"tool_name"`
	Model    struct {
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

		// Try to parse stdin for enrichment data (non-blocking)
		var stdinData hookStdin
		stdinParsed := false
		done := make(chan struct{})
		go func() {
			defer close(done)
			data, err := io.ReadAll(os.Stdin)
			if err == nil && len(data) > 0 {
				if json.Unmarshal(data, &stdinData) == nil {
					stdinParsed = true
				}
			}
		}()
		// Wait up to 100ms for stdin
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}

		msg := protocol.StatusReportMsg{
			SessionID: sessionID,
			Event:     event,
			ToolName:  reportTool,
		}

		if stdinParsed {
			if stdinData.ToolName != "" && msg.ToolName == "" {
				msg.ToolName = stdinData.ToolName
			}
			if stdinData.Model.DisplayName != "" {
				msg.Model = stdinData.Model.DisplayName
			}
			if stdinData.Cost.TotalCostUSD > 0 {
				cost := stdinData.Cost.TotalCostUSD
				msg.Usage = &protocol.UsageReport{CostUSD: &cost}
			}
			if stdinData.ContextWindow.UsedPercentage > 0 {
				pct := stdinData.ContextWindow.UsedPercentage
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
