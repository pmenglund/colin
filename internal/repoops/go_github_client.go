package repoops

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
)

const githubTokenHelp = "missing GitHub token: set repo.api_token, GITHUB_TOKEN, or GH_TOKEN"
const defaultGitHubHTTPTimeout = 2 * time.Minute

type goGitHubClient struct {
	client *githubapi.Client
	logger *slog.Logger
}

// NewGitHubClientFromConfig constructs the real GitHub API client used by repo automation.
func NewGitHubClientFromConfig(cfg domain.ServiceConfig, logger *slog.Logger) (GitHubClient, error) {
	return newGoGitHubClient(cfg.Repo.APIToken, http.DefaultClient, "", logger)
}

func newGoGitHubClient(token string, httpClient *http.Client, baseURL string, logger *slog.Logger) (*goGitHubClient, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New(githubTokenHelp)
	}
	httpClient = gitHubHTTPClientWithTimeout(httpClient)

	client := githubapi.NewClient(httpClient).WithAuthToken(token)
	if strings.TrimSpace(baseURL) != "" {
		parsed, err := neturl.Parse(ensureTrailingSlash(baseURL))
		if err != nil {
			return nil, fmt.Errorf("parse github base url: %w", err)
		}
		client.BaseURL = parsed
		client.UploadURL = parsed
	}

	return &goGitHubClient{client: client, logger: logger}, nil
}

func (c *goGitHubClient) ValidateAuth(ctx context.Context) error {
	_, _, err := c.client.Users.Get(ctx, "")
	return err
}

func (c *goGitHubClient) PullRequestByHead(ctx context.Context, owner, repo, head, base string) (*GitHubPullRequest, error) {
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
		// The list API reports merged PRs as closed; fetch the canonical state when needed.
		if strings.EqualFold(pr.GetState(), "closed") {
			full, _, err := c.client.PullRequests.Get(ctx, owner, repo, pr.GetNumber())
			if err == nil && full != nil {
				pr = full
			}
		}
		return gitHubPullRequestFromAPI(pr), nil
	}
	return nil, nil
}

func (c *goGitHubClient) PullRequestByNumber(ctx context.Context, owner, repo string, number int) (*GitHubPullRequest, error) {
	pr, _, err := c.client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		if isGitHubNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return gitHubPullRequestFromAPI(pr), nil
}

func (c *goGitHubClient) CreatePullRequest(ctx context.Context, owner, repo string, input CreatePullRequestInput) (*GitHubPullRequest, error) {
	pr, _, err := c.client.PullRequests.Create(ctx, owner, repo, &githubapi.NewPullRequest{
		Title: githubapi.Ptr(input.Title),
		Head:  githubapi.Ptr(input.Head),
		Base:  githubapi.Ptr(input.Base),
		Body:  githubapi.Ptr(input.Body),
	})
	if err != nil {
		return nil, err
	}
	return gitHubPullRequestFromAPI(pr), nil
}

func (c *goGitHubClient) MergePullRequest(ctx context.Context, owner, repo string, number int, method string) error {
	_, _, err := c.client.PullRequests.Merge(ctx, owner, repo, number, "", &githubapi.PullRequestOptions{
		MergeMethod: method,
	})
	return err
}

func (c *goGitHubClient) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	_, _, err := c.client.Git.GetRef(ctx, owner, repo, "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if isGitHubNotFound(err) {
		return false, nil
	}
	return false, err
}

func (c *goGitHubClient) ReviewThreads(ctx context.Context, owner, repo string, number int, cursor string) (GitHubReviewThreadPage, error) {
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
		return GitHubReviewThreadPage{}, err
	}

	nodes, _ := nestedSlice(resp, "data", "repository", "pullRequest", "reviewThreads", "nodes")
	hasNextPage, _ := nestedBool(resp, "data", "repository", "pullRequest", "reviewThreads", "pageInfo", "hasNextPage")
	endCursor, _ := nestedString(resp, "data", "repository", "pullRequest", "reviewThreads", "pageInfo", "endCursor")
	return GitHubReviewThreadPage{
		Threads:     nodes,
		HasNextPage: hasNextPage,
		EndCursor:   endCursor,
	}, nil
}

func (c *goGitHubClient) ReviewThreadComments(ctx context.Context, threadID, cursor string) (GitHubReviewThreadCommentPage, error) {
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
		return GitHubReviewThreadCommentPage{}, err
	}

	nodes, _ := nestedSlice(resp, "data", "node", "comments", "nodes")
	hasNextPage, _ := nestedBool(resp, "data", "node", "comments", "pageInfo", "hasNextPage")
	endCursor, _ := nestedString(resp, "data", "node", "comments", "pageInfo", "endCursor")
	return GitHubReviewThreadCommentPage{
		Comments:    nodes,
		HasNextPage: hasNextPage,
		EndCursor:   endCursor,
	}, nil
}

func (c *goGitHubClient) PullRequestReactions(ctx context.Context, owner, repo string, number int, cursor string) (GitHubReactionPage, error) {
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
		return GitHubReactionPage{}, err
	}

	nodes, _ := nestedSlice(resp, "data", "repository", "pullRequest", "reactions", "nodes")
	reactions := make([]GitHubReaction, 0, len(nodes))
	for _, node := range nodes {
		reaction := GitHubReaction{}
		reaction.Content, _ = stringValue(node["content"])
		reaction.UserLogin, _ = nestedString(node, "user", "login")
		if createdAt, ok := parseTimestamp(node["createdAt"]); ok {
			reaction.CreatedAt = &createdAt
		}
		reactions = append(reactions, reaction)
	}
	hasNextPage, _ := nestedBool(resp, "data", "repository", "pullRequest", "reactions", "pageInfo", "hasNextPage")
	endCursor, _ := nestedString(resp, "data", "repository", "pullRequest", "reactions", "pageInfo", "endCursor")
	return GitHubReactionPage{
		Reactions:   reactions,
		HasNextPage: hasNextPage,
		EndCursor:   endCursor,
	}, nil
}

func (c *goGitHubClient) ReplyToReviewThread(ctx context.Context, threadID, body string) error {
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

func (c *goGitHubClient) ResolveReviewThread(ctx context.Context, threadID string) error {
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

func (c *goGitHubClient) graphQL(ctx context.Context, query string, vars map[string]any) (map[string]any, error) {
	req, err := c.client.NewRequest("POST", "graphql", map[string]any{
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

func gitHubPullRequestFromAPI(pr *githubapi.PullRequest) *GitHubPullRequest {
	if pr == nil {
		return nil
	}

	state := strings.ToUpper(strings.TrimSpace(pr.GetState()))
	if pr.GetMerged() || pr.GetMergedAt().Time.After(time.Time{}) {
		state = "MERGED"
	}

	return &GitHubPullRequest{
		Number:      pr.GetNumber(),
		URL:         pr.GetHTMLURL(),
		State:       state,
		Body:        pr.GetBody(),
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

func gitHubHTTPClientWithTimeout(httpClient *http.Client) *http.Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clone := *httpClient
	if clone.Timeout <= 0 {
		clone.Timeout = defaultGitHubHTTPTimeout
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

func isGitHubNotFound(err error) bool {
	var responseErr *githubapi.ErrorResponse
	return errors.As(err, &responseErr) && responseErr.Response != nil && responseErr.Response.StatusCode == http.StatusNotFound
}
