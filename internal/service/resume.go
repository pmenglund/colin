package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode"

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

type ResumeIssueNotFoundError struct {
	Identifier string
}

func (e *ResumeIssueNotFoundError) Error() string {
	return fmt.Sprintf("Linear issue %q is not a watched Colin issue in this workflow", e.Identifier)
}

type ResumeIssueHasNoThreadError struct {
	Identifier string
}

func (e *ResumeIssueHasNoThreadError) Error() string {
	return fmt.Sprintf("Linear issue %q does not have a persisted Codex thread in Colin metadata yet", e.Identifier)
}

type ResumeSession struct {
	Issue         domain.Issue
	ThreadID      string
	WorkspacePath string
	CLICommand    string
}

// LoadResumeSession resolves the Colin issue, persisted Codex thread id, and local workspace for a selector.
// The selector may be either a persisted Codex thread id or a watched Linear issue identifier.
func LoadResumeSession(ctx context.Context, workflowPath string, selector string) (ResumeSession, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
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

	issue, threadID, err := resolveResumeSelector(ctx, trackerClient, selector)
	if err != nil {
		return ResumeSession{}, err
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := workspace.NewManager(cfg, logger)
	ws, err := manager.Ensure(ctx, issue)
	if err != nil {
		return ResumeSession{}, err
	}

	return ResumeSession{
		Issue:         issue,
		ThreadID:      threadID,
		WorkspacePath: ws.Path,
		CLICommand:    strings.TrimSpace(cfg.Codex.CLICommand),
	}, nil
}

func resolveResumeSelector(ctx context.Context, trackerClient *lineartracker.Client, selector string) (domain.Issue, string, error) {
	issue, err := trackerClient.FindIssueByCodexThreadID(ctx, selector)
	if err == nil {
		return issue, selector, nil
	}

	var ambiguousErr *lineartracker.AmbiguousCodexThreadError
	switch {
	case errors.As(err, &ambiguousErr):
		return domain.Issue{}, "", &AmbiguousResumeThreadError{
			ThreadID:         selector,
			IssueIdentifiers: append([]string(nil), ambiguousErr.IssueIdentifiers...),
		}
	case !errors.Is(err, lineartracker.ErrCodexThreadNotFound):
		return domain.Issue{}, "", err
	}

	issue, err = trackerClient.FindIssueByIdentifier(ctx, selector)
	if err != nil {
		if errors.Is(err, lineartracker.ErrIssueIdentifierNotFound) {
			if looksLikeIssueIdentifier(selector) {
				return domain.Issue{}, "", &ResumeIssueNotFoundError{Identifier: selector}
			}
			return domain.Issue{}, "", &ResumeThreadNotFoundError{ThreadID: selector}
		}
		return domain.Issue{}, "", err
	}

	threadID := ""
	if issue.ColinMetadata != nil {
		threadID = strings.TrimSpace(issue.ColinMetadata.CodexThreadID)
	}
	if threadID == "" {
		return domain.Issue{}, "", &ResumeIssueHasNoThreadError{Identifier: issue.Identifier}
	}
	return issue, threadID, nil
}

func looksLikeIssueIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	dash := strings.LastIndex(value, "-")
	if dash <= 0 || dash == len(value)-1 {
		return false
	}

	key := value[:dash]
	number := value[dash+1:]
	hasUpper := false
	for _, r := range key {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r), unicode.IsDigit(r), r == '_':
		default:
			return false
		}
	}
	if !hasUpper {
		return false
	}
	for _, r := range number {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
