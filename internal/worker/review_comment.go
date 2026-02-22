package worker

import "strings"

type executionContextInput struct {
	ThreadID     string
	BranchName   string
	WorktreePath string
}

type reviewCommentInput struct {
	ExecutionSummary string
	ReviewStateName  string
	ThreadID         string
	BranchName       string
	WorktreePath     string
	TranscriptRef    string
	ScreenshotRef    string
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
	b.WriteString("\n\n")
	writeExecutionContextSection(&b, executionContextInput{
		ThreadID:     input.ThreadID,
		BranchName:   input.BranchName,
		WorktreePath: input.WorktreePath,
	})

	transcriptRef := strings.TrimSpace(input.TranscriptRef)
	screenshotRef := strings.TrimSpace(input.ScreenshotRef)
	if transcriptRef != "" || screenshotRef != "" {
		b.WriteString("\n\n## Evidence\n")
		if transcriptRef != "" {
			b.WriteString("- Terminal transcript: ")
			b.WriteString(transcriptRef)
			if screenshotRef != "" {
				b.WriteString("\n")
			}
		}
		if screenshotRef != "" {
			b.WriteString("- Screenshot: ")
			b.WriteString(screenshotRef)
		}
	}

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
