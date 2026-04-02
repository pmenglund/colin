package cmd

import (
	"strings"

	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/spf13/cobra"
)

func newCommandRenderer(cmd *cobra.Command) *clioutput.Renderer {
	return clioutput.New(cmd.OutOrStdout(), isTerminalStream(cmd.OutOrStdout()))
}

func linesWithoutHeading(text string) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		return nil
	}
	if strings.HasSuffix(strings.TrimSpace(lines[0]), "setup:") {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}
