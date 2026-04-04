package service

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/tracker/linear"
)

const (
	PreflightStatusOK      = "ok"
	PreflightStatusError   = "error"
	PreflightStatusSkipped = "skipped"
)

type ConfigPreflightCheck struct {
	ID     string
	Label  string
	Status string
	Detail string
}

type ConfigPreflightReport struct {
	Checks []ConfigPreflightCheck
}

func (r ConfigPreflightReport) Ready() bool {
	if len(r.Checks) == 0 {
		return false
	}
	hasOK := false
	for _, check := range r.Checks {
		switch check.Status {
		case PreflightStatusOK:
			hasOK = true
		case PreflightStatusSkipped:
			continue
		default:
			return false
		}
	}
	return hasOK
}

var newPreflightRepoManager = func(cfg domain.ServiceConfig) *repoops.Manager {
	return repoops.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func PreflightTrackerConfig(ctx context.Context, cfg domain.ServiceConfig) (*linear.Client, ConfigPreflightReport, error) {
	report := ConfigPreflightReport{
		Checks: []ConfigPreflightCheck{
			{
				ID:    "dispatch",
				Label: "Config prerequisites",
			},
			{
				ID:    "states",
				Label: "Linear workflow states",
			},
			{
				ID:    "labels",
				Label: "Managed Linear labels",
			},
			{
				ID:    "github",
				Label: "Repository backend API access",
			},
		},
	}

	if err := config.ValidateDispatch(cfg); err != nil {
		report.Checks[0].Status = PreflightStatusError
		report.Checks[0].Detail = err.Error()
		return nil, report, err
	}
	report.Checks[0].Status = PreflightStatusOK
	report.Checks[0].Detail = "Tracker credentials and Codex command are configured."

	trackerClient, err := linear.New(cfg)
	if err != nil {
		report.Checks[1].Status = PreflightStatusError
		report.Checks[1].Detail = err.Error()
		return nil, report, err
	}
	if err := trackerClient.ValidateWorkflowStates(ctx, cfg); err != nil {
		report.Checks[1].Status = PreflightStatusError
		report.Checks[1].Detail = err.Error()
		return nil, report, err
	}
	report.Checks[1].Status = PreflightStatusOK
	report.Checks[1].Detail = "Configured project states are available."

	if err := ensureManagedIssueLabels(ctx, trackerClient); err != nil {
		report.Checks[2].Status = PreflightStatusError
		report.Checks[2].Detail = err.Error()
		return nil, report, err
	}
	report.Checks[2].Status = PreflightStatusOK
	report.Checks[2].Detail = "Managed labels exist."

	if strings.TrimSpace(cfg.Repo.APIToken) == "" {
		report.Checks[3].Status = PreflightStatusSkipped
		report.Checks[3].Detail = "Skipped: GITHUB_TOKEN or GH_TOKEN is not configured."
		return trackerClient, report, nil
	}

	if err := validateRepoAccess(ctx, cfg, newPreflightRepoManager(cfg)); err != nil {
		report.Checks[3].Status = PreflightStatusError
		report.Checks[3].Detail = err.Error()
		return nil, report, err
	}
	report.Checks[3].Status = PreflightStatusOK
	report.Checks[3].Detail = "Configured repository backend token can authenticate."

	return trackerClient, report, nil
}
