package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/hookoutput"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

type approvalHookStdin struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

var approveRequestCmd = &cobra.Command{
	Use:    "approve-request",
	Short:  "Submit a tool approval request and wait for decision (used by hooks)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		sessionID := os.Getenv("GRAITH_SESSION_ID")
		if sessionID == "" {
			return nil
		}

		agent := os.Getenv("GRAITH_AGENT_TYPE")

		// Read the full hook payload from stdin BEFORE connecting. Approvals
		// backends may need to evaluate the whole command, so we must not
		// truncate or time-out the stdin read here — only the daemon round-trip
		// is bounded (via ConnectForApproval). We skip the read when stdin is a
		// terminal (no piped payload) to avoid blocking on interactive use.
		var raw []byte
		if fi, statErr := os.Stdin.Stat(); statErr == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
			raw, _ = io.ReadAll(os.Stdin)
		}

		var toolName, toolInput string
		if len(raw) > 0 {
			var parsed approvalHookStdin
			if json.Unmarshal(raw, &parsed) == nil {
				toolName = parsed.ToolName
				if len(parsed.ToolInput) > 0 {
					toolInput = string(parsed.ToolInput)
				}
			}
		}

		requestID := generateApprovalID()

		hookPaths, err := config.ResolvePaths()
		if err != nil {
			fmt.Println(hookoutput.AllowAll(agent))
			return nil
		}

		c, err := client.ConnectForApproval(hookPaths, cfg.Approvals.TimeoutDuration())
		if err != nil {
			fmt.Println(hookoutput.AllowAll(agent))
			return nil
		}
		defer c.Close()

		_ = c.SendControl("approval_request", protocol.ApprovalRequestMsg{
			RequestID:   requestID,
			SessionID:   sessionID,
			ToolName:    toolName,
			ToolInput:   toolInput,
			HookPayload: string(raw),
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			fmt.Println(hookoutput.AllowAll(agent))
			return nil
		}

		if resp.Type == "approval_decision" {
			var decision protocol.ApprovalDecisionMsg

			_ = protocol.DecodePayload(resp, &decision)
			fmt.Println(hookoutput.Approval(agent, decision.Decision, decision.Reason))
		} else {
			fmt.Println(hookoutput.AllowAll(agent))
		}

		return nil
	},
}

func generateApprovalID() string {
	b := make([]byte, 8)
	rand.Read(b)

	return hex.EncodeToString(b)
}

// registerApproveRequestCmd registers this command on rootCmd. Called from registerCommands.
func registerApproveRequestCmd() {
	rootCmd.AddCommand(approveRequestCmd)
}
