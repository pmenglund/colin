package linear

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// InMemoryClient is a thread-safe fake Linear client for local and e2e testing.
type InMemoryClient struct {
	mu       sync.Mutex
	issues   map[string]Issue
	comments map[string][]string
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

// ListCandidateIssues returns all issues in states Colin can process automatically.
func (c *InMemoryClient) ListCandidateIssues(ctx context.Context, _ string) ([]Issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Issue, 0, len(c.issues))
	for _, issue := range c.issues {
		if isCandidateState(issue.StateName) {
			out = append(out, cloneInMemoryIssue(issue))
		}
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

// UpdateIssueMetadata applies metadata patch to issue description and metadata map.
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

	nextDescription, nextMetadata, err := upsertMetadata(issue.Description, patch)
	if err != nil {
		return err
	}
	issue.Description = nextDescription
	issue.Metadata = nextMetadata
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
	return out
}
