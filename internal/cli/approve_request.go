package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

type approvalControlConn interface {
	SendControl(controlType string, payload any) error
	ReadControlResponse() (protocol.Envelope, error)
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
			fmt.Println(approvalFailure(agent, "resolve daemon path", err))
			return nil
		}

		c, err := client.ConnectForApproval(hookPaths, cfg.Approvals.ServerTimeoutDuration())
		if err != nil {
			fmt.Println(approvalFailure(agent, "connect", err))
			return nil
		}
		defer c.Close()

		fmt.Println(submitApprovalRequest(c, agent, protocol.ApprovalRequestMsg{
			RequestID:   requestID,
			SessionID:   sessionID,
			ToolName:    toolName,
			ToolInput:   toolInput,
			HookPayload: string(raw),
		}))

		return nil
	},
}

func submitApprovalRequest(c approvalControlConn, agent string, req protocol.ApprovalRequestMsg) string {
	if err := c.SendControl("approval_request", req); err != nil {
		return approvalFailure(agent, "send request", err)
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return approvalFailure(agent, "wait for response", err)
	}

	if resp.Type == "error" {
		var msg protocol.ErrorMsg
		if err := protocol.DecodePayload(resp, &msg); err == nil && msg.Message != "" {
			return approvalFailure(agent, "daemon response", errors.New(msg.Message))
		}
	}

	if resp.Type != "approval_decision" {
		return approvalFailure(agent, "wait for response", fmt.Errorf("unexpected response %q", resp.Type))
	}

	var decision protocol.ApprovalDecisionMsg
	if err := protocol.DecodePayload(resp, &decision); err != nil {
		return approvalFailure(agent, "decode response", err)
	}

	return hookoutput.Approval(agent, decision.Decision, decision.Reason)
}

// approvalFailure preserves the documented fail-open hook edge while making
// the failure visible in the agent's hook result. In particular a socket
// deadline is reported as the approval-operation deadline, not silently mapped
// to an empty-reason allow.
func approvalFailure(agent, phase string, err error) string {
	reason := fmt.Sprintf("graith approval %s failed: %v; request allowed by fail-open hook policy", phase, err)

	var netErr net.Error
	if errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		reason = fmt.Sprintf("graith approval operation timed out while attempting to %s; request allowed by fail-open hook policy", phase)
	}

	return hookoutput.Approval(agent, "allow", reason)
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
