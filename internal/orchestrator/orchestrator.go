package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/execution"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/logging"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

const continuationDelay = time.Second

// TrackerClient describes the tracker reads required by the orchestrator.
type TrackerClient interface {
	ListCandidateIssues(ctx context.Context, teamID string) ([]linear.Issue, error)
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) (map[string]linear.Issue, error)
	FetchIssuesByStates(ctx context.Context, teamID string, stateNames []string) ([]linear.Issue, error)
}

// ConfigProvider returns the current last-known-good runtime config.
type ConfigProvider interface {
	Current() config.Config
	Reload() error
}

// AgentRunner executes one issue attempt.
type AgentRunner interface {
	RunAttempt(ctx context.Context, req execution.AttemptRequest, sink func(execution.SessionUpdate)) (execution.AttemptResult, error)
}

type workspaceManager interface {
	Ensure(ctx context.Context, issueIdentifier string) (workspace.Workspace, error)
	BeforeRun(ctx context.Context, ws workspace.Workspace) error
	AfterRun(ctx context.Context, ws workspace.Workspace, attemptErr error)
	Remove(ctx context.Context, ws workspace.Workspace) error
	CleanupTerminal(ctx context.Context, issueIdentifiers []string) error
}

// Options configure a new orchestrator.
type Options struct {
	Tracker    TrackerClient
	Configs    ConfigProvider
	Runner     AgentRunner
	Workspaces workspaceManager
	Logger     *slog.Logger
	Clock      func() time.Time
}

// Orchestrator owns runtime scheduling, reconciliation, and retries.
type Orchestrator struct {
	tracker    TrackerClient
	configs    ConfigProvider
	runner     AgentRunner
	workspaces workspaceManager
	logger     *slog.Logger
	clock      func() time.Time

	mu            sync.Mutex
	running       map[string]*runningEntry
	claimed       map[string]struct{}
	retryAttempts map[string]RetryEntry
	totals        Totals
	completions   chan completion
	started       bool
}

type runningEntry struct {
	issue              linear.Issue
	workspace          workspace.Workspace
	startedAt          time.Time
	lastEventAt        time.Time
	attempt            int
	turnCount          int
	threadID           string
	turnID             string
	lastMessage        string
	lastReportedTotal  int
	lastReportedInput  int
	lastReportedOutput int
	cancel             context.CancelFunc
	stopReason         stopReason
}

type completion struct {
	issueID string
	result  execution.AttemptResult
	err     error
}

type stopReason string

const (
	stopReasonNone     stopReason = ""
	stopReasonTerminal stopReason = "terminal"
	stopReasonInactive stopReason = "inactive"
	stopReasonStalled  stopReason = "stalled"
)

// RetryEntry describes a queued retry.
type RetryEntry struct {
	IssueID       string
	Identifier    string
	Attempt       int
	DueAt         time.Time
	Error         string
	ThreadID      string
	Continuation  bool
	WorkspacePath string
}

// Snapshot returns a point-in-time runtime view.
type Snapshot struct {
	Running     []RunningRow
	Retrying    []RetryRow
	CodexTotals Totals
}

// RunningRow is one running session row in a snapshot.
type RunningRow struct {
	IssueID        string
	Identifier     string
	State          string
	ThreadID       string
	TurnID         string
	TurnCount      int
	WorkspacePath  string
	LastEventAt    time.Time
	LastEvent      string
	LastMessage    string
	StartedAt      time.Time
	CurrentAttempt int
}

// RetryRow is one queued retry row in a snapshot.
type RetryRow struct {
	IssueID    string
	Identifier string
	Attempt    int
	DueAt      time.Time
	Error      string
	ThreadID   string
}

// Totals captures aggregate token and runtime totals.
type Totals struct {
	InputTokens    int
	OutputTokens   int
	TotalTokens    int
	SecondsRunning float64
}

// New constructs a new orchestrator.
func New(opts Options) (*Orchestrator, error) {
	if opts.Tracker == nil {
		return nil, errors.New("tracker is required")
	}
	if opts.Configs == nil {
		return nil, errors.New("config provider is required")
	}
	if opts.Workspaces == nil {
		return nil, errors.New("workspace manager is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = logging.NewSlog(nil, logging.LevelInfo, false)
	}
	return &Orchestrator{
		tracker:       opts.Tracker,
		configs:       opts.Configs,
		runner:        opts.Runner,
		workspaces:    opts.Workspaces,
		logger:        logger,
		clock:         clock,
		running:       map[string]*runningEntry{},
		claimed:       map[string]struct{}{},
		retryAttempts: map[string]RetryEntry{},
		completions:   make(chan completion, 128),
	}, nil
}

// Run starts the continuous orchestration loop.
func (o *Orchestrator) Run(ctx context.Context) error {
	cfg := o.configs.Current()
	if err := o.start(ctx, cfg); err != nil {
		return err
	}
	o.logger.Info("orchestrator run start",
		"poll_every", cfg.PollEvery.String(),
		"max_concurrency", cfg.MaxConcurrency,
		"workflow", cfg.WorkflowPath,
	)
	ticker := time.NewTicker(cfg.PollEvery)
	defer ticker.Stop()

	for {
		if err := o.tick(ctx); err != nil {
			o.logger.Error("orchestrator tick failed", "error", err)
		}
		cfg = o.configs.Current()
		select {
		case <-ctx.Done():
			o.cancelAll()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunOnce runs until no attempts are left running and no immediate retries are due.
func (o *Orchestrator) RunOnce(ctx context.Context) error {
	cfg := o.configs.Current()
	if err := o.start(ctx, cfg); err != nil {
		return err
	}
	o.logger.Info("orchestrator run once",
		"max_concurrency", cfg.MaxConcurrency,
		"workflow", cfg.WorkflowPath,
	)
	for {
		if err := o.tick(ctx); err != nil {
			return err
		}
		if o.isIdle() {
			return nil
		}
		select {
		case <-ctx.Done():
			o.cancelAll()
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (o *Orchestrator) start(ctx context.Context, cfg config.Config) error {
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return nil
	}
	o.started = true
	o.mu.Unlock()

	if err := o.cleanupTerminal(ctx, cfg); err != nil {
		o.logger.Warn("startup terminal cleanup failed", "error", err)
	}
	return nil
}

func (o *Orchestrator) tick(ctx context.Context) error {
	if err := o.configs.Reload(); err != nil {
		o.logger.Warn("config reload failed; keeping last known good config", "error", err)
	}
	cfg := o.configs.Current()
	now := o.clock().UTC()
	o.logger.Debug("orchestrator tick", "time", now.Format(time.RFC3339), "workflow", cfg.WorkflowPath)

	o.drainCompletions(cfg, now)
	o.reconcileStalled(cfg, now)
	if err := o.reconcileRunningStates(ctx, cfg, now); err != nil {
		o.logger.Warn("running state reconciliation failed", "error", err)
	}

	o.logger.Debug("tracker fetch start", "operation", "candidate_issues", "team", cfg.LinearTeamID)
	issues, err := o.tracker.ListCandidateIssues(ctx, cfg.LinearTeamID)
	if err != nil {
		return err
	}
	o.logger.Info("tracker fetch complete", "operation", "candidate_issues", "count", len(issues))
	issues = o.filterCandidates(cfg, issues)
	o.logger.Debug("candidate issues filtered", "count", len(issues))
	o.processDueRetries(ctx, cfg, now, issues)
	o.dispatchEligible(ctx, cfg, now, issues)
	return nil
}

func (o *Orchestrator) cleanupTerminal(ctx context.Context, cfg config.Config) error {
	o.logger.Debug("tracker fetch start", "operation", "terminal_issues", "team", cfg.LinearTeamID, "states", strings.Join(cfg.TerminalStates, ","))
	issues, err := o.tracker.FetchIssuesByStates(ctx, cfg.LinearTeamID, cfg.TerminalStates)
	if err != nil {
		return err
	}
	o.logger.Info("tracker fetch complete", "operation", "terminal_issues", "count", len(issues))
	identifiers := make([]string, 0, len(issues))
	for _, issue := range issues {
		identifiers = append(identifiers, issue.Identifier)
	}
	return o.workspaces.CleanupTerminal(ctx, identifiers)
}

func (o *Orchestrator) reconcileStalled(cfg config.Config, now time.Time) {
	if cfg.Codex.StallTimeout <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, entry := range o.running {
		last := entry.lastEventAt
		if last.IsZero() {
			last = entry.startedAt
		}
		if now.Sub(last) <= cfg.Codex.StallTimeout {
			continue
		}
		entry.stopReason = stopReasonStalled
		entry.cancel()
	}
}

func (o *Orchestrator) reconcileRunningStates(ctx context.Context, cfg config.Config, _ time.Time) error {
	ids := o.runningIDs()
	if len(ids) == 0 {
		return nil
	}
	o.logger.Debug("tracker fetch start", "operation", "running_issue_states", "count", len(ids))
	current, err := o.tracker.FetchIssueStatesByIDs(ctx, ids)
	if err != nil {
		return err
	}
	o.logger.Debug("tracker fetch complete", "operation", "running_issue_states", "count", len(current))

	o.mu.Lock()
	defer o.mu.Unlock()
	for issueID, entry := range o.running {
		issue, ok := current[issueID]
		if !ok {
			continue
		}
		entry.issue = issue
		if isInStates(issue.StateName, cfg.TerminalStates) {
			entry.stopReason = stopReasonTerminal
			entry.cancel()
			continue
		}
		if !isInStates(issue.StateName, cfg.ActiveStates) {
			entry.stopReason = stopReasonInactive
			entry.cancel()
		}
	}
	return nil
}

func (o *Orchestrator) processDueRetries(ctx context.Context, cfg config.Config, now time.Time, issues []linear.Issue) {
	candidates := map[string]linear.Issue{}
	for _, issue := range issues {
		candidates[issue.ID] = issue
	}

	o.mu.Lock()
	retries := make([]RetryEntry, 0, len(o.retryAttempts))
	for _, retry := range o.retryAttempts {
		if !retry.DueAt.After(now) {
			retries = append(retries, retry)
		}
	}
	o.mu.Unlock()
	sort.Slice(retries, func(i, j int) bool {
		if retries[i].DueAt.Equal(retries[j].DueAt) {
			return retries[i].Identifier < retries[j].Identifier
		}
		return retries[i].DueAt.Before(retries[j].DueAt)
	})

	for _, retry := range retries {
		issue, ok := candidates[retry.IssueID]
		if !ok {
			o.release(retry.IssueID)
			continue
		}
		if !o.dispatchOne(ctx, cfg, now, issue, retry.Attempt, retry.ThreadID, retry.Continuation) {
			break
		}
	}
}

func (o *Orchestrator) dispatchEligible(ctx context.Context, cfg config.Config, now time.Time, issues []linear.Issue) {
	sortIssues(issues)
	for _, issue := range issues {
		if !o.isEligible(cfg, issue) {
			continue
		}
		if !o.dispatchOne(ctx, cfg, now, issue, 0, "", false) {
			return
		}
	}
}

func (o *Orchestrator) dispatchOne(ctx context.Context, cfg config.Config, now time.Time, issue linear.Issue, attempt int, threadID string, continuation bool) bool {
	if !o.hasCapacity(cfg, issue.StateName) {
		return false
	}
	if cfg.DryRun || o.runner == nil {
		o.logger.Info("dispatch skipped", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "reason", "dry-run or runner unavailable")
		return true
	}

	o.claim(issue.ID)
	ws, err := o.workspaces.Ensure(ctx, issue.Identifier)
	if err != nil {
		o.scheduleRetry(cfg, issue, attempt+1, err.Error(), threadID, continuation)
		return true
	}
	if err := o.workspaces.BeforeRun(ctx, ws); err != nil {
		o.scheduleRetry(cfg, issue, attempt+1, err.Error(), threadID, continuation)
		return true
	}

	issueCopy := cloneIssue(issue)
	if issueCopy.Metadata == nil {
		issueCopy.Metadata = map[string]string{}
	}
	issueCopy.Metadata[workflow.MetaWorktreePath] = ws.Path
	issueCopy.BranchName = firstNonEmpty(ws.Metadata[workflow.MetaBranchName], issueCopy.BranchName)
	if issueCopy.BranchName != "" {
		issueCopy.Metadata[workflow.MetaBranchName] = issueCopy.BranchName
	}
	if threadID != "" {
		issueCopy.Metadata[workflow.MetaThreadID] = threadID
	}

	runCtx, cancel := context.WithCancel(ctx)
	entry := &runningEntry{
		issue:       issueCopy,
		workspace:   ws,
		startedAt:   now,
		lastEventAt: now,
		attempt:     attempt,
		cancel:      cancel,
		threadID:    threadID,
	}

	o.mu.Lock()
	delete(o.retryAttempts, issue.ID)
	o.running[issue.ID] = entry
	o.mu.Unlock()

	o.logger.Info("dispatching issue", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "attempt", attempt)
	go func() {
		var attemptPtr *int
		if attempt > 0 {
			value := attempt
			attemptPtr = &value
		}
		result, err := o.runner.RunAttempt(runCtx, execution.AttemptRequest{
			Issue:         issueCopy,
			Attempt:       attemptPtr,
			WorkspacePath: ws.Path,
			Continuation:  continuation,
		}, func(update execution.SessionUpdate) {
			o.onSessionUpdate(issue.ID, update)
		})
		o.workspaces.AfterRun(context.Background(), ws, err)
		o.completions <- completion{issueID: issue.ID, result: result, err: err}
	}()
	return true
}

func (o *Orchestrator) drainCompletions(cfg config.Config, now time.Time) {
	for {
		select {
		case completed := <-o.completions:
			o.finishRun(cfg, now, completed)
		default:
			return
		}
	}
}

func (o *Orchestrator) finishRun(cfg config.Config, now time.Time, completed completion) {
	o.mu.Lock()
	entry, ok := o.running[completed.issueID]
	if !ok {
		o.mu.Unlock()
		return
	}
	delete(o.running, completed.issueID)
	o.totals.SecondsRunning += now.Sub(entry.startedAt).Seconds()
	stop := entry.stopReason
	issue := entry.issue
	threadID := firstNonEmpty(completed.result.ThreadID, entry.threadID)
	o.mu.Unlock()

	switch stop {
	case stopReasonTerminal:
		_ = o.workspaces.Remove(context.Background(), entry.workspace)
		o.release(completed.issueID)
		return
	case stopReasonInactive:
		o.release(completed.issueID)
		return
	case stopReasonStalled:
		o.scheduleRetry(cfg, issue, entry.attempt+1, string(execution.AttemptStatusStalled), threadID, false)
		return
	}

	if completed.err != nil {
		if errors.Is(completed.err, context.Canceled) {
			o.release(completed.issueID)
			return
		}
		o.scheduleRetry(cfg, issue, entry.attempt+1, completed.err.Error(), threadID, false)
		return
	}
	if completed.result.ShouldContinue && entry.attempt+1 < cfg.MaxTurns {
		o.scheduleContinuation(issue, entry.attempt+1, threadID, entry.workspace.Path)
		return
	}
	o.release(completed.issueID)
}

func (o *Orchestrator) scheduleContinuation(issue linear.Issue, attempt int, threadID string, workspacePath string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.retryAttempts[issue.ID] = RetryEntry{
		IssueID:       issue.ID,
		Identifier:    issue.Identifier,
		Attempt:       attempt,
		DueAt:         o.clock().UTC().Add(continuationDelay),
		ThreadID:      threadID,
		Continuation:  true,
		WorkspacePath: workspacePath,
	}
	o.claimed[issue.ID] = struct{}{}
}

func (o *Orchestrator) scheduleRetry(cfg config.Config, issue linear.Issue, attempt int, errText string, threadID string, continuation bool) {
	if attempt <= 0 {
		attempt = 1
	}
	delay := retryDelay(attempt, cfg.MaxRetryBackoff)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.retryAttempts[issue.ID] = RetryEntry{
		IssueID:      issue.ID,
		Identifier:   issue.Identifier,
		Attempt:      attempt,
		DueAt:        o.clock().UTC().Add(delay),
		Error:        strings.TrimSpace(errText),
		ThreadID:     threadID,
		Continuation: continuation,
	}
	o.claimed[issue.ID] = struct{}{}
}

func retryDelay(attempt int, max time.Duration) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	delay := 10 * time.Second * time.Duration(1<<(attempt-1))
	if max > 0 && delay > max {
		return max
	}
	return delay
}

func (o *Orchestrator) onSessionUpdate(issueID string, update execution.SessionUpdate) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.running[issueID]
	if !ok {
		return
	}
	entry.lastEventAt = update.LastTimestamp
	entry.threadID = firstNonEmpty(update.ThreadID, entry.threadID)
	entry.turnID = firstNonEmpty(update.TurnID, entry.turnID)
	entry.lastMessage = firstNonEmpty(update.LastMessage, entry.lastMessage)
	if update.TotalTokens > entry.lastReportedTotal {
		o.totals.TotalTokens += update.TotalTokens - entry.lastReportedTotal
		entry.lastReportedTotal = update.TotalTokens
	}
	if update.InputTokens > entry.lastReportedInput {
		o.totals.InputTokens += update.InputTokens - entry.lastReportedInput
		entry.lastReportedInput = update.InputTokens
	}
	if update.OutputTokens > entry.lastReportedOutput {
		o.totals.OutputTokens += update.OutputTokens - entry.lastReportedOutput
		entry.lastReportedOutput = update.OutputTokens
	}
	entry.turnCount++
}

// Snapshot returns the current orchestration state.
func (o *Orchestrator) Snapshot() Snapshot {
	o.mu.Lock()
	defer o.mu.Unlock()

	snap := Snapshot{
		Running:     make([]RunningRow, 0, len(o.running)),
		Retrying:    make([]RetryRow, 0, len(o.retryAttempts)),
		CodexTotals: o.totals,
	}
	now := o.clock().UTC()
	for _, entry := range o.running {
		snap.CodexTotals.SecondsRunning += now.Sub(entry.startedAt).Seconds()
		snap.Running = append(snap.Running, RunningRow{
			IssueID:        entry.issue.ID,
			Identifier:     entry.issue.Identifier,
			State:          entry.issue.StateName,
			ThreadID:       entry.threadID,
			TurnID:         entry.turnID,
			TurnCount:      entry.turnCount,
			WorkspacePath:  entry.workspace.Path,
			LastEventAt:    entry.lastEventAt,
			LastMessage:    entry.lastMessage,
			StartedAt:      entry.startedAt,
			CurrentAttempt: entry.attempt,
		})
	}
	for _, retry := range o.retryAttempts {
		snap.Retrying = append(snap.Retrying, RetryRow{
			IssueID:    retry.IssueID,
			Identifier: retry.Identifier,
			Attempt:    retry.Attempt,
			DueAt:      retry.DueAt,
			Error:      retry.Error,
			ThreadID:   retry.ThreadID,
		})
	}
	sort.Slice(snap.Running, func(i, j int) bool { return snap.Running[i].Identifier < snap.Running[j].Identifier })
	sort.Slice(snap.Retrying, func(i, j int) bool { return snap.Retrying[i].Identifier < snap.Retrying[j].Identifier })
	return snap
}

func (o *Orchestrator) isEligible(cfg config.Config, issue linear.Issue) bool {
	if strings.TrimSpace(issue.ID) == "" || strings.TrimSpace(issue.Identifier) == "" || strings.TrimSpace(issue.Title) == "" {
		return false
	}
	if !isInStates(issue.StateName, cfg.ActiveStates) || isInStates(issue.StateName, cfg.TerminalStates) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(issue.StateName), workflow.StateTodo) && issue.Blocked {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.running[issue.ID]; ok {
		return false
	}
	if _, ok := o.claimed[issue.ID]; ok {
		return false
	}
	return true
}

func (o *Orchestrator) hasCapacity(cfg config.Config, stateName string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.running) >= cfg.MaxConcurrency {
		return false
	}
	stateLimit := cfg.MaxConcurrencyByState[strings.ToLower(strings.TrimSpace(stateName))]
	if stateLimit <= 0 {
		stateLimit = cfg.MaxConcurrency
	}
	count := 0
	for _, entry := range o.running {
		if strings.EqualFold(strings.TrimSpace(entry.issue.StateName), strings.TrimSpace(stateName)) {
			count++
		}
	}
	return count < stateLimit
}

func (o *Orchestrator) runningIDs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := make([]string, 0, len(o.running))
	for issueID := range o.running {
		ids = append(ids, issueID)
	}
	return ids
}

func (o *Orchestrator) filterCandidates(cfg config.Config, issues []linear.Issue) []linear.Issue {
	out := make([]linear.Issue, 0, len(issues))
	for _, issue := range issues {
		if cfg.LinearProjectSlug != "" && !strings.EqualFold(strings.TrimSpace(issue.ProjectSlug), strings.TrimSpace(cfg.LinearProjectSlug)) {
			continue
		}
		if len(cfg.ProjectFilter) > 0 && !matchesProjectFilter(issue, cfg.ProjectFilter) {
			continue
		}
		out = append(out, issue)
	}
	return out
}

func (o *Orchestrator) cancelAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, entry := range o.running {
		entry.cancel()
	}
}

func (o *Orchestrator) claim(issueID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.claimed[issueID] = struct{}{}
}

func (o *Orchestrator) release(issueID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.claimed, issueID)
	delete(o.retryAttempts, issueID)
}

func (o *Orchestrator) isIdle() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, retry := range o.retryAttempts {
		if !retry.DueAt.After(o.clock().UTC()) {
			return false
		}
	}
	return len(o.running) == 0
}

func sortIssues(issues []linear.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		pi := 999
		if issues[i].Priority != nil {
			pi = *issues[i].Priority
		}
		pj := 999
		if issues[j].Priority != nil {
			pj = *issues[j].Priority
		}
		if pi != pj {
			return pi < pj
		}
		if !issues[i].CreatedAt.Equal(issues[j].CreatedAt) {
			return issues[i].CreatedAt.Before(issues[j].CreatedAt)
		}
		return issues[i].Identifier < issues[j].Identifier
	})
}

func isInStates(state string, values []string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(state), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func matchesProjectFilter(issue linear.Issue, filters []string) bool {
	for _, filter := range filters {
		if strings.EqualFold(strings.TrimSpace(filter), strings.TrimSpace(issue.ProjectID)) {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(filter), strings.TrimSpace(issue.ProjectName)) {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(filter), strings.TrimSpace(issue.ProjectSlug)) {
			return true
		}
	}
	return false
}

func cloneIssue(issue linear.Issue) linear.Issue {
	out := issue
	if issue.Metadata != nil {
		out.Metadata = make(map[string]string, len(issue.Metadata))
		for key, value := range issue.Metadata {
			out.Metadata[key] = value
		}
	}
	out.BlockedBy = append([]string(nil), issue.BlockedBy...)
	out.Labels = append([]string(nil), issue.Labels...)
	if issue.Priority != nil {
		value := *issue.Priority
		out.Priority = &value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
