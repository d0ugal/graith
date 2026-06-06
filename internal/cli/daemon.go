package cli

import (
	"fmt"

	"github.com/dougalmatthews/graith/internal/daemon"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the graith daemon",
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.Run(cfg, paths)
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		out.Print("Stopping daemon...\n")
		return fmt.Errorf("not yet implemented")
	},
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
}
