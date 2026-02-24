package worker

import "strings"

type executionContextInput struct {
	ThreadID     string
	BranchName   string
	WorktreePath string
}

type reviewCommentInput struct {
	ExecutionSummary  string
	ReviewStateName   string
	PRURL             string
	BeforeEvidenceRef string
	AfterEvidenceRef  string
}

type threadResumeFallbackCommentInput struct {
	PreviousThreadID string
	NewThreadID      string
	Reason           string
}

func buildExecutionContextComment(input executionContextInput) string {
	var b strings.Builder
	b.WriteString("Starting Codex turn with current execution context.\n\n")
	writeExecutionContextSection(&b, input)
	return b.String()
}

func buildReviewComment(input reviewCommentInput) string {
	summary := strings.TrimSpace(input.ExecutionSummary)
	if summary == "" {
		summary = "Codex execution completed; no additional details were provided."
	}
	reviewStateName := strings.TrimSpace(input.ReviewStateName)
	if reviewStateName == "" {
		reviewStateName = "Review"
	}

	var b strings.Builder
	b.WriteString("Moved to **")
	b.WriteString(reviewStateName)
	b.WriteString("** after Codex execution.\n\n")
	b.WriteString("## Execution Summary\n")
	b.WriteString(summary)

	prURL := strings.TrimSpace(input.PRURL)
	if prURL != "" {
		b.WriteString("\n\n## Pull Request\n")
		b.WriteString("- URL: ")
		b.WriteString(prURL)
	}

	beforeEvidenceRef := strings.TrimSpace(input.BeforeEvidenceRef)
	afterEvidenceRef := strings.TrimSpace(input.AfterEvidenceRef)
	if beforeEvidenceRef != "" {
		b.WriteString("\n- Before evidence attachment: ")
		b.WriteString(beforeEvidenceRef)
	}
	if afterEvidenceRef != "" {
		b.WriteString("\n- After evidence attachment: ")
		b.WriteString(afterEvidenceRef)
	}

	return b.String()
}

func buildThreadResumeFallbackComment(input threadResumeFallbackCommentInput) string {
	var b strings.Builder
	b.WriteString("Could not resume the previous Codex thread; started a new thread instead.\n\n")
	b.WriteString("## Thread Resume Fallback\n")
	b.WriteString("- Previous thread: ")
	b.WriteString(formatContextValue(input.PreviousThreadID))
	b.WriteString("\n- New thread: ")
	b.WriteString(formatContextValue(input.NewThreadID))
	b.WriteString("\n- Reason: ")
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "resume failed without an error message"
	}
	b.WriteString(reason)
	return b.String()
}

func writeExecutionContextSection(b *strings.Builder, input executionContextInput) {
	b.WriteString("## Execution Context\n")
	b.WriteString("- Thread: ")
	b.WriteString(formatContextValue(input.ThreadID))
	b.WriteString("\n- Branch: ")
	b.WriteString(formatContextValue(input.BranchName))
	b.WriteString("\n- Worktree: ")
	b.WriteString(formatContextValue(input.WorktreePath))
}

func formatContextValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "_not recorded_"
	}
	return "`" + trimmed + "`"
}
