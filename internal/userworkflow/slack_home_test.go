package userworkflow

import (
	"reflect"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
)

func TestSlackHomeStateNames(t *testing.T) {
	t.Parallel()

	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates:   []string{"Todo", "In Progress", "Backlog"},
			TerminalStates: []string{"Done", "Closed"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Refine", "Review", "Done"},
			MergeStates:   []string{"Merge", "review"},
		},
	}

	got := SlackHomeStateNames(cfg)
	want := []string{"Todo", "In Progress", "Refine", "Review", "Merge"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SlackHomeStateNames() = %#v, want %#v", got, want)
	}
}

func TestSlackHomeIssueViewGroupsAndSortsIssues(t *testing.T) {
	t.Parallel()

	urlTodo := "https://linear.example.test/COLIN-2"
	view := SlackHomeIssueView(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Done", "Closed"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
	}, []domain.Issue{
		{ID: "4", Identifier: "COLIN-4", Title: "Done issue", State: "Done"},
		{ID: "3", Identifier: "COLIN-3", Title: "Needs refinement", State: "Refine"},
		{ID: "2", Identifier: "COLIN-2", Title: "Second todo", State: "Todo", URL: &urlTodo},
		{ID: "1", Identifier: "COLIN-1", Title: "First todo", State: "Todo"},
		{ID: "6", Identifier: "COLIN-6", Title: "Merged issue", State: "Closed"},
		{ID: "5", Identifier: "COLIN-5", Title: "Review me", State: "Review"},
	})

	if view.TotalIssues != 4 {
		t.Fatalf("TotalIssues = %d, want 4", view.TotalIssues)
	}
	if len(view.Groups) != 3 {
		t.Fatalf("len(Groups) = %d, want 3", len(view.Groups))
	}

	if view.Groups[0].State != "Todo" {
		t.Fatalf("Groups[0].State = %q, want Todo", view.Groups[0].State)
	}
	if got := view.Groups[0].Issues[0].Identifier; got != "COLIN-1" {
		t.Fatalf("Groups[0].Issues[0].Identifier = %q, want COLIN-1", got)
	}
	if got := view.Groups[0].Issues[1].URL; got != urlTodo {
		t.Fatalf("Groups[0].Issues[1].URL = %q, want %q", got, urlTodo)
	}
	if view.Groups[1].State != "Refine" {
		t.Fatalf("Groups[1].State = %q, want Refine", view.Groups[1].State)
	}
	if view.Groups[2].State != "Review" {
		t.Fatalf("Groups[2].State = %q, want Review", view.Groups[2].State)
	}
}
