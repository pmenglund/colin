package repoops

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestGoGitHubClientPullRequestByHeadReturnsMergedState(t *testing.T) {
	t.Parallel()

	var listQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls":
			listQuery = r.URL.RawQuery
			writeJSON(t, w, []map[string]any{
				{
					"number":   7,
					"html_url": "https://github.com/acme/widgets/pull/7",
					"state":    "closed",
					"head":     map[string]any{"ref": "feature"},
					"base":     map[string]any{"ref": "main"},
				},
			})
		case "/repos/acme/widgets/pulls/7":
			writeJSON(t, w, map[string]any{
				"number":   7,
				"html_url": "https://github.com/acme/widgets/pull/7",
				"state":    "closed",
				"merged":   true,
				"body":     "body text",
				"head":     map[string]any{"ref": "feature"},
				"base":     map[string]any{"ref": "main"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestGoGitHubClient(t, server)
	pr, err := client.PullRequestByHead(context.Background(), "acme", "widgets", "feature", "main")
	if err != nil {
		t.Fatalf("PullRequestByHead() error = %v", err)
	}
	if pr == nil {
		t.Fatal("PullRequestByHead() = nil, want PR")
	}
	if pr.State != "MERGED" {
		t.Fatalf("pr.State = %q, want MERGED", pr.State)
	}
	if !strings.Contains(listQuery, "head=acme%3Afeature") || !strings.Contains(listQuery, "base=main") {
		t.Fatalf("list query = %q, want head and base filters", listQuery)
	}
}

func TestNewGitHubClientFromConfigAppliesDefaultHTTPTimeout(t *testing.T) {
	t.Parallel()

	client, err := NewGitHubClientFromConfig(domain.ServiceConfig{
		Repo: domain.RepoConfig{APIToken: "test-token"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewGitHubClientFromConfig() error = %v", err)
	}

	goClient, ok := client.(*goGitHubClient)
	if !ok {
		t.Fatalf("client type = %T, want *goGitHubClient", client)
	}
	if got := goClient.HTTPTimeout(); got != 2*time.Minute {
		t.Fatalf("http timeout = %s, want %s", got, 2*time.Minute)
	}
}

func TestGoGitHubClientValidateAuth(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		var gotAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/user" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			gotAuth = r.Header.Get("Authorization")
			writeJSON(t, w, map[string]any{"login": "colin-bot"})
		}))
		defer server.Close()

		client := newTestGoGitHubClient(t, server)
		if err := client.ValidateAuth(context.Background()); err != nil {
			t.Fatalf("ValidateAuth() error = %v", err)
		}
		if gotAuth != "Bearer test-token" {
			t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/user" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(t, w, map[string]any{"message": "Bad credentials"})
		}))
		defer server.Close()

		client := newTestGoGitHubClient(t, server)
		if err := client.ValidateAuth(context.Background()); err == nil {
			t.Fatal("ValidateAuth() error = nil, want unauthorized error")
		}
	})
}

func TestGoGitHubClientPullRequestByHeadSupportsForkQualifiedBranch(t *testing.T) {
	t.Parallel()

	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/pulls" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		if strings.Contains(r.URL.RawQuery, "head=forkuser%3Afeature") {
			writeJSON(t, w, []map[string]any{
				{
					"number":   8,
					"html_url": "https://github.com/acme/widgets/pull/8",
					"state":    "open",
					"head":     map[string]any{"ref": "feature"},
					"base":     map[string]any{"ref": "main"},
				},
			})
			return
		}
		writeJSON(t, w, []map[string]any{})
	}))
	defer server.Close()

	client := newTestGoGitHubClient(t, server)
	pr, err := client.PullRequestByHead(context.Background(), "acme", "widgets", "forkuser/feature", "main")
	if err != nil {
		t.Fatalf("PullRequestByHead() error = %v", err)
	}
	if pr == nil {
		t.Fatal("PullRequestByHead() = nil, want PR")
	}
	if len(queries) != 1 {
		t.Fatalf("request count = %d, want 1", len(queries))
	}
	if !strings.Contains(queries[0], "head=forkuser%3Afeature") {
		t.Fatalf("list query = %q, want fork-qualified head", queries[0])
	}
}

func TestGoGitHubClientCreateAndMergePullRequestUseREST(t *testing.T) {
	t.Parallel()

	var createBody string
	var mergeBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("ReadAll(create body) error = %v", err)
			}
			createBody = string(body)
			writeJSON(t, w, map[string]any{
				"number":   11,
				"html_url": "https://github.com/acme/widgets/pull/11",
				"state":    "open",
				"head":     map[string]any{"ref": "feature"},
				"base":     map[string]any{"ref": "main"},
			})
		case "/repos/acme/widgets/pulls/11/merge":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("ReadAll(merge body) error = %v", err)
			}
			mergeBody = string(body)
			writeJSON(t, w, map[string]any{"merged": true})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestGoGitHubClient(t, server)
	pr, err := client.CreatePullRequest(context.Background(), "acme", "widgets", CreatePullRequestInput{
		Title: "Title",
		Head:  "feature",
		Base:  "main",
		Body:  "Body",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest() error = %v", err)
	}
	if pr.Number != 11 {
		t.Fatalf("pr.Number = %d, want 11", pr.Number)
	}
	if err := client.MergePullRequest(context.Background(), "acme", "widgets", 11, "squash"); err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}
	if !strings.Contains(createBody, `"title":"Title"`) || !strings.Contains(createBody, `"head":"feature"`) || !strings.Contains(createBody, `"base":"main"`) {
		t.Fatalf("create body = %q, want title/head/base", createBody)
	}
	if !strings.Contains(mergeBody, `"merge_method":"squash"`) {
		t.Fatalf("merge body = %q, want merge_method squash", mergeBody)
	}
}

func TestGoGitHubClientGraphQLPages(t *testing.T) {
	t.Parallel()

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll(graphql body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		switch {
		case strings.Contains(string(body), "ReviewThreads"):
			writeJSON(t, w, map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"pullRequest": map[string]any{
							"reviewThreads": map[string]any{
								"nodes": []any{
									map[string]any{
										"id":               "thread-1",
										"isResolved":       false,
										"isOutdated":       false,
										"viewerCanReply":   true,
										"viewerCanResolve": true,
										"path":             "internal/foo.go",
										"line":             42,
										"startLine":        40,
										"comments": map[string]any{
											"nodes": []any{
												map[string]any{
													"id":        "comment-1",
													"body":      "Please fix this.",
													"url":       "https://example.test/comment/1",
													"createdAt": "2026-03-28T18:00:00Z",
													"author":    map[string]any{"login": "reviewer"},
												},
											},
											"pageInfo": map[string]any{
												"hasNextPage": false,
												"endCursor":   nil,
											},
										},
									},
								},
								"pageInfo": map[string]any{
									"hasNextPage": true,
									"endCursor":   "page-2",
								},
							},
						},
					},
				},
			})
		case strings.Contains(string(body), "ReviewThreadComments"):
			writeJSON(t, w, map[string]any{
				"data": map[string]any{
					"node": map[string]any{
						"comments": map[string]any{
							"nodes": []any{
								map[string]any{"author": map[string]any{"login": "chatgpt-codex-connector[bot]"}},
							},
							"pageInfo": map[string]any{
								"hasNextPage": false,
								"endCursor":   nil,
							},
						},
					},
				},
			})
		case strings.Contains(string(body), "PullRequestReactions"):
			writeJSON(t, w, map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"pullRequest": map[string]any{
							"reactions": map[string]any{
								"nodes": []any{
									map[string]any{
										"content":   "EYES",
										"createdAt": "2026-03-28T18:01:00Z",
										"user":      map[string]any{"login": "chatgpt-codex-connector[bot]"},
									},
								},
								"pageInfo": map[string]any{
									"hasNextPage": false,
									"endCursor":   nil,
								},
							},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected graphql body %s", string(body))
		}
	}))
	defer server.Close()

	client := newTestGoGitHubClient(t, server)
	threads, err := client.ReviewThreads(context.Background(), "acme", "widgets", 7, "")
	if err != nil {
		t.Fatalf("ReviewThreads() error = %v", err)
	}
	if len(threads.Threads) != 1 || !threads.HasNextPage || threads.EndCursor != "page-2" {
		t.Fatalf("ReviewThreads() = %+v, want one thread and pagination", threads)
	}
	comments, err := client.ReviewThreadComments(context.Background(), "thread-1", "page-2")
	if err != nil {
		t.Fatalf("ReviewThreadComments() error = %v", err)
	}
	if len(comments.Comments) != 1 {
		t.Fatalf("ReviewThreadComments() len = %d, want 1", len(comments.Comments))
	}
	reactions, err := client.PullRequestReactions(context.Background(), "acme", "widgets", 7, "")
	if err != nil {
		t.Fatalf("PullRequestReactions() error = %v", err)
	}
	if len(reactions.Reactions) != 1 || reactions.Reactions[0].Content != "EYES" {
		t.Fatalf("PullRequestReactions() = %+v, want one EYES reaction", reactions)
	}
	if len(requestBodies) != 3 {
		t.Fatalf("graphql request count = %d, want 3", len(requestBodies))
	}
}

func TestGoGitHubClientReplyAndResolveReviewThreadUseGraphQLMutations(t *testing.T) {
	t.Parallel()

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll(graphql body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		writeJSON(t, w, map[string]any{"data": map[string]any{}})
	}))
	defer server.Close()

	client := newTestGoGitHubClient(t, server)
	if err := client.ReplyToReviewThread(context.Background(), "thread-1", "[colin] Addressed."); err != nil {
		t.Fatalf("ReplyToReviewThread() error = %v", err)
	}
	if err := client.ResolveReviewThread(context.Background(), "thread-1"); err != nil {
		t.Fatalf("ResolveReviewThread() error = %v", err)
	}
	if len(requestBodies) != 2 {
		t.Fatalf("graphql request count = %d, want 2", len(requestBodies))
	}
	if !strings.Contains(requestBodies[0], "ReplyReviewThread") || !strings.Contains(requestBodies[0], "[colin] Addressed.") {
		t.Fatalf("reply body = %q, want reply mutation and body", requestBodies[0])
	}
	if !strings.Contains(requestBodies[1], "ResolveReviewThread") {
		t.Fatalf("resolve body = %q, want resolve mutation", requestBodies[1])
	}
}

func newTestGoGitHubClient(t *testing.T, server *httptest.Server) *goGitHubClient {
	t.Helper()

	client, err := newGoGitHubClient("test-token", server.Client(), server.URL+"/", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newGoGitHubClient() error = %v", err)
	}
	return client
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
}
