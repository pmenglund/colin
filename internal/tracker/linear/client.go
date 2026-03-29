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
	"github.com/pmenglund/colin/internal/githubutil"
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
	query := `
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
      attachments(first: 50) {
        nodes {
          title
          subtitle
          url
          sourceType
          metadata
        }
      }
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
	if len(states) == 0 {
		query = `
query CandidateIssues($projectSlug: String!, $after: String) {
  issues(
    first: 50
    after: $after
    filter: {
      project: { slugId: { eq: $projectSlug } }
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
      attachments(first: 50) {
        nodes {
          title
          subtitle
          url
          sourceType
          metadata
        }
      }
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
	}
	var after *string
	var out []domain.Issue
	for {
		variables := map[string]any{"projectSlug": c.project, "after": after}
		if len(states) > 0 {
			variables["states"] = states
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
	if attachmentNodes, ok := nestedSlice(node, "attachments", "nodes"); ok {
		issue.PullRequests = normalizePullRequests(attachmentNodes)
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

func normalizePullRequests(nodes []map[string]any) []domain.PullRequest {
	out := make([]domain.PullRequest, 0, len(nodes))
	indexByURL := map[string]int{}
	for _, node := range nodes {
		sourceType, _ := stringValue(node["sourceType"])
		if strings.EqualFold(strings.TrimSpace(sourceType), "github") {
			pr, ok := pullRequestFromGitHubAttachment(node)
			if ok {
				out = appendPullRequest(out, indexByURL, pr)
			}
			continue
		}
		pr, ok := pullRequestFromMetadataAttachment(node)
		if ok {
			out = appendPullRequest(out, indexByURL, pr)
			continue
		}
		pr, ok = pullRequestFromURLAttachment(node)
		if ok {
			out = appendPullRequest(out, indexByURL, pr)
		}
	}
	return out
}

func appendPullRequest(out []domain.PullRequest, indexByURL map[string]int, pr domain.PullRequest) []domain.PullRequest {
	key := pullRequestIdentityKey(pr)
	if key == "" {
		return out
	}
	if index, ok := indexByURL[key]; ok {
		out[index] = mergePullRequest(out[index], pr)
		return out
	}
	indexByURL[key] = len(out)
	return append(out, pr)
}

func mergePullRequest(existing, incoming domain.PullRequest) domain.PullRequest {
	primary := existing
	secondary := incoming
	if pullRequestRichness(incoming) > pullRequestRichness(existing) {
		primary = incoming
		secondary = existing
	}

	merged := primary

	if strings.TrimSpace(merged.Title) == "" {
		merged.Title = secondary.Title
	}
	if merged.Number == nil && secondary.Number != nil {
		merged.Number = secondary.Number
	}
	if strings.TrimSpace(merged.Status) == "" {
		merged.Status = secondary.Status
	}
	if !merged.Draft {
		merged.Draft = secondary.Draft
	}
	if strings.TrimSpace(merged.Branch) == "" {
		merged.Branch = secondary.Branch
	}
	if strings.TrimSpace(merged.TargetBranch) == "" {
		merged.TargetBranch = secondary.TargetBranch
	}
	if strings.TrimSpace(merged.RepoLogin) == "" {
		merged.RepoLogin = secondary.RepoLogin
	}
	if strings.TrimSpace(merged.RepoName) == "" {
		merged.RepoName = secondary.RepoName
	}
	if merged.CreatedAt == nil {
		merged.CreatedAt = secondary.CreatedAt
	}
	if merged.UpdatedAt == nil {
		merged.UpdatedAt = secondary.UpdatedAt
	}
	if merged.ClosedAt == nil {
		merged.ClosedAt = secondary.ClosedAt
	}
	if merged.MergedAt == nil {
		merged.MergedAt = secondary.MergedAt
	}

	state := selectPullRequestState(existing, incoming)
	merged.Status = state.Status
	merged.Draft = state.Draft
	merged.ClosedAt = state.ClosedAt
	merged.MergedAt = state.MergedAt

	return merged
}

func selectPullRequestState(existing, incoming domain.PullRequest) domain.PullRequest {
	existingUpdated := pullRequestStateUpdatedAt(existing)
	incomingUpdated := pullRequestStateUpdatedAt(incoming)
	switch {
	case existingUpdated != nil && incomingUpdated != nil && !existingUpdated.Equal(*incomingUpdated):
		if incomingUpdated.After(*existingUpdated) {
			return incoming
		}
		return existing
	case hasPullRequestState(existing) && !hasPullRequestState(incoming):
		return existing
	case hasPullRequestState(incoming) && !hasPullRequestState(existing):
		return incoming
	case pullRequestStatePriority(incoming) > pullRequestStatePriority(existing):
		return incoming
	default:
		return existing
	}
}

func hasPullRequestState(pr domain.PullRequest) bool {
	return strings.TrimSpace(pr.Status) != "" || pr.Draft || pr.ClosedAt != nil || pr.MergedAt != nil
}

func pullRequestStateUpdatedAt(pr domain.PullRequest) *time.Time {
	for _, candidate := range []*time.Time{pr.UpdatedAt, pr.MergedAt, pr.ClosedAt, pr.CreatedAt} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func pullRequestStatePriority(pr domain.PullRequest) int {
	status := strings.ToLower(strings.TrimSpace(pr.Status))
	switch {
	case pr.MergedAt != nil || status == "merged":
		return 4
	case pr.ClosedAt != nil || status == "closed":
		return 3
	case pr.Draft || status == "draft":
		return 2
	case status != "":
		return 1
	default:
		return 0
	}
}

func pullRequestRichness(pr domain.PullRequest) int {
	score := 0
	if strings.TrimSpace(pr.Title) != "" {
		score++
	}
	if pr.Number != nil {
		score += 2
	}
	if strings.TrimSpace(pr.Status) != "" {
		score += 2
	}
	if pr.Draft {
		score++
	}
	if strings.TrimSpace(pr.Branch) != "" {
		score++
	}
	if strings.TrimSpace(pr.TargetBranch) != "" {
		score++
	}
	if strings.TrimSpace(pr.RepoLogin) != "" {
		score++
	}
	if strings.TrimSpace(pr.RepoName) != "" {
		score++
	}
	if pr.CreatedAt != nil {
		score++
	}
	if pr.UpdatedAt != nil {
		score++
	}
	if pr.ClosedAt != nil {
		score++
	}
	if pr.MergedAt != nil {
		score++
	}
	return score
}

func pullRequestIdentityKey(pr domain.PullRequest) string {
	if pr.Number != nil {
		repoLogin := strings.ToLower(strings.TrimSpace(pr.RepoLogin))
		repoName := strings.ToLower(strings.TrimSpace(pr.RepoName))
		if repoLogin != "" && repoName != "" {
			return fmt.Sprintf("github:%s/%s#%d", repoLogin, repoName, *pr.Number)
		}
	}
	if repoLogin, repoName, number, ok := githubutil.ParsePullRequestURL(pr.URL); ok {
		return fmt.Sprintf(
			"github:%s/%s#%d",
			strings.ToLower(strings.TrimSpace(repoLogin)),
			strings.ToLower(strings.TrimSpace(repoName)),
			number,
		)
	}
	return strings.TrimSpace(pr.URL)
}

func pullRequestFromGitHubAttachment(node map[string]any) (domain.PullRequest, bool) {
	url, _ := stringValue(node["url"])
	metadata, hasMetadata := mapValue(node["metadata"])
	if hasMetadata {
		if metadataURL, ok := metadataString(metadata, "url", "pullRequestUrl", "pull_request_url"); ok {
			metadataURL = strings.TrimSpace(metadataURL)
			if _, _, _, ok := githubutil.ParsePullRequestURL(metadataURL); ok {
				url = metadataURL
			}
		}
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return domain.PullRequest{}, false
	}
	if _, _, _, ok := githubutil.ParsePullRequestURL(url); !ok {
		return domain.PullRequest{}, false
	}

	title, _ := stringValue(node["title"])
	if hasMetadata {
		if value, ok := stringValue(metadata["title"]); ok && strings.TrimSpace(value) != "" {
			title = value
		}
	}
	pr := domain.PullRequest{
		URL:   url,
		Title: strings.TrimSpace(title),
	}
	applyGitHubURLFallback(&pr)
	if !hasMetadata {
		return pr, true
	}

	status, _ := metadataString(metadata, "status")
	pr.Status = strings.ToLower(strings.TrimSpace(status))
	pr.Draft = metadataBool(metadata, "draft")
	pr.Branch = trimmedMetadataString(metadata, "branch", "source_branch")
	pr.TargetBranch = trimmedMetadataString(metadata, "targetBranch", "target_branch")
	pr.RepoLogin = trimmedMetadataString(metadata, "repoLogin", "repo_login")
	pr.RepoName = trimmedMetadataString(metadata, "repoName", "repo_name")
	pr.CreatedAt = metadataTime(metadata, "createdAt", "created_at")
	pr.UpdatedAt = metadataTime(metadata, "updatedAt", "updated_at")
	pr.ClosedAt = metadataTime(metadata, "closedAt", "closed_at")
	pr.MergedAt = metadataTime(metadata, "mergedAt", "merged_at")
	if value, ok := metadataInt(metadata, "number"); ok {
		pr.Number = &value
	}
	applyGitHubURLFallback(&pr)
	return pr, true
}

func pullRequestFromMetadataAttachment(node map[string]any) (domain.PullRequest, bool) {
	metadata, ok := mapValue(node["metadata"])
	if !ok {
		return domain.PullRequest{}, false
	}
	url, ok := stringValue(metadata["colin.pr_url"])
	if !ok || strings.TrimSpace(url) == "" {
		return domain.PullRequest{}, false
	}
	url = strings.TrimSpace(url)
	if _, _, _, ok := githubutil.ParsePullRequestURL(url); !ok {
		return domain.PullRequest{}, false
	}
	title, _ := stringValue(node["title"])
	pr := domain.PullRequest{
		URL:   url,
		Title: strings.TrimSpace(title),
	}
	applyGitHubURLFallback(&pr)
	return pr, true
}

func pullRequestFromURLAttachment(node map[string]any) (domain.PullRequest, bool) {
	url, ok := stringValue(node["url"])
	if !ok || strings.TrimSpace(url) == "" {
		return domain.PullRequest{}, false
	}
	if _, _, _, ok := githubutil.ParsePullRequestURL(url); !ok {
		return domain.PullRequest{}, false
	}
	title, _ := stringValue(node["title"])
	pr := domain.PullRequest{
		URL:   strings.TrimSpace(url),
		Title: strings.TrimSpace(title),
	}
	applyGitHubURLFallback(&pr)
	return pr, true
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

func mapValue(value any) (map[string]any, bool) {
	v, ok := value.(map[string]any)
	return v, ok
}

func trimmedMetadataString(metadata map[string]any, keys ...string) string {
	value, _ := metadataString(metadata, keys...)
	return strings.TrimSpace(value)
}

func timeValue(value any) *time.Time {
	raw, ok := stringValue(value)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &parsed
}

func metadataString(metadata map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := stringValue(metadata[key]); ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}

func metadataBool(metadata map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := metadata[key].(bool); ok {
			return value
		}
	}
	return false
}

func metadataInt(metadata map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		if value, ok := intValue(metadata[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func metadataTime(metadata map[string]any, keys ...string) *time.Time {
	for _, key := range keys {
		if value := timeValue(metadata[key]); value != nil {
			return value
		}
	}
	return nil
}

func applyGitHubURLFallback(pr *domain.PullRequest) {
	if pr == nil {
		return
	}
	repoLogin, repoName, number, ok := githubutil.ParsePullRequestURL(pr.URL)
	if !ok {
		return
	}
	if canonicalURL, ok := githubutil.CanonicalPullRequestURL(pr.URL); ok {
		pr.URL = canonicalURL
	}
	if strings.TrimSpace(pr.RepoLogin) == "" {
		pr.RepoLogin = repoLogin
	}
	if strings.TrimSpace(pr.RepoName) == "" {
		pr.RepoName = repoName
	}
	if pr.Number == nil {
		pr.Number = &number
	}
}
