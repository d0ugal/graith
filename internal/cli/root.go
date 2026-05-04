package cli

import (
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/version"
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
	Use:           "gr",
	Short:         "graith — AI agent session manager",
	Version:       version.Version,
	SilenceErrors: true,
	SilenceUsage:  true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		cfg, err = config.LoadOrDefault(cfgFile)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		paths, err = config.ResolvePaths()
		if err != nil {
			return err
		}
		out = output.New(jsonOutput)
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func Execute() error {
	return executeWithArgs(os.Args[1:])
}

func executeWithArgs(args []string) error {
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	if err != nil {
		w := out
		if w == nil {
			// Cobra skips persistent flag parsing for some errors (e.g.
			// unknown subcommand). Parse them here so --json is respected.
			rootCmd.PersistentFlags().Parse(args)
			w = output.New(jsonOutput)
		}
		w.Error(err)
	}
	return err
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
}
