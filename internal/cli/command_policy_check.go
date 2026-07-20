package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
var commandPolicyWorker bool

const commandPolicySupervisorSlack = 4 * time.Second

var (
	commandPolicyExecutable = os.Executable
	commandPolicyExec       = exec.CommandContext
	commandPolicyDeadline   = func() time.Duration {
		return cfg.CommandPolicy.TimeoutDuration() + commandPolicySupervisorSlack
	}
)

type commandPolicyWorkerResult struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

var commandPolicyCheckCmd = &cobra.Command{
	Use:    "command-policy-check",
	Short:  "Synchronously restrict a shell command (used by agent hooks)",
	Hidden: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		if commandPolicyWorker {
			return json.NewEncoder(os.Stdout).Encode(evaluateCommandPolicyHook())
		}

		return superviseCommandPolicyHook()
	},
}

func evaluateCommandPolicyHook() commandPolicyWorkerResult {
	deny := func(reason string) commandPolicyWorkerResult {
		return commandPolicyWorkerResult{Decision: "deny", Reason: reason}
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

	return commandPolicyWorkerResult{Decision: decision.Decision, Reason: decision.Reason}
}

// superviseCommandPolicyHook isolates the synchronous daemon exchange in a
// child process. The child has its own socket deadline, while the parent owns a
// slightly larger hard deadline and validates the child's neutral result before
// rendering agent-native output. A wedged read, crash, signal, malformed
// output, or non-zero exit therefore becomes a valid deny response instead of
// an agent hook-runner failure that Codex would otherwise treat as non-blocking.
func superviseCommandPolicyHook() error {
	agent := os.Getenv("GRAITH_AGENT_TYPE")
	if commandPolicyStartupError != nil {
		writeCommandPolicyHookResponse(agent, commandPolicyWorkerResult{
			Decision: "deny",
			Reason:   "command policy startup failed: " + commandPolicyStartupError.Error(),
		})

		return nil
	}

	executable, err := commandPolicyExecutable()
	if err != nil {
		writeCommandPolicyHookResponse(agent, commandPolicyWorkerResult{
			Decision: "deny", Reason: "command policy supervisor could not resolve its executable: " + err.Error(),
		})

		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandPolicyDeadline())
	defer cancel()

	cmd := commandPolicyExec(ctx, executable, "command-policy-check", "--worker")
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		reason := "command policy worker failed: " + err.Error()
		if ctx.Err() != nil {
			reason = "command policy worker timed out: " + ctx.Err().Error()
		} else if detail := strings.TrimSpace(stderr.String()); detail != "" {
			reason += ": " + detail
		}

		writeCommandPolicyHookResponse(agent, commandPolicyWorkerResult{Decision: "deny", Reason: reason})

		return nil
	}

	var result commandPolicyWorkerResult

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&result); err != nil || (result.Decision != "allow" && result.Decision != "deny") {
		reason := "command policy worker returned malformed output"
		if err != nil {
			reason += ": " + err.Error()
		}

		writeCommandPolicyHookResponse(agent, commandPolicyWorkerResult{Decision: "deny", Reason: reason})

		return nil
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeCommandPolicyHookResponse(agent, commandPolicyWorkerResult{
			Decision: "deny", Reason: "command policy worker returned trailing output",
		})

		return nil
	}

	writeCommandPolicyHookResponse(agent, result)

	return nil
}

func writeCommandPolicyHookResponse(agent string, result commandPolicyWorkerResult) {
	response := hookoutput.CommandPolicy(agent, result.Decision, result.Reason)
	if response != "" {
		fmt.Println(response)
	}
}

func registerCommandPolicyCheckCmd() {
	commandPolicyCheckCmd.Flags().BoolVar(&commandPolicyWorker, "worker", false, "run the bounded internal policy worker")
	_ = commandPolicyCheckCmd.Flags().MarkHidden("worker")
	rootCmd.AddCommand(commandPolicyCheckCmd)
}
