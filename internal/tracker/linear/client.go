package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	ErrUnknownState     = errors.New("linear_unknown_state")
)

const (
	colinMetadataAttachmentTitle = "Colin metadata"
	colinMetadataURLPrefix       = "https://colin.invalid/linear/issues/"
	colinMetadataURLSuffix       = "/metadata"
)

// Client is the Linear-backed implementation of the tracker.Client interface.
type Client struct {
	endpoint string
	apiKey   string
	project  string
	active   []string
	client   *http.Client
	rateMu   sync.RWMutex
	rateInfo map[string]any
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
		active:   slices.Clone(config.CandidateStates(cfg)),
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

// UpdateIssueState moves an issue to the named workflow state within the issue's team.
func (c *Client) UpdateIssueState(ctx context.Context, issueID string, stateName string) error {
	stateID, err := c.lookupStateID(ctx, issueID, stateName)
	if err != nil {
		return err
	}

	const query = `
mutation UpdateIssueState($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) {
    success
    issue {
      id
      state { name }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"id":      issueID,
		"stateId": stateID,
	})
	if err != nil {
		return err
	}
	success, _ := nestedBool(resp, "data", "issueUpdate", "success")
	if !success {
		return ErrUnknownPayload
	}
	return nil
}

// ResolveGitAutomationState returns the team-configured Linear git automation state for the supplied event.
func (c *Client) ResolveGitAutomationState(ctx context.Context, issueID string, event string, targetBranch string) (string, bool, error) {
	const query = `
query GitAutomationState($id: String!) {
  issue(id: $id) {
    team {
      gitAutomationStates(first: 50) {
        nodes {
          event
          state { name }
          targetBranch {
            branchPattern
            isRegex
          }
        }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return "", false, err
	}
	nodes, ok := nestedSlice(resp, "data", "issue", "team", "gitAutomationStates", "nodes")
	if !ok {
		return "", false, nil
	}

	var (
		bestState string
		bestScore int
	)
	for _, node := range nodes {
		candidateEvent, _ := stringValue(node["event"])
		if !strings.EqualFold(strings.TrimSpace(candidateEvent), strings.TrimSpace(event)) {
			continue
		}
		stateName, _ := nestedString(node, "state", "name")
		score := gitAutomationMatchScore(node, targetBranch)
		if strings.TrimSpace(stateName) == "" || score == 0 || score < bestScore {
			continue
		}
		bestState = stateName
		bestScore = score
	}
	if strings.TrimSpace(bestState) == "" {
		return "", false, nil
	}
	return bestState, true, nil
}

// CreateIssueComment creates a top-level comment on a Linear issue.
func (c *Client) CreateIssueComment(ctx context.Context, issueID string, body string) (string, error) {
	const query = `
mutation CreateIssueComment($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    success
    comment { id }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"issueId": issueID,
			"body":    body,
		},
	})
	if err != nil {
		return "", err
	}
	return parseCreatedCommentID(resp)
}

// CreateCommentReply creates a reply comment under an existing issue comment.
func (c *Client) CreateCommentReply(ctx context.Context, issueID string, parentCommentID string, body string) (string, error) {
	const query = `
mutation CreateCommentReply($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    success
    comment { id }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"issueId":  issueID,
			"parentId": parentCommentID,
			"body":     body,
		},
	})
	if err != nil {
		return "", err
	}
	return parseCreatedCommentID(resp)
}

// UpsertIssueMetadata stores Colin-specific metadata on the Linear issue via a dedicated attachment.
func (c *Client) UpsertIssueMetadata(ctx context.Context, issueID string, metadata domain.ColinMetadata) (domain.ColinMetadata, error) {
	const query = `
mutation UpsertIssueMetadata($input: AttachmentCreateInput!) {
  attachmentCreate(input: $input) {
    success
    attachment {
      id
      title
      url
      metadata
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"issueId":  issueID,
			"title":    colinMetadataAttachmentTitle,
			"url":      colinMetadataAttachmentURL(issueID),
			"metadata": colinMetadataValue(metadata),
		},
	})
	if err != nil {
		return domain.ColinMetadata{}, err
	}
	success, _ := nestedBool(resp, "data", "attachmentCreate", "success")
	if !success {
		return domain.ColinMetadata{}, ErrUnknownPayload
	}
	attachment, ok := nestedMap(resp, "data", "attachmentCreate", "attachment")
	if !ok {
		return domain.ColinMetadata{}, ErrUnknownPayload
	}
	return parseColinMetadataAttachment(attachment)
}

// CurrentRateLimits returns the latest Linear request budget observed from HTTP response headers.
func (c *Client) CurrentRateLimits() map[string]any {
	c.rateMu.RLock()
	defer c.rateMu.RUnlock()
	return cloneMap(c.rateInfo)
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
      attachments(first: 50) {
        nodes {
          id
          title
          url
          metadata
        }
      }
      comments(first: 50) {
        nodes {
          id
          body
          createdAt
          parentId
          children(first: 50) {
            nodes {
              id
              body
              createdAt
              parentId
            }
          }
        }
      }
      history(first: 100) {
        nodes {
          createdAt
          fromState { name }
          toState { name }
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

func (c *Client) lookupStateID(ctx context.Context, issueID string, stateName string) (string, error) {
	const query = `
query IssueTeamStates($id: String!) {
  issue(id: $id) {
    team {
      states {
        nodes {
          id
          name
        }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return "", err
	}
	nodes, ok := nestedSlice(resp, "data", "issue", "team", "states", "nodes")
	if !ok {
		return "", ErrUnknownPayload
	}
	target := strings.TrimSpace(stateName)
	for _, node := range nodes {
		name, _ := stringValue(node["name"])
		if !strings.EqualFold(strings.TrimSpace(name), target) {
			continue
		}
		stateID, _ := stringValue(node["id"])
		if strings.TrimSpace(stateID) == "" {
			return "", ErrUnknownPayload
		}
		return stateID, nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnknownState, stateName)
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
	c.captureRateLimitHeaders(resp.Header, time.Now().UTC())
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

func (c *Client) captureRateLimitHeaders(header http.Header, observedAt time.Time) {
	limit, ok := parseHeaderInt(header.Get("X-RateLimit-Requests-Limit"))
	if !ok {
		return
	}
	remaining, ok := parseHeaderInt(header.Get("X-RateLimit-Requests-Remaining"))
	if !ok {
		return
	}
	resetAt, ok := parseHeaderTime(header.Get("X-RateLimit-Requests-Reset"))
	if !ok {
		return
	}

	info := map[string]any{
		"linear_requests": map[string]any{
			"limit":         limit,
			"remaining":     remaining,
			"resetsAt":      resetAt.Unix(),
			"observedAt":    observedAt.Unix(),
			"nextAllowedAt": nextAllowedAt(observedAt, resetAt, remaining).Unix(),
		},
	}
	if limit > 0 {
		info["linear_requests"].(map[string]any)["usedPercent"] = int(((limit - remaining) * 100) / limit)
	}

	c.rateMu.Lock()
	c.rateInfo = info
	c.rateMu.Unlock()
}

func nextAllowedAt(observedAt, resetAt time.Time, remaining int64) time.Time {
	if !resetAt.After(observedAt) {
		return observedAt
	}
	if remaining <= 0 {
		return resetAt
	}
	window := resetAt.Sub(observedAt)
	step := window / time.Duration(remaining+1)
	if step <= 0 {
		return observedAt
	}
	return observedAt.Add(step)
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
	issue.ColinMetadata = extractColinMetadata(node)
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
	if start, end, ok := latestReviewCycleWindow(node); ok && strings.EqualFold(strings.TrimSpace(issue.State), "Todo") {
		issue.ReviewCycle = &domain.ReviewCycle{
			EnteredReviewAt:  start,
			ReturnedToTodoAt: end,
		}
	}
	issue.ReviewFeedback = extractReviewFeedback(issue.State, node)
	return issue, nil
}

type linearComment struct {
	ID        string
	Body      string
	CreatedAt time.Time
	ParentID  *string
}

type linearStateChange struct {
	CreatedAt time.Time
	FromState string
	ToState   string
}

func extractReviewFeedback(state string, node map[string]any) []domain.ReviewFeedback {
	if !strings.EqualFold(strings.TrimSpace(state), "Todo") {
		return nil
	}

	start, end, ok := latestReviewCycleWindow(node)
	if !ok {
		return nil
	}

	comments := flattenComments(node)
	feedback := make([]domain.ReviewFeedback, 0, len(comments))
	for _, comment := range comments {
		if comment.CreatedAt.Before(start) || comment.CreatedAt.After(end) {
			continue
		}
		body := strings.TrimSpace(comment.Body)
		if body == "" || isColinComment(body) {
			continue
		}
		feedback = append(feedback, domain.ReviewFeedback{
			Body:      body,
			CreatedAt: comment.CreatedAt,
			ParentID:  comment.ParentID,
		})
	}
	return feedback
}

func latestReviewCycleWindow(node map[string]any) (time.Time, time.Time, bool) {
	changes := flattenStateChanges(node)
	if len(changes) == 0 {
		return time.Time{}, time.Time{}, false
	}

	for exitIdx := len(changes) - 1; exitIdx >= 0; exitIdx-- {
		change := changes[exitIdx]
		if !strings.EqualFold(strings.TrimSpace(change.FromState), "Review") || !strings.EqualFold(strings.TrimSpace(change.ToState), "Todo") {
			continue
		}
		for enterIdx := exitIdx - 1; enterIdx >= 0; enterIdx-- {
			enter := changes[enterIdx]
			if strings.EqualFold(strings.TrimSpace(enter.ToState), "Review") {
				return enter.CreatedAt, change.CreatedAt, true
			}
		}
		break
	}

	return time.Time{}, time.Time{}, false
}

func flattenComments(node map[string]any) []linearComment {
	nodes, ok := nestedSlice(node, "comments", "nodes")
	if !ok {
		return nil
	}

	seen := make(map[string]struct{}, len(nodes))
	out := make([]linearComment, 0, len(nodes))
	for _, commentNode := range nodes {
		if comment, ok := parseLinearComment(commentNode); ok {
			if _, exists := seen[comment.ID]; !exists {
				out = append(out, comment)
				seen[comment.ID] = struct{}{}
			}
		}
		if children, ok := nestedSlice(commentNode, "children", "nodes"); ok {
			for _, childNode := range children {
				if comment, ok := parseLinearComment(childNode); ok {
					if _, exists := seen[comment.ID]; !exists {
						out = append(out, comment)
						seen[comment.ID] = struct{}{}
					}
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			leftParent := derefStringValue(out[i].ParentID)
			rightParent := derefStringValue(out[j].ParentID)
			if leftParent == rightParent {
				return out[i].Body < out[j].Body
			}
			return leftParent < rightParent
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})

	return out
}

func flattenStateChanges(node map[string]any) []linearStateChange {
	nodes, ok := nestedSlice(node, "history", "nodes")
	if !ok {
		return nil
	}

	out := make([]linearStateChange, 0, len(nodes))
	for _, historyNode := range nodes {
		createdAtRaw, ok := stringValue(historyNode["createdAt"])
		if !ok {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, createdAtRaw)
		if err != nil {
			continue
		}
		fromState, _ := nestedString(historyNode, "fromState", "name")
		toState, _ := nestedString(historyNode, "toState", "name")
		if strings.TrimSpace(fromState) == "" && strings.TrimSpace(toState) == "" {
			continue
		}
		out = append(out, linearStateChange{
			CreatedAt: createdAt,
			FromState: fromState,
			ToState:   toState,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func parseLinearComment(node map[string]any) (linearComment, bool) {
	id, ok := stringValue(node["id"])
	if !ok || strings.TrimSpace(id) == "" {
		return linearComment{}, false
	}
	body, ok := stringValue(node["body"])
	if !ok {
		return linearComment{}, false
	}
	createdAtRaw, ok := stringValue(node["createdAt"])
	if !ok {
		return linearComment{}, false
	}
	createdAt, err := time.Parse(time.RFC3339, createdAtRaw)
	if err != nil {
		return linearComment{}, false
	}
	comment := linearComment{
		ID:        id,
		Body:      body,
		CreatedAt: createdAt,
	}
	if parentID, ok := stringValue(node["parentId"]); ok && strings.TrimSpace(parentID) != "" {
		comment.ParentID = &parentID
	}
	return comment, true
}

func isColinComment(body string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(body)), "[colin]")
}

func extractColinMetadata(node map[string]any) *domain.ColinMetadata {
	attachments, ok := nestedSlice(node, "attachments", "nodes")
	if !ok {
		return nil
	}
	for _, attachment := range attachments {
		metadata, err := parseColinMetadataAttachment(attachment)
		if err != nil {
			continue
		}
		return &metadata
	}
	return nil
}

func parseColinMetadataAttachment(node map[string]any) (domain.ColinMetadata, error) {
	title, _ := stringValue(node["title"])
	url, _ := stringValue(node["url"])
	if strings.TrimSpace(title) != colinMetadataAttachmentTitle || !isColinMetadataURL(url) {
		return domain.ColinMetadata{}, errors.New("not a Colin metadata attachment")
	}

	metadataMap, _ := node["metadata"].(map[string]any)
	metadata := domain.ColinMetadata{}
	metadata.AttachmentID, _ = stringValue(node["id"])
	metadata.ReviewPublishDirective, _ = stringValue(metadataMap["review_publish_directive"])
	metadata.LastRunType, _ = stringValue(metadataMap["last_run_type"])
	metadata.LastOutcome, _ = stringValue(metadataMap["last_outcome"])
	metadata.LastSummaryCommentID, _ = stringValue(metadataMap["last_summary_comment_id"])
	if value, _ := stringValue(metadataMap["updated_at"]); strings.TrimSpace(value) != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			metadata.UpdatedAt = &parsed
		}
	}
	return metadata, nil
}

func colinMetadataValue(metadata domain.ColinMetadata) map[string]any {
	value := map[string]any{
		"review_publish_directive": strings.TrimSpace(metadata.ReviewPublishDirective),
		"last_run_type":            strings.TrimSpace(metadata.LastRunType),
		"last_outcome":             strings.TrimSpace(metadata.LastOutcome),
		"last_summary_comment_id":  strings.TrimSpace(metadata.LastSummaryCommentID),
	}
	if metadata.UpdatedAt != nil {
		value["updated_at"] = metadata.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return value
}

func colinMetadataAttachmentURL(issueID string) string {
	return colinMetadataURLPrefix + strings.TrimSpace(issueID) + colinMetadataURLSuffix
}

func isColinMetadataURL(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, colinMetadataURLPrefix) && strings.HasSuffix(value, colinMetadataURLSuffix)
}

func derefStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func gitAutomationMatchScore(node map[string]any, targetBranch string) int {
	targetBranch = strings.TrimSpace(targetBranch)
	targetNode, ok := node["targetBranch"].(map[string]any)
	if !ok || targetNode == nil {
		return 1
	}

	pattern, _ := stringValue(targetNode["branchPattern"])
	isRegex, _ := targetNode["isRegex"].(bool)
	if targetBranch == "" || strings.TrimSpace(pattern) == "" {
		return 0
	}
	if isRegex {
		matched, err := regexp.MatchString(pattern, targetBranch)
		if err != nil || !matched {
			return 0
		}
		return 2
	}
	if pattern == targetBranch {
		return 3
	}
	return 0
}

func parseCreatedCommentID(resp map[string]any) (string, error) {
	success, _ := nestedBool(resp, "data", "commentCreate", "success")
	if !success {
		return "", ErrUnknownPayload
	}
	commentID, ok := nestedString(resp, "data", "commentCreate", "comment", "id")
	if !ok || strings.TrimSpace(commentID) == "" {
		return "", ErrUnknownPayload
	}
	return commentID, nil
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

func nestedMap(root map[string]any, keys ...string) (map[string]any, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok || value == nil {
		return nil, false
	}
	asMap, ok := value.(map[string]any)
	return asMap, ok
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

func parseHeaderInt(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseHeaderTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		if unix > 1_000_000_000_000 {
			return time.UnixMilli(unix).UTC(), true
		}
		return time.Unix(unix, 0).UTC(), true
	}
	for _, layout := range []string{time.RFC3339, time.RFC1123, http.TimeFormat} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if nested, ok := value.(map[string]any); ok {
			clone := make(map[string]any, len(nested))
			for nestedKey, nestedValue := range nested {
				clone[nestedKey] = nestedValue
			}
			out[key] = clone
			continue
		}
		out[key] = value
	}
	return out
}
