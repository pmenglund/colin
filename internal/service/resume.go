package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	lineartracker "github.com/pmenglund/colin/internal/tracker/linear"
	"github.com/pmenglund/colin/internal/workspace"
)

var ErrAmbiguousResumeThread = errors.New("ambiguous_resume_thread")

type ResumeThreadNotFoundError struct {
	ThreadID string
}

func (e *ResumeThreadNotFoundError) Error() string {
	return fmt.Sprintf(
		"codex thread %q is not linked from any watched Colin issue; use `codex resume %s` directly if you want to resume it outside Colin",
		e.ThreadID,
		e.ThreadID,
	)
}

type AmbiguousResumeThreadError struct {
	ThreadID         string
	IssueIdentifiers []string
}

func (e *AmbiguousResumeThreadError) Error() string {
	return fmt.Sprintf("codex thread %q is linked from multiple watched Colin issues: %s", e.ThreadID, strings.Join(e.IssueIdentifiers, ", "))
}

func (e *AmbiguousResumeThreadError) Unwrap() error {
	return ErrAmbiguousResumeThread
}

type ResumeSession struct {
	Issue         domain.Issue
	WorkspacePath string
	CLICommand    string
}

// LoadResumeSession resolves the Colin issue and local workspace for a persisted Codex thread id.
func LoadResumeSession(ctx context.Context, workflowPath string, threadID string) (ResumeSession, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ResumeSession{}, &ResumeThreadNotFoundError{}
	}

	_, cfg, err := loadConfig(workflowPath, buildOptions())
	if err != nil {
		return ResumeSession{}, err
	}
	if err := config.ValidateDispatch(cfg); err != nil {
		return ResumeSession{}, err
	}

	trackerClient, err := lineartracker.New(cfg)
	if err != nil {
		return ResumeSession{}, err
	}
	if err := trackerClient.ValidateWorkflowStates(ctx, cfg); err != nil {
		return ResumeSession{}, err
	}

	issue, err := trackerClient.FindIssueByCodexThreadID(ctx, threadID)
	if err != nil {
		switch {
		case errors.Is(err, lineartracker.ErrCodexThreadNotFound):
			return ResumeSession{}, &ResumeThreadNotFoundError{ThreadID: threadID}
		default:
			var ambiguousErr *lineartracker.AmbiguousCodexThreadError
			if errors.As(err, &ambiguousErr) {
				return ResumeSession{}, &AmbiguousResumeThreadError{
					ThreadID:         threadID,
					IssueIdentifiers: append([]string(nil), ambiguousErr.IssueIdentifiers...),
				}
			}
			return ResumeSession{}, err
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := workspace.NewManager(cfg, logger)
	ws, err := manager.Ensure(ctx, issue)
	if err != nil {
		return ResumeSession{}, err
	}

	return ResumeSession{
		Issue:         issue,
		WorkspacePath: ws.Path,
		CLICommand:    strings.TrimSpace(cfg.Codex.CLICommand),
	}, nil
}
