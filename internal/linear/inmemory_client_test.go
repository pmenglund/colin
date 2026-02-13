package linear

import (
	"context"
	"strings"
	"testing"
)

func TestInMemoryClientListCandidateIssuesFiltersStates(t *testing.T) {
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

	if len(issues) != 3 {
		t.Fatalf("candidate issue count = %d, want 3", len(issues))
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
	if !strings.Contains(issue.Description, "colin:metadata") {
		t.Fatalf("description missing metadata block: %q", issue.Description)
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
