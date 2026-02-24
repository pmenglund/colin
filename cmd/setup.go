package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/spf13/cobra"
)

var setupCanonicalOrder = []string{"todo", "in_progress", "refine", "review", "merge", "merged", "done"}

func newSetupCommand(rootOpts *RootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Ensure required Linear workflow states for Colin",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadCLIConfig(rootOpts)
			if err != nil {
				return err
			}
			return runSetup(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}
}

func runSetup(ctx context.Context, w io.Writer, cfg config.Config) error {
	if cfg.LinearBackend != config.LinearBackendHTTP {
		return fmt.Errorf("colin setup requires linear_backend=%q (got %q)", config.LinearBackendHTTP, cfg.LinearBackend)
	}

	admin := linear.NewWorkflowStateAdmin(cfg.LinearBaseURL, cfg.LinearAPIToken, cfg.LinearTeamID, nil)
	resolved, err := admin.EnsureWorkflowStates(ctx, cfg.WorkflowStates.AsRuntimeStates())
	if err != nil {
		return fmt.Errorf("ensure workflow states: %w", err)
	}

	if _, err := fmt.Fprintf(w, "Linear team: %s (%s)\n", resolved.TeamKey, resolved.TeamID); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "Workflow state setup:"); err != nil {
		return err
	}
	for _, canonical := range setupCanonicalOrder {
		mapping, ok := resolved.Mappings[canonical]
		if !ok {
			continue
		}
		status := "validated"
		if mapping.Created {
			status = "created"
		}
		if _, err := fmt.Fprintf(w, "- %s: %q -> %q [%s, type=%s]\n", canonical, mapping.ConfiguredName, mapping.ActualName, status, strings.ToLower(mapping.StateType)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "Resolved runtime mapping:"); err != nil {
		return err
	}
	runtimeStates := resolved.RuntimeStates()
	runtimeLines := map[string]string{
		"todo":        runtimeStates.Todo,
		"in_progress": runtimeStates.InProgress,
		"refine":      runtimeStates.Refine,
		"review":      runtimeStates.Review,
		"merge":       runtimeStates.Merge,
		"merged":      runtimeStates.Merged,
		"done":        runtimeStates.Done,
	}
	for _, canonical := range setupCanonicalOrder {
		if _, err := fmt.Fprintf(w, "- %s => %q\n", canonical, runtimeLines[canonical]); err != nil {
			return err
		}
	}

	return nil
}
