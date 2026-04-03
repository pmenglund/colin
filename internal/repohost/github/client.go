package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	githubapi "github.com/google/go-github/v79/github"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
)

const tokenHelp = "missing GitHub token: set repo.api_token, GITHUB_TOKEN, or GH_TOKEN"
const defaultHTTPTimeout = 2 * time.Minute

type Client struct {
	client          *githubapi.Client
	logger          *slog.Logger
	graphQLEndpoint string
}

func NewClientFromConfig(cfg domain.ServiceConfig, logger *slog.Logger) (repohost.Client, error) {
	return NewClient(cfg.Repo.APIToken, http.DefaultClient, cfg.Repo.APIBaseURL, logger)
}

func NewClient(token string, httpClient *http.Client, baseURL string, logger *slog.Logger) (*Client, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New(tokenHelp)
	}
	httpClient = httpClientWithTimeout(httpClient)

	client := githubapi.NewClient(httpClient).WithAuthToken(token)
	graphQLEndpoint := "graphql"
	if strings.TrimSpace(baseURL) != "" {
		parsed, err := neturl.Parse(ensureTrailingSlash(baseURL))
		if err != nil {
			return nil, fmt.Errorf("parse github base url: %w", err)
		}
		client.BaseURL = parsed
		client.UploadURL = parsed
		graphQLEndpoint = graphQLEndpointURL(parsed).String()
	}

	return &Client{client: client, logger: logger, graphQLEndpoint: graphQLEndpoint}, nil
}

func (c *Client) ValidateAuth(ctx context.Context) error {
	_, _, err := c.client.Users.Get(ctx, "")
	return err
}

func (c *Client) HTTPTimeout() time.Duration {
	if c == nil || c.client == nil || c.client.Client() == nil {
		return 0
	}
	return c.client.Client().Timeout
}

func (c *Client) PullRequestByHead(ctx context.Context, owner, repo, head, base string) (*repohost.PullRequest, error) {
	for _, queryHead := range pullRequestHeadQueries(owner, head) {
		prs, _, err := c.client.PullRequests.List(ctx, owner, repo, &githubapi.PullRequestListOptions{
			State: "all",
			Head:  queryHead,
			Base:  base,
			ListOptions: githubapi.ListOptions{
				PerPage: 1,
			},
		})
		if err != nil {
			return nil, err
		}
		if len(prs) == 0 {
			continue
		}

		pr := prs[0]
		if strings.EqualFold(pr.GetState(), "closed") {
			full, _, err := c.client.PullRequests.Get(ctx, owner, repo, pr.GetNumber())
			if err == nil && full != nil {
				pr = full
			}
		}
		return pullRequestFromAPI(pr), nil
	}
	return nil, nil
}

func (c *Client) PullRequestByNumber(ctx context.Context, owner, repo string, number int) (*repohost.PullRequest, error) {
	pr, _, err := c.client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return pullRequestFromAPI(pr), nil
}

func (c *Client) CreatePullRequest(ctx context.Context, owner, repo string, input repohost.CreatePullRequestInput) (*repohost.PullRequest, error) {
	pr, _, err := c.client.PullRequests.Create(ctx, owner, repo, &githubapi.NewPullRequest{
		Title: githubapi.Ptr(input.Title),
		Head:  githubapi.Ptr(input.Head),
		Base:  githubapi.Ptr(input.Base),
		Body:  githubapi.Ptr(input.Body),
	})
	if err != nil {
		return nil, err
	}
	return pullRequestFromAPI(pr), nil
}

func (c *Client) MergePullRequest(ctx context.Context, owner, repo string, number int, method string) error {
	_, _, err := c.client.PullRequests.Merge(ctx, owner, repo, number, "", &githubapi.PullRequestOptions{
		MergeMethod: method,
	})
	return err
}

func (c *Client) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	_, _, err := c.client.Git.GetRef(ctx, owner, repo, "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

func (c *Client) ReviewThreads(ctx context.Context, owner, repo string, number int, cursor string) (repohost.ReviewThreadPage, error) {
	const query = `query ReviewThreads($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 50, after: $cursor) {
        nodes {
          id
          isResolved
          isOutdated
          viewerCanReply
          viewerCanResolve
          path
          line
          startLine
          comments(first: 20) {
            nodes {
              id
              body
              url
              createdAt
              author { login }
            }
            pageInfo {
              hasNextPage
              endCursor
            }
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

	resp, err := c.graphQL(ctx, query, map[string]any{
		"owner":  owner,
		"name":   repo,
		"number": number,
		"cursor": nullableCursor(cursor),
	})
	if err != nil {
		return repohost.ReviewThreadPage{}, err
	}

	nodes, _ := nestedSlice(resp, "data", "repository", "pullRequest", "reviewThreads", "nodes")
	hasNextPage, _ := nestedBool(resp, "data", "repository", "pullRequest", "reviewThreads", "pageInfo", "hasNextPage")
	endCursor, _ := nestedString(resp, "data", "repository", "pullRequest", "reviewThreads", "pageInfo", "endCursor")
	threads := make([]repohost.ReviewThread, 0, len(nodes))
	for _, node := range nodes {
		thread, ok := reviewThread(node)
		if ok {
			threads = append(threads, thread)
		}
	}
	return repohost.ReviewThreadPage{
		Threads:     threads,
		HasNextPage: hasNextPage,
		EndCursor:   endCursor,
	}, nil
}

func (c *Client) ReviewThreadComments(ctx context.Context, threadID, cursor string) (repohost.ReviewThreadCommentPage, error) {
	const query = `query ReviewThreadComments($threadId: ID!, $cursor: String) {
  node(id: $threadId) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $cursor) {
        nodes {
          author { login }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

	resp, err := c.graphQL(ctx, query, map[string]any{
		"threadId": threadID,
		"cursor":   nullableCursor(cursor),
	})
	if err != nil {
		return repohost.ReviewThreadCommentPage{}, err
	}

	nodes, _ := nestedSlice(resp, "data", "node", "comments", "nodes")
	hasNextPage, _ := nestedBool(resp, "data", "node", "comments", "pageInfo", "hasNextPage")
	endCursor, _ := nestedString(resp, "data", "node", "comments", "pageInfo", "endCursor")
	comments := make([]repohost.ReviewComment, 0, len(nodes))
	for _, node := range nodes {
		comment, ok := reviewComment(node)
		if ok {
			comments = append(comments, comment)
		}
	}
	return repohost.ReviewThreadCommentPage{
		Comments:    comments,
		HasNextPage: hasNextPage,
		EndCursor:   endCursor,
	}, nil
}

func (c *Client) PullRequestReactions(ctx context.Context, owner, repo string, number int, cursor string) (repohost.ReactionPage, error) {
	const query = `query PullRequestReactions($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reactions(first: 100, after: $cursor) {
        nodes {
          content
          createdAt
          user { login }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

	resp, err := c.graphQL(ctx, query, map[string]any{
		"owner":  owner,
		"name":   repo,
		"number": number,
		"cursor": nullableCursor(cursor),
	})
	if err != nil {
		return repohost.ReactionPage{}, err
	}

	nodes, _ := nestedSlice(resp, "data", "repository", "pullRequest", "reactions", "nodes")
	reactions := make([]repohost.Reaction, 0, len(nodes))
	for _, node := range nodes {
		reaction := repohost.Reaction{}
		reaction.Content, _ = stringValue(node["content"])
		reaction.UserLogin, _ = nestedString(node, "user", "login")
		if createdAt, ok := parseTimestamp(node["createdAt"]); ok {
			reaction.CreatedAt = &createdAt
		}
		reactions = append(reactions, reaction)
	}
	hasNextPage, _ := nestedBool(resp, "data", "repository", "pullRequest", "reactions", "pageInfo", "hasNextPage")
	endCursor, _ := nestedString(resp, "data", "repository", "pullRequest", "reactions", "pageInfo", "endCursor")
	return repohost.ReactionPage{
		Reactions:   reactions,
		HasNextPage: hasNextPage,
		EndCursor:   endCursor,
	}, nil
}

func (c *Client) ReplyToReviewThread(ctx context.Context, threadID, body string) error {
	const mutation = `mutation ReplyReviewThread($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: { pullRequestReviewThreadId: $threadId, body: $body }) {
    comment { id url }
  }
}`
	_, err := c.graphQL(ctx, mutation, map[string]any{
		"threadId": threadID,
		"body":     body,
	})
	return err
}

func (c *Client) ResolveReviewThread(ctx context.Context, threadID string) error {
	const mutation = `mutation ResolveReviewThread($threadId: ID!) {
  resolveReviewThread(input: { threadId: $threadId }) {
    thread { id isResolved }
  }
}`
	_, err := c.graphQL(ctx, mutation, map[string]any{
		"threadId": threadID,
	})
	return err
}

func (c *Client) graphQL(ctx context.Context, query string, vars map[string]any) (map[string]any, error) {
	endpoint := strings.TrimSpace(c.graphQLEndpoint)
	if endpoint == "" {
		endpoint = "graphql"
	}

	req, err := c.client.NewRequest("POST", endpoint, map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return nil, err
	}

	var decoded map[string]any
	if _, err := c.client.Do(ctx, req, &decoded); err != nil {
		return nil, err
	}
	if errorsField, ok := decoded["errors"]; ok && errorsField != nil {
		return nil, fmt.Errorf("graphql errors: %v", errorsField)
	}
	return decoded, nil
}

func pullRequestFromAPI(pr *githubapi.PullRequest) *repohost.PullRequest {
	if pr == nil {
		return nil
	}

	state := strings.ToUpper(strings.TrimSpace(pr.GetState()))
	if pr.GetMerged() || pr.GetMergedAt().Time.After(time.Time{}) {
		state = "MERGED"
	}

	return &repohost.PullRequest{
		Number:      pr.GetNumber(),
		URL:         pr.GetHTMLURL(),
		State:       state,
		Body:        pr.GetBody(),
		Mergeable:   pr.Mergeable,
		HeadRefName: pr.GetHead().GetRef(),
		BaseRefName: pr.GetBase().GetRef(),
	}
}

func pullRequestHeadQueries(owner, head string) []string {
	head = strings.TrimSpace(head)
	owner = strings.TrimSpace(owner)
	if head == "" || owner == "" {
		return nil
	}

	queries := make([]string, 0, 2)
	seen := map[string]struct{}{}
	add := func(queryOwner, branch string) {
		queryOwner = strings.TrimSpace(queryOwner)
		branch = strings.TrimSpace(branch)
		if queryOwner == "" || branch == "" {
			return
		}
		query := queryOwner + ":" + branch
		if _, ok := seen[query]; ok {
			return
		}
		seen[query] = struct{}{}
		queries = append(queries, query)
	}

	if strings.HasPrefix(head, owner+"/") {
		add(owner, strings.TrimPrefix(head, owner+"/"))
	}
	if idx := strings.Index(head, "/"); idx > 0 && idx < len(head)-1 {
		add(head[:idx], head[idx+1:])
	}
	add(owner, head)
	return queries
}

func reviewThread(node map[string]any) (repohost.ReviewThread, bool) {
	id, _ := stringValue(node["id"])
	pathValue, _ := stringValue(node["path"])
	if strings.TrimSpace(id) == "" || strings.TrimSpace(pathValue) == "" {
		return repohost.ReviewThread{}, false
	}

	comments, ok := nestedSlice(node, "comments", "nodes")
	if !ok {
		return repohost.ReviewThread{}, false
	}
	thread := repohost.ReviewThread{
		ID:               id,
		Path:             pathValue,
		IsResolved:       boolValue(node["isResolved"]),
		IsOutdated:       boolValue(node["isOutdated"]),
		ViewerCanReply:   boolValue(node["viewerCanReply"]),
		ViewerCanResolve: boolValue(node["viewerCanResolve"]),
		Comments: repohost.ReviewCommentConnection{
			Comments:    make([]repohost.ReviewComment, 0, len(comments)),
			HasNextPage: nestedBoolValue(node, "comments", "pageInfo", "hasNextPage"),
			EndCursor:   nestedStringValue(node, "comments", "pageInfo", "endCursor"),
		},
	}
	if value, ok := intValue(node["line"]); ok {
		thread.Line = &value
	}
	if value, ok := intValue(node["startLine"]); ok {
		thread.StartLine = &value
	}
	for _, raw := range comments {
		comment, ok := reviewComment(raw)
		if ok {
			thread.Comments.Comments = append(thread.Comments.Comments, comment)
		}
	}
	return thread, true
}

func reviewComment(node map[string]any) (repohost.ReviewComment, bool) {
	comment := repohost.ReviewComment{}
	comment.ID, _ = stringValue(node["id"])
	comment.Body, _ = stringValue(node["body"])
	comment.URL, _ = stringValue(node["url"])
	comment.AuthorLogin, _ = nestedString(node, "author", "login")
	if createdAt, ok := parseTimestamp(node["createdAt"]); ok {
		comment.CreatedAt = &createdAt
	}
	if comment.ID == "" && comment.AuthorLogin == "" && comment.Body == "" && comment.URL == "" && comment.CreatedAt == nil {
		return repohost.ReviewComment{}, false
	}
	return comment, true
}

func nestedBoolValue(root map[string]any, keys ...string) bool {
	value, _ := nestedBool(root, keys...)
	return value
}

func nestedStringValue(root map[string]any, keys ...string) string {
	value, _ := nestedString(root, keys...)
	return value
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

func nestedBool(root map[string]any, keys ...string) (bool, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return false, false
	}
	return boolValue(value), true
}

func nestedString(root map[string]any, keys ...string) (string, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return "", false
	}
	return stringValue(value)
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
	v, ok := value.(string)
	return v, ok
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func parseTimestamp(value any) (time.Time, bool) {
	raw, ok := stringValue(value)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func httpClientWithTimeout(httpClient *http.Client) *http.Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clone := *httpClient
	if clone.Timeout <= 0 {
		clone.Timeout = defaultHTTPTimeout
	}
	return &clone
}

func nullableCursor(cursor string) any {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil
	}
	return cursor
}

func ensureTrailingSlash(raw string) string {
	if strings.HasSuffix(raw, "/") {
		return raw
	}
	return raw + "/"
}

func graphQLEndpointURL(base *neturl.URL) *neturl.URL {
	endpoint := *base
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	endpoint.RawPath = ""

	switch path := endpoint.Path; {
	case strings.HasSuffix(path, "/api/v3/"):
		endpoint.Path = strings.TrimSuffix(path, "/api/v3/") + "/api/graphql"
	case strings.HasSuffix(path, "/api/v3"):
		endpoint.Path = strings.TrimSuffix(path, "/api/v3") + "/api/graphql"
	default:
		endpoint.Path = strings.TrimRight(path, "/") + "/graphql"
	}

	return &endpoint
}

func isNotFound(err error) bool {
	var responseErr *githubapi.ErrorResponse
	return errors.As(err, &responseErr) && responseErr.Response != nil && responseErr.Response.StatusCode == http.StatusNotFound
}
