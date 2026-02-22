package linear

import (
	"context"
	"testing"

	"github.com/pmenglund/colin/internal/workflow"
)

func TestInMemoryClientListCandidateIssuesReturnsAllStates(t *testing.T) {
	client := NewInMemoryClient([]Issue{
		{ID: "1", Identifier: "COL-1", StateName: "Todo", Description: "spec"},
		{ID: "2", Identifier: "COL-2", StateName: "In Progress", Description: "spec"},
		{ID: "3", Identifier: "COL-3", StateName: "Merge", Description: "spec"},
		{ID: "4", Identifier: "COL-4", StateName: "Done", Description: "spec"},
	})

	issues, err := client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}

	if len(issues) != 4 {
		t.Fatalf("issue count = %d, want 4", len(issues))
	}
	if issues[0].Identifier != "COL-1" {
		t.Fatalf("first issue = %q, want COL-1", issues[0].Identifier)
	}
	if issues[1].Identifier != "COL-2" {
		t.Fatalf("second issue = %q, want COL-2", issues[1].Identifier)
	}
	if issues[2].Identifier != "COL-3" {
		t.Fatalf("third issue = %q, want COL-3", issues[2].Identifier)
	}
	if issues[3].Identifier != "COL-4" {
		t.Fatalf("fourth issue = %q, want COL-4", issues[3].Identifier)
	}
}

func TestInMemoryClientListCandidateIssuesTracksBlockedUntilDependencyDone(t *testing.T) {
	client := NewInMemoryClient([]Issue{
		{ID: "dep", Identifier: "COL-DEP", StateName: "Todo", Description: "dep"},
		{ID: "blocked", Identifier: "COL-BLOCKED", StateName: "Todo", Description: "blocked", BlockedBy: []string{"dep"}},
	})

	issues, err := client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("issue count = %d, want 2", len(issues))
	}
	if issues[0].Identifier != "COL-BLOCKED" {
		t.Fatalf("first issue = %q, want COL-BLOCKED", issues[0].Identifier)
	}
	if !issues[0].Blocked {
		t.Fatal("blocked issue should be marked blocked")
	}
	if issues[1].Identifier != "COL-DEP" {
		t.Fatalf("second issue = %q, want COL-DEP", issues[1].Identifier)
	}

	if err := client.UpdateIssueState(context.Background(), "dep", "Done"); err != nil {
		t.Fatalf("UpdateIssueState() error = %v", err)
	}

	issues, err = client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() after unblocking error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issue count after unblocking = %d, want 2", len(issues))
	}
	if issues[0].Identifier != "COL-BLOCKED" {
		t.Fatalf("first issue after unblocking = %q, want COL-BLOCKED", issues[0].Identifier)
	}
	if issues[0].Blocked {
		t.Fatal("blocked issue should be unblocked after dependency is done")
	}
}

func TestInMemoryClientListCandidateIssuesTracksBlockedInProgressUntilDependencyDone(t *testing.T) {
	client := NewInMemoryClient([]Issue{
		{ID: "dep", Identifier: "COL-DEP", StateName: "Todo", Description: "dep"},
		{ID: "blocked", Identifier: "COL-BLOCKED", StateName: "In Progress", Description: "blocked", BlockedBy: []string{"dep"}},
	})

	issues, err := client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issue count = %d, want 2", len(issues))
	}
	if issues[0].Identifier != "COL-BLOCKED" {
		t.Fatalf("first issue = %q, want COL-BLOCKED", issues[0].Identifier)
	}
	if !issues[0].Blocked {
		t.Fatal("blocked issue should be marked blocked")
	}
	if issues[1].Identifier != "COL-DEP" {
		t.Fatalf("second issue = %q, want COL-DEP", issues[1].Identifier)
	}

	if err := client.UpdateIssueState(context.Background(), "dep", "Done"); err != nil {
		t.Fatalf("UpdateIssueState() error = %v", err)
	}

	issues, err = client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() after unblocking error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issue count after unblocking = %d, want 2", len(issues))
	}
	if issues[0].Identifier != "COL-BLOCKED" {
		t.Fatalf("first issue after unblocking = %q, want COL-BLOCKED", issues[0].Identifier)
	}
	if issues[0].Blocked {
		t.Fatal("blocked issue should be unblocked after dependency is done")
	}
}

func TestInMemoryClientUpdates(t *testing.T) {
	client := NewInMemoryClient([]Issue{
		{
			ID:          "1",
			Identifier:  "COL-1",
			StateName:   "Todo",
			Description: "spec",
		},
	})

	if err := client.UpdateIssueMetadata(context.Background(), "1", MetadataPatch{
		Set: map[string]string{
			"colin.reason": "testing",
		},
	}); err != nil {
		t.Fatalf("UpdateIssueMetadata() error = %v", err)
	}
	if err := client.CreateIssueComment(context.Background(), "1", "hello world"); err != nil {
		t.Fatalf("CreateIssueComment() error = %v", err)
	}
	if err := client.UpdateIssueState(context.Background(), "1", "In Progress"); err != nil {
		t.Fatalf("UpdateIssueState() error = %v", err)
	}

	issue, err := client.GetIssue(context.Background(), "1")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}

	if issue.StateName != "In Progress" {
		t.Fatalf("StateName = %q, want In Progress", issue.StateName)
	}
	if issue.Metadata["colin.reason"] != "testing" {
		t.Fatalf("Metadata[colin.reason] = %q", issue.Metadata["colin.reason"])
	}
	if issue.Description != "spec" {
		t.Fatalf("Description = %q, want %q", issue.Description, "spec")
	}
}

func TestInMemoryClientGetIssueByIdentifier(t *testing.T) {
	client := NewInMemoryClient([]Issue{
		{
			ID:         "1",
			Identifier: "COL-1",
			StateName:  "Todo",
			Metadata: map[string]string{
				"colin.branch_name": "colin/COL-1",
			},
		},
	})

	issue, err := client.GetIssueByIdentifier(context.Background(), " col-1 ")
	if err != nil {
		t.Fatalf("GetIssueByIdentifier() error = %v", err)
	}
	if issue.ID != "1" {
		t.Fatalf("issue ID = %q, want %q", issue.ID, "1")
	}
	if issue.Metadata["colin.branch_name"] != "colin/COL-1" {
		t.Fatalf("metadata colin.branch_name = %q", issue.Metadata["colin.branch_name"])
	}
}

func TestInMemoryClientGetIssueByIdentifierNotFound(t *testing.T) {
	client := NewInMemoryClient(nil)

	_, err := client.GetIssueByIdentifier(context.Background(), "COL-404")
	if err == nil {
		t.Fatal("expected not found error")
	}
	if got := err.Error(); got != "issue COL-404 not found" {
		t.Fatalf("error = %q", got)
	}
}

func TestNewDefaultInMemoryClientHasSeedIssue(t *testing.T) {
	client := NewDefaultInMemoryClient()
	issues, err := client.ListCandidateIssues(context.Background(), "")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("expected default client to have at least one seeded issue")
	}
}

func TestInMemoryClientUsesConfiguredRuntimeStatesForBlockedCalculation(t *testing.T) {
	client := NewInMemoryClient([]Issue{
		{ID: "1", Identifier: "COL-1", StateName: "Backlog", Description: "spec"},
		{ID: "2", Identifier: "COL-2", StateName: "Blocked", Description: "blocked", BlockedBy: []string{"3"}},
		{ID: "3", Identifier: "COL-3", StateName: "Review", Description: "not done"},
	})
	if err := client.SetWorkflowStates(workflow.States{
		Todo:       "Backlog",
		InProgress: "Blocked",
		Refine:     "Needs Spec",
		Review:     "Review",
		Merge:      "Merge Queue",
		Done:       "Closed",
	}); err != nil {
		t.Fatalf("SetWorkflowStates() error = %v", err)
	}

	issues, err := client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("issue count = %d, want 3", len(issues))
	}
	if !issues[1].Blocked {
		t.Fatal("blocked issue should be marked blocked with configured done state")
	}
}
