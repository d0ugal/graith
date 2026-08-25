package cli

import (
	"os"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var dependencyStatusNoColor bool

var dependencyCmd = &cobra.Command{
	Use:   "dependency",
	Short: "Inspect configured dependency health",
}

var dependencyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the last dependency-health snapshot",
	Args:  cobra.NoArgs,
	RunE:  runDependencyStatus,
}

func runDependencyStatus(cmd *cobra.Command, _ []string) error {
	deps := commandDeps(cmd.Context())

	response, err := deps.health.Status()
	if err != nil {
		return err
	}

	if deps.out.IsJSON() {
		return deps.out.JSON(response)
	}

	renderDependencyStatus(deps.out, response, dependencyStatusColorEnabled(deps.out))

	return nil
}

func renderDependencyStatus(writer *output.Writer, response protocol.DependencyStatusResponseMsg, colorOn bool) {
	if len(response.Services) == 0 {
		writer.Printf("No dependencies configured.\n")
		return
	}

	rows := make([][]string, 0, len(response.Services)+1)
	rows = append(rows, []string{"SERVICE", "STATE", "SOURCE HEALTH", "OBSERVED", "ROUTING"})

	for _, service := range response.Services {
		rows = append(rows, []string{
			service.Name,
			colorize(service.ObservedState, client.DependencyStateColor(service.ObservedState), colorOn),
			colorize(service.SourceHealth, client.DependencySourceHealthColor(service.SourceHealth), colorOn),
			formatDependencyTime(service.ObservedAt),
			dependencyRouting(service),
		})
	}

	var rendered strings.Builder
	renderRows(&rendered, rows)
	writer.Printf("%s", rendered.String())
}

// dependencyStatusColorEnabled follows the same terminal and NO_COLOR rules as
// gr list while checking the writer that receives the rendered table.
func dependencyStatusColorEnabled(writer *output.Writer) bool {
	return shouldColor(dependencyStatusNoColor, os.Getenv("NO_COLOR"), writer.IsTerminal())
}

func dependencyRouting(service protocol.DependencyStatusService) string {
	if service.Global {
		return "all agents"
	}

	if len(service.AgentTypes) == 0 {
		return "none"
	}

	return strings.Join(service.AgentTypes, ", ")
}

func formatDependencyTime(value string) string {
	if value == "" {
		return "never"
	}

	return value
}

func registerDependencyCmd() {
	dependencyStatusCmd.Flags().BoolVar(&dependencyStatusNoColor, "no-color", false, "disable coloured status output")
	dependencyCmd.AddCommand(dependencyStatusCmd)
	rootCmd.AddCommand(dependencyCmd)
}
