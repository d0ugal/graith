package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

var manCmd = &cobra.Command{
	Use:    "man <outdir>",
	Short:  "Generate man pages",
	Long:   "Generate man pages for gr and all its subcommands into the given directory.",
	Args:   cobra.ExactArgs(1),
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		header := &doc.GenManHeader{
			Title:   "GR",
			Section: "1",
			Source:  "graith",
			Manual:  "graith Manual",
		}
		if err := doc.GenManTree(rootCmd, header, args[0]); err != nil {
			return fmt.Errorf("generating man pages: %w", err)
		}

		return nil
	},
}

// registerManCmd registers this command on rootCmd. Called from registerCommands.
func registerManCmd() {
	rootCmd.AddCommand(manCmd)
}
