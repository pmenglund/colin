package repoops

import (
	"strings"

	"github.com/pmenglund/colin/internal/domain"
)

// CodexReviewState captures the board-visible Codex PR review status.
type CodexReviewState string

const (
	CodexReviewStateNone               CodexReviewState = ""
	CodexReviewStatePending            CodexReviewState = "pending"
	CodexReviewStateApproved           CodexReviewState = "approved"
	CodexReviewStateUnresolvedFeedback CodexReviewState = "unresolved_feedback"
)

// CodexReviewStateFromContext classifies the current Codex PR review state.
func CodexReviewStateFromContext(reviewContext ReviewContext) CodexReviewState {
	if !strings.EqualFold(strings.TrimSpace(reviewContext.PullRequest.State), "OPEN") {
		return CodexReviewStateNone
	}
	if len(reviewContext.CodexReviewThreads) > 0 {
		return CodexReviewStateUnresolvedFeedback
	}
	if reviewContext.CodexReviewApprovedAt != nil && (reviewContext.CodexReviewRequestedAt == nil || reviewContext.CodexReviewApprovedAt.After(*reviewContext.CodexReviewRequestedAt)) {
		return CodexReviewStateApproved
	}
	if reviewContext.CodexReviewObserved {
		return CodexReviewStateNone
	}
	if reviewContext.CodexReviewRequestedAt != nil && (reviewContext.CodexReviewApprovedAt == nil || !reviewContext.CodexReviewApprovedAt.After(*reviewContext.CodexReviewRequestedAt)) {
		return CodexReviewStatePending
	}
	return CodexReviewStateNone
}

// LinearLabelForCodexReviewState maps a Codex review state to its managed Linear label.
func LinearLabelForCodexReviewState(state CodexReviewState) string {
	switch state {
	case CodexReviewStatePending:
		return domain.CodexReviewPendingLabel
	case CodexReviewStateApproved:
		return domain.CodexReviewApprovedLabel
	case CodexReviewStateUnresolvedFeedback:
		return domain.CodexReviewUnresolvedLabel
	default:
		return ""
	}
}
