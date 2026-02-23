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
	if err := cmd.NewRootCommand().Execute(); err != nil {
		_ = printMainError(os.Stderr, err)
		os.Exit(1)
	}
}

func printMainError(w io.Writer, err error) error {
	renderer := lipgloss.NewRenderer(w)
	renderer.SetColorProfile(termenv.TrueColor)
	red := renderer.NewStyle().Foreground(lipgloss.Color("1"))

	_, writeErr := fmt.Fprintln(w, red.Render(err.Error()))
	return writeErr
}
