package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/hookoutput"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

type commandPolicyHookStdin struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

var commandPolicyStartupError error

var commandPolicyCheckCmd = &cobra.Command{
	Use:    "command-policy-check",
	Short:  "Synchronously restrict a shell command (used by agent hooks)",
	Hidden: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		agent := os.Getenv("GRAITH_AGENT_TYPE")
		deny := func(reason string) error {
			fmt.Println(hookoutput.CommandPolicy(agent, "deny", reason))
			return nil
		}
		sessionID := os.Getenv("GRAITH_SESSION_ID")
		if sessionID == "" {
			return deny("command policy cannot identify the agent session")
		}
		if commandPolicyStartupError != nil {
			return deny("command policy startup failed: " + commandPolicyStartupError.Error())
		}

		const maxHookPayload = 16 << 20
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxHookPayload+1))
		if err != nil {
			return deny("command policy could not read hook input: " + err.Error())
		}
		if len(raw) > maxHookPayload {
			return deny("command policy hook input exceeded 16 MiB")
		}
		var parsed commandPolicyHookStdin
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return deny("command policy received malformed hook input: " + err.Error())
		}
		toolInput := ""
		if len(parsed.ToolInput) > 0 {
			toolInput = string(parsed.ToolInput)
		}

		deadline := cfg.CommandPolicy.TimeoutDuration() + 2*time.Second
		c, err := client.ConnectForPolicy(paths, deadline)
		if err != nil {
			return deny("command policy daemon unavailable: " + err.Error())
		}
		defer c.Close()

		if err := c.SendControl("command_policy_check", protocol.CommandPolicyCheckMsg{
			SessionID: sessionID, ToolName: parsed.ToolName, ToolInput: toolInput, HookPayload: string(raw),
		}); err != nil {
			return deny("command policy request failed: " + err.Error())
		}
		resp, err := c.ReadControlResponse()
		if err != nil {
			return deny("command policy response failed: " + err.Error())
		}
		if resp.Type != "command_policy_decision" {
			return deny("command policy returned unexpected response " + resp.Type)
		}
		var decision protocol.CommandPolicyDecisionMsg
		if err := protocol.DecodePayload(resp, &decision); err != nil {
			return deny("command policy returned malformed decision: " + err.Error())
		}
		fmt.Println(hookoutput.CommandPolicy(agent, decision.Decision, decision.Reason))
		return nil
	},
}

func registerCommandPolicyCheckCmd() {
	rootCmd.AddCommand(commandPolicyCheckCmd)
}
