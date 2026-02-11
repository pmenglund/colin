package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// ErrConflict indicates a state/lease conflict where action should be retried later.
	ErrConflict = errors.New("linear conflict")
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.1 -generate
//counterfeiter:generate -o linearfakes/fake_client.go . Client

// Client describes Linear operations used by the worker.
type Client interface {
	ListCandidateIssues(ctx context.Context, teamID string) ([]Issue, error)
	GetIssue(ctx context.Context, issueID string) (Issue, error)
	UpdateIssueState(ctx context.Context, issueID string, toState string) error
	UpdateIssueMetadata(ctx context.Context, issueID string, metadata MetadataPatch) error
	CreateIssueComment(ctx context.Context, issueID string, body string) error
}

// HTTPClient is a GraphQL client for the Linear API.
type HTTPClient struct {
	endpoint string
	token    string
	teamID   string
	http     *http.Client

	mu         sync.Mutex
	stateIDMap map[string]string
}

func NewHTTPClient(endpoint, token, teamID string, httpClient *http.Client) *HTTPClient {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	return &HTTPClient{
		endpoint:   endpoint,
		token:      token,
		teamID:     teamID,
		http:       httpClient,
		stateIDMap: map[string]string{},
	}
}

func (c *HTTPClient) ListCandidateIssues(ctx context.Context, teamID string) ([]Issue, error) {
	query := `query ListIssues($teamKey: String!) {
  issues(filter: { team: { key: { eq: $teamKey } } }, first: 50) {
    nodes {
      id
      identifier
      title
      description
      updatedAt
      state { name }
      inverseRelations(first: 5) { nodes { type } }
    }
  }
}`

	if strings.TrimSpace(teamID) == "" {
		teamID = c.teamID
	}

	var resp struct {
		Issues struct {
			Nodes []struct {
				ID          string `json:"id"`
				Identifier  string `json:"identifier"`
				Title       string `json:"title"`
				Description string `json:"description"`
				UpdatedAt   string `json:"updatedAt"`
				State       struct {
					Name string `json:"name"`
				} `json:"state"`
				InverseRelations struct {
					Nodes []struct {
						Type string `json:"type"`
					} `json:"nodes"`
				} `json:"inverseRelations"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.graphQL(ctx, query, map[string]string{"teamKey": teamID}, &resp); err != nil {
		return nil, err
	}

	issues := make([]Issue, 0, len(resp.Issues.Nodes))
	for _, n := range resp.Issues.Nodes {
		if !isCandidateState(n.State.Name) {
			continue
		}
		if n.State.Name == "Todo" && hasBlockingInverseRelation(n.InverseRelations.Nodes) {
			continue
		}

		meta, err := parseMetadata(n.Description)
		if err != nil {
			return nil, fmt.Errorf("parse issue %s metadata: %w", n.ID, err)
		}

		updatedAt, err := time.Parse(time.RFC3339, n.UpdatedAt)
		if err != nil {
			updatedAt = time.Time{}
		}

		issues = append(issues, Issue{
			ID:          n.ID,
			Identifier:  n.Identifier,
			Title:       n.Title,
			Description: n.Description,
			StateName:   n.State.Name,
			UpdatedAt:   updatedAt,
			Metadata:    meta,
		})
	}

	return issues, nil
}

func (c *HTTPClient) GetIssue(ctx context.Context, issueID string) (Issue, error) {
	query := `query GetIssue($issueId: String!) {
  issue(id: $issueId) {
    id
    identifier
    title
    description
    updatedAt
    state { name }
  }
}`

	var resp struct {
		Issue *struct {
			ID          string `json:"id"`
			Identifier  string `json:"identifier"`
			Title       string `json:"title"`
			Description string `json:"description"`
			UpdatedAt   string `json:"updatedAt"`
			State       struct {
				Name string `json:"name"`
			} `json:"state"`
		} `json:"issue"`
	}

	if err := c.graphQL(ctx, query, map[string]string{"issueId": issueID}, &resp); err != nil {
		return Issue{}, err
	}
	if resp.Issue == nil {
		return Issue{}, fmt.Errorf("issue %s not found", issueID)
	}

	meta, err := parseMetadata(resp.Issue.Description)
	if err != nil {
		return Issue{}, fmt.Errorf("parse issue metadata: %w", err)
	}

	updatedAt, err := time.Parse(time.RFC3339, resp.Issue.UpdatedAt)
	if err != nil {
		updatedAt = time.Time{}
	}

	return Issue{
		ID:          resp.Issue.ID,
		Identifier:  resp.Issue.Identifier,
		Title:       resp.Issue.Title,
		Description: resp.Issue.Description,
		StateName:   resp.Issue.State.Name,
		UpdatedAt:   updatedAt,
		Metadata:    meta,
	}, nil
}

func (c *HTTPClient) UpdateIssueState(ctx context.Context, issueID string, toState string) error {
	issue, err := c.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	if issue.StateName == toState {
		return nil
	}

	stateID, err := c.resolveStateID(ctx, toState)
	if err != nil {
		return err
	}

	mutation := `mutation UpdateIssueState($issueId: String!, $stateId: String!) {
  issueUpdate(id: $issueId, input: { stateId: $stateId }) {
    success
  }
}`

	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}

	if err := c.graphQL(ctx, mutation, map[string]string{"issueId": issueID, "stateId": stateID}, &resp); err != nil {
		return err
	}
	if !resp.IssueUpdate.Success {
		return fmt.Errorf("update issue state: %w", ErrConflict)
	}

	return nil
}

func (c *HTTPClient) UpdateIssueMetadata(ctx context.Context, issueID string, patch MetadataPatch) error {
	observed, err := c.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}

	nextDescription, _, err := upsertMetadata(observed.Description, patch)
	if err != nil {
		return err
	}
	if nextDescription == observed.Description {
		return nil
	}

	// Read one more time to detect obvious write races before mutating Linear.
	current, err := c.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	if current.Description != observed.Description || current.StateName != observed.StateName {
		return fmt.Errorf("update issue metadata: %w", ErrConflict)
	}

	mutation := `mutation UpdateIssueDescription($issueId: String!, $description: String!) {
  issueUpdate(id: $issueId, input: { description: $description }) {
    success
  }
}`

	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}

	if err := c.graphQL(ctx, mutation, map[string]string{"issueId": issueID, "description": nextDescription}, &resp); err != nil {
		return err
	}
	if !resp.IssueUpdate.Success {
		return fmt.Errorf("update issue metadata: %w", ErrConflict)
	}

	return nil
}

func (c *HTTPClient) CreateIssueComment(ctx context.Context, issueID string, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}

	mutation := `mutation CreateIssueComment($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
  }
}`

	var resp struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}

	if err := c.graphQL(ctx, mutation, map[string]string{"issueId": issueID, "body": body}, &resp); err != nil {
		return err
	}
	if !resp.CommentCreate.Success {
		return fmt.Errorf("create issue comment: %w", ErrConflict)
	}
	return nil
}

func (c *HTTPClient) resolveStateID(ctx context.Context, stateName string) (string, error) {
	c.mu.Lock()
	cached := c.stateIDMap[stateName]
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	query := `query WorkflowStates($teamKey: String!) {
  workflowStates(filter: { team: { key: { eq: $teamKey } } }) {
    nodes {
      id
      name
    }
  }
}`

	var resp struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}

	if err := c.graphQL(ctx, query, map[string]string{"teamKey": c.teamID}, &resp); err != nil {
		return "", err
	}

	stateIDMap := map[string]string{}
	for _, state := range resp.WorkflowStates.Nodes {
		stateIDMap[state.Name] = state.ID
	}

	id := stateIDMap[stateName]
	if id == "" {
		return "", fmt.Errorf("unknown workflow state %q", stateName)
	}

	c.mu.Lock()
	for k, v := range stateIDMap {
		c.stateIDMap[k] = v
	}
	c.mu.Unlock()

	return id, nil
}

func (c *HTTPClient) graphQL(ctx context.Context, query string, variables any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("marshal GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode response envelope: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 {
		return errors.New("graphql response missing data")
	}

	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode response payload: %w", err)
	}
	return nil
}

func isCandidateState(stateName string) bool {
	switch stateName {
	case "Todo", "In Progress", "Merge":
		return true
	default:
		return false
	}
}

func hasBlockingInverseRelation(relations []struct {
	Type string `json:"type"`
}) bool {
	for _, relation := range relations {
		relationType := strings.ToLower(strings.TrimSpace(relation.Type))
		if strings.Contains(relationType, "block") || strings.Contains(relationType, "depend") {
			return true
		}
	}
	return false
}
