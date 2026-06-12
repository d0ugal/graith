package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

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

		// Parse hook stdin for tool details (non-blocking with timeout).
		type stdinResult struct {
			data   approvalHookStdin
			parsed bool
		}
		ch := make(chan stdinResult, 1)
		go func() {
			data, err := io.ReadAll(os.Stdin)
			if err == nil && len(data) > 0 {
				var parsed approvalHookStdin
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

		requestID := generateApprovalID()

		var toolInput string
		if stdin.parsed && len(stdin.data.ToolInput) > 0 {
			toolInput = string(stdin.data.ToolInput)
			if len(toolInput) > 500 {
				toolInput = toolInput[:500] + "..."
			}
		}

		var toolName string
		if stdin.parsed {
			toolName = stdin.data.ToolName
		}

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

		c.SendControl("approval_request", protocol.ApprovalRequestMsg{
			RequestID: requestID,
			SessionID: sessionID,
			ToolName:  toolName,
			ToolInput: toolInput,
		})

		resp, err := c.ReadControlResponse()
		if err != nil {
			fmt.Println(hookoutput.AllowAll(agent))
			return nil
		}

		if resp.Type == "approval_decision" {
			var decision protocol.ApprovalDecisionMsg
			protocol.DecodePayload(resp, &decision)
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

func init() {
	rootCmd.AddCommand(approveRequestCmd)
}
