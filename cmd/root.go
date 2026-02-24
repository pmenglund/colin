package cmd

import (
	"github.com/spf13/cobra"
)

// RootOptions stores flags shared across all commands.
type RootOptions struct {
	Verbose    bool
	ConfigPath string
	NoColor    bool
}

// NewRootCommand constructs the root colin command.
func NewRootCommand() *cobra.Command {
	opts := &RootOptions{}
	runOpts := &workerRunOptions{}

	rootCmd := &cobra.Command{
		Use:          "colin",
		Short:        "Colin is an automation tool",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorker(cmd, opts, *runOpts)
		},
	}

	rootCmd.PersistentFlags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().StringVar(&opts.ConfigPath, "config", "", "Path to config file (default: ./colin.toml or $COLIN_CONFIG)")
	rootCmd.PersistentFlags().BoolVar(&opts.NoColor, "no-color", false, "Disable ANSI color output")
	addWorkerRunFlags(rootCmd, runOpts)
	rootCmd.AddCommand(newMetadataCommand(opts))
	rootCmd.AddCommand(newSetupCommand(opts))

	return rootCmd
}
