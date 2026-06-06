// internal/cli/root.go
package cli

import (
	"github.com/dougalmatthews/graith/internal/version"
	"github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:     "gr",
	Short:   "graith — AI agent session manager",
	Version: version.Version,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
}
