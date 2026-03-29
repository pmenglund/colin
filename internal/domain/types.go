package domain

import "time"

// Issue is the normalized tracker record consumed by orchestration, prompting, and logging.
type Issue struct {
	ID           string
	Identifier   string
	Title        string
	Description  *string
	Priority     *int
	State        string
	BranchName   *string
	URL          *string
	Labels       []string
	PullRequests []PullRequest
	BlockedBy    []BlockerRef
	CreatedAt    *time.Time
	UpdatedAt    *time.Time
}

// PullRequest is the normalized GitHub pull request metadata attached to an issue.
type PullRequest struct {
	URL          string
	Title        string
	Number       *int
	Status       string
	Draft        bool
	Branch       string
	TargetBranch string
	RepoLogin    string
	RepoName     string
	CreatedAt    *time.Time
	UpdatedAt    *time.Time
	ClosedAt     *time.Time
	MergedAt     *time.Time
}

// BlockerRef captures the minimal blocker fields needed for eligibility checks and prompt context.
type BlockerRef struct {
	ID         *string
	Identifier *string
	State      *string
}

// WorkflowDefinition is the parsed WORKFLOW.md content used for config and prompt rendering.
type WorkflowDefinition struct {
	Config         map[string]any
	PromptTemplate string
	SourcePath     string
}

// ServiceConfig is the typed runtime view built from workflow front matter and defaults.
type ServiceConfig struct {
	WorkflowPath string
	Tracker      TrackerConfig
	Polling      PollingConfig
	Workspace    WorkspaceConfig
	Hooks        HookConfig
	Agent        AgentConfig
	Codex        CodexConfig
	Server       ServerConfig
}

// TrackerConfig configures the issue tracker adapter.
type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string
	ProjectSlug    string
	ActiveStates   []string
	TerminalStates []string
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
}

// CodexConfig configures the Codex app-server process and timeout behavior.
type CodexConfig struct {
	Command           string
	ApprovalPolicy    string
	ThreadSandbox     string
	TurnSandboxPolicy map[string]any
	TurnTimeout       time.Duration
	ReadTimeout       time.Duration
	StallTimeout      time.Duration
}

// ServerConfig reserves space for optional server extensions.
type ServerConfig struct {
	Port *int
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

// Snapshot is a read-only summary of orchestrator state for observability.
type Snapshot struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Running     []SnapshotRunning   `json:"running"`
	Retrying    []RetryEntry        `json:"retrying"`
	CodexTotals Totals              `json:"codex_totals"`
	RateLimits  map[string]any      `json:"rate_limits"`
	Counts      map[string]int      `json:"counts"`
	Tracked     map[string]struct{} `json:"-"`
}

// SnapshotRunning is the per-running-issue row included in a Snapshot.
type SnapshotRunning struct {
	IssueID      string     `json:"issue_id"`
	Identifier   string     `json:"issue_identifier"`
	State        string     `json:"state"`
	SessionID    string     `json:"session_id"`
	TurnCount    int        `json:"turn_count"`
	LastEvent    string     `json:"last_event"`
	LastMessage  string     `json:"last_message"`
	StartedAt    time.Time  `json:"started_at"`
	LastEventAt  *time.Time `json:"last_event_at"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	TotalTokens  int64      `json:"total_tokens"`
}
