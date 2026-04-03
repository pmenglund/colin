package domain

import (
	"errors"
	"strings"
	"time"
)

type ReviewPublishDirective string
type ExecPlanDecision string
type RunType string
type RunOutcome string
type CodexReviewState string

const (
	ReviewPublishDirectivePublish ReviewPublishDirective = "publish"
	ReviewPublishDirectiveSkip    ReviewPublishDirective = "skip"
	PausedIssueLabel                                     = "paused"

	CodexReviewPendingLabel    = "codex-review: pending"
	CodexReviewApprovedLabel   = "codex-review: approved"
	CodexReviewUnresolvedLabel = "codex-review: unresolved-feedback"

	ExecPlanDecisionOneShot  ExecPlanDecision = "one_shot"
	ExecPlanDecisionExecPlan ExecPlanDecision = "exec_plan"

	RunTypeCoding        RunType = "coding"
	RunTypeReviewPublish RunType = "review_publish"
	RunTypeMerge         RunType = "merge"

	RunOutcomeReadyForReview   RunOutcome = "ready_for_review"
	RunOutcomeNeedsSpec        RunOutcome = "needs_spec"
	RunOutcomeMaxTurns         RunOutcome = "max_turns"
	RunOutcomeExecPlanConflict RunOutcome = "exec_plan_conflict"
	RunOutcomeExecPlanInvalid  RunOutcome = "exec_plan_invalid"
	RunOutcomeMerged           RunOutcome = "merged"
	RunOutcomeReadyForMergeFix RunOutcome = "ready_for_merge_retry"

	CodexReviewStatePending    CodexReviewState = "pending"
	CodexReviewStateApproved   CodexReviewState = "approved"
	CodexReviewStateUnresolved CodexReviewState = "unresolved_feedback"

	OutcomeReadyForReviewLine     = "COLIN_OUTCOME: READY_FOR_REVIEW"
	OutcomeReadyForMergeRetryLine = "COLIN_OUTCOME: READY_FOR_MERGE_RETRY"
	OutcomeNeedsSpecLine          = "COLIN_OUTCOME: NEEDS_SPEC"
	ExecPlanDecisionOneShotLine   = "COLIN_EXECPLAN_DECISION: ONE_SHOT"
	ExecPlanDecisionExecPlanLine  = "COLIN_EXECPLAN_DECISION: EXEC_PLAN"
)

// Issue is the normalized tracker record consumed by orchestration, prompting, and logging.
type Issue struct {
	ID                   string
	Identifier           string
	Title                string
	Description          *string
	Priority             *int
	ProjectID            string
	ProjectSlug          string
	State                string
	BranchName           *string
	URL                  *string
	Labels               []string
	BlockedBy            []BlockerRef
	ColinMetadata        *ColinMetadata
	ExecPlan             *ExecPlan
	ExecPlanCount        int
	ReviewCycle          *ReviewCycle
	ReviewFeedback       []ReviewFeedback
	ReviewThreads        []ReviewThread
	AttachedPullRequests []PullRequestRef
	PullRequest          *PullRequestRef
	CreatedAt            *time.Time
	UpdatedAt            *time.Time
}

// ManagedIssueLabels returns the Colin-managed Linear labels that must exist at startup.
func ManagedIssueLabels() []string {
	return []string{
		PausedIssueLabel,
		CodexReviewPendingLabel,
		CodexReviewApprovedLabel,
		CodexReviewUnresolvedLabel,
	}
}

// ManagedCodexReviewLabels returns the mutually exclusive Codex PR review status labels.
func ManagedCodexReviewLabels() []string {
	return []string{
		CodexReviewPendingLabel,
		CodexReviewApprovedLabel,
		CodexReviewUnresolvedLabel,
	}
}

// ColinMetadata is persisted on the Linear issue to track Colin-specific workflow state.
type ColinMetadata struct {
	AttachmentID            string
	URL                     string
	ActualBranchName        string
	ExecPlanDecision        ExecPlanDecision
	ReviewPublishDirective  ReviewPublishDirective
	LastRunType             RunType
	LastOutcome             RunOutcome
	LastSummaryCommentID    string
	PullRequestNumber       int
	PullRequestURL          string
	PullRequestState        string
	PullRequestHeadRef      string
	PullRequestBaseRef      string
	LoopFailureFingerprint  string
	LoopFailureCount        int
	PausedAt                *time.Time
	PausedRunType           string
	PausedState             string
	PausedReason            string
	SlackChannelID          string
	SlackMessageTS          string
	SlackPermalink          string
	SlackSummaryFingerprint string
	UpdatedAt               *time.Time
	CodexOutput             []OutputLog
}

// ExecPlan is persisted on the Linear issue to track the current issue execution plan.
type ExecPlan struct {
	AttachmentID string
	URL          string
	Body         string
	UpdatedAt    *time.Time
}

// BlockerRef captures the minimal blocker fields needed for eligibility checks and prompt context.
type BlockerRef struct {
	ID         *string
	Identifier *string
	State      *string
}

// ReviewFeedback is one human-authored comment or reply from the latest Review -> Todo cycle.
type ReviewFeedback struct {
	Body      string
	CreatedAt time.Time
	ParentID  *string
}

// ReviewCycle captures the latest Review -> Todo loop for an issue.
type ReviewCycle struct {
	EnteredReviewAt  time.Time
	ReturnedToTodoAt time.Time
}

// PullRequestRef is the minimal PR metadata Colin uses in prompts and comments.
type PullRequestRef struct {
	Number  int
	URL     string
	State   string
	HeadRef string
	BaseRef string
}

// ReviewThread is one unresolved pull-request review thread.
type ReviewThread struct {
	ID         string
	Path       string
	Line       *int
	StartLine  *int
	CommentID  string
	CommentURL string
	Author     string
	Body       string
	CreatedAt  *time.Time
	IsResolved bool
	IsOutdated bool
	CanReply   bool
	CanResolve bool
}

// GitHubReviewThread is kept as a compatibility alias while the codebase migrates to backend-neutral naming.
type GitHubReviewThread = ReviewThread

// WorkflowDefinition is the parsed WORKFLOW.md content used for config and prompt rendering.
type WorkflowDefinition struct {
	Config         WorkflowConfig
	PromptTemplate string
	SourcePath     string
}

// WorkflowConfig is the typed front matter contract accepted in WORKFLOW.md.
type WorkflowConfig struct {
	Tracker   WorkflowTrackerConfig   `yaml:"tracker"`
	Polling   WorkflowPollingConfig   `yaml:"polling"`
	Workspace WorkflowWorkspaceConfig `yaml:"workspace"`
	Repo      WorkflowRepoConfig      `yaml:"repo"`
	Targets   []WorkflowTargetConfig  `yaml:"targets"`
	Hooks     WorkflowHookConfig      `yaml:"hooks"`
	Agent     WorkflowAgentConfig     `yaml:"agent"`
	Codex     WorkflowCodexConfig     `yaml:"codex"`
	Server    WorkflowServerConfig    `yaml:"server"`
	Slack     WorkflowSlackConfig     `yaml:"slack"`
}

type WorkflowTrackerConfig struct {
	Kind                 *string  `yaml:"kind"`
	Endpoint             *string  `yaml:"endpoint"`
	APIKey               *string  `yaml:"api_key"`
	WebhookSigningSecret *string  `yaml:"webhook_signing_secret"`
	ProjectSlug          *string  `yaml:"project_slug"`
	ActiveStates         []string `yaml:"active_states"`
	TerminalStates       []string `yaml:"terminal_states"`
}

type WorkflowPollingConfig struct {
	IntervalMillis *int `yaml:"interval_ms"`
}

type WorkflowWorkspaceConfig struct {
	Root    *string `yaml:"root"`
	RepoURL *string `yaml:"repo_url"`
	BaseRef *string `yaml:"base_ref"`
}

type WorkflowTargetConfig struct {
	Name        *string `yaml:"name"`
	ProjectSlug *string `yaml:"project_slug"`
	RepoURL     *string `yaml:"repo_url"`
	BaseRef     *string `yaml:"base_ref"`
}

type WorkflowRepoConfig struct {
	Backend               *string  `yaml:"backend"`
	APIBaseURL            *string  `yaml:"api_base_url"`
	PublishStates         []string `yaml:"publish_states"`
	MergeStates           []string `yaml:"merge_states"`
	RemoteName            *string  `yaml:"remote_name"`
	MergeMethod           *string  `yaml:"merge_method"`
	BranchTemplate        *string  `yaml:"branch_template"`
	PRTemplate            *string  `yaml:"pr_template"`
	APIToken              *string  `yaml:"api_token"`
	WebhookSigningSecret  *string  `yaml:"webhook_signing_secret"`
	CodexPRReviewsEnabled *bool    `yaml:"codex_pr_reviews_enabled"`
}

type WorkflowHookConfig struct {
	AfterCreate   *string `yaml:"after_create"`
	BeforeRun     *string `yaml:"before_run"`
	AfterRun      *string `yaml:"after_run"`
	BeforeRemove  *string `yaml:"before_remove"`
	TimeoutMillis *int    `yaml:"timeout_ms"`
}

type WorkflowAgentConfig struct {
	MaxConcurrentAgents        *int           `yaml:"max_concurrent_agents"`
	MaxRetryBackoffMillis      *int           `yaml:"max_retry_backoff_ms"`
	MaxConcurrentAgentsByState map[string]int `yaml:"max_concurrent_agents_by_state"`
	MaxTurns                   *int           `yaml:"max_turns"`
	CreateExecPlan             *bool          `yaml:"create_exec_plan"`
}

type WorkflowSandboxPolicy struct {
	Type *string `yaml:"type"`
	Mode *string `yaml:"mode"`
}

type WorkflowCodexConfig struct {
	Command            *string                `yaml:"command"`
	ApprovalPolicy     *string                `yaml:"approval_policy"`
	ThreadSandbox      *string                `yaml:"thread_sandbox"`
	TurnSandboxPolicy  *WorkflowSandboxPolicy `yaml:"turn_sandbox_policy"`
	TurnTimeoutMillis  *int                   `yaml:"turn_timeout_ms"`
	ReadTimeoutMillis  *int                   `yaml:"read_timeout_ms"`
	StallTimeoutMillis *int                   `yaml:"stall_timeout_ms"`
}

type WorkflowServerConfig struct {
	Port             *int    `yaml:"port"`
	WebhookPort      *int    `yaml:"webhook_port"`
	PublicURL        *string `yaml:"public_url"`
	WebhookPublicURL *string `yaml:"webhook_public_url"`
	UIURL            *string `yaml:"ui_url"`
	LogBufferLines   *int    `yaml:"log_buffer_lines"`
}

type WorkflowSlackConfig struct {
	BotToken  *string `yaml:"bot_token"`
	ChannelID *string `yaml:"channel_id"`
}

// ServiceConfig is the typed runtime view built from workflow front matter and defaults.
type ServiceConfig struct {
	WorkflowPath string
	Tracker      TrackerConfig
	Polling      PollingConfig
	Workspace    WorkspaceConfig
	Repo         RepoConfig
	Targets      []TargetConfig
	Hooks        HookConfig
	Agent        AgentConfig
	Codex        CodexConfig
	Server       ServerConfig
	Slack        SlackConfig
}

// TrackerConfig configures the issue tracker adapter.
type TrackerConfig struct {
	Kind                 string
	Endpoint             string
	APIKey               string
	WebhookSigningSecret string
	ProjectSlug          string
	ActiveStates         []string
	TerminalStates       []string
}

// PollingConfig configures the orchestrator poll cadence.
type PollingConfig struct {
	Interval time.Duration
}

// WorkspaceConfig configures per-issue workspace layout and optional git population.
type WorkspaceConfig struct {
	Root    string
	RepoURL string
	BaseRef string
}

// TargetConfig is one normalized mapping from a Linear project to a git repository and base branch.
type TargetConfig struct {
	Key         string
	Name        string
	ProjectSlug string
	RepoURL     string
	BaseRef     string
}

// RepoConfig configures repository-host publish and merge automation tied to tracker states.
type RepoConfig struct {
	Backend               string
	APIBaseURL            string
	PublishStates         []string
	MergeStates           []string
	RemoteName            string
	MergeMethod           string
	BranchTemplate        string
	PRTemplate            string
	APIToken              string
	WebhookSigningSecret  string
	CodexPRReviewsEnabled bool
}

// HookConfig configures workspace lifecycle hooks.
type HookConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	Timeout      time.Duration
}

// AgentConfig configures orchestrator concurrency and retry behavior.
type AgentConfig struct {
	MaxConcurrentAgents        int
	MaxRetryBackoff            time.Duration
	MaxConcurrentAgentsByState map[string]int
	MaxTurns                   int
	CreateExecPlan             bool
}

// CodexConfig configures the Codex app-server process and timeout behavior.
type CodexConfig struct {
	Command           string
	ApprovalPolicy    string
	ThreadSandbox     string
	TurnSandboxPolicy SandboxPolicy
	TurnTimeout       time.Duration
	ReadTimeout       time.Duration
	StallTimeout      time.Duration
}

// SandboxPolicy describes the per-turn Codex sandbox contract Colin supports.
type SandboxPolicy struct {
	Type string `json:"type"`
}

// RateLimitWindow is one typed rate-limit bucket captured from a dependency.
type RateLimitWindow struct {
	WindowDurationMinutes *int64     `json:"window_duration_minutes,omitempty"`
	Limit                 *int64     `json:"limit,omitempty"`
	Remaining             *int64     `json:"remaining,omitempty"`
	UsedPercent           *int64     `json:"used_percent,omitempty"`
	ResetsAt              *time.Time `json:"resets_at,omitempty"`
	NextAllowedAt         *time.Time `json:"next_allowed_at,omitempty"`
}

// RateLimitSnapshot is the typed set of named rate-limit buckets used across the runtime.
type RateLimitSnapshot map[string]RateLimitWindow

// ServerConfig reserves space for optional server extensions.
type ServerConfig struct {
	Port             *int
	WebhookPort      *int
	PublicURL        string
	WebhookPublicURL string
	UIURL            string
	LogBufferLines   int
}

var ErrUnknownIssueTarget = errors.New("unknown_issue_target")

// MultiTarget reports whether the runtime is supervising more than one target mapping.
func (cfg ServiceConfig) MultiTarget() bool {
	return len(cfg.Targets) > 1
}

// TargetByProjectSlug resolves the normalized target for a Linear project slug.
func (cfg ServiceConfig) TargetByProjectSlug(projectSlug string) (TargetConfig, error) {
	projectSlug = strings.TrimSpace(projectSlug)
	for _, target := range cfg.Targets {
		if strings.EqualFold(strings.TrimSpace(target.ProjectSlug), projectSlug) {
			return target, nil
		}
	}
	return TargetConfig{}, ErrUnknownIssueTarget
}

// ResolveTargetForIssue resolves the normalized target for the supplied issue.
func ResolveTargetForIssue(cfg ServiceConfig, issue Issue) (TargetConfig, error) {
	if len(cfg.Targets) == 0 {
		if strings.TrimSpace(cfg.Tracker.ProjectSlug) == "" && strings.TrimSpace(cfg.Workspace.RepoURL) == "" && strings.TrimSpace(cfg.Workspace.BaseRef) == "" {
			return TargetConfig{}, nil
		}
		return TargetConfig{
			Key:         strings.TrimSpace(cfg.Tracker.ProjectSlug),
			Name:        strings.TrimSpace(cfg.Tracker.ProjectSlug),
			ProjectSlug: strings.TrimSpace(cfg.Tracker.ProjectSlug),
			RepoURL:     strings.TrimSpace(cfg.Workspace.RepoURL),
			BaseRef:     strings.TrimSpace(cfg.Workspace.BaseRef),
		}, nil
	}
	if len(cfg.Targets) == 1 && strings.TrimSpace(issue.ProjectSlug) == "" {
		return cfg.Targets[0], nil
	}
	return cfg.TargetByProjectSlug(issue.ProjectSlug)
}

// WatchedProjectSlugs returns the configured Linear project slugs in stable order.
func (cfg ServiceConfig) WatchedProjectSlugs() []string {
	if len(cfg.Targets) == 0 {
		if value := strings.TrimSpace(cfg.Tracker.ProjectSlug); value != "" {
			return []string{value}
		}
		return nil
	}
	out := make([]string, 0, len(cfg.Targets))
	for _, target := range cfg.Targets {
		if value := strings.TrimSpace(target.ProjectSlug); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// WatchedRepoURLs returns the configured repository URLs in stable order.
func (cfg ServiceConfig) WatchedRepoURLs() []string {
	if len(cfg.Targets) == 0 {
		if value := strings.TrimSpace(cfg.Workspace.RepoURL); value != "" {
			return []string{value}
		}
		return nil
	}
	out := make([]string, 0, len(cfg.Targets))
	for _, target := range cfg.Targets {
		if value := strings.TrimSpace(target.RepoURL); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// SlackConfig configures optional Slack issue-summary delivery.
type SlackConfig struct {
	BotToken  string
	ChannelID string
}

// SetupCheck is one operator-facing readiness check.
type SetupCheck struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Status      string    `json:"status"`
	Detail      string    `json:"detail,omitempty"`
	Remediation string    `json:"remediation,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
}

// FunnelSetupStatus describes Colin's current Tailscale Funnel readiness.
type FunnelSetupStatus struct {
	GeneratedAt           time.Time    `json:"generated_at"`
	Ready                 bool         `json:"ready"`
	PublicURLSource       string       `json:"public_url_source,omitempty"`
	LocalBaseURL          string       `json:"local_base_url,omitempty"`
	LocalWebhookBaseURL   string       `json:"local_webhook_base_url,omitempty"`
	TailnetUIBaseURL      string       `json:"tailnet_ui_base_url,omitempty"`
	LocalSetupURL         string       `json:"local_setup_url,omitempty"`
	LocalReadyURL         string       `json:"local_ready_url,omitempty"`
	PublicBaseURL         string       `json:"public_base_url,omitempty"`
	PublicSetupURL        string       `json:"public_setup_url,omitempty"`
	PublicReadyURL        string       `json:"public_ready_url,omitempty"`
	DetectedFunnelURL     string       `json:"detected_funnel_url,omitempty"`
	SuggestedServeCommand string       `json:"suggested_serve_command,omitempty"`
	SuggestedCommand      string       `json:"suggested_command,omitempty"`
	LinearWebhookURL      string       `json:"linear_webhook_url,omitempty"`
	GitHubWebhookURL      string       `json:"github_webhook_url,omitempty"`
	Checks                []SetupCheck `json:"checks,omitempty"`
}

// Workspace describes a prepared per-issue workspace directory.
type Workspace struct {
	Path         string
	WorkspaceKey string
	CreatedNow   bool
}

// LiveSession tracks the latest known Codex session state for a running issue.
type LiveSession struct {
	SessionID                string
	ThreadID                 string
	TurnID                   string
	CodexAppServerPID        *int
	LastCodexEvent           string
	LastCodexTimestamp       *time.Time
	LastCodexMessage         string
	CodexInputTokens         int64
	CodexOutputTokens        int64
	CodexTotalTokens         int64
	LastReportedInputTokens  int64
	LastReportedOutputTokens int64
	LastReportedTotalTokens  int64
	ContextWindow            *ContextWindowUsage
	TurnCount                int
}

// RetryEntry records one queued retry for an issue.
type RetryEntry struct {
	IssueID    string
	Identifier string
	Attempt    int
	DueAt      time.Time
	Error      string
}

// Totals holds aggregate Codex usage and runtime counters.
type Totals struct {
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	SecondsRunning float64 `json:"seconds_running"`
}

// ContextWindowUsage tracks how much of a session's model context window is in use.
type ContextWindowUsage struct {
	UsedTokens  int64 `json:"used_tokens"`
	LimitTokens int64 `json:"limit_tokens"`
}

// PausedStateSummary captures paused issue count and investigation URL for one Linear state.
type PausedStateSummary struct {
	Count int    `json:"count"`
	URL   string `json:"url,omitempty"`
}

// StateIssueSummary is the minimal issue metadata needed to render per-state issue lists in the UI.
type StateIssueSummary struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url,omitempty"`
}

// Snapshot is a read-only summary of orchestrator state for observability.
type Snapshot struct {
	GeneratedAt       time.Time                      `json:"generated_at"`
	ShutdownRequested bool                           `json:"shutdown_requested"`
	Running           []SnapshotRunning              `json:"running"`
	Retrying          []RetryEntry                   `json:"retrying"`
	CodexTotals       Totals                         `json:"codex_totals"`
	RateLimits        RateLimitSnapshot              `json:"rate_limits"`
	Counts            map[string]int                 `json:"counts"`
	IssueStates       map[string]int                 `json:"issue_states"`
	StateIssues       map[string][]StateIssueSummary `json:"state_issues,omitempty"`
	PausedIssueStates map[string]PausedStateSummary  `json:"paused_issue_states,omitempty"`
	Tracked           map[string]struct{}            `json:"-"`
}

// SnapshotRunning is the per-running-issue row included in a Snapshot.
type SnapshotRunning struct {
	IssueID       string              `json:"issue_id"`
	Identifier    string              `json:"issue_identifier"`
	Title         string              `json:"title"`
	URL           *string             `json:"url,omitempty"`
	State         string              `json:"state"`
	SessionID     string              `json:"session_id"`
	TurnCount     int                 `json:"turn_count"`
	LastEvent     string              `json:"last_event"`
	LastMessage   string              `json:"last_message"`
	StartedAt     time.Time           `json:"started_at"`
	LastEventAt   *time.Time          `json:"last_event_at"`
	InputTokens   int64               `json:"input_tokens"`
	OutputTokens  int64               `json:"output_tokens"`
	TotalTokens   int64               `json:"total_tokens"`
	ContextWindow *ContextWindowUsage `json:"context_window,omitempty"`
	OutputLog     []OutputLog         `json:"output_log"`
}

// OutputLog is one human-readable Codex event line captured for dashboard inspection.
type OutputLog struct {
	Timestamp time.Time `json:"timestamp"`
	Event     string    `json:"event"`
	Message   string    `json:"message"`
}
