package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
)

var (
	ErrAPIRequest       = errors.New("linear_api_request")
	ErrAPIStatus        = errors.New("linear_api_status")
	ErrGraphQLErrors    = errors.New("linear_graphql_errors")
	ErrUnknownPayload   = errors.New("linear_unknown_payload")
	ErrMissingEndCursor = errors.New("linear_missing_end_cursor")
)

// Client is the Linear-backed implementation of the tracker.Client interface.
type Client struct {
	endpoint string
	apiKey   string
	project  string
	active   []string
	client   *http.Client
}

// New constructs a Linear-backed tracker client from the current service config.
func New(cfg domain.ServiceConfig) (*Client, error) {
	if err := config.ValidateDispatch(cfg); err != nil {
		return nil, err
	}
	return &Client{
		endpoint: cfg.Tracker.Endpoint,
		apiKey:   cfg.Tracker.APIKey,
		project:  cfg.Tracker.ProjectSlug,
		active:   slices.Clone(cfg.Tracker.ActiveStates),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// FetchCandidateIssues returns the current active issues for the configured Linear project.
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	return c.fetchIssues(ctx, c.active)
}

// FetchIssuesByStates returns issues whose current Linear state is in the provided list.
func (c *Client) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return nil, nil
	}
	return c.fetchIssues(ctx, stateNames)
}

// FetchIssueStatesByIDs refreshes the minimal state snapshot for the supplied Linear issue IDs.
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}
	const query = `
query IssueStates($ids: [ID!]!) {
  issues(filter: { id: { in: $ids } }, first: 250) {
    nodes {
      id
      identifier
      title
      state { name }
      updatedAt
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"ids": issueIDs})
	if err != nil {
		return nil, err
	}
	nodes, ok := nestedSlice(resp, "data", "issues", "nodes")
	if !ok {
		return nil, ErrUnknownPayload
	}
	issues := make([]domain.Issue, 0, len(nodes))
	for _, node := range nodes {
		issue, err := normalizeIssue(node)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func (c *Client) fetchIssues(ctx context.Context, states []string) ([]domain.Issue, error) {
	const query = `
query CandidateIssues($projectSlug: String!, $states: [String!], $after: String) {
  issues(
    first: 50
    after: $after
    filter: {
      project: { slugId: { eq: $projectSlug } }
      state: { name: { in: $states } }
    }
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      description
      priority
      branchName
      url
      createdAt
      updatedAt
      state { name }
      labels { nodes { name } }
      inverseRelations {
        nodes {
          type
          issue {
            id
            identifier
            state { name }
          }
        }
      }
    }
  }
}
`
	var after *string
	var out []domain.Issue
	for {
		variables := map[string]any{
			"projectSlug": c.project,
			"states":      states,
			"after":       after,
		}
		resp, err := c.doQuery(ctx, query, variables)
		if err != nil {
			return nil, err
		}
		nodes, ok := nestedSlice(resp, "data", "issues", "nodes")
		if !ok {
			return nil, ErrUnknownPayload
		}
		for _, node := range nodes {
			issue, err := normalizeIssue(node)
			if err != nil {
				return nil, err
			}
			out = append(out, issue)
		}
		hasNextPage, _ := nestedBool(resp, "data", "issues", "pageInfo", "hasNextPage")
		if !hasNextPage {
			break
		}
		cursor, ok := nestedString(resp, "data", "issues", "pageInfo", "endCursor")
		if !ok || strings.TrimSpace(cursor) == "" {
			return nil, ErrMissingEndCursor
		}
		after = &cursor
	}
	return out, nil
}

func (c *Client) doQuery(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	body := map[string]any{
		"query":     query,
		"variables": variables,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status=%d body=%s", ErrAPIStatus, resp.StatusCode, string(payload))
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownPayload, err)
	}
	if errorsField, ok := decoded["errors"]; ok && errorsField != nil {
		return nil, fmt.Errorf("%w: %v", ErrGraphQLErrors, errorsField)
	}
	return decoded, nil
}

func normalizeIssue(node map[string]any) (domain.Issue, error) {
	id, _ := stringValue(node["id"])
	identifier, _ := stringValue(node["identifier"])
	title, _ := stringValue(node["title"])
	state, _ := nestedString(node, "state", "name")

	issue := domain.Issue{
		ID:         id,
		Identifier: identifier,
		Title:      title,
		State:      state,
	}
	if value, ok := stringValue(node["description"]); ok {
		issue.Description = &value
	}
	if value, ok := intValue(node["priority"]); ok {
		issue.Priority = &value
	}
	if value, ok := stringValue(node["branchName"]); ok {
		issue.BranchName = &value
	}
	if value, ok := stringValue(node["url"]); ok {
		issue.URL = &value
	}
	if value, ok := stringValue(node["createdAt"]); ok {
		if ts, err := time.Parse(time.RFC3339, value); err == nil {
			issue.CreatedAt = &ts
		}
	}
	if value, ok := stringValue(node["updatedAt"]); ok {
		if ts, err := time.Parse(time.RFC3339, value); err == nil {
			issue.UpdatedAt = &ts
		}
	}
	if labelNodes, ok := nestedSlice(node, "labels", "nodes"); ok {
		for _, label := range labelNodes {
			if name, ok := stringValue(label["name"]); ok {
				issue.Labels = append(issue.Labels, strings.ToLower(name))
			}
		}
	}
	if relationNodes, ok := nestedSlice(node, "inverseRelations", "nodes"); ok {
		for _, relation := range relationNodes {
			relationType, _ := stringValue(relation["type"])
			if strings.ToLower(relationType) != "blocks" {
				continue
			}
			related, ok := relation["issue"].(map[string]any)
			if !ok {
				continue
			}
			blocker := domain.BlockerRef{}
			if value, ok := stringValue(related["id"]); ok {
				blocker.ID = &value
			}
			if value, ok := stringValue(related["identifier"]); ok {
				blocker.Identifier = &value
			}
			if value, ok := nestedString(related, "state", "name"); ok {
				blocker.State = &value
			}
			issue.BlockedBy = append(issue.BlockedBy, blocker)
		}
	}
	return issue, nil
}

func nestedSlice(root map[string]any, keys ...string) ([]map[string]any, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok || value == nil {
		return nil, false
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		asMap, ok := item.(map[string]any)
		if ok {
			out = append(out, asMap)
		}
	}
	return out, true
}

func nestedString(root map[string]any, keys ...string) (string, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return "", false
	}
	return stringValue(value)
}

func nestedBool(root map[string]any, keys ...string) (bool, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return false, false
	}
	v, ok := value.(bool)
	return v, ok
}

func nestedValue(root map[string]any, keys ...string) (any, bool) {
	current := any(root)
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = asMap[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func stringValue(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	default:
		return "", false
	}
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
