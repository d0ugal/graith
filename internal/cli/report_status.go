package cli

import (
	"os"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	reportEvent string
	reportTool  string
)

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

		c, err := client.ConnectFast(config.ResolvePaths())
		if err != nil {
			return nil
		}
		defer c.Close()

		c.SendControl("status_report", protocol.StatusReportMsg{
			SessionID: sessionID,
			Event:     event,
			ToolName:  reportTool,
		})

		c.ReadControlResponse()

		return nil
	},
}

func init() {
	rootCmd.AddCommand(reportStatusCmd)
	reportStatusCmd.Flags().StringVar(&reportEvent, "event", "", "hook event name")
	reportStatusCmd.Flags().StringVar(&reportTool, "tool", "", "tool name")
}
