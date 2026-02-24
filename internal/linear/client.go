package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/workflow"
)

var (
	// ErrConflict indicates a state/lease conflict where action should be retried later.
	ErrConflict = errors.New("linear conflict")
)

const (
	colinMetadataAttachmentTitle    = "Colin metadata"
	colinMetadataAttachmentSubtitle = "Managed by Colin"
	colinMetadataAttachmentURL      = "https://github.com/pmenglund/colin/blob/main/docs/metadata.md"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.1 -generate
//counterfeiter:generate -o fakes/fake_client.go . Client

// Client describes Linear operations used by the worker.
type Client interface {
	ListCandidateIssues(ctx context.Context, teamID string) ([]Issue, error)
	GetIssue(ctx context.Context, issueID string) (Issue, error)
	ListIssueComments(ctx context.Context, issueID string) ([]IssueComment, error)
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
	states     workflow.States
}

// NewHTTPClient creates a Linear GraphQL client with sane defaults.
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
		states:     workflow.DefaultStates(),
	}
}

// SetWorkflowStates configures runtime workflow state names.
func (c *HTTPClient) SetWorkflowStates(states workflow.States) error {
	states = states.WithDefaults()
	if err := states.Validate(); err != nil {
		return err
	}

	c.mu.Lock()
	c.states = states
	c.mu.Unlock()
	return nil
}

// SetStateIDs seeds the client cache with known state name to state ID mappings.
func (c *HTTPClient) SetStateIDs(stateIDs map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stateIDMap = map[string]string{}
	for name, id := range stateIDs {
		trimmedName := strings.TrimSpace(name)
		trimmedID := strings.TrimSpace(id)
		if trimmedName == "" || trimmedID == "" {
			continue
		}
		c.stateIDMap[trimmedName] = trimmedID
	}
}

func (c *HTTPClient) ListCandidateIssues(ctx context.Context, teamID string) ([]Issue, error) {
	states := c.runtimeStates()

	query := `query ListIssues($teamKey: String!) {
  issues(filter: { team: { key: { eq: $teamKey } } }, first: 50) {
    nodes {
      id
      identifier
      title
      project {
        id
        name
      }
      description
      updatedAt
      state { name }
      inverseRelations(first: 20) {
        nodes {
          type
          issue {
            id
            state { name }
          }
          relatedIssue {
            id
            state { name }
          }
        }
      }
    }
  }
}`

	if strings.TrimSpace(teamID) == "" {
		teamID = c.teamID
	}

	var resp struct {
		Issues struct {
			Nodes []struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
				Project    *struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"project"`
				Description string `json:"description"`
				UpdatedAt   string `json:"updatedAt"`
				State       struct {
					Name string `json:"name"`
				} `json:"state"`
				InverseRelations struct {
					Nodes []inverseRelation `json:"nodes"`
				} `json:"inverseRelations"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.graphQL(ctx, query, map[string]any{"teamKey": teamID}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return nil, err
	}

	issues := make([]Issue, 0, len(resp.Issues.Nodes))
	for _, n := range resp.Issues.Nodes {
		blocked := hasActiveBlockingInverseRelation(n.ID, n.InverseRelations.Nodes, states)

		meta, err := c.getIssueMetadata(ctx, n.ID)
		if err != nil {
			return nil, fmt.Errorf("read issue %s metadata attachment: %w", n.ID, err)
		}

		updatedAt, err := time.Parse(time.RFC3339, n.UpdatedAt)
		if err != nil {
			updatedAt = time.Time{}
		}

		issues = append(issues, Issue{
			ID:          n.ID,
			Identifier:  n.Identifier,
			Title:       n.Title,
			ProjectID:   projectID(n.Project),
			ProjectName: projectName(n.Project),
			Description: n.Description,
			StateName:   n.State.Name,
			UpdatedAt:   updatedAt,
			Blocked:     blocked,
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
    inverseRelations(first: 20) {
      nodes {
        type
        issue {
          id
          state { name }
        }
        relatedIssue {
          id
          state { name }
        }
      }
    }
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
			InverseRelations struct {
				Nodes []inverseRelation `json:"nodes"`
			} `json:"inverseRelations"`
		} `json:"issue"`
	}

	if err := c.graphQL(ctx, query, map[string]any{"issueId": issueID}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return Issue{}, err
	}
	if resp.Issue == nil {
		return Issue{}, fmt.Errorf("issue %s not found", issueID)
	}

	meta, err := c.getIssueMetadata(ctx, issueID)
	if err != nil {
		return Issue{}, fmt.Errorf("read issue metadata attachment: %w", err)
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
		Blocked:     hasActiveBlockingInverseRelation(resp.Issue.ID, resp.Issue.InverseRelations.Nodes, c.runtimeStates()),
		Metadata:    meta,
	}, nil
}

// GetIssueByIdentifier returns one issue snapshot by identifier.
func (c *HTTPClient) GetIssueByIdentifier(ctx context.Context, issueIdentifier string) (Issue, error) {
	identifier := strings.TrimSpace(issueIdentifier)
	if identifier == "" {
		return Issue{}, errors.New("issue identifier is required")
	}

	query := `query GetIssueByIdentifier($teamKey: String!, $identifier: ID!) {
  issues(
    filter: {
      team: { key: { eq: $teamKey } }
      id: { eq: $identifier }
    }
    first: 1
  ) {
    nodes {
      id
    }
  }
}`

	var resp struct {
		Issues struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"issues"`
	}

	if err := c.graphQL(ctx, query, map[string]any{
		"teamKey":    c.teamID,
		"identifier": identifier,
	}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return Issue{}, err
	}
	if len(resp.Issues.Nodes) == 0 {
		return Issue{}, fmt.Errorf("issue %s not found", identifier)
	}

	return c.GetIssue(ctx, resp.Issues.Nodes[0].ID)
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

	if err := c.graphQL(ctx, mutation, map[string]any{"issueId": issueID, "stateId": stateID}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
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

	nextMetadata := applyMetadataPatch(observed.Metadata, patch)
	if maps.Equal(nextMetadata, observed.Metadata) {
		return nil
	}

	// Read one more time to detect obvious write races before mutating Linear.
	current, err := c.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	if !maps.Equal(current.Metadata, observed.Metadata) || current.StateName != observed.StateName {
		return fmt.Errorf("update issue metadata: %w", ErrConflict)
	}
	return c.upsertIssueMetadataAttachment(ctx, issueID, nextMetadata)
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

	if err := c.graphQL(ctx, mutation, map[string]any{"issueId": issueID, "body": body}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return err
	}
	if !resp.CommentCreate.Success {
		return fmt.Errorf("create issue comment: %w", ErrConflict)
	}
	return nil
}

func (c *HTTPClient) ListIssueComments(ctx context.Context, issueID string) ([]IssueComment, error) {
	query := `query ListIssueComments($issueId: String!) {
  issue(id: $issueId) {
    comments(first: 100) {
      nodes {
        id
        body
        createdAt
      }
    }
  }
}`

	var resp struct {
		Issue *struct {
			Comments struct {
				Nodes []struct {
					ID        string `json:"id"`
					Body      string `json:"body"`
					CreatedAt string `json:"createdAt"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}

	if err := c.graphQL(ctx, query, map[string]any{"issueId": issueID}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return nil, err
	}
	if resp.Issue == nil {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	out := make([]IssueComment, 0, len(resp.Issue.Comments.Nodes))
	for _, node := range resp.Issue.Comments.Nodes {
		createdAtRaw := strings.TrimSpace(node.CreatedAt)
		createdAt, err := time.Parse(time.RFC3339, createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse issue comment %q createdAt %q: %w", strings.TrimSpace(node.ID), createdAtRaw, err)
		}
		out = append(out, IssueComment{
			ID:        strings.TrimSpace(node.ID),
			Body:      strings.TrimSpace(node.Body),
			CreatedAt: createdAt.UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (c *HTTPClient) resolveStateID(ctx context.Context, stateName string) (string, error) {
	c.mu.Lock()
	cached := resolveStateIDFromMap(c.stateIDMap, stateName)
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

	if err := c.graphQL(ctx, query, map[string]any{"teamKey": c.teamID}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return "", err
	}

	stateIDMap := map[string]string{}
	for _, state := range resp.WorkflowStates.Nodes {
		stateIDMap[state.Name] = state.ID
	}

	id := resolveStateIDFromMap(stateIDMap, stateName)
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

func resolveStateIDFromMap(stateIDMap map[string]string, stateName string) string {
	if len(stateIDMap) == 0 {
		return ""
	}

	if id := strings.TrimSpace(stateIDMap[stateName]); id != "" {
		return id
	}

	normalized := normalizeStateName(stateName)
	for candidate, id := range stateIDMap {
		if normalizeStateName(candidate) == normalized && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}

	return ""
}

func metadataAttachmentURL(_ string) string {
	return colinMetadataAttachmentURL
}

func (c *HTTPClient) getIssueMetadata(ctx context.Context, issueID string) (map[string]string, error) {
	url := metadataAttachmentURL(issueID)
	query := `query AttachmentsForURL($url: String!) {
  attachmentsForURL(url: $url) {
    nodes {
      id
      updatedAt
      metadata
      issue {
        id
      }
    }
  }
}`

	var resp struct {
		AttachmentsForURL struct {
			Nodes []struct {
				ID        string                     `json:"id"`
				UpdatedAt string                     `json:"updatedAt"`
				Metadata  map[string]json.RawMessage `json:"metadata"`
				Issue     *struct {
					ID string `json:"id"`
				} `json:"issue"`
			} `json:"nodes"`
		} `json:"attachmentsForURL"`
	}
	if err := c.graphQL(ctx, query, map[string]any{"url": url}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return nil, err
	}

	trimmedIssueID := strings.TrimSpace(issueID)
	var (
		selected        map[string]string
		selectedTimeUTC time.Time
	)
	for _, node := range resp.AttachmentsForURL.Nodes {
		if node.Issue == nil || strings.TrimSpace(node.Issue.ID) != trimmedIssueID {
			continue
		}
		nodeMetadata, err := metadataObjectToStringMap(node.Metadata)
		if err != nil {
			return nil, fmt.Errorf("decode metadata attachment %q: %w", strings.TrimSpace(node.ID), err)
		}
		if selected == nil {
			selected = nodeMetadata
			selectedTimeUTC = parseRFC3339OrZero(node.UpdatedAt)
			continue
		}
		nodeUpdatedAt := parseRFC3339OrZero(node.UpdatedAt)
		if nodeUpdatedAt.After(selectedTimeUTC) {
			selected = nodeMetadata
			selectedTimeUTC = nodeUpdatedAt
		}
	}
	if selected == nil {
		return map[string]string{}, nil
	}
	return selected, nil
}

func parseRFC3339OrZero(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func metadataObjectToStringMap(raw map[string]json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return map[string]string{}, nil
	}

	out := make(map[string]string, len(raw))
	for rawKey, rawValue := range raw {
		key := strings.TrimSpace(rawKey)
		if key == "" || len(rawValue) == 0 || string(rawValue) == "null" {
			continue
		}

		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, fmt.Errorf("metadata key %q must be a string", key)
		}
		out[key] = value
	}
	return out, nil
}

func metadataStringMapToObject(metadata map[string]string) map[string]any {
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		out[trimmedKey] = value
	}
	return out
}

func (c *HTTPClient) upsertIssueMetadataAttachment(ctx context.Context, issueID string, metadata map[string]string) error {
	mutation := `mutation UpsertIssueMetadataAttachment($input: AttachmentCreateInput!) {
  attachmentCreate(input: $input) {
    success
  }
}`

	var resp struct {
		AttachmentCreate struct {
			Success bool `json:"success"`
		} `json:"attachmentCreate"`
	}
	if err := c.graphQL(ctx, mutation, map[string]any{
		"input": map[string]any{
			"issueId":  strings.TrimSpace(issueID),
			"url":      metadataAttachmentURL(issueID),
			"title":    colinMetadataAttachmentTitle,
			"subtitle": colinMetadataAttachmentSubtitle,
			"metadata": metadataStringMapToObject(metadata),
		},
	}, func(data json.RawMessage) error {
		return json.Unmarshal(data, &resp)
	}); err != nil {
		return err
	}
	if !resp.AttachmentCreate.Success {
		return fmt.Errorf("update issue metadata: %w", ErrConflict)
	}
	return nil
}

func normalizeStateName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

func (c *HTTPClient) graphQL(ctx context.Context, query string, variables map[string]any, decodePayload func(data json.RawMessage) error) error {
	if decodePayload == nil {
		return errors.New("graphql payload decoder is nil")
	}
	if variables == nil {
		variables = map[string]any{}
	}
	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{
		Query:     query,
		Variables: variables,
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
		return newGraphQLStatusError(resp.StatusCode, resp.Header, respBody)
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
		return newGraphQLMessageError(envelope.Errors[0].Message, resp.Header, respBody)
	}
	if len(envelope.Data) == 0 {
		return errors.New("graphql response missing data")
	}

	if err := decodePayload(envelope.Data); err != nil {
		return fmt.Errorf("decode response payload: %w", err)
	}
	return nil
}

type inverseRelation struct {
	Type         string                `json:"type"`
	Issue        *inverseRelationIssue `json:"issue"`
	RelatedIssue *inverseRelationIssue `json:"relatedIssue"`
}

type inverseRelationIssue struct {
	ID    string `json:"id"`
	State struct {
		Name string `json:"name"`
	} `json:"state"`
}

func hasActiveBlockingInverseRelation(issueID string, relations []inverseRelation, states workflow.States) bool {
	normalizedIssueID := strings.TrimSpace(issueID)
	for _, relation := range relations {
		if !strings.EqualFold(strings.TrimSpace(relation.Type), "blocks") {
			continue
		}
		blocker := relationBlockerIssue(normalizedIssueID, relation)
		if blocker == nil {
			// Treat unknown relation shape as blocked for safety.
			return true
		}
		if !states.IsDone(blocker.State.Name) {
			return true
		}
	}
	return false
}

func (c *HTTPClient) runtimeStates() workflow.States {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.states.WithDefaults()
}

func relationBlockerIssue(issueID string, relation inverseRelation) *inverseRelationIssue {
	candidates := make([]*inverseRelationIssue, 0, 2)
	if relation.Issue != nil {
		issueRef := strings.TrimSpace(relation.Issue.ID)
		if issueRef != "" && issueRef != issueID {
			candidates = append(candidates, relation.Issue)
		}
	}
	if relation.RelatedIssue != nil {
		issueRef := strings.TrimSpace(relation.RelatedIssue.ID)
		if issueRef != "" && issueRef != issueID {
			candidates = append(candidates, relation.RelatedIssue)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	if issueID == "" {
		if relation.Issue != nil && relation.RelatedIssue == nil {
			return relation.Issue
		}
		if relation.RelatedIssue != nil && relation.Issue == nil {
			return relation.RelatedIssue
		}
	}
	return nil
}

func projectID(project *struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}) string {
	if project == nil {
		return ""
	}
	return strings.TrimSpace(project.ID)
}

func projectName(project *struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}) string {
	if project == nil {
		return ""
	}
	return strings.TrimSpace(project.Name)
}
