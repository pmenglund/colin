package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	"github.com/pmenglund/colin/internal/repohost/builtin"
	"github.com/pmenglund/colin/internal/tracker"
)

func init() {
	builtin.Register()
}

func mustTestRepoAdapter(t *testing.T) repohost.Adapter {
	t.Helper()

	adapter, err := repohost.Lookup("github")
	if err != nil {
		t.Fatalf("repohost.Lookup() error = %v", err)
	}
	return adapter
}

type fakeAttachmentAdapter struct {
	kind repohost.HostKind
}

func (a fakeAttachmentAdapter) Kind() repohost.HostKind { return a.kind }
func (a fakeAttachmentAdapter) DisplayName() string     { return "Attachment Test" }
func (a fakeAttachmentAdapter) CurrentToken() string    { return "" }
func (a fakeAttachmentAdapter) IsValidToken(string) bool {
	return true
}
func (a fakeAttachmentAdapter) RecommendedEnvVar() string    { return "ATTACHMENT_TEST_TOKEN" }
func (a fakeAttachmentAdapter) ValidateTokenMessage() string { return "" }
func (a fakeAttachmentAdapter) ParseRepositoryURL(raw string) (repohost.Repository, error) {
	return repohost.Repository{}, repohost.ErrUnsupportedRepositoryURL
}
func (a fakeAttachmentAdapter) ParsePullRequestURL(raw string) (string, string, int, bool) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "attachment://"))
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return "", "", 0, false
	}
	prParts := strings.SplitN(parts[1], "#", 2)
	if len(prParts) != 2 {
		return "", "", 0, false
	}
	number, err := strconv.Atoi(strings.TrimSpace(prParts[1]))
	if err != nil || number <= 0 {
		return "", "", 0, false
	}
	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSpace(prParts[0])
	return owner, repo, number, true
}
func (a fakeAttachmentAdapter) RenderSetupInstructions(repohost.Repository, string) string { return "" }
func (a fakeAttachmentAdapter) NewClient(domain.ServiceConfig, *slog.Logger) (repohost.Client, error) {
	return nil, nil
}

func TestNewDoesNotValidateWorkflowStates(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("New() unexpectedly issued request to %s", r.URL.Path)
	}))
	defer server.Close()

	client, err := New(testLinearClientConfig(server.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client == nil {
		t.Fatal("New() returned nil client")
	}
	if requests != 0 {
		t.Fatalf("request count = %d, want 0", requests)
	}
	if got := client.WatchedProjectIDs(); len(got) != 0 {
		t.Fatalf("WatchedProjectIDs() = %v, want no validated projects before explicit validation", got)
	}
}

func TestValidateWorkflowStatesPopulatesWatchedProjects(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		slug, _ := request.Variables["slug"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"projects": map[string]any{
					"nodes": []map[string]any{
						{
							"id": slug + "-id",
							"teams": map[string]any{
								"nodes": []map[string]any{
									{
										"id":   "team-1",
										"name": "Product",
										"states": map[string]any{
											"nodes": []map[string]any{
												{"name": "Todo"},
												{"name": "In Progress"},
												{"name": "Review"},
												{"name": "Merge"},
												{"name": "Done"},
												{"name": "Refine"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	cfg := testLinearClientConfig(server.URL)
	cfg.Targets = []domain.TargetConfig{
		{ProjectSlug: "project-1"},
		{ProjectSlug: "project-2"},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.ValidateWorkflowStates(context.Background(), cfg); err != nil {
		t.Fatalf("ValidateWorkflowStates() error = %v", err)
	}

	got := client.WatchedProjectIDs()
	if len(got) != 2 || got[0] != "project-1-id" || got[1] != "project-2-id" {
		t.Fatalf("WatchedProjectIDs() = %v, want [project-1-id project-2-id]", got)
	}
}

func TestValidateWorkflowStatesFailsWhenWorkflowStateMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"projects": map[string]any{
					"nodes": []map[string]any{
						{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{
									{
										"id":   "team-1",
										"name": "Product",
										"states": map[string]any{
											"nodes": []map[string]any{
												{"name": "Todo"},
												{"name": "In Progress"},
												{"name": "Review"},
												{"name": "Merge"},
												{"name": "Done"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	cfg := testLinearClientConfig(server.URL)
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = client.ValidateWorkflowStates(context.Background(), cfg)
	if !errors.Is(err, ErrMissingWorkflowState) {
		t.Fatalf("ValidateWorkflowStates() error = %v, want ErrMissingWorkflowState", err)
	}
	if !strings.Contains(err.Error(), "Refine") {
		t.Fatalf("ValidateWorkflowStates() error = %q, want missing Refine state", err)
	}
}

func TestValidateWorkflowStatesHonorsCancellation(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	cfg := testLinearClientConfig(server.URL)
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = client.ValidateWorkflowStates(ctx, cfg)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrAPIRequest) {
		t.Fatalf("ValidateWorkflowStates() error = %v, want ErrAPIRequest", err)
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("ValidateWorkflowStates() error = %v, want context deadline exceeded", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("ValidateWorkflowStates() took %s, want under 1s", elapsed)
	}

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("validation request did not reach server")
	}
}

func testLinearClientConfig(endpoint string) domain.ServiceConfig {
	return domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			Endpoint:       endpoint,
			APIKey:         "token",
			ProjectSlug:    "project-1",
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Done"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
		Codex: domain.CodexConfig{
			Command: "codex app-server",
		},
	}
}

func TestFindIssueByCodexThreadID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(request.Query, "IssuesByCodexThreadID") {
			t.Fatalf("unexpected query: %s", request.Query)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						testLinearIssueNode("issue-1", "COLIN-1", "thread-1"),
						testLinearIssueNode("issue-2", "COLIN-2", "thread-2"),
					},
				},
			},
		})
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "token")
	client.watchedProjectIDs = []string{"project-1"}

	issue, err := client.FindIssueByCodexThreadID(context.Background(), "thread-2")
	if err != nil {
		t.Fatalf("FindIssueByCodexThreadID() error = %v", err)
	}
	if got := issue.ID; got != "issue-2" {
		t.Fatalf("issue.ID = %q, want %q", got, "issue-2")
	}
	if issue.ColinMetadata == nil || issue.ColinMetadata.CodexThreadID != "thread-2" {
		t.Fatalf("issue.ColinMetadata = %#v, want thread-2", issue.ColinMetadata)
	}
}

func TestFindIssueByCodexThreadIDPaginates(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		requestCount++

		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(request.Query, "IssuesByCodexThreadID") {
			t.Fatalf("unexpected query: %s", request.Query)
		}

		switch requestCount {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"pageInfo": map[string]any{
							"hasNextPage": true,
							"endCursor":   "cursor-1",
						},
						"nodes": []map[string]any{
							testLinearIssueNode("issue-1", "COLIN-1", "thread-1"),
						},
					},
				},
			})
		case 2:
			if got, _ := request.Variables["after"].(string); got != "cursor-1" {
				t.Fatalf("after = %q, want %q", got, "cursor-1")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"pageInfo": map[string]any{
							"hasNextPage": false,
							"endCursor":   nil,
						},
						"nodes": []map[string]any{
							testLinearIssueNode("issue-2", "COLIN-2", "thread-2"),
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "token")
	client.watchedProjectIDs = []string{"project-1"}

	issue, err := client.FindIssueByCodexThreadID(context.Background(), "thread-2")
	if err != nil {
		t.Fatalf("FindIssueByCodexThreadID() error = %v", err)
	}
	if got := issue.Identifier; got != "COLIN-2" {
		t.Fatalf("issue.Identifier = %q, want %q", got, "COLIN-2")
	}
}

func TestFindIssueByCodexThreadIDReturnsNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						testLinearIssueNode("issue-1", "COLIN-1", "thread-1"),
					},
				},
			},
		})
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "token")
	client.watchedProjectIDs = []string{"project-1"}

	_, err := client.FindIssueByCodexThreadID(context.Background(), "thread-2")
	if !errors.Is(err, ErrCodexThreadNotFound) {
		t.Fatalf("FindIssueByCodexThreadID() error = %v, want ErrCodexThreadNotFound", err)
	}
}

func TestFindIssueByCodexThreadIDReturnsAmbiguousMatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						testLinearIssueNode("issue-1", "COLIN-1", "thread-1"),
						testLinearIssueNode("issue-2", "COLIN-2", "thread-1"),
					},
				},
			},
		})
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "token")
	client.watchedProjectIDs = []string{"project-1"}

	_, err := client.FindIssueByCodexThreadID(context.Background(), "thread-1")
	var ambiguousErr *AmbiguousCodexThreadError
	if !errors.As(err, &ambiguousErr) {
		t.Fatalf("FindIssueByCodexThreadID() error = %v, want AmbiguousCodexThreadError", err)
	}
	if got := strings.Join(ambiguousErr.IssueIdentifiers, ","); got != "COLIN-1,COLIN-2" {
		t.Fatalf("IssueIdentifiers = %q, want %q", got, "COLIN-1,COLIN-2")
	}
}

func TestFindIssueByIdentifier(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						testLinearIssueNode("issue-1", "COLIN-1", "thread-1"),
						testLinearIssueNode("issue-2", "COLIN-2", "thread-2"),
					},
				},
			},
		})
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "token")
	client.watchedProjectIDs = []string{"project-1"}

	issue, err := client.FindIssueByIdentifier(context.Background(), "colin-2")
	if err != nil {
		t.Fatalf("FindIssueByIdentifier() error = %v", err)
	}
	if got := issue.Identifier; got != "COLIN-2" {
		t.Fatalf("issue.Identifier = %q, want %q", got, "COLIN-2")
	}
}

func TestFindIssueByIdentifierReturnsNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						testLinearIssueNode("issue-1", "COLIN-1", "thread-1"),
					},
				},
			},
		})
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "token")
	client.watchedProjectIDs = []string{"project-1"}

	_, err := client.FindIssueByIdentifier(context.Background(), "COLIN-2")
	if !errors.Is(err, ErrIssueIdentifierNotFound) {
		t.Fatalf("FindIssueByIdentifier() error = %v, want ErrIssueIdentifierNotFound", err)
	}
}

func TestListProjectsPaginatesAndSortsByName(t *testing.T) {
	t.Parallel()

	workspaceProjectRequests := 0
	teamProjectRequests := 0
	teamProjectPageRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		variables := request.Variables
		after, _ := variables["after"].(string)
		switch {
		case strings.Contains(request.Query, "query ProjectList"):
			workspaceProjectRequests++
			switch workspaceProjectRequests {
			case 1:
				if after != "" {
					t.Fatalf("after = %q on first workspace request, want empty", after)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{
						"projects": map[string]any{
							"pageInfo": map[string]any{
								"hasNextPage": true,
								"endCursor":   "cursor-1",
							},
							"nodes": []map[string]any{
								{
									"name":   "Zulu",
									"slugId": "zulu",
									"teams": map[string]any{
										"nodes": []map[string]any{{"name": "Product"}},
									},
								},
							},
						},
					},
				})
			case 2:
				if after != "cursor-1" {
					t.Fatalf("after = %q on second workspace request, want cursor-1", after)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{
						"projects": map[string]any{
							"pageInfo": map[string]any{
								"hasNextPage": false,
								"endCursor":   nil,
							},
							"nodes": []map[string]any{
								{
									"name":   "Alpha",
									"slugId": "alpha",
									"teams": map[string]any{
										"nodes": []map[string]any{{"name": "Platform"}, {"name": "Infra"}},
									},
								},
							},
						},
					},
				})
			default:
				t.Fatalf("unexpected workspace project request count: %d", workspaceProjectRequests)
			}
		case strings.Contains(request.Query, "query TeamProjectList"):
			teamProjectRequests++
			if teamProjectRequests != 1 {
				t.Fatalf("unexpected team project request count: %d", teamProjectRequests)
			}
			if after != "" {
				t.Fatalf("after = %q on team request, want empty", after)
			}
			if strings.Contains(request.Query, "projects(") {
				t.Fatalf("TeamProjectList query = %q, want cheap team-only query", request.Query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"teams": map[string]any{
						"pageInfo": map[string]any{
							"hasNextPage": false,
							"endCursor":   nil,
						},
						"nodes": []map[string]any{
							{
								"id":   "team-1",
								"name": "Platform",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "query TeamProjectPage"):
			teamProjectPageRequests++
			if !strings.Contains(request.Query, "includeSubTeams: true") {
				t.Fatalf("TeamProjectPage query = %q, want includeSubTeams", request.Query)
			}
			if got, _ := variables["teamID"].(string); got != "team-1" {
				t.Fatalf("teamID = %q, want team-1", got)
			}
			switch teamProjectPageRequests {
			case 1:
				if after != "" {
					t.Fatalf("after = %q on first team project page request, want empty", after)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{
						"team": map[string]any{
							"projects": map[string]any{
								"pageInfo": map[string]any{
									"hasNextPage": true,
									"endCursor":   "team-project-cursor-1",
								},
								"nodes": []map[string]any{
									{
										"name":   "Alpha",
										"slugId": "alpha",
										"teams": map[string]any{
											"nodes": []map[string]any{{"name": "Platform"}, {"name": "Subteam"}},
										},
									},
									{
										"name":   "Beta",
										"slugId": "beta",
										"teams": map[string]any{
											"nodes": []map[string]any{{"name": "Subteam"}},
										},
									},
								},
							},
						},
					},
				})
			case 2:
				if after != "team-project-cursor-1" {
					t.Fatalf("after = %q on second team project page request, want team-project-cursor-1", after)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{
						"team": map[string]any{
							"projects": map[string]any{
								"pageInfo": map[string]any{
									"hasNextPage": false,
									"endCursor":   nil,
								},
								"nodes": []map[string]any{
									{
										"name":   "Gamma",
										"slugId": "gamma",
										"teams": map[string]any{
											"nodes": []map[string]any{{"name": "Subteam"}},
										},
									},
								},
							},
						},
					},
				})
			default:
				t.Fatalf("unexpected team project page request count: %d", teamProjectPageRequests)
			}
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	projects, err := ListProjects(context.Background(), server.URL, "token")
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if workspaceProjectRequests != 2 {
		t.Fatalf("workspaceProjectRequests = %d, want 2", workspaceProjectRequests)
	}
	if teamProjectRequests != 1 {
		t.Fatalf("teamProjectRequests = %d, want 1", teamProjectRequests)
	}
	if teamProjectPageRequests != 2 {
		t.Fatalf("teamProjectPageRequests = %d, want 2", teamProjectPageRequests)
	}
	if len(projects) != 4 {
		t.Fatalf("len(projects) = %d, want 4", len(projects))
	}
	if projects[0].Slug != "alpha" || projects[1].Slug != "beta" || projects[2].Slug != "gamma" || projects[3].Slug != "zulu" {
		t.Fatalf("projects = %#v, want alphabetical order by name", projects)
	}
	if got := projects[0].TeamNames; len(got) != 3 || got[0] != "Platform" || got[1] != "Infra" || got[2] != "Subteam" {
		t.Fatalf("projects[0].TeamNames = %#v, want [Platform Infra Subteam]", got)
	}
}

func TestListProjectsSplitsTeamProjectLookupToAvoidComplexityLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		switch {
		case strings.Contains(request.Query, "query ProjectList"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"pageInfo": map[string]any{
							"hasNextPage": false,
							"endCursor":   nil,
						},
						"nodes": []map[string]any{},
					},
				},
			})
		case strings.Contains(request.Query, "query TeamProjectList"):
			if strings.Contains(request.Query, "projects(") {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"errors": []map[string]any{
						{
							"message": "Query too complex",
							"extensions": map[string]any{
								"code": "INPUT_ERROR",
							},
						},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"teams": map[string]any{
						"pageInfo": map[string]any{
							"hasNextPage": false,
							"endCursor":   nil,
						},
						"nodes": []map[string]any{
							{
								"id":   "team-1",
								"name": "Parent",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "query TeamProjectPage"):
			if !strings.Contains(request.Query, "includeSubTeams: true") {
				t.Fatalf("TeamProjectPage query = %q, want includeSubTeams", request.Query)
			}
			if got, _ := request.Variables["teamID"].(string); got != "team-1" {
				t.Fatalf("teamID = %q, want team-1", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"team": map[string]any{
						"projects": map[string]any{
							"pageInfo": map[string]any{
								"hasNextPage": false,
								"endCursor":   nil,
							},
							"nodes": []map[string]any{
								{
									"name":   "Sub-team Project",
									"slugId": "sub-team-project",
									"teams": map[string]any{
										"nodes": []map[string]any{{"name": "Subteam"}},
									},
								},
							},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	projects, err := ListProjects(context.Background(), server.URL, "token")
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("len(projects) = %d, want 1", len(projects))
	}
	if got := projects[0]; got.Name != "Sub-team Project" || got.Slug != "sub-team-project" {
		t.Fatalf("projects[0] = %#v, want sub-team project", got)
	}
	if got := projects[0].TeamNames; len(got) != 1 || got[0] != "Subteam" {
		t.Fatalf("projects[0].TeamNames = %#v, want [Subteam]", got)
	}
}

func testLinearIssueNode(issueID, identifier, threadID string) map[string]any {
	return map[string]any{
		"id":         issueID,
		"identifier": identifier,
		"title":      identifier + " title",
		"project": map[string]any{
			"id":     "project-1",
			"slugId": "project-1",
		},
		"state": map[string]any{
			"name": "Todo",
		},
		"attachments": map[string]any{
			"nodes": []map[string]any{
				{
					"id":        "attachment-" + issueID,
					"title":     "Colin metadata",
					"url":       "https://colin.invalid/linear/issues/" + issueID + "/metadata",
					"createdAt": "2026-04-03T12:00:00Z",
					"updatedAt": "2026-04-03T12:00:00Z",
					"metadata": map[string]any{
						"codex_thread_id": threadID,
					},
				},
			},
		},
	}
}

func TestNewAllowsTerminalStateAliasesWhenOneExists(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"projects": map[string]any{
					"nodes": []map[string]any{
						{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{
									{
										"id":   "team-1",
										"name": "Product",
										"states": map[string]any{
											"nodes": []map[string]any{
												{"name": "Todo"},
												{"name": "In Progress"},
												{"name": "Review"},
												{"name": "Merge"},
												{"name": "Canceled"},
												{"name": "Refine"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client, err := New(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			Endpoint:       server.URL,
			APIKey:         "token",
			ProjectSlug:    "project-1",
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Cancelled", "Canceled"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
		Codex: domain.CodexConfig{
			Command: "codex app-server",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client == nil {
		t.Fatal("New() returned nil client")
	}
}

func TestCreateIssueComment(t *testing.T) {
	t.Parallel()

	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		input, _ := request.Variables["input"].(map[string]any)
		gotBody, _ = input["body"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{
					"success": true,
					"comment": map[string]any{
						"id": "comment-1",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	commentID, err := client.CreateIssueComment(context.Background(), "issue-1", "hello world")
	if err != nil {
		t.Fatalf("CreateIssueComment() error = %v", err)
	}
	if commentID != "comment-1" {
		t.Fatalf("commentID = %q, want %q", commentID, "comment-1")
	}
	if gotBody != "hello world" {
		t.Fatalf("body = %q, want %q", gotBody, "hello world")
	}
}

func TestActorIdentityReadsViewerAppState(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(request.Query, "ViewerIdentity") {
			t.Fatalf("unexpected query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"viewer": map[string]any{
					"id":                    "app-user-1",
					"name":                  "Colin",
					"displayName":           "Colin Bot",
					"app":                   true,
					"supportsAgentSessions": true,
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	identity, err := client.ActorIdentity(context.Background())
	if err != nil {
		t.Fatalf("ActorIdentity() error = %v", err)
	}
	if identity.ID != "app-user-1" {
		t.Fatalf("identity.ID = %q, want %q", identity.ID, "app-user-1")
	}
	if identity.Name != "Colin Bot" {
		t.Fatalf("identity.Name = %q, want %q", identity.Name, "Colin Bot")
	}
	if !identity.IsApp {
		t.Fatal("identity.IsApp = false, want true")
	}
	if !identity.SupportsAgentSessions {
		t.Fatal("identity.SupportsAgentSessions = false, want true")
	}
}

func TestCreateCommentReply(t *testing.T) {
	t.Parallel()

	var gotParentID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		input, _ := request.Variables["input"].(map[string]any)
		gotParentID, _ = input["parentId"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{
					"success": true,
					"comment": map[string]any{
						"id": "comment-2",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	commentID, err := client.CreateCommentReply(context.Background(), "issue-1", "comment-1", "reply")
	if err != nil {
		t.Fatalf("CreateCommentReply() error = %v", err)
	}
	if commentID != "comment-2" {
		t.Fatalf("commentID = %q, want %q", commentID, "comment-2")
	}
	if gotParentID != "comment-1" {
		t.Fatalf("parentId = %q, want %q", gotParentID, "comment-1")
	}
}

func TestCreateAgentActivityThought(t *testing.T) {
	t.Parallel()

	var (
		gotSessionID   string
		gotContentType string
		gotBody        string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		input, _ := request.Variables["input"].(map[string]any)
		gotSessionID, _ = input["agentSessionId"].(string)
		content, _ := input["content"].(map[string]any)
		gotContentType, _ = content["type"].(string)
		gotBody, _ = content["body"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"agentActivityCreate": map[string]any{
					"success": true,
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	if err := client.CreateAgentActivityThought(context.Background(), "session-1", "Colin is assigned and will start work."); err != nil {
		t.Fatalf("CreateAgentActivityThought() error = %v", err)
	}
	if gotSessionID != "session-1" {
		t.Fatalf("agentSessionId = %q, want %q", gotSessionID, "session-1")
	}
	if gotContentType != "thought" {
		t.Fatalf("content.type = %q, want %q", gotContentType, "thought")
	}
	if gotBody != "Colin is assigned and will start work." {
		t.Fatalf("content.body = %q, want %q", gotBody, "Colin is assigned and will start work.")
	}
}

func TestEnsureProjectIssueWebhookCreatesMissingWebhook(t *testing.T) {
	t.Parallel()

	var createdInput map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectTeamInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"nodes": []map[string]any{{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{{
									"id":   "team-1",
									"name": "Colin",
								}},
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "OrganizationWebhooks"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhooks": map[string]any{
						"nodes": []map[string]any{},
					},
				},
			})
		case strings.Contains(request.Query, "CreateWebhook"):
			createdInput, _ = request.Variables["input"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookCreate": map[string]any{
						"success": true,
						"webhook": map[string]any{
							"id":      "webhook-1",
							"enabled": true,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	result, err := client.EnsureProjectIssueWebhook(context.Background(), "https://hooks.colin.example.test/webhooks/linear", "colin")
	if err != nil {
		t.Fatalf("EnsureProjectIssueWebhook() error = %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("Action = %q, want %q", result.Action, "created")
	}
	if got, _ := createdInput["url"].(string); got != "https://hooks.colin.example.test/webhooks/linear" {
		t.Fatalf("create input url = %q", got)
	}
	if got, _ := createdInput["teamId"].(string); got != "team-1" {
		t.Fatalf("create input teamId = %q", got)
	}
	if got, _ := createdInput["label"].(string); got != "colin" {
		t.Fatalf("create input label = %q", got)
	}
	resourceTypes, _ := createdInput["resourceTypes"].([]any)
	if len(resourceTypes) != 2 || resourceTypes[0] != linearWebhookResourceTypeIssue || resourceTypes[1] != linearWebhookResourceTypeIssueLabel {
		t.Fatalf("resourceTypes = %#v, want [%q %q]", resourceTypes, linearWebhookResourceTypeIssue, linearWebhookResourceTypeIssueLabel)
	}
}

func TestEnsureProjectIssueWebhookLeavesMatchingWebhookUnchanged(t *testing.T) {
	t.Parallel()

	createCalls := 0
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectTeamInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"nodes": []map[string]any{{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{{
									"id":   "team-1",
									"name": "Colin",
								}},
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "OrganizationWebhooks"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhooks": map[string]any{
						"nodes": []map[string]any{{
							"id":            "webhook-1",
							"label":         "colin",
							"url":           "https://hooks.colin.example.test/webhooks/linear",
							"enabled":       true,
							"resourceTypes": []string{linearWebhookResourceTypeIssue, linearWebhookResourceTypeIssueLabel},
							"team": map[string]any{
								"id":   "team-1",
								"name": "Colin",
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "CreateWebhook"):
			createCalls++
			t.Fatalf("CreateWebhook should not be called")
		case strings.Contains(request.Query, "DeleteWebhook"):
			deleteCalls++
			t.Fatalf("DeleteWebhook should not be called")
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	result, err := client.EnsureProjectIssueWebhook(context.Background(), "https://hooks.colin.example.test/webhooks/linear", "colin")
	if err != nil {
		t.Fatalf("EnsureProjectIssueWebhook() error = %v", err)
	}
	if result.Action != "unchanged" {
		t.Fatalf("Action = %q, want %q", result.Action, "unchanged")
	}
	if createCalls != 0 || deleteCalls != 0 {
		t.Fatalf("createCalls=%d deleteCalls=%d, want 0", createCalls, deleteCalls)
	}
}

func TestEnsureProjectIssueWebhookReplacesManagedWebhookMissingIssueLabelSubscription(t *testing.T) {
	t.Parallel()

	deletedIDs := []string{}
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectTeamInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"nodes": []map[string]any{{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{{
									"id":   "team-1",
									"name": "Colin",
								}},
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "OrganizationWebhooks"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhooks": map[string]any{
						"nodes": []map[string]any{{
							"id":            "webhook-old",
							"label":         "colin",
							"url":           "https://hooks.colin.example.test/webhooks/linear",
							"enabled":       true,
							"resourceTypes": []string{linearWebhookResourceTypeIssue},
							"team": map[string]any{
								"id":   "team-1",
								"name": "Colin",
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "DeleteWebhook"):
			id, _ := request.Variables["id"].(string)
			deletedIDs = append(deletedIDs, id)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookDelete": map[string]any{
						"success": true,
					},
				},
			})
		case strings.Contains(request.Query, "CreateWebhook"):
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookCreate": map[string]any{
						"success": true,
						"webhook": map[string]any{
							"id":      "webhook-new",
							"enabled": true,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	result, err := client.EnsureProjectIssueWebhook(context.Background(), "https://hooks.colin.example.test/webhooks/linear", "colin")
	if err != nil {
		t.Fatalf("EnsureProjectIssueWebhook() error = %v", err)
	}
	if result.Action != "replaced" {
		t.Fatalf("Action = %q, want %q", result.Action, "replaced")
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "webhook-old" {
		t.Fatalf("deletedIDs = %#v, want [webhook-old]", deletedIDs)
	}
}

func TestEnsureProjectIssueWebhookReplacesManagedWebhookAtOldURL(t *testing.T) {
	t.Parallel()

	deletedIDs := []string{}
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectTeamInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"nodes": []map[string]any{{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{{
									"id":   "team-1",
									"name": "Colin",
								}},
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "OrganizationWebhooks"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhooks": map[string]any{
						"nodes": []map[string]any{{
							"id":      "webhook-old",
							"label":   "colin",
							"url":     "https://old.colin.example.test/webhooks/linear",
							"enabled": true,
							"team": map[string]any{
								"id":   "team-1",
								"name": "Colin",
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "DeleteWebhook"):
			id, _ := request.Variables["id"].(string)
			deletedIDs = append(deletedIDs, id)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookDelete": map[string]any{
						"success": true,
					},
				},
			})
		case strings.Contains(request.Query, "CreateWebhook"):
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookCreate": map[string]any{
						"success": true,
						"webhook": map[string]any{
							"id":      "webhook-new",
							"enabled": true,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	result, err := client.EnsureProjectIssueWebhook(context.Background(), "https://hooks.colin.example.test/webhooks/linear", "colin")
	if err != nil {
		t.Fatalf("EnsureProjectIssueWebhook() error = %v", err)
	}
	if result.Action != "replaced" {
		t.Fatalf("Action = %q, want %q", result.Action, "replaced")
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "webhook-old" {
		t.Fatalf("deletedIDs = %#v, want [webhook-old]", deletedIDs)
	}
}

func TestUpsertIssueMetadata(t *testing.T) {
	t.Parallel()

	var (
		queryCount  int
		createCount int
		gotIssueID  string
		gotTitle    string
		gotURL      string
		gotMetadata map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueMetadataAttachments"):
			queryCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueMetadata"):
			createCount++
			input, _ := request.Variables["input"].(map[string]any)
			gotIssueID, _ = input["issueId"].(string)
			gotTitle, _ = input["title"].(string)
			gotURL, _ = input["url"].(string)
			gotMetadata, _ = input["metadata"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentCreate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       "attachment-1",
							"title":    gotTitle,
							"url":      gotURL,
							"metadata": gotMetadata,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	now := time.Date(2026, 3, 29, 17, 0, 0, 0, time.UTC)
	metadata, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{
		CodexThreadID:           "thread-1",
		ProgressRootCommentID:   "comment-root-1",
		ColinCommentIDs:         []string{"comment-root-1", "reply-1"},
		DelegationAckKind:       "ready",
		DelegationAckState:      "Todo",
		DelegationAckSessionID:  "session-1",
		ActualBranchName:        "colin-94",
		ExecPlanDecision:        domain.ExecPlanDecisionOneShot,
		ReviewPublishDirective:  domain.ReviewPublishDirectiveSkip,
		LastRunType:             "coding",
		LastOutcome:             "needs_spec",
		LastSummaryCommentID:    "comment-1",
		PullRequestNumber:       11,
		PullRequestURL:          "https://github.com/pmenglund/colin/pull/11",
		PullRequestState:        "OPEN",
		PullRequestHeadRef:      "pmenglund/colin-94",
		PullRequestBaseRef:      "main",
		LoopFailureFingerprint:  "review_publish\nReview\nno commits",
		LoopFailureCount:        2,
		PausedAt:                &now,
		PausedRunType:           "review_publish",
		PausedState:             "Review",
		PausedReason:            "no commits between main and branch",
		SlackChannelID:          "C12345678",
		SlackMessageTS:          "1743270000.123456",
		SlackPermalink:          "https://example.slack.com/archives/C12345678/p1743270000123456",
		SlackSummaryFingerprint: "fp-1",
		UpdatedAt:               &now,
	})
	if err != nil {
		t.Fatalf("UpsertIssueMetadata() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("queryCount = %d, want 1", queryCount)
	}
	if createCount != 1 {
		t.Fatalf("createCount = %d, want 1", createCount)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("issueId = %q, want %q", gotIssueID, "issue-1")
	}
	if gotTitle != "Colin metadata" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin metadata")
	}
	if gotURL != "https://colin.example.test/root/linear/issues/issue-1/metadata" {
		t.Fatalf("url = %q, want %q", gotURL, "https://colin.example.test/root/linear/issues/issue-1/metadata")
	}
	if gotMetadata["review_publish_directive"] != "skip" {
		t.Fatalf("review_publish_directive = %v, want skip", gotMetadata["review_publish_directive"])
	}
	if gotMetadata["codex_thread_id"] != "thread-1" {
		t.Fatalf("codex_thread_id = %v, want thread-1", gotMetadata["codex_thread_id"])
	}
	if gotMetadata["progress_root_comment_id"] != "comment-root-1" {
		t.Fatalf("progress_root_comment_id = %v, want comment-root-1", gotMetadata["progress_root_comment_id"])
	}
	if gotMetadata["delegation_ack_kind"] != "ready" {
		t.Fatalf("delegation_ack_kind = %v, want ready", gotMetadata["delegation_ack_kind"])
	}
	if gotMetadata["delegation_ack_state"] != "Todo" {
		t.Fatalf("delegation_ack_state = %v, want Todo", gotMetadata["delegation_ack_state"])
	}
	if gotMetadata["delegation_ack_session_id"] != "session-1" {
		t.Fatalf("delegation_ack_session_id = %v, want session-1", gotMetadata["delegation_ack_session_id"])
	}
	if got, ok := gotMetadata["colin_comment_ids"].([]any); !ok || len(got) != 2 || got[0] != "comment-root-1" || got[1] != "reply-1" {
		t.Fatalf("colin_comment_ids = %#v, want root/reply ids", gotMetadata["colin_comment_ids"])
	}
	if gotMetadata["exec_plan_decision"] != string(domain.ExecPlanDecisionOneShot) {
		t.Fatalf("exec_plan_decision = %v, want %q", gotMetadata["exec_plan_decision"], domain.ExecPlanDecisionOneShot)
	}
	if gotMetadata["pull_request_number"] != float64(11) && gotMetadata["pull_request_number"] != 11 {
		t.Fatalf("pull_request_number = %v, want 11", gotMetadata["pull_request_number"])
	}
	if gotMetadata["pull_request_url"] != "https://github.com/pmenglund/colin/pull/11" {
		t.Fatalf("pull_request_url = %v, want GitHub PR URL", gotMetadata["pull_request_url"])
	}
	if gotMetadata["pull_request_head_ref"] != "pmenglund/colin-94" {
		t.Fatalf("pull_request_head_ref = %v, want pmenglund/colin-94", gotMetadata["pull_request_head_ref"])
	}
	if gotMetadata["pull_request_base_ref"] != "main" {
		t.Fatalf("pull_request_base_ref = %v, want main", gotMetadata["pull_request_base_ref"])
	}
	if gotMetadata["loop_failure_fingerprint"] != "review_publish\nReview\nno commits" {
		t.Fatalf("loop_failure_fingerprint = %v", gotMetadata["loop_failure_fingerprint"])
	}
	if gotMetadata["loop_failure_count"] != float64(2) && gotMetadata["loop_failure_count"] != 2 {
		t.Fatalf("loop_failure_count = %v", gotMetadata["loop_failure_count"])
	}
	if gotMetadata["paused_run_type"] != "review_publish" {
		t.Fatalf("paused_run_type = %v", gotMetadata["paused_run_type"])
	}
	if gotMetadata["paused_state"] != "Review" {
		t.Fatalf("paused_state = %v", gotMetadata["paused_state"])
	}
	if gotMetadata["paused_reason"] != "no commits between main and branch" {
		t.Fatalf("paused_reason = %v", gotMetadata["paused_reason"])
	}
	if gotMetadata["slack_channel_id"] != "C12345678" {
		t.Fatalf("slack_channel_id = %v, want C12345678", gotMetadata["slack_channel_id"])
	}
	if gotMetadata["slack_message_ts"] != "1743270000.123456" {
		t.Fatalf("slack_message_ts = %v, want 1743270000.123456", gotMetadata["slack_message_ts"])
	}
	if gotMetadata["slack_permalink"] != "https://example.slack.com/archives/C12345678/p1743270000123456" {
		t.Fatalf("slack_permalink = %v, want permalink", gotMetadata["slack_permalink"])
	}
	if gotMetadata["slack_summary_fingerprint"] != "fp-1" {
		t.Fatalf("slack_summary_fingerprint = %v, want fp-1", gotMetadata["slack_summary_fingerprint"])
	}
	if gotMetadata["actual_branch_name"] != "colin-94" {
		t.Fatalf("actual_branch_name = %v, want colin-94", gotMetadata["actual_branch_name"])
	}
	if metadata.AttachmentID != "attachment-1" {
		t.Fatalf("metadata.AttachmentID = %q, want %q", metadata.AttachmentID, "attachment-1")
	}
	if metadata.URL != "https://colin.example.test/root/linear/issues/issue-1/metadata" {
		t.Fatalf("metadata.URL = %q, want metadata attachment URL", metadata.URL)
	}
	if metadata.CodexThreadID != "thread-1" {
		t.Fatalf("metadata.CodexThreadID = %q, want thread-1", metadata.CodexThreadID)
	}
	if metadata.ProgressRootCommentID != "comment-root-1" {
		t.Fatalf("metadata.ProgressRootCommentID = %q, want comment-root-1", metadata.ProgressRootCommentID)
	}
	if metadata.DelegationAckKind != "ready" {
		t.Fatalf("metadata.DelegationAckKind = %q, want ready", metadata.DelegationAckKind)
	}
	if metadata.DelegationAckState != "Todo" {
		t.Fatalf("metadata.DelegationAckState = %q, want Todo", metadata.DelegationAckState)
	}
	if metadata.DelegationAckSessionID != "session-1" {
		t.Fatalf("metadata.DelegationAckSessionID = %q, want session-1", metadata.DelegationAckSessionID)
	}
	if metadata.ActualBranchName != "colin-94" {
		t.Fatalf("metadata.ActualBranchName = %q, want %q", metadata.ActualBranchName, "colin-94")
	}
	if metadata.ExecPlanDecision != domain.ExecPlanDecisionOneShot {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", metadata.ExecPlanDecision, domain.ExecPlanDecisionOneShot)
	}
	if metadata.PullRequestNumber != 11 {
		t.Fatalf("metadata.PullRequestNumber = %d, want 11", metadata.PullRequestNumber)
	}
	if metadata.PullRequestHeadRef != "pmenglund/colin-94" {
		t.Fatalf("metadata.PullRequestHeadRef = %q, want %q", metadata.PullRequestHeadRef, "pmenglund/colin-94")
	}
	if metadata.LoopFailureCount != 2 {
		t.Fatalf("metadata.LoopFailureCount = %d, want 2", metadata.LoopFailureCount)
	}
	if metadata.SlackPermalink != "https://example.slack.com/archives/C12345678/p1743270000123456" {
		t.Fatalf("metadata.SlackPermalink = %q, want permalink", metadata.SlackPermalink)
	}
}

func TestUpsertIssueMetadataRecoversFromDuplicateURLCreateError(t *testing.T) {
	t.Parallel()

	var (
		queryCount      int
		createCount     int
		updateCount     int
		gotAttachmentID string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueMetadataAttachments"):
			queryCount++
			nodes := []map[string]any{}
			if queryCount > 1 {
				nodes = []map[string]any{
					{
						"id":        "attachment-existing",
						"title":     "Legacy Colin metadata",
						"url":       "https://colin.example.test/root/linear/issues/issue-1/metadata",
						"createdAt": "2026-03-29T17:00:00Z",
						"updatedAt": "2026-03-29T17:01:00Z",
						"metadata":  map[string]any{},
					},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": nodes,
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueMetadata"):
			createCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{
					{
						"message": "duplicate url",
						"path":    []any{"attachmentCreate"},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpdateIssueMetadata"):
			updateCount++
			gotAttachmentID, _ = request.Variables["id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentUpdate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       gotAttachmentID,
							"title":    "Colin metadata",
							"url":      "https://colin.example.test/root/linear/issues/issue-1/metadata",
							"metadata": map[string]any{"slack_message_ts": "1743270000.444444"},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	metadata, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{
		SlackMessageTS: "1743270000.444444",
	})
	if err != nil {
		t.Fatalf("UpsertIssueMetadata() error = %v", err)
	}
	if queryCount != 2 {
		t.Fatalf("queryCount = %d, want 2", queryCount)
	}
	if createCount != 1 {
		t.Fatalf("createCount = %d, want 1", createCount)
	}
	if updateCount != 1 {
		t.Fatalf("updateCount = %d, want 1", updateCount)
	}
	if gotAttachmentID != "attachment-existing" {
		t.Fatalf("attachment id = %q, want %q", gotAttachmentID, "attachment-existing")
	}
	if metadata.AttachmentID != "attachment-existing" {
		t.Fatalf("metadata.AttachmentID = %q, want %q", metadata.AttachmentID, "attachment-existing")
	}
}

func TestUpsertIssueMetadataUsesDynamicPublicURLResolver(t *testing.T) {
	t.Parallel()

	var (
		queryCount  int
		createCount int
		gotURL      string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueMetadataAttachments"):
			queryCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueMetadata"):
			createCount++
			input, _ := request.Variables["input"].(map[string]any)
			gotURL, _ = input["url"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentCreate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       "attachment-1",
							"title":    "Colin metadata",
							"url":      gotURL,
							"metadata": map[string]any{},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "http://127.0.0.1:8888",
		client:    &http.Client{Timeout: 5 * time.Second},
	}
	client.SetUIBaseURLResolver(func(context.Context) string {
		return "https://colin.tail.example.ts.net"
	})

	if _, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{}); err != nil {
		t.Fatalf("UpsertIssueMetadata() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("queryCount = %d, want 1", queryCount)
	}
	if createCount != 1 {
		t.Fatalf("createCount = %d, want 1", createCount)
	}
	if gotURL != "https://colin.tail.example.ts.net/linear/issues/issue-1/metadata" {
		t.Fatalf("url = %q", gotURL)
	}
}

func TestUpsertIssueMetadataUpdatesExistingAttachment(t *testing.T) {
	t.Parallel()

	var (
		queryCount      int
		updateCount     int
		gotAttachmentID string
		gotTitle        string
		gotMetadata     map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueMetadataAttachments"):
			queryCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{
								{
									"id":        "attachment-7",
									"title":     "Colin metadata",
									"url":       "https://colin.example.test/root/linear/issues/issue-1/metadata",
									"createdAt": "2026-03-29T17:00:00Z",
									"updatedAt": "2026-03-29T17:10:00Z",
									"metadata": map[string]any{
										"slack_message_ts": "1743270000.123456",
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpdateIssueMetadata"):
			updateCount++
			gotAttachmentID, _ = request.Variables["id"].(string)
			input, _ := request.Variables["input"].(map[string]any)
			gotTitle, _ = input["title"].(string)
			if _, ok := input["url"]; ok {
				t.Fatal("attachment update input unexpectedly included url")
			}
			gotMetadata, _ = input["metadata"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentUpdate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       gotAttachmentID,
							"title":    gotTitle,
							"url":      "https://colin.example.test/root/linear/issues/issue-1/metadata",
							"metadata": gotMetadata,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	metadata, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{
		SlackMessageTS: "1743270001.654321",
	})
	if err != nil {
		t.Fatalf("UpsertIssueMetadata() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("queryCount = %d, want 1", queryCount)
	}
	if updateCount != 1 {
		t.Fatalf("updateCount = %d, want 1", updateCount)
	}
	if gotAttachmentID != "attachment-7" {
		t.Fatalf("attachment id = %q, want %q", gotAttachmentID, "attachment-7")
	}
	if gotTitle != "Colin metadata" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin metadata")
	}
	if gotMetadata["slack_message_ts"] != "1743270001.654321" {
		t.Fatalf("slack_message_ts = %v, want updated timestamp", gotMetadata["slack_message_ts"])
	}
	if metadata.AttachmentID != "attachment-7" {
		t.Fatalf("metadata.AttachmentID = %q, want %q", metadata.AttachmentID, "attachment-7")
	}
}

func TestUpsertIssueMetadataUpdatesNewestDuplicateAttachment(t *testing.T) {
	t.Parallel()

	var (
		createCount      int
		gotAttachmentID  string
		updateCount      int
		expectedUpdateID = "attachment-new"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueMetadataAttachments"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{
								{
									"id":        "attachment-old",
									"title":     "Colin metadata",
									"url":       "https://colin.example.test/root/linear/issues/issue-1/metadata",
									"createdAt": "2026-03-29T17:00:00Z",
									"updatedAt": "2026-03-29T17:01:00Z",
									"metadata": map[string]any{
										"slack_message_ts": "1743270000.111111",
									},
								},
								{
									"id":        expectedUpdateID,
									"title":     "Colin metadata",
									"url":       "https://colin.example.test/root/linear/issues/issue-1/metadata",
									"createdAt": "2026-03-29T17:02:00Z",
									"updatedAt": "2026-03-29T17:03:00Z",
									"metadata": map[string]any{
										"slack_message_ts": "1743270000.222222",
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpdateIssueMetadata"):
			updateCount++
			gotAttachmentID, _ = request.Variables["id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentUpdate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       gotAttachmentID,
							"title":    "Colin metadata",
							"url":      "https://colin.example.test/root/linear/issues/issue-1/metadata",
							"metadata": map[string]any{"slack_message_ts": "1743270000.333333"},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueMetadata"):
			createCount++
			t.Fatalf("unexpected attachmentCreate mutation when duplicate metadata attachments already exist")
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	metadata, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{
		SlackMessageTS: "1743270000.333333",
	})
	if err != nil {
		t.Fatalf("UpsertIssueMetadata() error = %v", err)
	}
	if updateCount != 1 {
		t.Fatalf("updateCount = %d, want 1", updateCount)
	}
	if createCount != 0 {
		t.Fatalf("createCount = %d, want 0", createCount)
	}
	if gotAttachmentID != expectedUpdateID {
		t.Fatalf("attachment id = %q, want %q", gotAttachmentID, expectedUpdateID)
	}
	if metadata.AttachmentID != expectedUpdateID {
		t.Fatalf("metadata.AttachmentID = %q, want %q", metadata.AttachmentID, expectedUpdateID)
	}
}

func TestEnsureIssueLabelCreatesMissingLabel(t *testing.T) {
	t.Parallel()

	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		queries = append(queries, request.Query)

		switch {
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{},
					},
				},
			})
		case strings.Contains(request.Query, "mutation CreateIssueLabel"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabelCreate": map[string]any{
						"success": true,
						"issueLabel": map[string]any{
							"id":   "label-1",
							"name": "paused",
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.EnsureIssueLabel(context.Background(), domain.PausedIssueLabel); err != nil {
		t.Fatalf("EnsureIssueLabel() error = %v", err)
	}
	if got := client.labelIDs[domain.PausedIssueLabel]; got != "label-1" {
		t.Fatalf("cached label id = %q, want %q", got, "label-1")
	}
	if len(queries) != 2 {
		t.Fatalf("query count = %d, want 2", len(queries))
	}
}

func TestAddIssueLabelUsesExistingLabelID(t *testing.T) {
	t.Parallel()

	var gotIssueID string
	var gotLabelID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		switch {
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{
							{
								"id":   "label-1",
								"name": "paused",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation AddIssueLabel"):
			gotIssueID, _ = request.Variables["id"].(string)
			gotLabelID, _ = request.Variables["labelId"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueAddLabel": map[string]any{
						"success": true,
						"issue": map[string]any{
							"id": gotIssueID,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.AddIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel); err != nil {
		t.Fatalf("AddIssueLabel() error = %v", err)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("gotIssueID = %q, want %q", gotIssueID, "issue-1")
	}
	if gotLabelID != "label-1" {
		t.Fatalf("gotLabelID = %q, want %q", gotLabelID, "label-1")
	}
	if got := client.labelIDs[domain.PausedIssueLabel]; got != "label-1" {
		t.Fatalf("cached label id = %q, want %q", got, "label-1")
	}
}

func TestRemoveIssueLabelUsesExistingLabelID(t *testing.T) {
	t.Parallel()

	var gotIssueID string
	var gotLabelID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		switch {
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{
							{
								"id":   "label-1",
								"name": "paused",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation RemoveIssueLabel"):
			gotIssueID, _ = request.Variables["id"].(string)
			gotLabelID, _ = request.Variables["labelId"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueRemoveLabel": map[string]any{
						"success": true,
						"issue": map[string]any{
							"id": gotIssueID,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.RemoveIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel); err != nil {
		t.Fatalf("RemoveIssueLabel() error = %v", err)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("gotIssueID = %q, want %q", gotIssueID, "issue-1")
	}
	if gotLabelID != "label-1" {
		t.Fatalf("gotLabelID = %q, want %q", gotLabelID, "label-1")
	}
}

func TestRemoveIssueLabelNoopWhenLabelMissing(t *testing.T) {
	t.Parallel()

	queryCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		queryCount++
		if !strings.Contains(request.Query, "query IssueLabelsByName") {
			t.Fatalf("unexpected query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueLabels": map[string]any{
					"nodes": []map[string]any{},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.RemoveIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel); err != nil {
		t.Fatalf("RemoveIssueLabel() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("query count = %d, want 1", queryCount)
	}
}

func TestRemoveIssueLabelNoopWhenIssueAlreadyLacksLabel(t *testing.T) {
	t.Parallel()

	var mutationCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		switch {
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{
							{
								"id":   "label-1",
								"name": "paused",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation RemoveIssueLabel"):
			mutationCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{
					{
						"message": "Label not on issue",
						"path":    []any{"issueRemoveLabel"},
						"extensions": map[string]any{
							"code":                   "INPUT_ERROR",
							"userPresentableMessage": "Label label-1 is not on issue issue-1 and cannot be removed.",
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.RemoveIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel); err != nil {
		t.Fatalf("RemoveIssueLabel() error = %v, want nil", err)
	}
	if mutationCount != 1 {
		t.Fatalf("mutation count = %d, want 1", mutationCount)
	}
}

func TestRemoveIssueLabelReturnsMixedGraphQLErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		switch {
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{
							{
								"id":   "label-1",
								"name": "paused",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation RemoveIssueLabel"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{
					{
						"message": "Label not on issue",
						"path":    []any{"issueRemoveLabel"},
						"extensions": map[string]any{
							"code":                   "INPUT_ERROR",
							"userPresentableMessage": "Label label-1 is not on issue issue-1 and cannot be removed.",
						},
					},
					{
						"message": "backend unavailable",
						"path":    []any{"issueRemoveLabel"},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	err := client.RemoveIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel)
	if err == nil {
		t.Fatal("RemoveIssueLabel() error = nil, want graphQL error")
	}
	if !errors.Is(err, ErrGraphQLErrors) {
		t.Fatalf("RemoveIssueLabel() error = %v, want ErrGraphQLErrors", err)
	}
}

func TestUpsertIssueExecPlan(t *testing.T) {
	t.Parallel()

	var (
		queryCount  int
		createCount int
		gotIssueID  string
		gotTitle    string
		gotURL      string
		gotMetadata map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueExecPlans"):
			queryCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueExecPlan"):
			createCount++
			input, _ := request.Variables["input"].(map[string]any)
			gotIssueID, _ = input["issueId"].(string)
			gotTitle, _ = input["title"].(string)
			gotURL, _ = input["url"].(string)
			gotMetadata, _ = input["metadata"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentCreate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       "attachment-2",
							"title":    gotTitle,
							"url":      gotURL,
							"metadata": gotMetadata,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	now := time.Date(2026, 3, 29, 17, 5, 0, 0, time.UTC)
	plan, err := client.UpsertIssueExecPlan(context.Background(), "issue-1", domain.ExecPlan{
		Body:      "# Plan\n\nDetails.",
		UpdatedAt: &now,
	})
	if err != nil {
		t.Fatalf("UpsertIssueExecPlan() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("queryCount = %d, want 1", queryCount)
	}
	if createCount != 1 {
		t.Fatalf("createCount = %d, want 1", createCount)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("issueId = %q, want %q", gotIssueID, "issue-1")
	}
	if gotTitle != "Colin ExecPlan" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin ExecPlan")
	}
	if gotURL != "http://127.0.0.1/linear/issues/issue-1/exec-plan" {
		t.Fatalf("url = %q, want %q", gotURL, "http://127.0.0.1/linear/issues/issue-1/exec-plan")
	}
	if gotMetadata["body"] != "# Plan\n\nDetails." {
		t.Fatalf("body = %v, want plan body", gotMetadata["body"])
	}
	if plan.AttachmentID != "attachment-2" {
		t.Fatalf("plan.AttachmentID = %q, want %q", plan.AttachmentID, "attachment-2")
	}
}

func TestUpsertIssueExecPlanUpdatesExistingAttachment(t *testing.T) {
	t.Parallel()

	var (
		queryCount      int
		updateCount     int
		gotAttachmentID string
		gotTitle        string
		gotMetadata     map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueExecPlans"):
			queryCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{
								{
									"id":    "attachment-2",
									"title": "Colin ExecPlan",
									"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
									"metadata": map[string]any{
										"body": "# Old plan",
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpdateIssueExecPlan"):
			updateCount++
			gotAttachmentID, _ = request.Variables["id"].(string)
			input, _ := request.Variables["input"].(map[string]any)
			gotTitle, _ = input["title"].(string)
			if _, ok := input["url"]; ok {
				t.Fatal("attachment update input unexpectedly included url")
			}
			gotMetadata, _ = input["metadata"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentUpdate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       gotAttachmentID,
							"title":    gotTitle,
							"url":      "http://127.0.0.1/linear/issues/issue-1/exec-plan",
							"metadata": gotMetadata,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	plan, err := client.UpsertIssueExecPlan(context.Background(), "issue-1", domain.ExecPlan{
		Body:      "# Updated plan\n\n## Progress\n\n- [x] Done",
		UpdatedAt: &now,
	})
	if err != nil {
		t.Fatalf("UpsertIssueExecPlan() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("queryCount = %d, want 1", queryCount)
	}
	if updateCount != 1 {
		t.Fatalf("updateCount = %d, want 1", updateCount)
	}
	if gotAttachmentID != "attachment-2" {
		t.Fatalf("attachment id = %q, want %q", gotAttachmentID, "attachment-2")
	}
	if gotTitle != "Colin ExecPlan" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin ExecPlan")
	}
	if gotMetadata["body"] != "# Updated plan\n\n## Progress\n\n- [x] Done" {
		t.Fatalf("body = %v, want updated plan body", gotMetadata["body"])
	}
	if plan.AttachmentID != "attachment-2" {
		t.Fatalf("plan.AttachmentID = %q, want %q", plan.AttachmentID, "attachment-2")
	}
}

func TestUpsertIssueExecPlanRejectsDuplicates(t *testing.T) {
	t.Parallel()

	createCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "query IssueExecPlans"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{
								{
									"id":    "attachment-1",
									"title": "Colin ExecPlan",
									"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
									"metadata": map[string]any{
										"body": "# Plan A",
									},
								},
								{
									"id":    "attachment-2",
									"title": "Colin ExecPlan",
									"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
									"metadata": map[string]any{
										"body": "# Plan B",
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueExecPlan"):
			createCount++
			t.Fatalf("unexpected attachmentCreate mutation when duplicate exec plans already exist")
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	_, err := client.UpsertIssueExecPlan(context.Background(), "issue-1", domain.ExecPlan{Body: "# Plan\n\nDetails."})
	if !errors.Is(err, tracker.ErrDuplicateExecPlans) {
		t.Fatalf("UpsertIssueExecPlan() error = %v, want ErrDuplicateExecPlans", err)
	}
	if createCount != 0 {
		t.Fatalf("createCount = %d, want 0", createCount)
	}
}

func TestUpdateIssueState(t *testing.T) {
	t.Parallel()

	var gotIssueID string
	var gotStateID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		switch {
		case strings.Contains(request.Query, "IssueTeamStates"):
			gotIssueID, _ = request.Variables["id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"team": map[string]any{
							"states": map[string]any{
								"nodes": []map[string]any{
									{"id": "state-review", "name": "Review"},
									{"id": "state-merge", "name": "Merge"},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "UpdateIssueState"):
			gotIssueID, _ = request.Variables["id"].(string)
			gotStateID, _ = request.Variables["stateId"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{
						"success": true,
						"issue": map[string]any{
							"id":    "issue-1",
							"state": map[string]any{"name": "Review"},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	if err := client.UpdateIssueState(context.Background(), "issue-1", "Review"); err != nil {
		t.Fatalf("UpdateIssueState() error = %v", err)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("issueID = %q, want %q", gotIssueID, "issue-1")
	}
	if gotStateID != "state-review" {
		t.Fatalf("stateId = %q, want %q", gotStateID, "state-review")
	}
}

func TestUpdateIssueStateUnknownState(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"team": map[string]any{
						"states": map[string]any{
							"nodes": []map[string]any{{"id": "state-merge", "name": "Merge"}},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	err := client.UpdateIssueState(context.Background(), "issue-1", "Review")
	if !errors.Is(err, ErrUnknownState) {
		t.Fatalf("UpdateIssueState() error = %v, want ErrUnknownState", err)
	}
}

func TestCurrentRateLimitsCapturesRequestHeaders(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().UTC().Add(90 * time.Second).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Requests-Limit", "100")
		w.Header().Set("X-RateLimit-Requests-Remaining", "25")
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(resetAt.Unix(), 10))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes":    []map[string]any{},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		active:             []string{"Todo"},
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	if _, err := client.FetchCandidateIssueSnapshots(context.Background()); err != nil {
		t.Fatalf("FetchCandidateIssueSnapshots() error = %v", err)
	}

	limits := client.CurrentRateLimits()
	linearRequests, ok := limits["linear_requests"]
	if !ok {
		t.Fatalf("linear_requests missing from rate limits: %#v", limits)
	}
	if linearRequests.Limit == nil || *linearRequests.Limit != 100 {
		t.Fatalf("limit = %v, want 100", linearRequests.Limit)
	}
	if linearRequests.Remaining == nil || *linearRequests.Remaining != 25 {
		t.Fatalf("remaining = %v, want 25", linearRequests.Remaining)
	}
	if linearRequests.ResetsAt == nil || !linearRequests.ResetsAt.Equal(resetAt.UTC()) {
		t.Fatalf("resetsAt = %v, want %v", linearRequests.ResetsAt, resetAt.UTC())
	}
	if linearRequests.NextAllowedAt == nil {
		t.Fatalf("nextAllowedAt missing from rate limits: %#v", linearRequests)
	}
	if !linearRequests.NextAllowedAt.After(time.Now().UTC()) {
		t.Fatalf("nextAllowedAt = %v, want future timestamp", linearRequests.NextAllowedAt)
	}
}

func TestResolveGitAutomationStatePrefersBranchSpecificMatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"team": map[string]any{
						"gitAutomationStates": map[string]any{
							"nodes": []map[string]any{
								{
									"event": "merge",
									"state": map[string]any{"name": "Merged"},
								},
								{
									"event": "merge",
									"state": map[string]any{"name": "Deployed"},
									"targetBranch": map[string]any{
										"branchPattern": "main",
										"isRegex":       false,
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	stateName, ok, err := client.ResolveGitAutomationState(context.Background(), "issue-1", "merge", "main")
	if err != nil {
		t.Fatalf("ResolveGitAutomationState() error = %v", err)
	}
	if !ok {
		t.Fatal("ResolveGitAutomationState() ok = false, want true")
	}
	if stateName != "Deployed" {
		t.Fatalf("stateName = %q, want %q", stateName, "Deployed")
	}
}

func TestFetchCandidateIssueSnapshotsStayLightweight(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if strings.Contains(request.Query, "attachments(first: 50)") {
			t.Fatalf("snapshot query unexpectedly fetched attachments: %s", request.Query)
		}
		if strings.Contains(request.Query, "comments(first: 50)") {
			t.Fatalf("snapshot query unexpectedly fetched comments: %s", request.Query)
		}
		if strings.Contains(request.Query, "history(first: 100)") {
			t.Fatalf("snapshot query unexpectedly fetched history: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id":         "issue-1",
							"identifier": "COLIN-94",
							"title":      "Address review",
							"priority":   2,
							"project": map[string]any{
								"id":     "project-1",
								"slugId": "project-1",
							},
							"branchName": "colin-94",
							"url":        "https://linear.app/example/issue/COLIN-94",
							"createdAt":  "2026-03-28T18:00:00Z",
							"updatedAt":  "2026-03-28T19:00:00Z",
							"state":      map[string]any{"name": "Todo"},
							"labels":     map[string]any{"nodes": []map[string]any{{"name": "paused"}}},
							"inverseRelations": map[string]any{
								"nodes": []map[string]any{
									{
										"type": "blocks",
										"issue": map[string]any{
											"id":         "issue-2",
											"identifier": "COLIN-95",
											"state":      map[string]any{"name": "In Progress"},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		active:             []string{"Todo"},
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	issues, err := client.FetchCandidateIssueSnapshots(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssueSnapshots() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}
	if issues[0].ColinMetadata != nil {
		t.Fatalf("ColinMetadata = %#v, want nil on snapshot fetch", issues[0].ColinMetadata)
	}
	if issues[0].ExecPlan != nil {
		t.Fatalf("ExecPlan = %#v, want nil on snapshot fetch", issues[0].ExecPlan)
	}
	if len(issues[0].AttachedPullRequests) != 0 {
		t.Fatalf("AttachedPullRequests = %#v, want empty on snapshot fetch", issues[0].AttachedPullRequests)
	}
	if issues[0].ReviewCycle != nil {
		t.Fatalf("ReviewCycle = %#v, want nil on snapshot fetch", issues[0].ReviewCycle)
	}
	if len(issues[0].ReviewFeedback) != 0 {
		t.Fatalf("ReviewFeedback = %#v, want empty on snapshot fetch", issues[0].ReviewFeedback)
	}
	if issues[0].Priority == nil || *issues[0].Priority != 2 {
		t.Fatalf("Priority = %v, want 2", issues[0].Priority)
	}
	if len(issues[0].BlockedBy) != 1 || issues[0].BlockedBy[0].Identifier == nil || *issues[0].BlockedBy[0].Identifier != "COLIN-95" {
		t.Fatalf("BlockedBy = %#v, want COLIN-95 blocker", issues[0].BlockedBy)
	}
}

func TestFetchIssueByIDIncludesLatestHumanReviewFeedback(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 28, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(request.Query, "attachments(first: 50)") {
			t.Fatalf("detail query missing attachments fetch: %s", request.Query)
		}
		if !strings.Contains(request.Query, "comments(first: 50)") {
			t.Fatalf("detail query missing comments fetch: %s", request.Query)
		}
		if !strings.Contains(request.Query, "history(first: 100)") {
			t.Fatalf("detail query missing history fetch: %s", request.Query)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"id":         "issue-1",
					"identifier": "COLIN-94",
					"title":      "Address review",
					"state":      map[string]any{"name": "Todo"},
					"labels":     map[string]any{"nodes": []map[string]any{}},
					"inverseRelations": map[string]any{
						"nodes": []map[string]any{},
					},
					"attachments": map[string]any{
						"nodes": []map[string]any{
							{
								"id":    "attachment-1",
								"title": "Colin metadata",
								"url":   "https://colin.example.test/linear/issues/issue-1/metadata",
								"metadata": map[string]any{
									"colin_comment_ids": []any{"comment-colin"},
								},
							},
						},
					},
					"comments": map[string]any{
						"nodes": []map[string]any{
							{
								"id":        "comment-old",
								"body":      "Old review cycle feedback",
								"createdAt": base.Add(20 * time.Minute).Format(time.RFC3339),
								"children":  map[string]any{"nodes": []map[string]any{}},
							},
							{
								"id":        "comment-human",
								"body":      "Address the code review feedback.",
								"createdAt": base.Add(70 * time.Minute).Format(time.RFC3339),
								"children": map[string]any{
									"nodes": []map[string]any{
										{
											"id":        "reply-human",
											"body":      "Then mark the PR comment resolved.",
											"createdAt": base.Add(71 * time.Minute).Format(time.RFC3339),
											"parentId":  "comment-human",
										},
									},
								},
							},
							{
								"id":        "comment-colin",
								"body":      "Colin started work on this issue.",
								"createdAt": base.Add(72 * time.Minute).Format(time.RFC3339),
								"children":  map[string]any{"nodes": []map[string]any{}},
							},
							{
								"id":        "comment-after",
								"body":      "This was added after the issue moved back to Todo.",
								"createdAt": base.Add(95 * time.Minute).Format(time.RFC3339),
								"children":  map[string]any{"nodes": []map[string]any{}},
							},
						},
					},
					"history": map[string]any{
						"nodes": []map[string]any{
							{
								"createdAt": base.Add(10 * time.Minute).Format(time.RFC3339),
								"fromState": map[string]any{"name": "In Progress"},
								"toState":   map[string]any{"name": "Review"},
							},
							{
								"createdAt": base.Add(30 * time.Minute).Format(time.RFC3339),
								"fromState": map[string]any{"name": "Review"},
								"toState":   map[string]any{"name": "Todo"},
							},
							{
								"createdAt": base.Add(60 * time.Minute).Format(time.RFC3339),
								"fromState": map[string]any{"name": "In Progress"},
								"toState":   map[string]any{"name": "Review"},
							},
							{
								"createdAt": base.Add(90 * time.Minute).Format(time.RFC3339),
								"fromState": map[string]any{"name": "Review"},
								"toState":   map[string]any{"name": "Todo"},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		repoAdapter:        mustTestRepoAdapter(t),
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		active:             []string{"Review"},
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	issue, err := client.FetchIssueByID(context.Background(), "issue-1")
	if err != nil {
		t.Fatalf("FetchIssueByID() error = %v", err)
	}

	got := issue.ReviewFeedback
	if len(got) != 2 {
		t.Fatalf("review feedback length = %d, want 2", len(got))
	}
	if got[0].Body != "Address the code review feedback." {
		t.Fatalf("first review feedback = %q, want %q", got[0].Body, "Address the code review feedback.")
	}
	if got[1].Body != "Then mark the PR comment resolved." {
		t.Fatalf("second review feedback = %q, want %q", got[1].Body, "Then mark the PR comment resolved.")
	}
	if got[1].ParentID == nil || *got[1].ParentID != "comment-human" {
		t.Fatalf("reply parent id = %v, want %q", got[1].ParentID, "comment-human")
	}
}

func TestFetchIssueByIDDedupesRepliesReturnedAtMultipleLevels(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 28, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"id":         "issue-1",
					"identifier": "COLIN-94",
					"title":      "Address review",
					"state":      map[string]any{"name": "Todo"},
					"labels":     map[string]any{"nodes": []map[string]any{}},
					"inverseRelations": map[string]any{
						"nodes": []map[string]any{},
					},
					"attachments": map[string]any{
						"nodes": []map[string]any{},
					},
					"comments": map[string]any{
						"nodes": []map[string]any{
							{
								"id":        "comment-human",
								"body":      "Address the review feedback.",
								"createdAt": base.Add(10 * time.Minute).Format(time.RFC3339),
								"children": map[string]any{
									"nodes": []map[string]any{
										{
											"id":        "reply-human",
											"body":      "Mark the PR thread resolved.",
											"createdAt": base.Add(11 * time.Minute).Format(time.RFC3339),
											"parentId":  "comment-human",
										},
									},
								},
							},
							{
								"id":        "reply-human",
								"body":      "Mark the PR thread resolved.",
								"createdAt": base.Add(11 * time.Minute).Format(time.RFC3339),
								"parentId":  "comment-human",
								"children":  map[string]any{"nodes": []map[string]any{}},
							},
						},
					},
					"history": map[string]any{
						"nodes": []map[string]any{
							{
								"createdAt": base.Add(5 * time.Minute).Format(time.RFC3339),
								"fromState": map[string]any{"name": "In Progress"},
								"toState":   map[string]any{"name": "Review"},
							},
							{
								"createdAt": base.Add(20 * time.Minute).Format(time.RFC3339),
								"fromState": map[string]any{"name": "Review"},
								"toState":   map[string]any{"name": "Todo"},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		repoAdapter:        mustTestRepoAdapter(t),
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		active:             []string{"Review"},
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	issue, err := client.FetchIssueByID(context.Background(), "issue-1")
	if err != nil {
		t.Fatalf("FetchIssueByID() error = %v", err)
	}

	got := issue.ReviewFeedback
	if len(got) != 2 {
		t.Fatalf("review feedback length = %d, want 2", len(got))
	}
	if got[0].Body != "Address the review feedback." {
		t.Fatalf("first review feedback = %q, want %q", got[0].Body, "Address the review feedback.")
	}
	if got[1].Body != "Mark the PR thread resolved." {
		t.Fatalf("second review feedback = %q, want %q", got[1].Body, "Mark the PR thread resolved.")
	}
}

func TestFetchIssueSchedulingMetadataByIDsExtractsColinMetadataFromAttachment(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 29, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if strings.Contains(request.Query, "comments(first: 50)") || strings.Contains(request.Query, "history(first: 100)") {
			t.Fatalf("metadata query unexpectedly fetched comments/history: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id": "issue-1",
							"attachments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":    "attachment-1",
										"title": "Colin metadata",
										"url":   "https://colin.example.test/linear/issues/issue-1/metadata",
										"metadata": map[string]any{
											"codex_thread_id":           "thread-1",
											"progress_root_comment_id":  "comment-root-1",
											"colin_comment_ids":         []any{"comment-root-1", "reply-1"},
											"delegation_ack_kind":       "ready",
											"delegation_ack_state":      "Todo",
											"delegation_ack_session_id": "session-1",
											"actual_branch_name":        "colin-94",
											"exec_plan_decision":        "one_shot",
											"review_publish_directive":  "skip",
											"last_run_type":             "coding",
											"last_outcome":              "needs_spec",
											"last_summary_comment_id":   "comment-2",
											"loop_failure_fingerprint":  "review_publish\nReview\nno commits",
											"loop_failure_count":        3,
											"paused_at":                 base.Add(3 * time.Minute).Format(time.RFC3339),
											"paused_run_type":           "review_publish",
											"paused_state":              "Review",
											"paused_reason":             "no commits between main and branch",
											"slack_channel_id":          "C12345678",
											"slack_message_ts":          "1743270000.123456",
											"slack_permalink":           "https://example.slack.com/archives/C12345678/p1743270000123456",
											"slack_summary_fingerprint": "fp-1",
											"updated_at":                base.Add(2 * time.Minute).Format(time.RFC3339),
											"codex_output": []map[string]any{
												{
													"timestamp": base.Add(90 * time.Second).Format(time.RFC3339),
													"event":     "turn_completed",
													"message":   "Implemented the change.",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		repoAdapter:        mustTestRepoAdapter(t),
		endpoint:           server.URL,
		apiKey:             "token",
		primaryProjectSlug: "project-1",
		active:             []string{"Review"},
		client:             &http.Client{Timeout: 5 * time.Second},
	}

	metadataByIssueID, err := client.FetchIssueSchedulingMetadataByIDs(context.Background(), []string{"issue-1"})
	if err != nil {
		t.Fatalf("FetchIssueSchedulingMetadataByIDs() error = %v", err)
	}
	metadata, ok := metadataByIssueID["issue-1"]
	if !ok {
		t.Fatalf("metadataByIssueID = %#v, want issue-1 entry", metadataByIssueID)
	}
	if got := metadata.ColinCommentIDs; len(got) != 2 || got[0] != "comment-root-1" || got[1] != "reply-1" {
		t.Fatalf("ColinCommentIDs = %#v, want root/reply ids", got)
	}
	if metadata.URL != "https://colin.example.test/linear/issues/issue-1/metadata" {
		t.Fatalf("URL = %q, want metadata attachment URL", metadata.URL)
	}
	if metadata.ReviewPublishDirective != "skip" {
		t.Fatalf("ReviewPublishDirective = %q, want %q", metadata.ReviewPublishDirective, "skip")
	}
	if metadata.ExecPlanDecision != domain.ExecPlanDecisionOneShot {
		t.Fatalf("ExecPlanDecision = %q, want %q", metadata.ExecPlanDecision, domain.ExecPlanDecisionOneShot)
	}
	if got := len(metadata.CodexOutput); got != 1 {
		t.Fatalf("CodexOutput length = %d, want 1", got)
	}
	if got := metadata.CodexOutput[0].Message; got != "Implemented the change." {
		t.Fatalf("CodexOutput[0].Message = %q, want %q", got, "Implemented the change.")
	}
	if metadata.ActualBranchName != "colin-94" {
		t.Fatalf("ActualBranchName = %q, want %q", metadata.ActualBranchName, "colin-94")
	}
	if metadata.CodexThreadID != "thread-1" {
		t.Fatalf("CodexThreadID = %q, want thread-1", metadata.CodexThreadID)
	}
	if metadata.ProgressRootCommentID != "comment-root-1" {
		t.Fatalf("ProgressRootCommentID = %q, want comment-root-1", metadata.ProgressRootCommentID)
	}
	if metadata.DelegationAckKind != "ready" {
		t.Fatalf("DelegationAckKind = %q, want ready", metadata.DelegationAckKind)
	}
	if metadata.DelegationAckState != "Todo" {
		t.Fatalf("DelegationAckState = %q, want Todo", metadata.DelegationAckState)
	}
	if metadata.DelegationAckSessionID != "session-1" {
		t.Fatalf("DelegationAckSessionID = %q, want session-1", metadata.DelegationAckSessionID)
	}
	if metadata.LoopFailureCount != 3 {
		t.Fatalf("LoopFailureCount = %d, want 3", metadata.LoopFailureCount)
	}
	if metadata.PausedRunType != "review_publish" {
		t.Fatalf("PausedRunType = %q, want review_publish", metadata.PausedRunType)
	}
	if metadata.SlackPermalink != "https://example.slack.com/archives/C12345678/p1743270000123456" {
		t.Fatalf("SlackPermalink = %q, want permalink", metadata.SlackPermalink)
	}
}

func TestFetchIssueSchedulingMetadataByIDsPrefersNewestColinMetadataAttachment(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id": "issue-1",
							"attachments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":        "attachment-old",
										"title":     "Colin metadata",
										"url":       "https://colin.example.test/linear/issues/issue-1/metadata",
										"createdAt": "2026-04-03T15:32:00Z",
										"updatedAt": "2026-04-03T15:32:10Z",
										"metadata": map[string]any{
											"slack_channel_id": "C12345678",
										},
									},
									{
										"id":        "attachment-new",
										"title":     "Colin metadata",
										"url":       "https://colin.example.test/linear/issues/issue-1/metadata",
										"createdAt": "2026-04-03T15:33:00Z",
										"updatedAt": "2026-04-03T15:33:10Z",
										"metadata": map[string]any{
											"slack_channel_id":          "C12345678",
											"slack_message_ts":          "1743723180.123456",
											"slack_permalink":           "https://example.slack.com/archives/C12345678/p1743723180123456",
											"slack_summary_fingerprint": "fp-2",
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "token", client: &http.Client{Timeout: 5 * time.Second}}

	metadataByIssueID, err := client.FetchIssueSchedulingMetadataByIDs(context.Background(), []string{"issue-1"})
	if err != nil {
		t.Fatalf("FetchIssueSchedulingMetadataByIDs() error = %v", err)
	}
	metadata := metadataByIssueID["issue-1"]
	if metadata.AttachmentID != "attachment-new" {
		t.Fatalf("AttachmentID = %q, want newest metadata attachment", metadata.AttachmentID)
	}
	if metadata.SlackMessageTS != "1743723180.123456" {
		t.Fatalf("SlackMessageTS = %q, want newest metadata timestamp", metadata.SlackMessageTS)
	}
	if metadata.SlackSummaryFingerprint != "fp-2" {
		t.Fatalf("SlackSummaryFingerprint = %q, want newest fingerprint", metadata.SlackSummaryFingerprint)
	}
}

func TestFetchIssueSchedulingMetadataByIDsBackfillsSlackStateFromOlderDuplicateMetadataAttachment(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id": "issue-1",
							"attachments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":        "attachment-old",
										"title":     "Colin metadata",
										"url":       "https://colin.example.test/linear/issues/issue-1/metadata",
										"createdAt": "2026-04-03T15:32:00Z",
										"updatedAt": "2026-04-03T15:32:10Z",
										"metadata": map[string]any{
											"slack_channel_id":          "C12345678",
											"slack_message_ts":          "1743723180.123456",
											"slack_permalink":           "https://example.slack.com/archives/C12345678/p1743723180123456",
											"slack_summary_fingerprint": "fp-2",
										},
									},
									{
										"id":        "attachment-new",
										"title":     "Colin metadata",
										"url":       "https://colin.example.test/linear/issues/issue-1/metadata",
										"createdAt": "2026-04-03T15:33:00Z",
										"updatedAt": "2026-04-03T15:33:10Z",
										"metadata": map[string]any{
											"progress_root_comment_id": "comment-root-1",
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "token", client: &http.Client{Timeout: 5 * time.Second}}

	metadataByIssueID, err := client.FetchIssueSchedulingMetadataByIDs(context.Background(), []string{"issue-1"})
	if err != nil {
		t.Fatalf("FetchIssueSchedulingMetadataByIDs() error = %v", err)
	}
	metadata := metadataByIssueID["issue-1"]
	if metadata.AttachmentID != "attachment-new" {
		t.Fatalf("AttachmentID = %q, want newest metadata attachment", metadata.AttachmentID)
	}
	if metadata.ProgressRootCommentID != "comment-root-1" {
		t.Fatalf("ProgressRootCommentID = %q, want newest metadata field", metadata.ProgressRootCommentID)
	}
	if metadata.SlackMessageTS != "1743723180.123456" {
		t.Fatalf("SlackMessageTS = %q, want backfilled slack timestamp", metadata.SlackMessageTS)
	}
	if metadata.SlackPermalink != "https://example.slack.com/archives/C12345678/p1743723180123456" {
		t.Fatalf("SlackPermalink = %q, want backfilled slack permalink", metadata.SlackPermalink)
	}
}

func TestFetchIssueSchedulingMetadataByIDsBatchesBeyondFirst250Issues(t *testing.T) {
	t.Parallel()

	const totalIssues = 251
	requestSizes := make([]int, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		rawIDs, ok := request.Variables["ids"].([]any)
		if !ok {
			t.Fatalf("ids variable type = %T, want []any", request.Variables["ids"])
		}
		requestSizes = append(requestSizes, len(rawIDs))
		nodes := make([]map[string]any, 0, len(rawIDs))
		for _, rawID := range rawIDs {
			issueID, ok := rawID.(string)
			if !ok {
				t.Fatalf("issue id type = %T, want string", rawID)
			}
			nodes = append(nodes, map[string]any{
				"id": issueID,
				"attachments": map[string]any{
					"nodes": []map[string]any{
						{
							"id":    "attachment-" + issueID,
							"title": "Colin metadata",
							"url":   "https://colin.example.test/linear/issues/" + issueID + "/metadata",
							"metadata": map[string]any{
								"progress_root_comment_id": "comment-" + issueID,
							},
						},
					},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": nodes,
				},
			},
		})
	}))
	defer server.Close()

	issueIDs := make([]string, 0, totalIssues)
	for i := 0; i < totalIssues; i++ {
		issueIDs = append(issueIDs, fmt.Sprintf("issue-%03d", i))
	}

	client := &Client{endpoint: server.URL, apiKey: "token", client: &http.Client{Timeout: 5 * time.Second}}

	metadataByIssueID, err := client.FetchIssueSchedulingMetadataByIDs(context.Background(), issueIDs)
	if err != nil {
		t.Fatalf("FetchIssueSchedulingMetadataByIDs() error = %v", err)
	}
	if got := len(requestSizes); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	if requestSizes[0] != 250 || requestSizes[1] != 1 {
		t.Fatalf("request sizes = %#v, want [250 1]", requestSizes)
	}
	if got := len(metadataByIssueID); got != totalIssues {
		t.Fatalf("metadataByIssueID length = %d, want %d", got, totalIssues)
	}
	if metadataByIssueID["issue-250"].ProgressRootCommentID != "comment-issue-250" {
		t.Fatalf("ProgressRootCommentID for tail issue = %q, want %q", metadataByIssueID["issue-250"].ProgressRootCommentID, "comment-issue-250")
	}
}

func TestFetchIssueByIDExtractsAttachedPullRequests(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"id":         "issue-1",
					"identifier": "COLIN-112",
					"title":      "Prevent duplicate PRs",
					"state":      map[string]any{"name": "Review"},
					"labels":     map[string]any{"nodes": []map[string]any{}},
					"inverseRelations": map[string]any{
						"nodes": []map[string]any{},
					},
					"attachments": map[string]any{
						"nodes": []map[string]any{
							{
								"id":    "attachment-1",
								"title": "PR 11",
								"url":   "attachment://acme/widgets#11",
							},
							{
								"id":    "attachment-2",
								"title": "PR 11 in console repo",
								"url":   "attachment://acme/console#11",
							},
							{
								"id":    "attachment-3",
								"title": "Duplicate PR 11",
								"url":   "attachment://acme/widgets#11",
							},
							{
								"id":    "attachment-4",
								"title": "Metadata",
								"url":   "https://colin.example.test/root/linear/issues/issue-1/metadata",
							},
						},
					},
					"comments": map[string]any{"nodes": []map[string]any{}},
					"history":  map[string]any{"nodes": []map[string]any{}},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		repoAdapter: fakeAttachmentAdapter{kind: "attachmenttest"},
		endpoint:    server.URL,
		apiKey:      "token",
		client:      &http.Client{Timeout: 5 * time.Second},
	}

	issue, err := client.FetchIssueByID(context.Background(), "issue-1")
	if err != nil {
		t.Fatalf("FetchIssueByID() error = %v", err)
	}
	if len(issue.AttachedPullRequests) != 2 {
		t.Fatalf("AttachedPullRequests length = %d, want 2", len(issue.AttachedPullRequests))
	}
	if issue.AttachedPullRequests[0].Number != 11 {
		t.Fatalf("AttachedPullRequests[0].Number = %d, want 11", issue.AttachedPullRequests[0].Number)
	}
	if issue.AttachedPullRequests[0].Backend != "attachmenttest" {
		t.Fatalf("AttachedPullRequests[0].Backend = %q, want attachmenttest", issue.AttachedPullRequests[0].Backend)
	}
	if issue.AttachedPullRequests[0].RepositoryOwner != "acme" {
		t.Fatalf("AttachedPullRequests[0].RepositoryOwner = %q, want acme", issue.AttachedPullRequests[0].RepositoryOwner)
	}
	if issue.AttachedPullRequests[0].RepositoryName != "widgets" {
		t.Fatalf("AttachedPullRequests[0].RepositoryName = %q, want widgets", issue.AttachedPullRequests[0].RepositoryName)
	}
	if issue.AttachedPullRequests[1].Number != 11 {
		t.Fatalf("AttachedPullRequests[1].Number = %d, want 11", issue.AttachedPullRequests[1].Number)
	}
	if issue.AttachedPullRequests[1].Backend != "attachmenttest" {
		t.Fatalf("AttachedPullRequests[1].Backend = %q, want attachmenttest", issue.AttachedPullRequests[1].Backend)
	}
	if issue.AttachedPullRequests[1].RepositoryOwner != "acme" {
		t.Fatalf("AttachedPullRequests[1].RepositoryOwner = %q, want acme", issue.AttachedPullRequests[1].RepositoryOwner)
	}
	if issue.AttachedPullRequests[1].RepositoryName != "console" {
		t.Fatalf("AttachedPullRequests[1].RepositoryName = %q, want console", issue.AttachedPullRequests[1].RepositoryName)
	}
}

func TestNormalizeIssueParsesAttachedPullRequestsWithConfiguredBackend(t *testing.T) {
	t.Parallel()

	client := &Client{repoAdapter: fakeAttachmentAdapter{kind: "attachmenttest"}}
	issue, err := client.normalizeIssue(map[string]any{
		"id":         "issue-1",
		"identifier": "COLIN-177",
		"title":      "Normalize pull request attachments",
		"state":      map[string]any{"name": "Review"},
		"attachments": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":  "attachment-1",
					"url": "attachment://acme/widgets#17",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeIssue() error = %v", err)
	}
	if len(issue.AttachedPullRequests) != 1 {
		t.Fatalf("len(issue.AttachedPullRequests) = %d, want 1", len(issue.AttachedPullRequests))
	}
	got := issue.AttachedPullRequests[0]
	if got.Backend != "attachmenttest" {
		t.Fatalf("AttachedPullRequests[0].Backend = %q, want %q", got.Backend, "attachmenttest")
	}
	if got.RepositoryOwner != "acme" || got.RepositoryName != "widgets" || got.Number != 17 {
		t.Fatalf("AttachedPullRequests[0] = %+v, want acme/widgets#17", got)
	}
}

func TestNormalizeIssueMarksDelegationToCurrentAppActor(t *testing.T) {
	t.Parallel()

	client := &Client{
		appMode:       true,
		actorIdentity: &linearActorIdentity{ID: "app-user-1", Name: "Colin", IsApp: true},
	}
	issue, err := client.normalizeIssue(map[string]any{
		"id":         "issue-1",
		"identifier": "COLIN-188",
		"title":      "Linear app",
		"state":      map[string]any{"name": "Todo"},
		"project":    map[string]any{"id": "project-1", "slugId": "bothnia"},
		"delegate":   map[string]any{"id": "app-user-1"},
	})
	if err != nil {
		t.Fatalf("normalizeIssue() error = %v", err)
	}
	if !issue.DelegatedToColin {
		t.Fatal("issue.DelegatedToColin = false, want true")
	}
}

func TestNormalizeIssueFiltersAppAuthoredReviewFeedbackWithoutPrefix(t *testing.T) {
	t.Parallel()

	reviewStart := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	reviewEnd := reviewStart.Add(10 * time.Minute)
	client := &Client{
		appMode:       true,
		actorIdentity: &linearActorIdentity{ID: "app-user-1", Name: "Colin", IsApp: true},
	}
	issue, err := client.normalizeIssue(map[string]any{
		"id":         "issue-1",
		"identifier": "COLIN-188",
		"title":      "Linear app",
		"state":      map[string]any{"name": "Todo"},
		"project":    map[string]any{"id": "project-1", "slugId": "bothnia"},
		"comments": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":        "comment-app",
					"body":      "Colin started work on this issue.",
					"createdAt": reviewStart.Add(2 * time.Minute).Format(time.RFC3339),
					"user":      map[string]any{"id": "app-user-1", "app": true},
					"children":  map[string]any{"nodes": []any{}},
				},
				map[string]any{
					"id":        "comment-human",
					"body":      "Please also document the setup flow.",
					"createdAt": reviewStart.Add(3 * time.Minute).Format(time.RFC3339),
					"user":      map[string]any{"id": "user-1", "app": false},
					"children":  map[string]any{"nodes": []any{}},
				},
			},
		},
		"history": map[string]any{
			"nodes": []any{
				map[string]any{
					"createdAt": reviewStart.Add(-time.Minute).Format(time.RFC3339),
					"fromState": map[string]any{"name": "In Progress"},
					"toState":   map[string]any{"name": "Review"},
				},
				map[string]any{
					"createdAt": reviewEnd.Format(time.RFC3339),
					"fromState": map[string]any{"name": "Review"},
					"toState":   map[string]any{"name": "Todo"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeIssue() error = %v", err)
	}
	if len(issue.ReviewFeedback) != 1 {
		t.Fatalf("len(issue.ReviewFeedback) = %d, want 1", len(issue.ReviewFeedback))
	}
	if issue.ReviewFeedback[0].Body != "Please also document the setup flow." {
		t.Fatalf("ReviewFeedback[0].Body = %q, want human comment", issue.ReviewFeedback[0].Body)
	}
}

func TestNormalizeIssueDedupesAttachedPullRequestsByBackendAndRepository(t *testing.T) {
	t.Parallel()

	client := &Client{repoAdapter: fakeAttachmentAdapter{kind: "attachmenttest"}}
	issue, err := client.normalizeIssue(map[string]any{
		"id":         "issue-1",
		"identifier": "COLIN-177",
		"title":      "Keep repository-specific PR references distinct",
		"state":      map[string]any{"name": "Review"},
		"attachments": map[string]any{
			"nodes": []any{
				map[string]any{"id": "attachment-1", "url": "attachment://acme/widgets#17"},
				map[string]any{"id": "attachment-2", "url": "attachment://acme/widgets#17"},
				map[string]any{"id": "attachment-3", "url": "attachment://acme/console#17"},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeIssue() error = %v", err)
	}
	if len(issue.AttachedPullRequests) != 2 {
		t.Fatalf("len(issue.AttachedPullRequests) = %d, want 2", len(issue.AttachedPullRequests))
	}
	if issue.AttachedPullRequests[0].RepositoryName != "widgets" {
		t.Fatalf("AttachedPullRequests[0].RepositoryName = %q, want widgets", issue.AttachedPullRequests[0].RepositoryName)
	}
	if issue.AttachedPullRequests[1].RepositoryName != "console" {
		t.Fatalf("AttachedPullRequests[1].RepositoryName = %q, want console", issue.AttachedPullRequests[1].RepositoryName)
	}
}

func TestFetchIssueByIDExtractsExecPlanFromAttachment(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 29, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"id":         "issue-1",
					"identifier": "COLIN-108",
					"title":      "Add exec plans",
					"state":      map[string]any{"name": "In Progress"},
					"labels":     map[string]any{"nodes": []map[string]any{}},
					"inverseRelations": map[string]any{
						"nodes": []map[string]any{},
					},
					"attachments": map[string]any{
						"nodes": []map[string]any{
							{
								"id":    "attachment-2",
								"title": "Colin ExecPlan",
								"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
								"metadata": map[string]any{
									"body":       "# Plan\n\nDetails.",
									"updated_at": base.Format(time.RFC3339),
								},
							},
						},
					},
					"comments": map[string]any{
						"nodes": []map[string]any{},
					},
					"history": map[string]any{
						"nodes": []map[string]any{},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "token", client: &http.Client{Timeout: 5 * time.Second}}

	issue, err := client.FetchIssueByID(context.Background(), "issue-1")
	if err != nil {
		t.Fatalf("FetchIssueByID() error = %v", err)
	}
	if issue.ExecPlan == nil {
		t.Fatal("ExecPlan = nil, want plan")
	}
	if issue.ExecPlanCount != 1 {
		t.Fatalf("ExecPlanCount = %d, want 1", issue.ExecPlanCount)
	}
	if issue.ExecPlan.Body != "# Plan\n\nDetails." {
		t.Fatalf("ExecPlan.Body = %q, want plan body", issue.ExecPlan.Body)
	}
	if issue.ExecPlan.URL != "http://127.0.0.1/linear/issues/issue-1/exec-plan" {
		t.Fatalf("ExecPlan.URL = %q, want attachment URL", issue.ExecPlan.URL)
	}
}

func TestFetchIssueByIDRejectsDuplicateExecPlanAttachments(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"id":         "issue-1",
					"identifier": "COLIN-110",
					"title":      "Repair exec plan metadata",
					"state":      map[string]any{"name": "Todo"},
					"labels":     map[string]any{"nodes": []map[string]any{}},
					"inverseRelations": map[string]any{
						"nodes": []map[string]any{},
					},
					"attachments": map[string]any{
						"nodes": []map[string]any{
							{
								"id":    "attachment-1",
								"title": "Colin ExecPlan",
								"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
								"metadata": map[string]any{
									"body": "# Plan A",
								},
							},
							{
								"id":    "attachment-2",
								"title": "Colin ExecPlan",
								"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
								"metadata": map[string]any{
									"body": "# Plan B",
								},
							},
						},
					},
					"comments": map[string]any{
						"nodes": []map[string]any{},
					},
					"history": map[string]any{
						"nodes": []map[string]any{},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "token", client: &http.Client{Timeout: 5 * time.Second}}

	issue, err := client.FetchIssueByID(context.Background(), "issue-1")
	if err != nil {
		t.Fatalf("FetchIssueByID() error = %v", err)
	}
	if issue.ExecPlan != nil {
		t.Fatalf("ExecPlan = %#v, want nil when duplicates exist", issue.ExecPlan)
	}
	if issue.ExecPlanCount != 2 {
		t.Fatalf("ExecPlanCount = %d, want 2", issue.ExecPlanCount)
	}
}
