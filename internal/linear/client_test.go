package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/workflow"
)

func TestApplyMetadataPatch(t *testing.T) {
	metadata := map[string]string{"a": "1", "b": "2"}
	patched := applyMetadataPatch(metadata, MetadataPatch{
		Set:    map[string]string{"a": "10", "c": "3"},
		Delete: []string{"b"},
	})

	if patched["a"] != "10" {
		t.Fatalf("patched[a] = %q, want %q", patched["a"], "10")
	}
	if patched["c"] != "3" {
		t.Fatalf("patched[c] = %q, want %q", patched["c"], "3")
	}
	if _, ok := patched["b"]; ok {
		t.Fatalf("patched should delete key b: %#v", patched)
	}
	if metadata["a"] != "1" {
		t.Fatalf("input metadata mutated: %#v", metadata)
	}
}

func TestGetIssueReadsMetadataFromAttachment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query GetIssue"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "1",
						"identifier":  "COL-1",
						"title":       "x",
						"description": "spec",
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "Todo"},
					},
				},
			})
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{
						"nodes": []map[string]any{
							{
								"id":        "a-1",
								"updatedAt": "2026-02-11T00:00:00Z",
								"metadata":  map[string]any{"colin.merge_ready": "true"},
								"issue":     map[string]any{"id": "1"},
							},
						},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	issue, err := client.GetIssue(context.Background(), "1")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}
	if issue.Metadata["colin.merge_ready"] != "true" {
		t.Fatalf("Metadata[colin.merge_ready] = %q, want %q", issue.Metadata["colin.merge_ready"], "true")
	}
}

func TestGetIssueByIdentifierReadsMetadataFromAttachment(t *testing.T) {
	var requestedTeamKey string
	var requestedIdentifier string
	var issueLookupQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query GetIssueByIdentifier"):
			issueLookupQuery = req.Query
			requestedTeamKey = stringVariable(req.Variables, "teamKey")
			requestedIdentifier = stringVariable(req.Variables, "identifier")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{"id": "1"},
						},
					},
				},
			})
		case strings.Contains(req.Query, "query GetIssue"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "1",
						"identifier":  "COL-42",
						"title":       "x",
						"description": "spec",
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "Todo"},
					},
				},
			})
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{
						"nodes": []map[string]any{
							{
								"id":        "a-1",
								"updatedAt": "2026-02-11T00:00:00Z",
								"metadata":  map[string]any{"colin.branch_name": "colin/COL-42"},
								"issue":     map[string]any{"id": "1"},
							},
						},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "COLIN", srv.Client())
	issue, err := client.GetIssueByIdentifier(context.Background(), "COL-42")
	if err != nil {
		t.Fatalf("GetIssueByIdentifier() error = %v", err)
	}
	if requestedTeamKey != "COLIN" {
		t.Fatalf("teamKey variable = %q, want %q", requestedTeamKey, "COLIN")
	}
	if requestedIdentifier != "COL-42" {
		t.Fatalf("identifier variable = %q, want %q", requestedIdentifier, "COL-42")
	}
	if !strings.Contains(issueLookupQuery, "id: { eq: $identifier }") {
		t.Fatalf("issue lookup query missing id filter: %q", issueLookupQuery)
	}
	if strings.Contains(issueLookupQuery, "identifier: { eq: $identifier }") {
		t.Fatalf("issue lookup query should not filter by identifier field: %q", issueLookupQuery)
	}
	if issue.ID != "1" {
		t.Fatalf("issue ID = %q, want %q", issue.ID, "1")
	}
	if issue.Metadata["colin.branch_name"] != "colin/COL-42" {
		t.Fatalf("Metadata[colin.branch_name] = %q", issue.Metadata["colin.branch_name"])
	}
}

func TestGetIssueByIdentifierReturnsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if strings.Contains(req.Query, "query GetIssueByIdentifier") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{},
					},
				},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "COLIN", srv.Client())
	_, err := client.GetIssueByIdentifier(context.Background(), "COL-404")
	if err == nil {
		t.Fatal("expected not found error")
	}
	if got := err.Error(); got != "issue COL-404 not found" {
		t.Fatalf("error = %q", got)
	}
}

func TestListCandidateIssuesReturnsBlockedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query ListIssues"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{"id": "1", "identifier": "COL-1", "title": "todo-unblocked", "project": map[string]any{"id": "project-1", "name": "Alpha"}, "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Todo"}, "inverseRelations": map[string]any{"nodes": []map[string]any{}}},
							{"id": "2", "identifier": "COL-2", "title": "todo-blocked", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Todo"}, "inverseRelations": map[string]any{"nodes": []map[string]any{
								{"type": "blocks", "issue": map[string]any{"id": "dep-2", "state": map[string]any{"name": "In Progress"}}, "relatedIssue": map[string]any{"id": "2", "state": map[string]any{"name": "Todo"}}},
							}}},
							{"id": "3", "identifier": "COL-3", "title": "todo-unblocked", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Todo"}, "inverseRelations": map[string]any{"nodes": []map[string]any{
								{"type": "blocks", "issue": map[string]any{"id": "dep-3", "state": map[string]any{"name": "Done"}}, "relatedIssue": map[string]any{"id": "3", "state": map[string]any{"name": "Todo"}}},
							}}},
							{"id": "4", "identifier": "COL-4", "title": "inprogress-blocked", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "In Progress"}, "inverseRelations": map[string]any{"nodes": []map[string]any{
								{"type": "blocks", "issue": map[string]any{"id": "dep-4", "state": map[string]any{"name": "Todo"}}, "relatedIssue": map[string]any{"id": "4", "state": map[string]any{"name": "In Progress"}}},
							}}},
							{"id": "5", "identifier": "COL-5", "title": "done", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Done"}, "inverseRelations": map[string]any{"nodes": []map[string]any{}}},
						},
					},
				},
			})
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			nodes := []map[string]any{
				{
					"id":        "att-1",
					"updatedAt": "2026-02-11T00:00:00Z",
					"metadata":  map[string]any{"colin.thread_id": "thread-1"},
					"issue":     map[string]any{"id": "1"},
				},
				{
					"id":        "att-3",
					"updatedAt": "2026-02-11T00:00:00Z",
					"metadata":  map[string]any{"colin.thread_id": "thread-3"},
					"issue":     map[string]any{"id": "3"},
				},
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{"nodes": nodes},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	issues, err := client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}
	if len(issues) != 5 {
		t.Fatalf("expected 5 issues, got %d", len(issues))
	}
	if issues[0].Identifier != "COL-1" {
		t.Fatalf("unexpected first issue identifier: %q", issues[0].Identifier)
	}
	if issues[1].Identifier != "COL-2" {
		t.Fatalf("unexpected second issue identifier: %q", issues[1].Identifier)
	}
	if issues[2].Identifier != "COL-3" {
		t.Fatalf("unexpected third issue identifier: %q", issues[2].Identifier)
	}
	if issues[3].Identifier != "COL-4" {
		t.Fatalf("unexpected fourth issue identifier: %q", issues[3].Identifier)
	}
	if issues[4].Identifier != "COL-5" {
		t.Fatalf("unexpected fifth issue identifier: %q", issues[4].Identifier)
	}
	if issues[0].Metadata[workflow.MetaThreadID] != "thread-1" {
		t.Fatalf("issues[0] metadata thread id = %q", issues[0].Metadata[workflow.MetaThreadID])
	}
	if issues[2].Metadata[workflow.MetaThreadID] != "thread-3" {
		t.Fatalf("issues[2] metadata thread id = %q", issues[2].Metadata[workflow.MetaThreadID])
	}
	if issues[0].Blocked {
		t.Fatal("issues[0] blocked = true, want false")
	}
	if !issues[1].Blocked {
		t.Fatal("issues[1] blocked = false, want true")
	}
	if issues[2].Blocked {
		t.Fatal("issues[2] blocked = true, want false")
	}
	if !issues[3].Blocked {
		t.Fatal("issues[3] blocked = false, want true")
	}
	if issues[4].Blocked {
		t.Fatal("issues[4] blocked = true, want false")
	}
	if issues[0].ProjectID != "project-1" {
		t.Fatalf("issues[0] project id = %q, want %q", issues[0].ProjectID, "project-1")
	}
	if issues[0].ProjectName != "Alpha" {
		t.Fatalf("issues[0] project name = %q, want %q", issues[0].ProjectName, "Alpha")
	}
	if issues[2].ProjectID != "" {
		t.Fatalf("issues[2] project id = %q, want empty", issues[2].ProjectID)
	}
	if issues[2].ProjectName != "" {
		t.Fatalf("issues[2] project name = %q, want empty", issues[2].ProjectName)
	}
}

func TestListCandidateIssuesRejectsNonStringMetadataValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query ListIssues"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{
								"id":               "1",
								"identifier":       "COL-1",
								"title":            "todo",
								"description":      "x",
								"updatedAt":        "2026-02-11T00:00:00Z",
								"state":            map[string]any{"name": "Todo"},
								"inverseRelations": map[string]any{"nodes": []map[string]any{}},
							},
						},
					},
				},
			})
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{
						"nodes": []map[string]any{
							{
								"id":        "att-1",
								"updatedAt": "2026-02-11T00:00:00Z",
								"metadata":  map[string]any{"colin.thread_id": 42},
								"issue":     map[string]any{"id": "1"},
							},
						},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	_, err := client.ListCandidateIssues(context.Background(), "team")
	if err == nil {
		t.Fatal("expected metadata decoding error")
	}
	if !strings.Contains(err.Error(), `metadata key "colin.thread_id" must be a string`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestUpdateIssueMetadataDetectsConflict(t *testing.T) {
	var mutationCalled bool
	attachmentReads := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query GetIssue"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "1",
						"identifier":  "COL-1",
						"title":       "x",
						"description": "spec",
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "Todo"},
					},
				},
			})
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			attachmentReads++
			metadata := map[string]any{"k": "old"}
			if attachmentReads >= 2 {
				metadata = map[string]any{"k": "changed"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{
						"nodes": []map[string]any{
							{
								"id":        "a-1",
								"updatedAt": "2026-02-11T00:00:00Z",
								"metadata":  metadata,
								"issue":     map[string]any{"id": "1"},
							},
						},
					},
				},
			})
		case strings.Contains(req.Query, "mutation UpsertIssueMetadataAttachment"):
			mutationCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"attachmentCreate": map[string]any{"success": true}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	err := client.UpdateIssueMetadata(context.Background(), "1", MetadataPatch{Set: map[string]string{"k": "v"}})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if mutationCalled {
		t.Fatal("expected mutation not to be called after conflict detection")
	}
}

func TestUpdateIssueMetadataUsesAttachmentCreate(t *testing.T) {
	var issueUpdateDescriptionCalled bool
	var attachmentCreateCalled bool
	var metadataValue string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query GetIssue"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "1",
						"identifier":  "COL-1",
						"title":       "x",
						"description": "spec",
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "Todo"},
					},
				},
			})
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{
						"nodes": []map[string]any{
							{
								"id":        "a-1",
								"updatedAt": "2026-02-11T00:00:00Z",
								"metadata":  map[string]any{"k": "old"},
								"issue":     map[string]any{"id": "1"},
							},
						},
					},
				},
			})
		case strings.Contains(req.Query, "mutation UpdateIssueDescription"):
			issueUpdateDescriptionCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		case strings.Contains(req.Query, "mutation UpsertIssueMetadataAttachment"):
			attachmentCreateCalled = true
			input, _ := req.Variables["input"].(map[string]any)
			metadata, _ := input["metadata"].(map[string]any)
			metadataValue = stringVariable(metadata, "k")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"attachmentCreate": map[string]any{"success": true}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	if err := client.UpdateIssueMetadata(context.Background(), "1", MetadataPatch{Set: map[string]string{"k": "new"}}); err != nil {
		t.Fatalf("UpdateIssueMetadata() error = %v", err)
	}
	if issueUpdateDescriptionCalled {
		t.Fatal("did not expect issueUpdate description mutation")
	}
	if !attachmentCreateCalled {
		t.Fatal("expected attachmentCreate mutation")
	}
	if metadataValue != "new" {
		t.Fatalf("attachment metadata k = %q, want %q", metadataValue, "new")
	}
}

func TestCreateIssueComment(t *testing.T) {
	var mutationCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if strings.Contains(req.Query, "mutation CreateIssueComment") {
			mutationCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"commentCreate": map[string]any{"success": true},
				},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	if err := client.CreateIssueComment(context.Background(), "1", "hello"); err != nil {
		t.Fatalf("CreateIssueComment() error = %v", err)
	}
	if !mutationCalled {
		t.Fatal("expected commentCreate mutation")
	}
}

func TestResolveStateIDFromMapSupportsNormalizedMatching(t *testing.T) {
	stateID := resolveStateIDFromMap(map[string]string{
		"Human Review": "state-human-review",
	}, "  human   review ")
	if stateID != "state-human-review" {
		t.Fatalf("resolveStateIDFromMap() = %q, want %q", stateID, "state-human-review")
	}
}

func TestUpdateIssueStateUsesConfiguredStateName(t *testing.T) {
	var updateStateID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query GetIssue"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "1",
						"identifier":  "COL-1",
						"title":       "test",
						"description": "x",
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "In Progress"},
					},
				},
			})
			return
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{"nodes": []map[string]any{}},
				},
			})
			return
		case strings.Contains(req.Query, "query WorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"workflowStates": map[string]any{
						"nodes": []map[string]any{
							{"id": "state-human-review", "name": "Human Review"},
						},
					},
				},
			})
			return
		case strings.Contains(req.Query, "mutation UpdateIssueState"):
			updateStateID = stringVariable(req.Variables, "stateId")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": true},
				},
			})
			return
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "COLIN", srv.Client())
	if err := client.SetWorkflowStates(workflow.States{
		Todo:       "Todo",
		InProgress: "In Progress",
		Refine:     "Refine",
		Review:     "Human Review",
		Merge:      "Merge",
		Done:       "Done",
	}); err != nil {
		t.Fatalf("SetWorkflowStates() error = %v", err)
	}
	if err := client.UpdateIssueState(context.Background(), "1", "Human Review"); err != nil {
		t.Fatalf("UpdateIssueState() error = %v", err)
	}
	if updateStateID != "state-human-review" {
		t.Fatalf("UpdateIssueState() stateId = %q, want %q", updateStateID, "state-human-review")
	}
}

func TestListCandidateIssuesUsesConfiguredRuntimeStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query ListIssues"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{"id": "1", "identifier": "COL-1", "title": "custom todo", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Backlog"}, "inverseRelations": map[string]any{"nodes": []map[string]any{}}},
							{"id": "2", "identifier": "COL-2", "title": "custom done", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Closed"}, "inverseRelations": map[string]any{"nodes": []map[string]any{}}},
						},
					},
				},
			})
			return
		case strings.Contains(req.Query, "query AttachmentsForURL"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentsForURL": map[string]any{"nodes": []map[string]any{}},
				},
			})
			return
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	if err := client.SetWorkflowStates(workflow.States{
		Todo:       "Backlog",
		InProgress: "Doing",
		Refine:     "Needs Spec",
		Review:     "Human Review",
		Merge:      "Merge Queue",
		Done:       "Closed",
	}); err != nil {
		t.Fatalf("SetWorkflowStates() error = %v", err)
	}

	issues, err := client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issue count = %d, want 2", len(issues))
	}
	if issues[0].Identifier != "COL-1" {
		t.Fatalf("issue identifier = %q, want %q", issues[0].Identifier, "COL-1")
	}
	if issues[1].Identifier != "COL-2" {
		t.Fatalf("issue identifier = %q, want %q", issues[1].Identifier, "COL-2")
	}
}

func stringVariable(variables map[string]any, key string) string {
	if len(variables) == 0 {
		return ""
	}
	raw, ok := variables[key]
	if !ok || raw == nil {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
