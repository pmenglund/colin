package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeIssuePullRequests(t *testing.T) {
	t.Run("github attachment metadata", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-1",
			"identifier": "COLIN-82",
			"title":      "Add PR info",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-82: add PR info",
						"url":        "https://github.com/pmenglund/colin/pull/2",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":          "https://github.com/pmenglund/colin/pull/2",
							"title":        "COLIN-82: add PR info",
							"number":       2.0,
							"status":       "open",
							"draft":        true,
							"branch":       "colin/COLIN-82",
							"targetBranch": "main",
							"repoLogin":    "pmenglund",
							"repoName":     "colin",
							"createdAt":    "2026-03-01T12:00:00Z",
							"updatedAt":    "2026-03-02T12:00:00Z",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.URL != "https://github.com/pmenglund/colin/pull/2" {
			t.Fatalf("pr.URL = %q", pr.URL)
		}
		if pr.Title != "COLIN-82: add PR info" {
			t.Fatalf("pr.Title = %q", pr.Title)
		}
		if pr.Number == nil || *pr.Number != 2 {
			t.Fatalf("pr.Number = %v, want 2", pr.Number)
		}
		if pr.Status != "open" {
			t.Fatalf("pr.Status = %q", pr.Status)
		}
		if !pr.Draft {
			t.Fatal("pr.Draft = false, want true")
		}
		if pr.Branch != "colin/COLIN-82" {
			t.Fatalf("pr.Branch = %q", pr.Branch)
		}
		if pr.TargetBranch != "main" {
			t.Fatalf("pr.TargetBranch = %q", pr.TargetBranch)
		}
	})

	t.Run("github attachment metadata accepts snake case aliases", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-1a",
			"identifier": "COLIN-82A",
			"title":      "Add PR info",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-82A: add PR info",
						"url":        "https://github.com/pmenglund/colin/pull/21",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":           "https://github.com/pmenglund/colin/pull/21",
							"title":         "COLIN-82A: add PR info",
							"number":        21.0,
							"status":        "open",
							"draft":         true,
							"source_branch": "colin/COLIN-82A",
							"target_branch": "main",
							"repo_login":    "pmenglund",
							"repo_name":     "colin",
							"created_at":    "2026-03-01T12:00:00Z",
							"updated_at":    "2026-03-02T12:00:00Z",
							"closed_at":     "2026-03-03T12:00:00Z",
							"merged_at":     "2026-03-04T12:00:00Z",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.Number == nil || *pr.Number != 21 {
			t.Fatalf("pr.Number = %v, want 21", pr.Number)
		}
		if pr.Branch != "colin/COLIN-82A" {
			t.Fatalf("pr.Branch = %q", pr.Branch)
		}
		if pr.TargetBranch != "main" {
			t.Fatalf("pr.TargetBranch = %q", pr.TargetBranch)
		}
		if pr.RepoLogin != "pmenglund" || pr.RepoName != "colin" {
			t.Fatalf("pr repository = %q/%q, want pmenglund/colin", pr.RepoLogin, pr.RepoName)
		}
		if pr.CreatedAt == nil || pr.UpdatedAt == nil || pr.ClosedAt == nil || pr.MergedAt == nil {
			t.Fatalf("timestamps = %#v, want all timestamps populated", pr)
		}
	})

	t.Run("metadata fallback deduplicates github url", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-2",
			"identifier": "COLIN-83",
			"title":      "Fallback PR URL",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-83: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/3",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":    "https://github.com/pmenglund/colin/pull/3",
							"title":  "COLIN-83: fallback",
							"status": "closed",
						},
					},
					map[string]any{
						"title":      "Colin metadata",
						"sourceType": "unknown",
						"metadata": map[string]any{
							"colin.pr_url": "https://github.com/pmenglund/colin/pull/3",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}
	})

	t.Run("metadata fallback ignores non github pr url", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-2a",
			"identifier": "COLIN-83A",
			"title":      "Fallback PR URL",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "Colin metadata",
						"sourceType": "unknown",
						"metadata": map[string]any{
							"colin.pr_url": "https://example.com/pmenglund/colin/pull/3",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 0 {
			t.Fatalf("len(issue.PullRequests) = %d, want 0", len(issue.PullRequests))
		}
	})

	t.Run("github metadata enriches duplicate fallback url", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-2b",
			"identifier": "COLIN-83",
			"title":      "Fallback PR URL",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "Colin metadata",
						"sourceType": "unknown",
						"metadata": map[string]any{
							"colin.pr_url": "https://github.com/pmenglund/colin/pull/3",
						},
					},
					map[string]any{
						"title":      "COLIN-83: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/3",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":          "https://github.com/pmenglund/colin/pull/3",
							"title":        "COLIN-83: fallback",
							"number":       3.0,
							"status":       "open",
							"branch":       "colin/COLIN-83",
							"targetBranch": "main",
							"repoLogin":    "pmenglund",
							"repoName":     "colin",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.Title != "COLIN-83: fallback" {
			t.Fatalf("pr.Title = %q, want %q", pr.Title, "COLIN-83: fallback")
		}
		if pr.Number == nil || *pr.Number != 3 {
			t.Fatalf("pr.Number = %v, want 3", pr.Number)
		}
		if pr.Status != "open" {
			t.Fatalf("pr.Status = %q, want open", pr.Status)
		}
		if pr.RepoLogin != "pmenglund" || pr.RepoName != "colin" {
			t.Fatalf("pr repository = %q/%q, want pmenglund/colin", pr.RepoLogin, pr.RepoName)
		}
	})

	t.Run("github attachments deduplicate canonical and suffixed urls", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-2c",
			"identifier": "COLIN-83",
			"title":      "Duplicate PR URL forms",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-83: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/3/files",
						"sourceType": "github",
					},
					map[string]any{
						"title":      "COLIN-83: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/3",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":          "https://github.com/pmenglund/colin/pull/3",
							"title":        "COLIN-83: fallback",
							"number":       3.0,
							"status":       "open",
							"branch":       "colin/COLIN-83",
							"targetBranch": "main",
							"repoLogin":    "pmenglund",
							"repoName":     "colin",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.URL != "https://github.com/pmenglund/colin/pull/3" {
			t.Fatalf("pr.URL = %q, want canonical github pull request url", pr.URL)
		}
		if pr.Number == nil || *pr.Number != 3 {
			t.Fatalf("pr.Number = %v, want 3", pr.Number)
		}
		if pr.Status != "open" {
			t.Fatalf("pr.Status = %q, want open", pr.Status)
		}
		if pr.Branch != "colin/COLIN-83" {
			t.Fatalf("pr.Branch = %q, want colin/COLIN-83", pr.Branch)
		}
	})

	t.Run("duplicate github attachments prefer terminal status", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-2d",
			"identifier": "COLIN-83D",
			"title":      "Duplicate PR URL forms",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-83D: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/13",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":          "https://github.com/pmenglund/colin/pull/13",
							"title":        "COLIN-83D: fallback",
							"number":       13.0,
							"status":       "open",
							"branch":       "colin/COLIN-83D",
							"targetBranch": "main",
							"repoLogin":    "pmenglund",
							"repoName":     "colin",
						},
					},
					map[string]any{
						"title":      "COLIN-83D: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/13/files",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":       "https://github.com/pmenglund/colin/pull/13",
							"title":     "COLIN-83D: fallback",
							"number":    13.0,
							"status":    "closed",
							"repoLogin": "pmenglund",
							"repoName":  "colin",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.Status != "closed" {
			t.Fatalf("pr.Status = %q, want closed", pr.Status)
		}
		if pr.Branch != "colin/COLIN-83D" {
			t.Fatalf("pr.Branch = %q, want colin/COLIN-83D", pr.Branch)
		}
	})

	t.Run("duplicate github attachments prefer fresher reopened state", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-2e",
			"identifier": "COLIN-83E",
			"title":      "Reopened PR URL forms",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-83E: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/14",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":       "https://github.com/pmenglund/colin/pull/14",
							"title":     "COLIN-83E: fallback",
							"number":    14.0,
							"status":    "closed",
							"repoLogin": "pmenglund",
							"repoName":  "colin",
							"closedAt":  "2026-03-03T12:00:00Z",
							"updatedAt": "2026-03-03T12:00:00Z",
						},
					},
					map[string]any{
						"title":      "COLIN-83E: fallback",
						"url":        "https://github.com/pmenglund/colin/pull/14/files",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":          "https://github.com/pmenglund/colin/pull/14",
							"title":        "COLIN-83E: fallback",
							"number":       14.0,
							"status":       "open",
							"branch":       "colin/COLIN-83E",
							"targetBranch": "main",
							"repoLogin":    "pmenglund",
							"repoName":     "colin",
							"updatedAt":    "2026-03-04T12:00:00Z",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.Status != "open" {
			t.Fatalf("pr.Status = %q, want open", pr.Status)
		}
		if pr.ClosedAt != nil {
			t.Fatalf("pr.ClosedAt = %v, want nil after reopen", pr.ClosedAt)
		}
		if pr.Branch != "colin/COLIN-83E" {
			t.Fatalf("pr.Branch = %q, want colin/COLIN-83E", pr.Branch)
		}
	})

	t.Run("github attachment without metadata still produces basic pr info", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-3",
			"identifier": "COLIN-84",
			"title":      "Bare GitHub attachment",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-84: bare attachment",
						"url":        "https://github.com/pmenglund/colin/pull/4",
						"sourceType": "github",
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.URL != "https://github.com/pmenglund/colin/pull/4" {
			t.Fatalf("pr.URL = %q", pr.URL)
		}
		if pr.Title != "COLIN-84: bare attachment" {
			t.Fatalf("pr.Title = %q", pr.Title)
		}
		if pr.Number == nil || *pr.Number != 4 {
			t.Fatalf("pr.Number = %v, want 4", pr.Number)
		}
		if pr.RepoLogin != "pmenglund" || pr.RepoName != "colin" {
			t.Fatalf("pr repository = %q/%q, want pmenglund/colin", pr.RepoLogin, pr.RepoName)
		}
		if pr.Status != "" {
			t.Fatalf("pr.Status = %q, want empty", pr.Status)
		}
	})

	t.Run("github attachment canonicalizes suffixed url", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-3c",
			"identifier": "COLIN-84C",
			"title":      "Suffixed GitHub attachment",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-84C: review changes",
						"url":        "https://github.com/pmenglund/colin/pull/14/files?diff=split",
						"sourceType": "github",
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.URL != "https://github.com/pmenglund/colin/pull/14" {
			t.Fatalf("pr.URL = %q, want canonical github pull request url", pr.URL)
		}
		if pr.Number == nil || *pr.Number != 14 {
			t.Fatalf("pr.Number = %v, want 14", pr.Number)
		}
	})

	t.Run("github attachment falls back to node url when metadata url is not a pr", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-3a",
			"identifier": "COLIN-84A",
			"title":      "GitHub attachment with bad metadata url",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-84A: bad metadata url",
						"url":        "https://github.com/pmenglund/colin/pull/14",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":   "https://github.com/pmenglund/colin",
							"title": "COLIN-84A: bad metadata url",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.URL != "https://github.com/pmenglund/colin/pull/14" {
			t.Fatalf("pr.URL = %q", pr.URL)
		}
		if pr.Number == nil || *pr.Number != 14 {
			t.Fatalf("pr.Number = %v, want 14", pr.Number)
		}
	})

	t.Run("github attachment uses metadata url when node url is empty", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-3aa",
			"identifier": "COLIN-84AA",
			"title":      "GitHub attachment with metadata url only",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-84AA: metadata url only",
						"url":        "",
						"sourceType": "github",
						"metadata": map[string]any{
							"pull_request_url": "https://github.com/pmenglund/colin/pull/16",
							"title":            "COLIN-84AA: metadata url only",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.URL != "https://github.com/pmenglund/colin/pull/16" {
			t.Fatalf("pr.URL = %q", pr.URL)
		}
		if pr.Number == nil || *pr.Number != 16 {
			t.Fatalf("pr.Number = %v, want 16", pr.Number)
		}
		if pr.RepoLogin != "pmenglund" || pr.RepoName != "colin" {
			t.Fatalf("pr repository = %q/%q, want pmenglund/colin", pr.RepoLogin, pr.RepoName)
		}
	})

	t.Run("github attachment with non-pr url is ignored", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-3b",
			"identifier": "COLIN-84",
			"title":      "Non PR GitHub attachment",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "repository link",
						"url":        "https://github.com/pmenglund/colin",
						"sourceType": "github",
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 0 {
			t.Fatalf("len(issue.PullRequests) = %d, want 0", len(issue.PullRequests))
		}
	})

	t.Run("non-github attachment url still produces basic pr info", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-4",
			"identifier": "COLIN-90",
			"title":      "Linked PR URL",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "COLIN-90: linked pr",
						"url":        "https://github.com/pmenglund/colin/pull/10",
						"sourceType": "link",
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.URL != "https://github.com/pmenglund/colin/pull/10" {
			t.Fatalf("pr.URL = %q", pr.URL)
		}
		if pr.Title != "COLIN-90: linked pr" {
			t.Fatalf("pr.Title = %q", pr.Title)
		}
		if pr.Number == nil || *pr.Number != 10 {
			t.Fatalf("pr.Number = %v, want 10", pr.Number)
		}
		if pr.RepoLogin != "pmenglund" || pr.RepoName != "colin" {
			t.Fatalf("pr repository = %q/%q, want pmenglund/colin", pr.RepoLogin, pr.RepoName)
		}
	})

	t.Run("non-github attachment url deduplicates richer github metadata", func(t *testing.T) {
		node := map[string]any{
			"id":         "issue-5",
			"identifier": "COLIN-91",
			"title":      "Duplicate linked PR URL",
			"state":      map[string]any{"name": "Review"},
			"attachments": map[string]any{
				"nodes": []any{
					map[string]any{
						"title":      "linked pr",
						"url":        "https://github.com/pmenglund/colin/pull/11",
						"sourceType": "link",
					},
					map[string]any{
						"title":      "COLIN-91: richer pr",
						"url":        "https://github.com/pmenglund/colin/pull/11",
						"sourceType": "github",
						"metadata": map[string]any{
							"url":          "https://github.com/pmenglund/colin/pull/11",
							"title":        "COLIN-91: richer pr",
							"number":       11.0,
							"status":       "open",
							"branch":       "colin/COLIN-91",
							"targetBranch": "main",
							"repoLogin":    "pmenglund",
							"repoName":     "colin",
						},
					},
				},
			},
		}

		issue, err := normalizeIssue(node)
		if err != nil {
			t.Fatalf("normalizeIssue() error = %v", err)
		}
		if len(issue.PullRequests) != 1 {
			t.Fatalf("len(issue.PullRequests) = %d, want 1", len(issue.PullRequests))
		}

		pr := issue.PullRequests[0]
		if pr.Title != "COLIN-91: richer pr" {
			t.Fatalf("pr.Title = %q, want %q", pr.Title, "COLIN-91: richer pr")
		}
		if pr.Status != "open" {
			t.Fatalf("pr.Status = %q, want open", pr.Status)
		}
		if pr.Branch != "colin/COLIN-91" {
			t.Fatalf("pr.Branch = %q, want colin/COLIN-91", pr.Branch)
		}
	})
}

func TestFetchIssuesByStatesEmptyListFetchesWholeProject(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		states []string
	}{
		{name: "nil slice", states: nil},
		{name: "empty slice", states: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requestBody struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Fatalf("method = %s, want %s", r.Method, http.MethodPost)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("io.ReadAll() error = %v", err)
				}
				if err := json.Unmarshal(body, &requestBody); err != nil {
					t.Fatalf("json.Unmarshal() error = %v", err)
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"data": {
						"issues": {
							"pageInfo": { "hasNextPage": false, "endCursor": null },
							"nodes": [{
								"id": "issue-1",
								"identifier": "COLIN-94",
								"title": "Add basic GitHub PR info",
								"state": { "name": "Review" },
								"attachments": { "nodes": [] },
								"inverseRelations": { "nodes": [] }
							}]
						}
					}
				}`))
			}))
			defer server.Close()

			client := &Client{
				endpoint: server.URL,
				apiKey:   "token",
				project:  "colin",
				client:   server.Client(),
			}

			issues, err := client.FetchIssuesByStates(context.Background(), tc.states)
			if err != nil {
				t.Fatalf("FetchIssuesByStates() error = %v", err)
			}
			if len(issues) != 1 {
				t.Fatalf("len(issues) = %d, want 1", len(issues))
			}
			if issues[0].Identifier != "COLIN-94" {
				t.Fatalf("issues[0].Identifier = %q, want COLIN-94", issues[0].Identifier)
			}
			if _, ok := requestBody.Variables["states"]; ok {
				t.Fatal("variables unexpectedly included states for empty state filter")
			}
			if requestBody.Variables["projectSlug"] != "colin" {
				t.Fatalf("projectSlug = %v, want colin", requestBody.Variables["projectSlug"])
			}
			if got := requestBody.Query; got == "" {
				t.Fatal("query was empty")
			}
			if containsStateFilter(requestBody.Query) {
				t.Fatalf("query unexpectedly contained state filter: %s", requestBody.Query)
			}
		})
	}
}

func containsStateFilter(query string) bool {
	return query != "" && (strings.Contains(query, "$states") || strings.Contains(query, "state: { name: { in: $states } }"))
}
