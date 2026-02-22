package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/spf13/cobra"
)

type metadataIssueLookup interface {
	GetIssueByIdentifier(ctx context.Context, issueIdentifier string) (linear.Issue, error)
}

func newMetadataCommand(rootOpts *RootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "metadata <issue-identifier>",
		Short: "Display Colin metadata for a Linear issue",
		Args:  cobra.ExactArgs(1),
		Example: strings.Join([]string{
			"colin metadata COLIN-42",
		}, "\n"),
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := config.DefaultConfigPath
			if rootOpts != nil {
				configPath = rootOpts.ConfigPath
			}

			cfg, err := config.LoadFromPath(configPath)
			if err != nil {
				return err
			}

			return runMetadata(cmd.Context(), cmd.OutOrStdout(), cfg, args[0])
		},
	}
}

func runMetadata(ctx context.Context, w io.Writer, cfg config.Config, issueIdentifier string) error {
	lookup, err := newMetadataIssueLookup(cfg)
	if err != nil {
		return err
	}

	return runMetadataWithLookup(ctx, w, lookup, issueIdentifier)
}

func newMetadataIssueLookup(cfg config.Config) (metadataIssueLookup, error) {
	switch cfg.LinearBackend {
	case config.LinearBackendHTTP:
		return linear.NewHTTPClient(cfg.LinearBaseURL, cfg.LinearAPIToken, cfg.LinearTeamID, nil), nil
	case config.LinearBackendFake:
		return linear.NewDefaultInMemoryClient(), nil
	default:
		return nil, fmt.Errorf("unsupported linear_backend %q", cfg.LinearBackend)
	}
}

func runMetadataWithLookup(ctx context.Context, w io.Writer, lookup metadataIssueLookup, issueIdentifier string) error {
	if lookup == nil {
		return errors.New("metadata lookup client is required")
	}

	trimmedIdentifier := strings.TrimSpace(issueIdentifier)
	if trimmedIdentifier == "" {
		return errors.New("issue identifier is required")
	}

	issue, err := lookup.GetIssueByIdentifier(ctx, trimmedIdentifier)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "Issue: %s\n", issue.Identifier); err != nil {
		return err
	}
	if len(issue.Metadata) == 0 {
		_, err := fmt.Fprintln(w, "Metadata: (empty)")
		return err
	}

	if _, err := fmt.Fprintln(w, "Metadata:"); err != nil {
		return err
	}
	keys := make([]string, 0, len(issue.Metadata))
	for key := range issue.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(w, "%s=%s\n", key, issue.Metadata[key]); err != nil {
			return err
		}
	}

	return nil
}
