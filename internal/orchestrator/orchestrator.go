package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	"github.com/pmenglund/colin/internal/repoops"
)

// New constructs an Orchestrator for the supplied runtime dependencies.
func New(runtime Runtime, logger *slog.Logger) *Orchestrator {
	orch := &Orchestrator{
		logger:            logger,
		eventCh:           make(chan any, 256),
		runtime:           runtime,
		subscribers:       map[uint64]chan domain.SnapshotUpdate{},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		stateIssues:       map[string][]domain.StateIssueSummary{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}
	orch.publishSnapshot(time.Now().UTC())
	return orch
}

// RequestRefresh queues an immediate reconciliation cycle without waiting for the next poll interval.
func (o *Orchestrator) RequestRefresh(reason string) (queued bool, coalesced bool) {
	if o == nil {
		return false, false
	}
	if !o.refreshReady.Load() {
		return false, false
	}
	if !o.refreshPending.CompareAndSwap(false, true) {
		return true, true
	}
	select {
	case o.eventCh <- refreshRequestedEvent{reason: strings.TrimSpace(reason)}:
		return true, false
	default:
		o.refreshPending.Store(false)
		return false, false
	}
}

// RefreshReady reports whether the orchestrator loop is ready to accept immediate refresh requests.
func (o *Orchestrator) RefreshReady() bool {
	if o == nil {
		return false
	}
	return o.refreshReady.Load()
}

// UpdateRuntime swaps in a reloaded runtime configuration for future scheduling decisions.
func (o *Orchestrator) UpdateRuntime(runtime Runtime) {
	o.eventCh <- configUpdatedEvent{runtime: runtime}
}

// RequestShutdownDrain asks the orchestrator to stop dispatching new work and let current workers drain.
func (o *Orchestrator) RequestShutdownDrain() bool {
	if o == nil || !o.loopStarted.Load() {
		return false
	}
	o.shutdownRequested.Store(true)
	select {
	case o.eventCh <- shutdownDrainRequestedEvent{}:
		return true
	default:
		return true
	}
}

// Run starts the main event loop and exits when the provided context is canceled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.loopStarted.Store(true)
	defer o.loopStarted.Store(false)
	defer o.refreshReady.Store(false)

	o.logger.Info(
		"orchestrator started",
		"poll_interval", o.runtime.Config.Polling.Interval.String(),
		"max_concurrent_agents", o.runtime.Config.Agent.MaxConcurrentAgents,
	)
	o.handleTick(ctx)

	tick := time.NewTimer(o.runtime.Config.Polling.Interval)
	defer tick.Stop()
	o.refreshReady.Store(true)
	for {
		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator stopping")
			o.stopAll("shutdown")
			return nil
		case <-tick.C:
			o.handleTick(ctx)
			tick.Reset(o.runtime.Config.Polling.Interval)
		case raw := <-o.eventCh:
			switch event := raw.(type) {
			case configUpdatedEvent:
				o.runtime = event.runtime
				o.logger.Info(
					"runtime updated",
					"poll_interval", o.runtime.Config.Polling.Interval.String(),
					"max_concurrent_agents", o.runtime.Config.Agent.MaxConcurrentAgents,
				)
				if !tick.Stop() {
					select {
					case <-tick.C:
					default:
					}
				}
				tick.Reset(o.runtime.Config.Polling.Interval)
			case refreshRequestedEvent:
				o.refreshPending.Store(false)
				o.logger.Info("processing immediate refresh request", "reason", event.reason)
				o.handleTick(ctx)
				if !tick.Stop() {
					select {
					case <-tick.C:
					default:
					}
				}
				tick.Reset(o.runtime.Config.Polling.Interval)
			case shutdownDrainRequestedEvent:
				o.enterShutdownDrain()
			case codexEvent:
				o.handleCodexEvent(ctx, event.event)
			case workerExitedEvent:
				o.handleWorkerExit(ctx, event)
			case retryFiredEvent:
				o.handleRetry(ctx, event.issueID)
			}
			o.publishSnapshot(time.Now().UTC())
		}
	}
}

func (o *Orchestrator) handleTick(ctx context.Context) {
	defer o.publishSnapshot(time.Now().UTC())

	args := []any{
		"running", len(o.running),
		"retrying", len(o.retrying),
		"claimed", len(o.claimed),
	}
	if summaries := o.runningIssueSummaries(time.Now().UTC()); len(summaries) > 0 {
		args = append(args, "running_issues", summaries)
	}
	if summaries := o.retrySummaries(); len(summaries) > 0 {
		args = append(args, "retry_issues", summaries)
	}
	o.logger.Debug("poll tick started", args...)
	o.reconcileRunning(ctx)
	if o.draining {
		trackedIssues := o.refreshIssueStateCounts(ctx)
		o.syncCodexReviewLabels(ctx, trackedIssues)
		o.syncSlackIssues(ctx, trackedIssues)
		args = []any{
			"draining", true,
			"running", len(o.running),
			"retrying", len(o.retrying),
		}
		if summaries := o.runningIssueSummaries(time.Now().UTC()); len(summaries) > 0 {
			args = append(args, "running_issues", summaries)
		}
		o.logger.Debug("poll tick completed", args...)
		return
	}
	if err := config.ValidateDispatch(o.dispatchConfig()); err != nil {
		o.logger.Error("dispatch validation failed", "error", err)
		return
	}
	now := time.Now().UTC()
	var issues []domain.Issue
	dispatchDecision := o.linearBudgetDecision(now, linearRequestDispatch)
	if dispatchDecision.Allowed {
		var err error
		issues, err = o.runtime.Tracker.FetchCandidateIssueSnapshots(ctx)
		if err != nil {
			o.logger.Error("candidate fetch failed", "error", err)
			return
		}
	} else {
		o.logger.Debug("dispatch candidate fetch deferred by Linear request budget", o.linearBudgetLogArgs(dispatchDecision)...)
	}
	o.cleanupReviewSync(issues)
	sortIssues(issues)
	dispatched := 0
	eligible := 0
	for _, issue := range issues {
		if !o.shouldDispatch(issue) {
			continue
		}
		if !o.hasGlobalSlots() {
			break
		}
		detailed, err := o.runtime.Tracker.FetchIssueByID(ctx, issue.ID)
		if err != nil {
			o.logger.Warn("candidate detail fetch failed", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			continue
		}
		issue, ready := o.prepareReviewIssue(ctx, detailed, now)
		if !ready {
			continue
		}
		issue = o.clearPausedLoopMetadataIfUnpaused(ctx, issue)
		if !o.shouldDispatch(issue) {
			continue
		}
		eligible++
		o.dispatch(ctx, issue, nil, nil)
		dispatched++
	}
	var trackedIssues []domain.Issue
	backgroundDecision := o.linearBudgetDecision(now, linearRequestBackground)
	if backgroundDecision.Allowed {
		trackedIssues = o.refreshIssueStateCounts(ctx)
	} else {
		o.logger.Debug("background state-count refresh skipped to preserve Linear reserve", o.linearBudgetLogArgs(backgroundDecision)...)
	}
	var reviewFollowUpIssues []domain.Issue
	trackedIssues, reviewFollowUpIssues = o.syncGitHubReviewFollowUps(ctx, trackedIssues, now)
	o.syncCodexReviewLabels(ctx, trackedIssues)
	o.syncSlackIssues(ctx, trackedIssues)
	for _, issue := range reviewFollowUpIssues {
		if !o.hasGlobalSlots() {
			break
		}
		issue, ready := o.prepareReviewIssue(ctx, issue, now)
		if !ready {
			continue
		}
		if !o.shouldDispatch(issue) {
			continue
		}
		o.dispatch(ctx, issue, nil, nil)
		dispatched++
	}
	args = []any{
		"candidate_count", len(issues),
		"eligible_count", eligible,
		"dispatched_count", dispatched,
		"running", len(o.running),
		"retrying", len(o.retrying),
	}
	if summaries := o.runningIssueSummaries(time.Now().UTC()); len(summaries) > 0 {
		args = append(args, "running_issues", summaries)
	}
	if summaries := o.retrySummaries(); len(summaries) > 0 {
		args = append(args, "retry_issues", summaries)
	}
	o.logger.Debug("poll tick completed", args...)
}

func (o *Orchestrator) dispatchConfig() domain.ServiceConfig {
	cfg := o.runtime.Config
	if o.runtime.Tracker != nil {
		if strings.TrimSpace(cfg.Tracker.Kind) == "" {
			cfg.Tracker.Kind = "linear"
		}
		if strings.TrimSpace(cfg.Tracker.APIKey) == "" {
			cfg.Tracker.APIKey = "runtime"
		}
		if len(cfg.Targets) == 0 && strings.TrimSpace(cfg.Tracker.ProjectSlug) == "" {
			cfg.Tracker.ProjectSlug = "runtime"
		}
	}
	if o.runtime.Runner != nil && strings.TrimSpace(cfg.Codex.Command) == "" {
		cfg.Codex.Command = "codex"
	}
	if o.runtime.Repo != nil && strings.TrimSpace(cfg.Repo.Backend) == "" {
		cfg.Repo.Backend = string(repohost.HostKindGitHub)
	}
	return cfg
}

func (o *Orchestrator) enterShutdownDrain() {
	if o.draining {
		return
	}
	o.shutdownRequested.Store(true)
	o.draining = true
	clearedRetries := 0
	for issueID, state := range o.retrying {
		if state.timer != nil {
			state.timer.Stop()
		}
		delete(o.retrying, issueID)
		delete(o.claimed, issueID)
		clearedRetries++
	}
	o.logger.Info(
		"shutdown drain requested",
		"running", len(o.running),
		"cleared_retries", clearedRetries,
	)
}

func (o *Orchestrator) refreshIssueStateCounts(ctx context.Context) []domain.Issue {
	stateNames := trackedStateNames(o.runtime.Config)
	dashboardStates := dashboardStateNames()
	if len(stateNames) == 0 {
		o.issueStates = map[string]int{}
		o.stateIssues = map[string][]domain.StateIssueSummary{}
		o.pausedIssueStates = map[string]domain.PausedStateSummary{}
		return nil
	}

	issues, err := o.runtime.Tracker.FetchIssueSnapshotsByStates(ctx, stateNames)
	if err != nil {
		o.logger.Warn("issue state count refresh failed", "error", err)
		return nil
	}

	counts := make(map[string]int, len(stateNames))
	stateIssues := map[string][]domain.StateIssueSummary{}
	paused := map[string]domain.PausedStateSummary{}
	for _, issue := range issues {
		counts[issue.State]++
		if config.ContainsState(dashboardStates, issue.State) {
			stateIssues[issue.State] = append(stateIssues[issue.State], stateIssueSummary(issue))
		}
		if !hasIssueLabel(issue, domain.PausedIssueLabel) {
			continue
		}
		summary := paused[issue.State]
		summary.Count++
		if strings.TrimSpace(summary.URL) == "" && issue.URL != nil {
			summary.URL = buildPausedIssueSearchURL(*issue.URL, issue.State)
		}
		paused[issue.State] = summary
	}
	for state, items := range stateIssues {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Identifier != items[j].Identifier {
				return items[i].Identifier < items[j].Identifier
			}
			if items[i].Title != items[j].Title {
				return items[i].Title < items[j].Title
			}
			return items[i].ID < items[j].ID
		})
		stateIssues[state] = items
	}
	o.issueStates = counts
	o.stateIssues = stateIssues
	o.pausedIssueStates = paused
	issues = o.hydrateSchedulingMetadata(ctx, issues)
	return issues
}

func (o *Orchestrator) hydrateSchedulingMetadata(ctx context.Context, issues []domain.Issue) []domain.Issue {
	if len(issues) == 0 || o.runtime.Tracker == nil {
		return issues
	}
	issueIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		if strings.TrimSpace(issue.ID) == "" {
			continue
		}
		issueIDs = append(issueIDs, issue.ID)
	}
	if len(issueIDs) == 0 {
		return issues
	}
	metadataByIssueID, err := o.runtime.Tracker.FetchIssueSchedulingMetadataByIDs(ctx, issueIDs)
	if err != nil {
		o.logger.Warn("issue scheduling metadata refresh failed", "error", err)
		return issues
	}
	if len(metadataByIssueID) == 0 {
		return issues
	}
	hydrated := append([]domain.Issue(nil), issues...)
	for i := range hydrated {
		metadata, ok := metadataByIssueID[hydrated[i].ID]
		if !ok {
			continue
		}
		metadataCopy := metadata
		hydrated[i].ColinMetadata = &metadataCopy
	}
	return hydrated
}

func (o *Orchestrator) syncCodexReviewLabels(ctx context.Context, issues []domain.Issue) {
	if len(issues) == 0 || o.runtime.Tracker == nil {
		return
	}
	for _, issue := range issues {
		if ctx.Err() != nil {
			return
		}
		if !o.shouldFetchCodexReviewLabelState(issue.State) {
			o.syncIssueCodexReviewLabel(ctx, issue, "")
			continue
		}
		if !hasIssueReviewSyncPullRequestSignal(issue) {
			o.syncIssueCodexReviewLabel(ctx, issue, "")
			continue
		}
		if o.runtime.Repo == nil || o.runtime.Workspace == nil {
			continue
		}
		workspace, err := o.runtime.Workspace.Ensure(ctx, issue)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			o.logger.Warn("failed to prepare workspace for codex review label sync", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			continue
		}
		reviewContext, err := o.runtime.Repo.ReviewContext(ctx, issue, workspace.Path)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			o.logger.Warn("failed to read review context for codex review label sync", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			continue
		}
		labelName := repoops.LinearLabelForCodexReviewState(repoops.CodexReviewStateFromContext(reviewContext))
		o.syncIssueCodexReviewLabel(ctx, issue, labelName)
	}
}

func (o *Orchestrator) syncIssueCodexReviewLabel(ctx context.Context, issue domain.Issue, wantLabel string) {
	if wantLabel != "" && !hasIssueLabel(issue, wantLabel) {
		if err := o.runtime.Tracker.AddIssueLabel(ctx, issue.ID, wantLabel); err != nil {
			o.logger.Warn("failed to add codex review label", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "label", wantLabel, "error", err)
			return
		}
	}

	for _, labelName := range domain.ManagedCodexReviewLabels() {
		if labelName == wantLabel || !hasIssueLabel(issue, labelName) {
			continue
		}
		if err := o.runtime.Tracker.RemoveIssueLabel(ctx, issue.ID, labelName); err != nil {
			o.logger.Warn("failed to remove codex review label", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "label", labelName, "error", err)
		}
	}
}

func (o *Orchestrator) shouldFetchCodexReviewLabelState(state string) bool {
	return o.isActive(state) || o.isPublishState(state) || o.isMergeState(state)
}

func (o *Orchestrator) trackerThrottleDelay(now time.Time) time.Duration {
	linearRequests, ok := o.currentLinearRequests()
	if !ok {
		return 0
	}
	if linearRequests.NextAllowedAt == nil || !linearRequests.NextAllowedAt.After(now) {
		return 0
	}
	return linearRequests.NextAllowedAt.Sub(now)
}

func (o *Orchestrator) linearRateLimitLogArgs() []any {
	linearRequests, ok := o.currentLinearRequests()
	if !ok {
		return nil
	}
	args := make([]any, 0, 8)
	if linearRequests.Remaining != nil {
		args = append(args, "linear_requests_remaining", *linearRequests.Remaining)
	}
	if linearRequests.Limit != nil {
		args = append(args, "linear_requests_limit", *linearRequests.Limit)
	}
	if linearRequests.ResetsAt != nil {
		args = append(args, "linear_requests_reset_at", linearRequests.ResetsAt.Format(time.RFC3339))
	}
	if linearRequests.NextAllowedAt != nil {
		args = append(args, "linear_requests_next_allowed_at", linearRequests.NextAllowedAt.Format(time.RFC3339))
	}
	return args
}

func (o *Orchestrator) currentLinearRequests() (domain.RateLimitWindow, bool) {
	limits := o.runtime.Tracker.CurrentRateLimits()
	if len(limits) == 0 {
		return domain.RateLimitWindow{}, false
	}
	linearRequests, ok := limits["linear_requests"]
	if !ok {
		return domain.RateLimitWindow{}, false
	}
	return linearRequests, true
}

func trackedStateNames(cfg domain.ServiceConfig) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, state := range append(config.CandidateStates(cfg), cfg.Tracker.TerminalStates...) {
		key := config.StateKey(state)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, state)
	}
	return out
}

func dashboardStateNames() []string {
	return domain.DashboardStateNames()
}

func buildPausedIssueSearchURL(issueURL string, state string) string {
	parsed, err := url.Parse(strings.TrimSpace(issueURL))
	if err != nil {
		return ""
	}
	if strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Host) == "" {
		return ""
	}

	pathSegments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(pathSegments) == 0 || strings.TrimSpace(pathSegments[0]) == "" {
		return ""
	}
	workspace := pathSegments[0]

	query := fmt.Sprintf(`label:%s status:"%s"`, domain.PausedIssueLabel, strings.TrimSpace(state))
	values := url.Values{}
	values.Set("q", query)

	return (&url.URL{
		Scheme:   parsed.Scheme,
		Host:     parsed.Host,
		Path:     "/" + workspace + "/search",
		RawQuery: values.Encode(),
	}).String()
}

func (o *Orchestrator) handleCodexEvent(ctx context.Context, event codex.Event) {
	entry, ok := o.running[event.IssueID]
	if !ok {
		return
	}
	o.handleCommentEvent(ctx, entry, event)
	entry.session.SessionID = event.SessionID
	entry.session.ThreadID = event.ThreadID
	entry.session.TurnID = event.TurnID
	entry.session.CodexAppServerPID = event.PID
	entry.session.LastCodexEvent = event.Event
	entry.session.LastCodexMessage = event.Message
	entry.session.LastCodexTimestamp = &event.Timestamp
	if event.Event == codex.EventIssueStateRefreshed {
		o.applyObservedIssueStateTransition(event.PrevState, event.State)
		o.applyObservedStateIssueTransition(entry.issue, event.PrevState, event.State)
	}
	if event.State != "" {
		entry.issue.State = event.State
	}
	if event.Event == codex.EventSessionStarted {
		entry.session.TurnCount++
	}
	o.applyUsage(entry, event.Usage)
	if event.ContextWindow != nil {
		entry.session.ContextWindow = cloneContextWindowUsage(event.ContextWindow)
	}
	o.appendOutput(entry, event)
	if event.RateLimits != nil {
		o.rateLimits = event.RateLimits
	}
	switch event.Event {
	case codex.EventSessionStarted:
		o.logger.Info(
			"codex session started",
			"issue_id", event.IssueID,
			"issue_identifier", event.Identifier,
			"session_id", event.SessionID,
			"thread_id", event.ThreadID,
			"turn_id", event.TurnID,
			"turn_count", entry.session.TurnCount,
			"state", entry.issue.State,
		)
	case codex.EventApprovalAutoApproved:
		o.logger.Info(
			"codex approval auto-approved",
			"issue_id", event.IssueID,
			"issue_identifier", event.Identifier,
			"session_id", event.SessionID,
			"turn_id", event.TurnID,
		)
	case codex.EventTurnCompleted, codex.EventTurnFailed, codex.EventTurnCancelled, codex.EventTurnInputRequired:
		o.logger.Info(
			"codex turn event",
			"issue_id", event.IssueID,
			"issue_identifier", event.Identifier,
			"session_id", event.SessionID,
			"turn_id", event.TurnID,
			"turn_count", entry.session.TurnCount,
			"state", entry.issue.State,
			"event", event.Event,
			"message", event.Message,
			"input_tokens", entry.session.CodexInputTokens,
			"output_tokens", entry.session.CodexOutputTokens,
			"total_tokens", entry.session.CodexTotalTokens,
		)
	}
}

func (o *Orchestrator) applyObservedIssueStateTransition(previousState string, currentState string) {
	previousKey := config.StateKey(previousState)
	currentKey := config.StateKey(currentState)
	if previousKey == "" || currentKey == "" || previousKey == currentKey {
		return
	}

	trackedStates := trackedStateNames(o.runtime.Config)
	if len(trackedStates) == 0 {
		return
	}
	if !config.ContainsState(trackedStates, previousState) || !config.ContainsState(trackedStates, currentState) {
		return
	}

	if o.issueStates == nil {
		o.issueStates = map[string]int{}
	}
	for state, count := range o.issueStates {
		if config.StateKey(state) != previousKey {
			continue
		}
		if count > 0 {
			o.issueStates[state] = count - 1
		}
		break
	}
	for state := range o.issueStates {
		if config.StateKey(state) != currentKey {
			continue
		}
		o.issueStates[state]++
		return
	}
	o.issueStates[currentState]++
}

func (o *Orchestrator) applyObservedStateIssueTransition(issue domain.Issue, previousState string, currentState string) {
	previousKey := config.StateKey(previousState)
	currentKey := config.StateKey(currentState)
	if previousKey == "" || currentKey == "" || previousKey == currentKey {
		return
	}

	dashboardStates := dashboardStateNames()
	if len(dashboardStates) == 0 {
		return
	}
	if !config.ContainsState(dashboardStates, previousState) && !config.ContainsState(dashboardStates, currentState) {
		return
	}

	summary := stateIssueSummary(issue)
	if strings.TrimSpace(summary.Identifier) == "" {
		return
	}
	if o.stateIssues == nil {
		o.stateIssues = map[string][]domain.StateIssueSummary{}
	}

	for state, items := range o.stateIssues {
		filtered := items[:0]
		for _, item := range items {
			if sameStateIssueSummary(item, summary) {
				continue
			}
			filtered = append(filtered, item)
		}
		if len(filtered) == 0 {
			delete(o.stateIssues, state)
			continue
		}
		o.stateIssues[state] = append([]domain.StateIssueSummary(nil), filtered...)
	}

	if !config.ContainsState(dashboardStates, currentState) {
		return
	}
	o.stateIssues[currentState] = append(o.stateIssues[currentState], summary)
	sort.Slice(o.stateIssues[currentState], func(i, j int) bool {
		left := o.stateIssues[currentState][i]
		right := o.stateIssues[currentState][j]
		if left.Identifier != right.Identifier {
			return left.Identifier < right.Identifier
		}
		if left.Title != right.Title {
			return left.Title < right.Title
		}
		return left.ID < right.ID
	})
}

func stateIssueSummary(issue domain.Issue) domain.StateIssueSummary {
	summary := domain.StateIssueSummary{
		ID:         strings.TrimSpace(issue.ID),
		Identifier: strings.TrimSpace(issue.Identifier),
		Title:      strings.TrimSpace(issue.Title),
	}
	if issue.URL != nil {
		summary.URL = strings.TrimSpace(*issue.URL)
	}
	return summary
}

func sameStateIssueSummary(left domain.StateIssueSummary, right domain.StateIssueSummary) bool {
	if left.ID != "" && right.ID != "" {
		return left.ID == right.ID
	}
	return left.Identifier != "" && left.Identifier == right.Identifier
}

func (o *Orchestrator) appendOutput(entry *runningEntry, event codex.Event) {
	if entry == nil {
		return
	}

	message := strings.TrimSpace(event.Message)
	if skipOutputEvent(event.Event, message) {
		return
	}
	if message == "" {
		switch event.Event {
		case codex.EventSessionStarted:
			message = "session started"
		case codex.EventApprovalAutoApproved:
			message = "approval auto-approved"
		default:
			message = event.Event
		}
	}
	if isDuplicateOutput(entry.outputLog, event.Event, message) {
		return
	}

	entry.outputLog = append(entry.outputLog, domain.OutputLog{
		Timestamp: event.Timestamp,
		Event:     event.Event,
		Message:   message,
	})
	if len(entry.outputLog) > 200 {
		entry.outputLog = append([]domain.OutputLog(nil), entry.outputLog[len(entry.outputLog)-200:]...)
	}
}

func skipOutputEvent(eventName, message string) bool {
	switch eventName {
	case codex.EventOtherMessage, codex.EventNotification:
		if message == "" || message == eventName {
			return true
		}
	}
	return false
}

func isDuplicateOutput(log []domain.OutputLog, eventName, message string) bool {
	if len(log) == 0 {
		return false
	}
	last := log[len(log)-1]
	lastMessage := strings.TrimSpace(last.Message)
	currentMessage := strings.TrimSpace(message)
	if lastMessage == "" || currentMessage == "" || lastMessage != currentMessage {
		return false
	}
	if last.Event == eventName {
		return true
	}
	if isTurnTerminalEvent(eventName) && isContentEvent(last.Event) {
		return true
	}
	return false
}

func isTurnTerminalEvent(eventName string) bool {
	switch eventName {
	case codex.EventTurnCompleted, codex.EventTurnFailed, codex.EventTurnCancelled, codex.EventTurnInputRequired:
		return true
	default:
		return false
	}
}

func isContentEvent(eventName string) bool {
	switch eventName {
	case codex.EventOtherMessage, codex.EventNotification:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) applyUsage(entry *runningEntry, usage map[string]int64) {
	if len(usage) == 0 {
		return
	}
	if total, ok := usage["input_tokens"]; ok {
		delta := total - entry.session.LastReportedInputTokens
		if delta > 0 {
			o.totalTokens.InputTokens += delta
		}
		entry.session.CodexInputTokens = total
		entry.session.LastReportedInputTokens = total
	}
	if total, ok := usage["output_tokens"]; ok {
		delta := total - entry.session.LastReportedOutputTokens
		if delta > 0 {
			o.totalTokens.OutputTokens += delta
		}
		entry.session.CodexOutputTokens = total
		entry.session.LastReportedOutputTokens = total
	}
	if total, ok := usage["total_tokens"]; ok {
		delta := total - entry.session.LastReportedTotalTokens
		if delta > 0 {
			o.totalTokens.TotalTokens += delta
		}
		entry.session.CodexTotalTokens = total
		entry.session.LastReportedTotalTokens = total
	}
}
