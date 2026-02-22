package linear

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/workflow"
)

// InMemoryClient is a thread-safe fake Linear client for local and e2e testing.
type InMemoryClient struct {
	mu       sync.Mutex
	issues   map[string]Issue
	comments map[string][]string
	states   workflow.States
}

// NewInMemoryClient returns a fake Linear client seeded with provided issues.
func NewInMemoryClient(seed []Issue) *InMemoryClient {
	issues := make(map[string]Issue, len(seed))
	for _, issue := range seed {
		issueCopy := cloneInMemoryIssue(issue)
		if issueCopy.UpdatedAt.IsZero() {
			issueCopy.UpdatedAt = time.Now().UTC()
		}
		issues[issueCopy.ID] = issueCopy
	}

	return &InMemoryClient{
		issues:   issues,
		comments: map[string][]string{},
		states:   workflow.DefaultStates(),
	}
}

// NewDefaultInMemoryClient returns a fake client with deterministic seed data.
func NewDefaultInMemoryClient() *InMemoryClient {
	return NewInMemoryClient([]Issue{
		{
			ID:          "fake-issue-1",
			Identifier:  "COL-FAKE-1",
			Title:       "Seed issue for offline runs",
			Description: "This issue is intentionally seeded for fake Linear backend tests.",
			StateName:   "Todo",
		},
	})
}

// SetWorkflowStates configures runtime workflow state names.
func (c *InMemoryClient) SetWorkflowStates(states workflow.States) error {
	states = states.WithDefaults()
	if err := states.Validate(); err != nil {
		return err
	}

	c.mu.Lock()
	c.states = states
	c.mu.Unlock()
	return nil
}

// ListCandidateIssues returns all issues in states Colin can process automatically.
func (c *InMemoryClient) ListCandidateIssues(ctx context.Context, _ string) ([]Issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	states := c.states.WithDefaults()

	out := make([]Issue, 0, len(c.issues))
	for _, issue := range c.issues {
		if !states.IsCandidate(issue.StateName) {
			continue
		}
		if issueHasBlockingDependency(issue, c.issues, states) {
			continue
		}
		out = append(out, cloneInMemoryIssue(issue))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Identifier < out[j].Identifier
	})
	return out, nil
}

// GetIssue returns one issue snapshot by id.
func (c *InMemoryClient) GetIssue(ctx context.Context, issueID string) (Issue, error) {
	if err := ctx.Err(); err != nil {
		return Issue{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	issue, ok := c.issues[issueID]
	if !ok {
		return Issue{}, fmt.Errorf("issue %s not found", issueID)
	}
	return cloneInMemoryIssue(issue), nil
}

// GetIssueByIdentifier returns one issue snapshot by identifier.
func (c *InMemoryClient) GetIssueByIdentifier(ctx context.Context, issueIdentifier string) (Issue, error) {
	if err := ctx.Err(); err != nil {
		return Issue{}, err
	}

	trimmedIdentifier := strings.TrimSpace(issueIdentifier)
	if trimmedIdentifier == "" {
		return Issue{}, fmt.Errorf("issue identifier is required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var selected *Issue
	for _, issue := range c.issues {
		if !strings.EqualFold(strings.TrimSpace(issue.Identifier), trimmedIdentifier) {
			continue
		}
		issueCopy := cloneInMemoryIssue(issue)
		if selected == nil || strings.Compare(issueCopy.ID, selected.ID) < 0 {
			selected = &issueCopy
		}
	}
	if selected == nil {
		return Issue{}, fmt.Errorf("issue %s not found", trimmedIdentifier)
	}

	return *selected, nil
}

// UpdateIssueState updates issue workflow state.
func (c *InMemoryClient) UpdateIssueState(ctx context.Context, issueID string, toState string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	issue, ok := c.issues[issueID]
	if !ok {
		return fmt.Errorf("issue %s not found", issueID)
	}
	issue.StateName = strings.TrimSpace(toState)
	issue.UpdatedAt = time.Now().UTC()
	c.issues[issueID] = issue
	return nil
}

// UpdateIssueMetadata applies metadata patch to issue metadata map.
func (c *InMemoryClient) UpdateIssueMetadata(ctx context.Context, issueID string, patch MetadataPatch) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	issue, ok := c.issues[issueID]
	if !ok {
		return fmt.Errorf("issue %s not found", issueID)
	}

	issue.Metadata = applyMetadataPatch(issue.Metadata, patch)
	issue.UpdatedAt = time.Now().UTC()
	c.issues[issueID] = issue
	return nil
}

// CreateIssueComment appends a comment to issue history.
func (c *InMemoryClient) CreateIssueComment(ctx context.Context, issueID string, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.issues[issueID]; !ok {
		return fmt.Errorf("issue %s not found", issueID)
	}
	c.comments[issueID] = append(c.comments[issueID], body)
	return nil
}

func cloneInMemoryIssue(issue Issue) Issue {
	out := issue
	out.Metadata = map[string]string{}
	for k, v := range issue.Metadata {
		out.Metadata[k] = v
	}
	out.BlockedBy = append([]string(nil), issue.BlockedBy...)
	return out
}

func issueHasBlockingDependency(issue Issue, issues map[string]Issue, states workflow.States) bool {
	for _, blockedByID := range issue.BlockedBy {
		dependencyID := strings.TrimSpace(blockedByID)
		if dependencyID == "" {
			continue
		}
		dependency, ok := issues[dependencyID]
		if !ok {
			return true
		}
		if !states.IsDone(dependency.StateName) {
			return true
		}
	}
	return false
}
