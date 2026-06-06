package cli

import (
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			out.JSON(struct {
				Version string `json:"version"`
				Commit  string `json:"commit"`
			}{version.Version, version.CommitSHA})
			return
		}
		out.Print("graith %s (%s)\n", version.Version, version.CommitSHA)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
