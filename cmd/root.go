package cmd

import (
	"fmt"
	"io"

	"github.com/pmenglund/colin/internal/config"
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

	rootCmd := &cobra.Command{
		Use:          "colin",
		Short:        "Colin is an automation tool",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRoot(cmd.OutOrStdout(), opts)
		},
	}

	rootCmd.PersistentFlags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().StringVar(&opts.ConfigPath, "config", config.DefaultConfigPath, "Path to config file")
	rootCmd.AddCommand(newSetupCommand(opts))
	rootCmd.AddCommand(newWorkerCommand(opts))

	return rootCmd
}

func runRoot(w io.Writer, opts *RootOptions) error {
	if opts.Verbose {
		_, err := fmt.Fprintln(w, "colin running (verbose mode)")
		return err
	}

	_, err := fmt.Fprintln(w, "colin running")
	return err
}
