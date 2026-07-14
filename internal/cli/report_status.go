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
	// Trigger is the compaction trigger ("manual" | "auto") on Pre/PostCompact.
	Trigger string `json:"trigger"`
	// AgentID / AgentType identify a Claude sub-agent on SubagentStart/Stop.
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
	// Reason is Claude's SessionEnd reason (clear/resume/logout/prompt_input_exit/other).
	Reason string `json:"reason"`
	// LastAssistantMsg is the agent's final message on Stop; truncated before send.
	LastAssistantMsg string `json:"last_assistant_message"`
}

// maxLastMessageRunes bounds the last_assistant_message the CLI forwards to the
// daemon. It's the agent's full final output — we want a snippet, not an
// unbounded control frame — so truncate it before it hits the wire.
const maxLastMessageRunes = 2000

// truncateRunes returns s bounded to at most maxRunes runes, cutting on a rune
// boundary so a multi-byte character is never split.
func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}

	return string(r[:maxRunes])
}

// buildStatusReport assembles the status_report message from the flags and the
// parsed hook stdin. The CLI forwards the raw notification subtype (empty when
// stdin didn't parse within the budget) and no longer decides what a subtype
// means — the daemon routes it (idle_prompt -> ready, permission_prompt ->
// approval, everything else -> no status change). This is deliberately coupled
// with the daemon's subtype-aware switch: an empty/unparsed subtype must map to
// no status change there, not to approval.
func buildStatusReport(sessionID, event, toolFlag string, stdin hookStdin, parsed bool) protocol.StatusReportMsg {
	msg := protocol.StatusReportMsg{
		SessionID: sessionID,
		Event:     event,
		ToolName:  toolFlag,
	}

	if parsed {
		msg.NotificationType = stdin.NotificationType

		if stdin.ToolName != "" && msg.ToolName == "" {
			msg.ToolName = stdin.ToolName
		}

		// Compaction (Pre/PostCompact) and sub-agent (Subagent*) metadata; empty
		// for every other event (issue #1073). The daemon routes on the event
		// name, so carrying these unconditionally is harmless.
		msg.Trigger = stdin.Trigger
		msg.AgentID = stdin.AgentID
		msg.AgentType = stdin.AgentType

		// SessionEnd reason and the (truncated) Stop final message (issue #1073,
		// tier 1). The daemon records the raw reason and maps only process-ending
		// reasons onto a StopReason; the final message is bounded here so it never
		// hits the wire unbounded.
		msg.Reason = stdin.Reason

		if stdin.LastAssistantMsg != "" {
			msg.LastMessage = truncateRunes(stdin.LastAssistantMsg, maxLastMessageRunes)
		}
	}

	return msg
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

		msg := buildStatusReport(sessionID, event, reportTool, stdin.data, stdin.parsed)

		hookPaths, err := config.ResolvePaths()
		if err != nil {
			return nil
		}

		c, err := client.ConnectFast(hookPaths)
		if err != nil {
			return nil
		}
		defer c.Close()

		_ = c.SendControl("status_report", msg)
		_, _ = c.ReadControlResponse()

		return nil
	},
}

// registerReportStatusCmd registers this command on rootCmd. Called from registerCommands.
func registerReportStatusCmd() {
	rootCmd.AddCommand(reportStatusCmd)
	reportStatusCmd.Flags().StringVar(&reportEvent, "event", "", "hook event name")
	reportStatusCmd.Flags().StringVar(&reportTool, "tool", "", "tool name")
}
