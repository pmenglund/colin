package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
)

func (o *Orchestrator) syncGitHubPullRequestChecks(ctx context.Context, issues []domain.Issue, now time.Time) ([]domain.Issue, []domain.Issue) {
	if len(issues) == 0 || o.runtime.Repo == nil || o.runtime.Workspace == nil {
		return issues, nil
	}
	out := append([]domain.Issue(nil), issues...)
	var repairIssues []domain.Issue
	for i, issue := range out {
		if ctx.Err() != nil {
			return out, repairIssues
		}
		next, queued := o.syncGitHubPullRequestCheck(ctx, issue, now)
		out[i] = next
		if queued {
			repairIssues = append(repairIssues, next)
		}
	}
	return out, repairIssues
}

func (o *Orchestrator) syncGitHubPullRequestCheck(ctx context.Context, issue domain.Issue, now time.Time) (domain.Issue, bool) {
	if !o.shouldWatchPullRequestChecks(issue) {
		return issue, false
	}
	workspace, err := o.runtime.Workspace.Ensure(ctx, issue)
	if err != nil {
		o.logger.Warn("failed to prepare workspace for github pr check scan", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return issue, false
	}
	rollup, err := o.runtime.Repo.PullRequestChecks(ctx, issue, workspace.Path)
	if err != nil {
		o.logger.Warn("failed to read github pr checks", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return issue, false
	}
	if rollup.PullRequest.Number == 0 && strings.TrimSpace(rollup.PullRequest.URL) == "" {
		return issue, false
	}
	if rollup.PullRequest.Number > 0 || strings.TrimSpace(rollup.PullRequest.URL) != "" {
		issue.PullRequest = pullRequestRefFromCheckRollup(rollup)
	}

	metadata := checkMetadata(issue)
	metadata.LastCheckHeadSHA = strings.TrimSpace(rollup.HeadSHA)
	metadata.LastCheckState = checkFingerprint(rollup)
	switch rollup.State {
	case repohost.PullRequestCheckStatePassed:
		if metadata.PendingCheckFailure != nil || metadataChangedForChecks(issue.ColinMetadata, metadata) {
			metadata.PendingCheckFailure = nil
			issue = o.persistPullRequestCheckMetadata(ctx, issue, metadata)
		}
		return issue, false
	case repohost.PullRequestCheckStateFailed:
		failed := firstRepairableCheck(rollup.Failed)
		if failed == nil {
			metadata.PendingCheckFailure = nil
			if metadataChangedForChecks(issue.ColinMetadata, metadata) {
				issue = o.persistPullRequestCheckMetadata(ctx, issue, metadata)
				issue, _ = o.postIssueStatus(ctx, issue, issue.Identifier, nil, nonRepairableCheckFailureMessage(rollup))
			}
			return issue, false
		}
		metadata.PendingCheckFailure = pendingCheckFailure(rollup, *failed, now)
		return o.startPullRequestCheckRepair(ctx, issue, metadata)
	default:
		if metadataChangedForChecks(issue.ColinMetadata, metadata) {
			metadata.PendingCheckFailure = nil
			issue = o.persistPullRequestCheckMetadata(ctx, issue, metadata)
		}
		return issue, false
	}
}

func (o *Orchestrator) shouldWatchPullRequestChecks(issue domain.Issue) bool {
	if !o.isPublishState(issue.State) {
		return false
	}
	if hasIssueLabel(issue, domain.PausedIssueLabel) {
		return false
	}
	if o.runtime.Config.Tracker.AppMode && !issue.DelegatedToColin {
		return false
	}
	if _, ok := o.running[issue.ID]; ok {
		return false
	}
	if _, ok := o.claimed[issue.ID]; ok {
		return false
	}
	return hasIssueReviewSyncPullRequestSignal(issue)
}

func (o *Orchestrator) startPullRequestCheckRepair(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) (domain.Issue, bool) {
	if o.runtime.Tracker == nil {
		return issue, false
	}
	issue = o.persistPullRequestCheckMetadata(ctx, issue, metadata)
	previousState := issue.State
	targetState := targetedReviewFollowUpState(o.runtime.Config.Tracker.ActiveStates)
	if err := o.runtime.Tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
		o.logger.Warn("failed to move issue for github pr check repair", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", targetState, "error", err)
		return issue, false
	}
	issue.State = targetState
	o.applyObservedDashboardIssueTransition(issue, previousState, issue.State)
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	delete(o.completed, issue.ID)
	issue, _ = o.postIssueStatus(ctx, issue, issue.Identifier, nil, checkRepairStartMessage(issue, targetState))
	return issue, true
}

func (o *Orchestrator) persistPullRequestCheckMetadata(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) domain.Issue {
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	if o.runtime.Tracker == nil {
		issue.ColinMetadata = &metadata
		return issue
	}
	persisted, err := o.runtime.Tracker.UpsertIssueMetadata(ctx, issue.ID, metadata)
	if err != nil {
		o.logger.Warn("failed to persist github pr check metadata", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		issue.ColinMetadata = &metadata
		return issue
	}
	issue.ColinMetadata = &persisted
	return issue
}

func firstRepairableCheck(checks []repohost.PullRequestCheck) *repohost.PullRequestCheck {
	for i := range checks {
		switch checks[i].FailureKind {
		case repohost.PullRequestCheckFailureKindTimeout, repohost.PullRequestCheckFailureKindFlaky:
			continue
		default:
			return &checks[i]
		}
	}
	return nil
}

func pendingCheckFailure(rollup repohost.PullRequestCheckRollup, check repohost.PullRequestCheck, now time.Time) *domain.PendingPullRequestCheckFailure {
	return &domain.PendingPullRequestCheckFailure{
		Name:        strings.TrimSpace(check.Name),
		FailureKind: strings.TrimSpace(string(check.FailureKind)),
		Status:      strings.TrimSpace(check.Status),
		Conclusion:  strings.TrimSpace(check.Conclusion),
		DetailsURL:  strings.TrimSpace(check.DetailsURL),
		Summary:     strings.TrimSpace(check.Summary),
		HeadSHA:     strings.TrimSpace(rollup.HeadSHA),
		PRNumber:    rollup.PullRequest.Number,
		PRURL:       strings.TrimSpace(rollup.PullRequest.URL),
		ObservedAt:  &now,
	}
}

func checkMetadata(issue domain.Issue) domain.ColinMetadata {
	if issue.ColinMetadata != nil {
		return *issue.ColinMetadata
	}
	return domain.ColinMetadata{}
}

func metadataChangedForChecks(previous *domain.ColinMetadata, next domain.ColinMetadata) bool {
	if previous == nil {
		return strings.TrimSpace(next.LastCheckHeadSHA) != "" || strings.TrimSpace(next.LastCheckState) != "" || next.PendingCheckFailure != nil
	}
	return strings.TrimSpace(previous.LastCheckHeadSHA) != strings.TrimSpace(next.LastCheckHeadSHA) ||
		strings.TrimSpace(previous.LastCheckState) != strings.TrimSpace(next.LastCheckState) ||
		!samePendingCheckFailure(previous.PendingCheckFailure, next.PendingCheckFailure)
}

func samePendingCheckFailure(left, right *domain.PendingPullRequestCheckFailure) bool {
	if left == nil || right == nil {
		return left == right
	}
	return strings.TrimSpace(left.Name) == strings.TrimSpace(right.Name) &&
		strings.TrimSpace(left.FailureKind) == strings.TrimSpace(right.FailureKind) &&
		strings.TrimSpace(left.HeadSHA) == strings.TrimSpace(right.HeadSHA) &&
		left.PRNumber == right.PRNumber
}

func checkFingerprint(rollup repohost.PullRequestCheckRollup) string {
	parts := []string{strings.TrimSpace(string(rollup.State)), strings.TrimSpace(rollup.HeadSHA)}
	for _, check := range rollup.Failed {
		parts = append(parts, strings.TrimSpace(check.Name)+":"+strings.TrimSpace(string(check.FailureKind))+":"+strings.TrimSpace(check.Conclusion))
	}
	for _, check := range rollup.Pending {
		parts = append(parts, strings.TrimSpace(check.Name)+":pending")
	}
	return strings.Join(parts, "|")
}

func pullRequestRefFromCheckRollup(rollup repohost.PullRequestCheckRollup) *domain.PullRequestRef {
	pr := rollup.PullRequest
	if pr.Number == 0 && strings.TrimSpace(pr.URL) == "" {
		return nil
	}
	return &domain.PullRequestRef{
		Number:  pr.Number,
		URL:     strings.TrimSpace(pr.URL),
		State:   strings.TrimSpace(pr.State),
		HeadRef: strings.TrimSpace(pr.HeadRefName),
		BaseRef: strings.TrimSpace(pr.BaseRefName),
	}
}

func checkRepairStartMessage(issue domain.Issue, targetState string) string {
	failure := (*domain.PendingPullRequestCheckFailure)(nil)
	if issue.ColinMetadata != nil {
		failure = issue.ColinMetadata.PendingCheckFailure
	}
	if failure == nil {
		return fmt.Sprintf("GitHub reported a PR check failure, so Colin moved this issue to `%s` to repair it.", targetState)
	}
	lines := []string{
		fmt.Sprintf("GitHub check `%s` failed on PR #%d, so Colin moved this issue to `%s` to repair the actual CI failure.", strings.TrimSpace(failure.Name), failure.PRNumber, targetState),
		fmt.Sprintf("- Classification: `%s`", strings.TrimSpace(failure.FailureKind)),
	}
	if strings.TrimSpace(failure.HeadSHA) != "" {
		lines = append(lines, fmt.Sprintf("- Head SHA: `%s`", strings.TrimSpace(failure.HeadSHA)))
	}
	if strings.TrimSpace(failure.DetailsURL) != "" {
		lines = append(lines, "- Details: "+strings.TrimSpace(failure.DetailsURL))
	}
	lines = append(lines, "- What Colin is doing next: starting a coding round with this check failure in the prompt.")
	return strings.Join(lines, "\n")
}

func nonRepairableCheckFailureMessage(rollup repohost.PullRequestCheckRollup) string {
	kinds := map[repohost.PullRequestCheckFailureKind]int{}
	for _, check := range rollup.Failed {
		kinds[check.FailureKind]++
	}
	lines := []string{
		fmt.Sprintf("GitHub reported PR check failures on PR #%d, but Colin classified them as non-code failures and left the issue in `Review`.", rollup.PullRequest.Number),
	}
	if kinds[repohost.PullRequestCheckFailureKindTimeout] > 0 {
		lines = append(lines, fmt.Sprintf("- Timeout failures: `%d`", kinds[repohost.PullRequestCheckFailureKindTimeout]))
	}
	if kinds[repohost.PullRequestCheckFailureKindFlaky] > 0 {
		lines = append(lines, fmt.Sprintf("- Flaky failures: `%d`", kinds[repohost.PullRequestCheckFailureKindFlaky]))
	}
	lines = append(lines, "- What Colin is doing next: waiting for the checks to be retried or updated.")
	return strings.Join(lines, "\n")
}
