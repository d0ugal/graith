package cli

import (
	"github.com/dougalmatthews/graith/internal/config"
	"github.com/dougalmatthews/graith/internal/output"
	"github.com/dougalmatthews/graith/internal/version"
	"github.com/spf13/cobra"
)

var (
	cfgFile    string
	jsonOutput bool
	cfg        *config.Config
	paths      config.Paths
	out        *output.Writer
)

var rootCmd = &cobra.Command{
	Use:     "gr",
	Short:   "graith — AI agent session manager",
	Version: version.Version,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cfg = config.LoadOrDefault(cfgFile)
		paths = config.ResolvePaths()
		out = output.New(jsonOutput)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAttach(cmd, "")
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
}
