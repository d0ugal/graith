package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Inspect configured agents",
}

var agentListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured agents",
	Args:    cobra.NoArgs,
	RunE:    runAgentList,
}

var agentInfoCmd = &cobra.Command{
	Use:   "info <agent> [key]",
	Short: "Run configured provider info commands",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runAgentInfo,
}

func runAgentList(cmd *cobra.Command, _ []string) error {
	deps := commandDeps(cmd.Context())

	catalog, err := deps.agent.AgentCatalog()
	if err != nil {
		return err
	}

	if deps.out.IsJSON() {
		return deps.out.JSON(catalog)
	}

	renderAgentCatalog(deps, catalog)

	return nil
}

func renderAgentCatalog(deps commandDependencies, catalog protocol.AgentCatalogResponseMsg) {
	var b strings.Builder

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)

	_, _ = tw.Write([]byte("NAME\tDEFAULT\tCOMMAND\tINFO\n"))

	for _, agent := range catalog.Agents {
		defaultMark := ""
		if agent.Name == catalog.DefaultAgent {
			defaultMark = "*"
		}

		command := agent.Command
		if command == "" {
			command = "-"
		}

		info := "-"
		if len(agent.InfoKeys) > 0 {
			info = strings.Join(agent.InfoKeys, ",")
		}

		_, _ = tw.Write([]byte(agent.Name + "\t" + defaultMark + "\t" + command + "\t" + info + "\n"))
	}

	_ = tw.Flush()

	deps.out.Printf("%s", b.String())
}

func runAgentInfo(cmd *cobra.Command, args []string) error {
	deps := commandDeps(cmd.Context())

	req := protocol.AgentInfoMsg{Agent: args[0]}
	if len(args) > 1 {
		req.Key = args[1]
	}

	resp, err := deps.agent.AgentInfo(req)
	if err != nil {
		return err
	}

	if deps.out.IsJSON() {
		if err := deps.out.JSON(resp); err != nil {
			return err
		}

		return agentInfoResponseError(resp)
	}

	renderAgentInfo(deps, resp)

	return agentInfoResponseError(resp)
}

func renderAgentInfo(deps commandDependencies, resp protocol.AgentInfoResponseMsg) {
	if len(resp.Results) == 1 {
		renderAgentInfoOutput(deps, resp.Results[0])

		return
	}

	deps.out.Printf("Agent: %s\n", resp.Agent)

	for _, result := range resp.Results {
		deps.out.Printf("\n")
		deps.out.Printf("%s:\n", result.Key)
		renderAgentInfoOutput(deps, result)
	}
}

func renderAgentInfoOutput(deps commandDependencies, result protocol.AgentInfoResult) {
	if result.Error != "" {
		deps.out.Printf("error: %s\n", result.Error)
	}

	if result.Stdout != "" {
		deps.out.Printf("%s", result.Stdout)

		if !strings.HasSuffix(result.Stdout, "\n") {
			deps.out.Printf("\n")
		}
	}

	if result.StdoutTruncated {
		deps.out.Printf("[stdout truncated]\n")
	}

	if result.Stderr != "" {
		deps.out.Printf("stderr:\n%s", result.Stderr)

		if !strings.HasSuffix(result.Stderr, "\n") {
			deps.out.Printf("\n")
		}
	}

	if result.StderrTruncated {
		deps.out.Printf("[stderr truncated]\n")
	}
}

func agentInfoResponseError(resp protocol.AgentInfoResponseMsg) error {
	failedKeys := make([]string, 0)

	for _, result := range resp.Results {
		if result.Error == "" && result.ExitCode == 0 {
			continue
		}

		failedKeys = append(failedKeys, result.Key)
	}

	if len(failedKeys) == 0 {
		return nil
	}

	if len(failedKeys) == 1 {
		return fmt.Errorf("agent info %s.%s failed", resp.Agent, failedKeys[0])
	}

	return fmt.Errorf("agent info %s failed for keys: %s", resp.Agent, strings.Join(failedKeys, ", "))
}

// registerAgentCmd registers this command on rootCmd. Called from registerCommands.
func registerAgentCmd() {
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentInfoCmd)
	rootCmd.AddCommand(agentCmd)
}
