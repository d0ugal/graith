package cli

import (
	"strings"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

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

	renderDependencyStatus(deps.out, response)

	return nil
}

func renderDependencyStatus(writer *output.Writer, response protocol.DependencyStatusResponseMsg) {
	if len(response.Services) == 0 {
		writer.Printf("No dependencies configured.\n")
		return
	}

	for i, service := range response.Services {
		if i > 0 {
			writer.Printf("\n")
		}
		writer.Printf("%s\n", service.Name)
		writer.Printf("  provider: %s\n", service.Provider)
		writer.Printf("  source: %s\n", service.SourceURL)
		writer.Printf("  routing: %s\n", dependencyRouting(service))
		writer.Printf("  state: %s\n", service.ObservedState)
		writer.Printf("  source health: %s\n", service.SourceHealth)
		writer.Printf("  last observed: %s\n", formatDependencyTime(service.ObservedAt))
		writer.Printf("  last success: %s\n", formatDependencyTime(service.LastSuccessAt))
		writer.Printf("  last failure: %s\n", formatDependencyTime(service.LastFailureAt))
		if len(service.IncidentIDs) > 0 {
			writer.Printf("  incidents: %s\n", strings.Join(service.IncidentIDs, ", "))
		} else {
			writer.Printf("  incidents: none\n")
		}
	}
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
	dependencyCmd.AddCommand(dependencyStatusCmd)
	rootCmd.AddCommand(dependencyCmd)
}
