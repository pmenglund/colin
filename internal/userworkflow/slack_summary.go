package userworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/notify"
)

// SlackIssueSummary builds the stable Slack-facing summary for one tracked issue.
func SlackIssueSummary(cfg domain.ServiceConfig, issue domain.Issue) notify.IssueSummary {
	summary := notify.IssueSummary{
		Identifier:     strings.TrimSpace(issue.Identifier),
		Title:          strings.TrimSpace(issue.Title),
		State:          strings.TrimSpace(issue.State),
		NextAction:     slackNextAction(cfg, issue),
		LinearURL:      stringPtrValue(issue.URL),
		PullRequestURL: slackPullRequestURL(issue),
		MetadataURL:    slackMetadataURL(issue),
		ExecPlanURL:    slackExecPlanURL(issue),
	}
	summary.Fingerprint = slackSummaryFingerprint(summary)
	return summary
}

func slackNextAction(cfg domain.ServiceConfig, issue domain.Issue) string {
	if hasIssueLabel(issue, domain.PausedIssueLabel) {
		return "Human action is required to clear the pause before Colin will continue."
	}
	switch state := strings.TrimSpace(issue.State); {
	case containsState(cfg.Tracker.ActiveStates, state):
		return "Colin is actively working this issue and will keep retrying while it stays in an active state."
	case containsState(cfg.Repo.PublishStates, state):
		return "Review the PR, then move the issue back to active work or forward to Merge."
	case strings.EqualFold(state, "Refine"):
		return "Clarify the issue and move it back to active work when it is ready."
	case containsState(cfg.Repo.MergeStates, state):
		return "Colin is handling merge automation. Human action is only required if the issue returns to Review."
	case containsState(cfg.Tracker.TerminalStates, state):
		return "The workflow is complete unless the issue is reopened."
	default:
		return "Check the issue in Linear to decide the next handoff."
	}
}

func slackPullRequestURL(issue domain.Issue) string {
	if issue.PullRequest != nil && strings.TrimSpace(issue.PullRequest.URL) != "" {
		return strings.TrimSpace(issue.PullRequest.URL)
	}
	if issue.ColinMetadata != nil && strings.TrimSpace(issue.ColinMetadata.PullRequestURL) != "" {
		return strings.TrimSpace(issue.ColinMetadata.PullRequestURL)
	}
	if len(issue.AttachedPullRequests) > 0 && strings.TrimSpace(issue.AttachedPullRequests[0].URL) != "" {
		return strings.TrimSpace(issue.AttachedPullRequests[0].URL)
	}
	return ""
}

func slackMetadataURL(issue domain.Issue) string {
	if issue.ColinMetadata == nil {
		return ""
	}
	return strings.TrimSpace(issue.ColinMetadata.URL)
}

func slackExecPlanURL(issue domain.Issue) string {
	if issue.ExecPlan == nil {
		return ""
	}
	return strings.TrimSpace(issue.ExecPlan.URL)
}

func slackSummaryFingerprint(summary notify.IssueSummary) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{
		summary.Identifier,
		summary.Title,
		summary.State,
		summary.NextAction,
		summary.LinearURL,
		summary.PullRequestURL,
		summary.MetadataURL,
		summary.ExecPlanURL,
	}, "\n")))
	return hex.EncodeToString(hash[:])
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func containsState(values []string, state string) bool {
	state = strings.TrimSpace(state)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), state) {
			return true
		}
	}
	return false
}

func hasIssueLabel(issue domain.Issue, label string) bool {
	label = strings.TrimSpace(label)
	for _, value := range issue.Labels {
		if strings.EqualFold(strings.TrimSpace(value), label) {
			return true
		}
	}
	return false
}
