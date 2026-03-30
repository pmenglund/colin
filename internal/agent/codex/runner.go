package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/tracker"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

// Runner executes one issue attempt inside a workspace using the Codex app-server protocol.
type Runner struct {
	cfg        domain.ServiceConfig
	workflow   domain.WorkflowDefinition
	repo       *repoops.Manager
	tracker    tracker.Client
	workspaces *workspace.Manager
	logger     *slog.Logger
}

const (
	outcomeReadyForReview = "COLIN_OUTCOME: READY_FOR_REVIEW"
	outcomeNeedsSpec      = "COLIN_OUTCOME: NEEDS_SPEC"
	refineStateName       = "Refine"
	metadataOutcomeReady  = "ready_for_review"
	metadataOutcomeSpec   = "needs_spec"
	metadataOutcomeMax    = "max_turns"
	metadataOutcomeMerged = "merged"
)

// NewRunner constructs a Runner bound to the current workflow, tracker, and workspace manager.
func NewRunner(cfg domain.ServiceConfig, def domain.WorkflowDefinition, trackerClient tracker.Client, manager *workspace.Manager, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:        cfg,
		workflow:   def,
		repo:       repoops.NewManager(cfg, logger),
		tracker:    trackerClient,
		workspaces: manager,
		logger:     logger,
	}
}

// Run executes one worker lifecycle for an issue, including workspace hooks and continuation turns.
func (r *Runner) Run(ctx context.Context, issue domain.Issue, attempt *int, onEvent func(Event)) Result {
	runType := RunTypeCoding
	switch {
	case isPublishState(r.cfg, issue.State):
		runType = RunTypeReviewPublish
	case isMergeState(r.cfg, issue.State):
		runType = RunTypeMerge
	}

	emit := func(event Event) {
		if onEvent == nil {
			return
		}
		event.RunType = runType
		event.Attempt = attemptNumber(attempt)
		if event.State == "" {
			event.State = issue.State
		}
		onEvent(event)
	}

	ws, err := r.workspaces.Ensure(ctx, issue)
	if err != nil {
		return Result{Issue: issue, RunType: runType, Status: "failed", Err: err}
	}
	r.logger.Info(
		"workspace prepared",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", ws.Path,
		"created_now", ws.CreatedNow,
		"attempt", attemptNumber(attempt),
	)
	emit(Event{
		Event:     EventWorkspacePrepared,
		Timestamp: time.Now().UTC(),
		Workspace: ws.Path,
		State:     issue.State,
		Message:   "Workspace prepared",
	})
	issue = r.persistActualBranchNameBestEffort(ctx, issue, ws.Path)
	if err := r.workspaces.RunBeforeRun(ctx, ws.Path); err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	defer func() {
		if err := r.workspaces.RunAfterRun(context.Background(), ws.Path); err != nil {
			r.logger.Warn("after_run hook failed", "workspace_path", ws.Path, "error", err)
		}
	}()

	current, err := r.moveActiveIssueToWorkingState(ctx, issue)
	if err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}

	client := &appServerClient{
		cfg:       r.cfg,
		logger:    r.logger,
		onEvent:   emit,
		issue:     current,
		workspace: ws.Path,
		runType:   runType,
	}
	if isPublishState(r.cfg, issue.State) {
		emit(Event{
			Event:     EventReviewPublishStarted,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Starting publish automation",
		})
		result, err := r.repo.Publish(ctx, issue, ws.Path)
		if err != nil {
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		r.logger.Info(
			"review automation completed",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"workspace_path", ws.Path,
			"branch", result.Branch,
			"base_ref", result.BaseRef,
			"pr_number", result.PRNumber,
			"pr_url", result.PRURL,
			"action", result.Action,
		)
		emit(Event{
			Event:     EventReviewPublishDone,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Publish automation completed",
			Branch:    result.Branch,
			BaseRef:   result.BaseRef,
			PRNumber:  result.PRNumber,
			PRURL:     result.PRURL,
			PRState:   result.PRState,
			Action:    result.Action,
		})
		issue = r.persistActualBranchNameValueBestEffort(ctx, issue, result.Branch)
		issue = r.persistIssueMetadataBestEffort(ctx, issue, codexMetadata(issue, runType, metadataOutcomeReady, ""))
		return Result{
			Issue:         issue,
			RunType:       runType,
			WorkspacePath: ws.Path,
			Status:        "succeeded",
			PR: &domain.PullRequestRef{
				Number: result.PRNumber,
				URL:    result.PRURL,
				State:  result.PRState,
			},
		}
	}
	if isMergeState(r.cfg, issue.State) {
		emit(Event{
			Event:     EventMergeStarted,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Starting merge automation",
		})
		reviewContext, err := r.repo.ReviewContext(ctx, issue, ws.Path)
		if err != nil {
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		current, summary, blocked, err := r.blockMergeForCodexReview(ctx, issue, reviewContext)
		if err != nil {
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		if blocked {
			return Result{
				Issue:         current,
				RunType:       runType,
				WorkspacePath: ws.Path,
				Status:        "succeeded",
				Summary:       summary,
				PR:            pullRequestRef(reviewContext.PullRequest),
			}
		}
		result, err := r.repo.Merge(ctx, issue, ws.Path)
		if err != nil {
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		r.logger.Info(
			"merge automation completed",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"workspace_path", ws.Path,
			"branch", result.Branch,
			"base_ref", result.BaseRef,
			"pr_number", result.PRNumber,
			"pr_url", result.PRURL,
			"pr_state", result.PRState,
			"action", result.Action,
		)
		emit(Event{
			Event:     EventMergeDone,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Merge automation completed",
			Branch:    result.Branch,
			BaseRef:   result.BaseRef,
			PRNumber:  result.PRNumber,
			PRURL:     result.PRURL,
			PRState:   result.PRState,
			Action:    result.Action,
		})
		issue = r.applyPostMergeState(ctx, issue, result.BaseRef)
		issue = r.persistActualBranchNameValueBestEffort(ctx, issue, result.Branch)
		issue = r.persistIssueMetadataBestEffort(ctx, issue, codexMetadata(issue, runType, metadataOutcomeMerged, ""))
		return Result{
			Issue:         issue,
			RunType:       runType,
			WorkspacePath: ws.Path,
			Status:        "succeeded",
			PR: &domain.PullRequestRef{
				Number: result.PRNumber,
				URL:    result.PRURL,
				State:  result.PRState,
			},
		}
	}

	if err := client.start(ctx, ws.Path); err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	r.logger.Info(
		"codex session ready",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", ws.Path,
		"thread_id", client.threadID,
	)
	defer client.stop()

	maxTurnsReached := false
	for turn := 1; turn <= r.cfg.Agent.MaxTurns; turn++ {
		prompt, err := workflow.RenderPrompt(r.workflow, current, attempt)
		if err != nil {
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: client.finalSummary(), Err: err}
		}
		if turn > 1 {
			prompt = fmt.Sprintf(
				"Continue working on %s without restating the original plan. Pick up from the current thread history and leave the issue in a handoff-ready state if appropriate.",
				current.Identifier,
			)
		}
		r.logger.Info(
			"runner turn starting",
			"issue_id", current.ID,
			"issue_identifier", current.Identifier,
			"workspace_path", ws.Path,
			"state", current.State,
			"turn", turn,
			"max_turns", r.cfg.Agent.MaxTurns,
			"continuation", turn > 1,
		)
		startedAt := time.Now()
		if err := client.runTurn(ctx, ws.Path, current, prompt); err != nil {
			duration := time.Since(startedAt).Round(time.Millisecond)
			r.logger.Warn(
				"runner turn failed",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"workspace_path", ws.Path,
				"state", current.State,
				"turn", turn,
				"duration", duration.String(),
				"error", err,
			)
			emit(Event{
				Event:     EventRunFailed,
				Timestamp: time.Now().UTC(),
				Workspace: ws.Path,
				State:     current.State,
				Duration:  duration,
				Message:   err.Error(),
			})
			status := "failed"
			switch {
			case errors.Is(err, ErrTurnTimeout):
				status = "timed_out"
			case errors.Is(err, ErrTurnCancelled):
				status = "failed"
			case errors.Is(err, ErrTurnInputNeeded):
				status = "failed"
			}
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: status, Err: err}
		}

		duration := time.Since(startedAt).Round(time.Millisecond)
		previousState := current.State
		issues, err := r.tracker.FetchIssueStatesByIDs(ctx, []string{current.ID})
		if err != nil {
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		refreshed := false
		if len(issues) > 0 {
			current = mergeIssueContext(current, issues[0])
			refreshed = true
		} else {
			r.logger.Warn(
				"issue state refresh returned no rows",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"turn", turn,
			)
		}
		active := isActive(r.cfg, current.State)
		r.logger.Info(
			"runner turn completed",
			"issue_id", current.ID,
			"issue_identifier", current.Identifier,
			"workspace_path", ws.Path,
			"turn", turn,
			"duration", duration.String(),
			"previous_state", previousState,
			"current_state", current.State,
			"state_refreshed", refreshed,
			"still_active", active,
		)
		emit(Event{
			Event:     EventIssueStateRefreshed,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     current.State,
			PrevState: previousState,
			Duration:  duration,
			Message:   fmt.Sprintf("Turn %d completed; issue state is now %s", turn, current.State),
		})
		if !active {
			r.logger.Info(
				"issue left active states; stopping runner",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"turn", turn,
				"current_state", current.State,
			)
			break
		}
		if strings.TrimSpace(client.finalSummary()) != "" {
			r.logger.Info(
				"runner turn produced a final summary; finishing coding run",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"current_state", current.State,
				"turn", turn,
			)
			break
		}
		if turn < r.cfg.Agent.MaxTurns {
			r.logger.Info(
				"issue still active after turn and no final summary was captured; continuing",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"current_state", current.State,
				"next_turn", turn+1,
				"max_turns", r.cfg.Agent.MaxTurns,
			)
			emit(Event{
				Event:     EventContinuationNeeded,
				Timestamp: time.Now().UTC(),
				Workspace: ws.Path,
				State:     current.State,
				Message:   fmt.Sprintf("Issue is still active in %s; continuing in the same run", current.State),
			})
			continue
		}
		r.logger.Warn(
			"max turns reached while issue is still active",
			"issue_id", current.ID,
			"issue_identifier", current.Identifier,
			"current_state", current.State,
			"max_turns", r.cfg.Agent.MaxTurns,
		)
		maxTurnsReached = true
	}

	summary := client.finalSummary()
	prRef := (*domain.PullRequestRef)(nil)
	threadsHandled := 0
	threadsRemaining := 0
	if len(current.ReviewThreads) > 0 {
		var blocked bool
		current, prRef, threadsHandled, threadsRemaining, summary, blocked = r.finalizeReviewThreads(ctx, current, ws.Path, summary)
		if blocked {
			return Result{
				Issue:            current,
				RunType:          runType,
				WorkspacePath:    ws.Path,
				Status:           "blocked",
				Summary:          summary,
				PR:               prRef,
				ThreadsHandled:   threadsHandled,
				ThreadsRemaining: threadsRemaining,
			}
		}
	}

	handoffOutcome, summary := parseCodingSummaryOutcome(summary)
	if maxTurnsReached {
		summary = appendMaxTurnsSummary(summary, current.State, r.cfg.Agent.MaxTurns)
	}
	current, err = r.persistIssueMetadata(ctx, current, codexMetadataWithDirective(current, runType, codingOutcome(handoffOutcome, maxTurnsReached), "", ""))
	if err != nil {
		return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: summary, PR: prRef, ThreadsHandled: threadsHandled, ThreadsRemaining: threadsRemaining, Err: err}
	}
	current, err = r.moveSuccessfulCodingRunToHandoffState(ctx, current, codingHandoffState(handoffOutcome, maxTurnsReached, r.cfg.Repo.PublishStates))
	if err != nil {
		return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: summary, PR: prRef, ThreadsHandled: threadsHandled, ThreadsRemaining: threadsRemaining, Err: err}
	}

	emit(Event{
		Event:     EventRunSucceeded,
		Timestamp: time.Now().UTC(),
		Workspace: ws.Path,
		State:     current.State,
		Message:   "Run completed successfully",
	})
	return Result{
		Issue:            current,
		RunType:          runType,
		WorkspacePath:    ws.Path,
		Status:           "succeeded",
		Summary:          summary,
		PR:               prRef,
		ThreadsHandled:   threadsHandled,
		ThreadsRemaining: threadsRemaining,
	}
}

func (r *Runner) blockMergeForCodexReview(ctx context.Context, issue domain.Issue, reviewContext repoops.ReviewContext) (domain.Issue, string, bool, error) {
	summary, blocked := buildMergeBlockedSummary(reviewContext)
	if !blocked {
		return issue, "", false, nil
	}
	if r.tracker == nil {
		return issue, "", false, errors.New("missing tracker client")
	}

	targetState, ok := firstConfiguredState(r.cfg.Repo.PublishStates)
	if !ok {
		targetState = "Review"
	}
	if !strings.EqualFold(issue.State, targetState) {
		if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
			return issue, "", false, fmt.Errorf("update issue state to %s: %w", targetState, err)
		}
		issue.State = targetState
		now := time.Now().UTC()
		issue.UpdatedAt = &now
	}
	if pr := pullRequestRef(reviewContext.PullRequest); pr != nil {
		issue.PullRequest = pr
	}
	issue.ReviewThreads = append([]domain.GitHubReviewThread(nil), reviewContext.CodexReviewThreads...)
	return issue, summary, true, nil
}

func (r *Runner) moveActiveIssueToWorkingState(ctx context.Context, issue domain.Issue) (domain.Issue, error) {
	if !isActive(r.cfg, issue.State) {
		return issue, nil
	}

	targetState, ok := nextConfiguredState(r.cfg.Tracker.ActiveStates, issue.State)
	if !ok {
		return issue, nil
	}
	if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
		return issue, fmt.Errorf("update issue state to %s: %w", targetState, err)
	}
	r.logger.Info(
		"issue moved to working state before coding run",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"previous_state", issue.State,
		"current_state", targetState,
	)
	issue.State = targetState
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	return issue, nil
}

func (r *Runner) moveSuccessfulCodingRunToHandoffState(ctx context.Context, issue domain.Issue, targetState string) (domain.Issue, error) {
	if !isActive(r.cfg, issue.State) {
		return issue, nil
	}

	targetState = strings.TrimSpace(targetState)
	if targetState == "" || config.ContainsState([]string{issue.State}, targetState) {
		return issue, nil
	}

	if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
		return issue, fmt.Errorf("update issue state to %s: %w", targetState, err)
	}
	r.logger.Info(
		"issue moved to handoff state after coding run",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"previous_state", issue.State,
		"current_state", targetState,
	)
	issue.State = targetState
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	return issue, nil
}

func (r *Runner) applyPostMergeState(ctx context.Context, issue domain.Issue, targetBranch string) domain.Issue {
	if r.tracker == nil {
		return issue
	}

	stateName, ok, err := r.tracker.ResolveGitAutomationState(ctx, issue.ID, "merge", targetBranch)
	if err != nil {
		r.logger.Warn(
			"failed to resolve post-merge Linear automation state",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"target_branch", targetBranch,
			"error", err,
		)
		return issue
	}
	if !ok || strings.TrimSpace(stateName) == "" || config.ContainsState([]string{issue.State}, stateName) {
		return issue
	}
	if err := r.tracker.UpdateIssueState(ctx, issue.ID, stateName); err != nil {
		r.logger.Warn(
			"failed to update issue to post-merge Linear automation state",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"target_branch", targetBranch,
			"state", stateName,
			"error", err,
		)
		return issue
	}

	r.logger.Info(
		"issue moved to configured post-merge Linear automation state",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"previous_state", issue.State,
		"current_state", stateName,
		"target_branch", targetBranch,
	)
	issue.State = stateName
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	return issue
}

func mergeIssueContext(previous, refreshed domain.Issue) domain.Issue {
	refreshed.Description = firstStringPtr(refreshed.Description, previous.Description)
	refreshed.Priority = firstIntPtr(refreshed.Priority, previous.Priority)
	refreshed.BranchName = firstStringPtr(refreshed.BranchName, previous.BranchName)
	refreshed.URL = firstStringPtr(refreshed.URL, previous.URL)
	if len(refreshed.Labels) == 0 {
		refreshed.Labels = append([]string(nil), previous.Labels...)
	}
	if len(refreshed.BlockedBy) == 0 {
		refreshed.BlockedBy = append([]domain.BlockerRef(nil), previous.BlockedBy...)
	}
	if refreshed.ReviewCycle == nil {
		refreshed.ReviewCycle = previous.ReviewCycle
	}
	if len(refreshed.ReviewFeedback) == 0 {
		refreshed.ReviewFeedback = append([]domain.ReviewFeedback(nil), previous.ReviewFeedback...)
	}
	if len(refreshed.ReviewThreads) == 0 {
		refreshed.ReviewThreads = append([]domain.GitHubReviewThread(nil), previous.ReviewThreads...)
	}
	if refreshed.ColinMetadata == nil {
		refreshed.ColinMetadata = previous.ColinMetadata
	}
	if refreshed.PullRequest == nil {
		refreshed.PullRequest = previous.PullRequest
	}
	if refreshed.CreatedAt == nil {
		refreshed.CreatedAt = previous.CreatedAt
	}
	return refreshed
}

func (r *Runner) finalizeReviewThreads(ctx context.Context, issue domain.Issue, workspacePath string, summary string) (domain.Issue, *domain.PullRequestRef, int, int, string, bool) {
	reviewContext, err := r.repo.ReviewContext(ctx, issue, workspacePath)
	if err != nil {
		return issue, nil, 0, len(issue.ReviewThreads), buildReviewBlockedSummary(summary, nil, 0, len(issue.ReviewThreads), fmt.Sprintf("failed to fetch GitHub review threads: %v", err)), true
	}
	if reviewContext.PullRequest.Number == 0 && strings.TrimSpace(reviewContext.PullRequest.URL) == "" {
		return issue, nil, 0, len(issue.ReviewThreads), buildReviewBlockedSummary(summary, nil, 0, len(issue.ReviewThreads), "no pull request found for the issue branch"), true
	}
	pr := &reviewContext.PullRequest
	if len(reviewContext.Threads) == 0 {
		return issue, pr, 0, 0, buildReviewReadySummary(summary, pr, 0, 0), false
	}

	replyBody := buildReviewThreadReplyBody(summary)
	handled := 0
	failures := 0
	for _, thread := range reviewContext.Threads {
		if err := r.repo.ReplyAndResolveReviewThread(ctx, workspacePath, thread, replyBody); err != nil {
			failures++
			r.logger.Warn(
				"failed to reply to or resolve GitHub review thread",
				"issue_id", issue.ID,
				"issue_identifier", issue.Identifier,
				"thread_id", thread.ID,
				"path", thread.Path,
				"error", err,
			)
			continue
		}
		handled++
	}

	postContext, err := r.repo.ReviewContext(ctx, issue, workspacePath)
	if err != nil {
		return issue, pr, handled, len(reviewContext.Threads), buildReviewBlockedSummary(summary, pr, handled, len(reviewContext.Threads), fmt.Sprintf("failed to verify GitHub review threads after update: %v", err)), true
	}
	remaining := len(postContext.Threads)
	if postContext.PullRequest.Number != 0 || strings.TrimSpace(postContext.PullRequest.URL) != "" {
		pr = &postContext.PullRequest
	}
	if failures > 0 || remaining > 0 {
		return issue, pr, handled, remaining, buildReviewBlockedSummary(summary, pr, handled, remaining, ""), true
	}
	return issue, pr, handled, 0, buildReviewReadySummary(summary, pr, handled, 0), false
}

func buildReviewThreadReplyBody(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "[colin] Addressed in the latest update."
	}
	return "[colin] Addressed in the latest update.\n\n" + summary
}

func buildReviewReadySummary(summary string, pr *domain.PullRequestRef, handled int, remaining int) string {
	lines := []string{"Ready for review."}
	if pr != nil && pr.Number > 0 {
		lines = append(lines, fmt.Sprintf("- PR: `#%d`", pr.Number))
	}
	if pr != nil && strings.TrimSpace(pr.URL) != "" {
		lines = append(lines, fmt.Sprintf("- PR URL: %s", pr.URL))
	}
	lines = append(lines, fmt.Sprintf("- Review threads handled: `%d`", handled))
	lines = append(lines, fmt.Sprintf("- Review threads remaining: `%d`", remaining))
	if strings.TrimSpace(summary) != "" {
		lines = append(lines, "", "Codex summary:", "", summary)
	}
	return strings.Join(lines, "\n")
}

func buildReviewBlockedSummary(summary string, pr *domain.PullRequestRef, handled int, remaining int, reason string) string {
	lines := []string{"Staying in `Todo` until GitHub review feedback is fully addressed."}
	if pr != nil && pr.Number > 0 {
		lines = append(lines, fmt.Sprintf("- PR: `#%d`", pr.Number))
	}
	if pr != nil && strings.TrimSpace(pr.URL) != "" {
		lines = append(lines, fmt.Sprintf("- PR URL: %s", pr.URL))
	}
	lines = append(lines, fmt.Sprintf("- Review threads handled: `%d`", handled))
	lines = append(lines, fmt.Sprintf("- Review threads remaining: `%d`", remaining))
	if strings.TrimSpace(reason) != "" {
		lines = append(lines, fmt.Sprintf("- Blocker: %s", reason))
	}
	if strings.TrimSpace(summary) != "" {
		lines = append(lines, "", "Codex summary:", "", summary)
	}
	return strings.Join(lines, "\n")
}

func buildMergeBlockedSummary(reviewContext repoops.ReviewContext) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(reviewContext.PullRequest.State), "MERGED") {
		return "", false
	}

	pendingApproval := codexReviewApprovalPending(reviewContext)
	threadCount := len(reviewContext.CodexReviewThreads)
	if !pendingApproval && threadCount == 0 {
		return "", false
	}

	lines := []string{"Returning issue to `Review` because Codex PR feedback still needs to be resolved."}
	if reviewContext.PullRequest.Number > 0 {
		lines = append(lines, fmt.Sprintf("- PR: `#%d`", reviewContext.PullRequest.Number))
	}
	if strings.TrimSpace(reviewContext.PullRequest.URL) != "" {
		lines = append(lines, fmt.Sprintf("- PR URL: %s", reviewContext.PullRequest.URL))
	}
	if pendingApproval {
		lines = append(lines, "- Codex review status: waiting for a `thumbs up` reaction after the latest `eyes` reaction.")
	}
	if threadCount > 0 {
		lines = append(lines, fmt.Sprintf("- Unresolved Codex review threads: `%d`", threadCount))
	}
	lines = append(lines, "- Resolve the remaining Codex PR feedback, then move the issue back to `Merge`.")
	return strings.Join(lines, "\n"), true
}

func codexReviewApprovalPending(reviewContext repoops.ReviewContext) bool {
	if reviewContext.CodexReviewRequestedAt == nil {
		return false
	}
	if reviewContext.CodexReviewApprovedAt == nil {
		return true
	}
	return !reviewContext.CodexReviewApprovedAt.After(*reviewContext.CodexReviewRequestedAt)
}

func pullRequestRef(pr domain.PullRequestRef) *domain.PullRequestRef {
	if pr.Number == 0 && strings.TrimSpace(pr.URL) == "" && strings.TrimSpace(pr.State) == "" {
		return nil
	}
	value := pr
	return &value
}

func firstStringPtr(value, fallback *string) *string {
	if value != nil {
		return value
	}
	return fallback
}

func firstIntPtr(value, fallback *int) *int {
	if value != nil {
		return value
	}
	return fallback
}

func attemptNumber(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func isActive(cfg domain.ServiceConfig, state string) bool {
	stateKey := strings.ToLower(strings.TrimSpace(state))
	for _, active := range cfg.Tracker.ActiveStates {
		if strings.ToLower(strings.TrimSpace(active)) == stateKey {
			return true
		}
	}
	return false
}

func isPublishState(cfg domain.ServiceConfig, state string) bool {
	return config.ContainsState(cfg.Repo.PublishStates, state)
}

func isMergeState(cfg domain.ServiceConfig, state string) bool {
	return config.ContainsState(cfg.Repo.MergeStates, state)
}

func firstConfiguredState(states []string) (string, bool) {
	for _, state := range states {
		state = strings.TrimSpace(state)
		if state != "" {
			return state, true
		}
	}
	return "", false
}

func nextConfiguredState(states []string, current string) (string, bool) {
	currentKey := config.StateKey(current)
	for i, state := range states {
		if config.StateKey(state) != currentKey {
			continue
		}
		for _, candidate := range states[i+1:] {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				return candidate, true
			}
		}
		return "", false
	}
	return "", false
}

func parseCodingSummaryOutcome(summary string) (string, string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return outcomeReadyForReview, ""
	}

	lines := strings.Split(summary, "\n")
	first := strings.TrimSpace(lines[0])
	switch first {
	case outcomeNeedsSpec:
		return outcomeNeedsSpec, strings.TrimSpace(strings.Join(lines[1:], "\n"))
	case outcomeReadyForReview:
		return outcomeReadyForReview, strings.TrimSpace(strings.Join(lines[1:], "\n"))
	default:
		return outcomeReadyForReview, summary
	}
}

func codingOutcome(handoffOutcome string, maxTurnsReached bool) string {
	if maxTurnsReached {
		return metadataOutcomeMax
	}
	if handoffOutcome == outcomeNeedsSpec {
		return metadataOutcomeSpec
	}
	return metadataOutcomeReady
}

func codingHandoffState(handoffOutcome string, maxTurnsReached bool, publishStates []string) string {
	if maxTurnsReached || handoffOutcome == outcomeNeedsSpec {
		return refineStateName
	}
	targetState, _ := firstConfiguredState(publishStates)
	return targetState
}

func appendMaxTurnsSummary(summary string, state string, maxTurns int) string {
	note := fmt.Sprintf("Colin reached the maximum of `%d` turns while the issue remained in `%s`, so it is handing off for human refinement before more implementation work.", maxTurns, state)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return note
	}
	return summary + "\n\n" + note
}

func codexMetadata(issue domain.Issue, runType string, outcome string, summaryCommentID string) domain.ColinMetadata {
	return codexMetadataWithDirective(issue, runType, outcome, summaryCommentID, reviewPublishDirective(issue))
}

func codexMetadataWithDirective(issue domain.Issue, runType string, outcome string, summaryCommentID string, directive string) domain.ColinMetadata {
	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	metadata.ReviewPublishDirective = strings.TrimSpace(directive)
	metadata.LastRunType = strings.TrimSpace(runType)
	metadata.LastOutcome = strings.TrimSpace(outcome)
	metadata.LastSummaryCommentID = strings.TrimSpace(summaryCommentID)
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return metadata
}

func actualBranchMetadata(issue domain.Issue, branch string) (domain.ColinMetadata, bool) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return domain.ColinMetadata{}, false
	}

	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	if strings.TrimSpace(metadata.ActualBranchName) == branch {
		return metadata, false
	}

	metadata.ActualBranchName = branch
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return metadata, true
}

func reviewPublishDirective(issue domain.Issue) string {
	if issue.ColinMetadata == nil {
		return ""
	}
	return strings.TrimSpace(issue.ColinMetadata.ReviewPublishDirective)
}

func (r *Runner) persistIssueMetadata(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) (domain.Issue, error) {
	if r.tracker == nil {
		issue.ColinMetadata = &metadata
		return issue, nil
	}
	persisted, err := r.tracker.UpsertIssueMetadata(ctx, issue.ID, metadata)
	if err != nil {
		return issue, fmt.Errorf("upsert issue metadata: %w", err)
	}
	issue.ColinMetadata = &persisted
	return issue, nil
}

func (r *Runner) persistIssueMetadataBestEffort(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) domain.Issue {
	updated, err := r.persistIssueMetadata(ctx, issue, metadata)
	if err != nil {
		r.logger.Warn(
			"failed to persist issue metadata",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"run_type", metadata.LastRunType,
			"outcome", metadata.LastOutcome,
			"error", err,
		)
		issue.ColinMetadata = &metadata
		return issue
	}
	return updated
}

func (r *Runner) persistActualBranchNameBestEffort(ctx context.Context, issue domain.Issue, workspacePath string) domain.Issue {
	if r.repo == nil {
		return issue
	}
	branch, err := r.repo.CurrentBranch(ctx, workspacePath)
	if err != nil {
		return issue
	}
	return r.persistActualBranchNameValueBestEffort(ctx, issue, branch)
}

func (r *Runner) persistActualBranchNameValueBestEffort(ctx context.Context, issue domain.Issue, branch string) domain.Issue {
	metadata, changed := actualBranchMetadata(issue, branch)
	if !changed {
		return issue
	}
	return r.persistIssueMetadataBestEffort(ctx, issue, metadata)
}
