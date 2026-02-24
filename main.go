package main

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/pmenglund/colin/cmd"
)

func main() {
	rootCmd := cmd.NewRootCommand()
	if err := rootCmd.Execute(); err != nil {
		noColor, getErr := rootCmd.PersistentFlags().GetBool("no-color")
		if getErr != nil {
			noColor = false
		}
		_ = printMainError(os.Stderr, err, noColor)
		os.Exit(1)
	}
}

func printMainError(w io.Writer, err error, noColor bool) error {
	renderer := lipgloss.NewRenderer(w)
	if noColor {
		renderer.SetColorProfile(termenv.Ascii)
	} else {
		renderer.SetColorProfile(termenv.TrueColor)
	}
	red := renderer.NewStyle().Foreground(lipgloss.Color("1"))

	_, writeErr := fmt.Fprintln(w, red.Render(err.Error()))
	return writeErr
}
