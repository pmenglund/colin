package cmd

import (
	"github.com/spf13/cobra"
)

// RootOptions stores flags shared across all commands.
type RootOptions struct {
	Verbose    bool
	ConfigPath string
}

// NewRootCommand constructs the root colin command.
func NewRootCommand() *cobra.Command {
	opts := &RootOptions{}
	var once bool
	var dryRun bool

	rootCmd := &cobra.Command{
		Use:          "colin",
		Short:        "Colin is an automation tool",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorkerCommand(cmd, opts, once, dryRun)
		},
	}

	rootCmd.PersistentFlags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().StringVar(&opts.ConfigPath, "config", "", "Path to config file (default: ./colin.toml or $COLIN_CONFIG)")
	rootCmd.Flags().BoolVar(&once, "once", false, "Run one poll cycle and exit")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log decisions without writing to Linear")
	rootCmd.AddCommand(newMetadataCommand(opts))
	rootCmd.AddCommand(newSetupCommand(opts))
	rootCmd.AddCommand(newWorkerCommand(opts))

	return rootCmd
}
