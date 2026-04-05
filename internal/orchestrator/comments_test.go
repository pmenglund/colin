package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
)

func TestColinCommentBodyPrefixesMessages(t *testing.T) {
	t.Parallel()

	got := colinCommentBody("Run completed successfully.")
	want := "[colin] Run completed successfully."
	if got != want {
		t.Fatalf("colinCommentBody() = %q, want %q", got, want)
	}
}

func TestColinCommentBodyDoesNotDoublePrefix(t *testing.T) {
	t.Parallel()

	got := colinCommentBody("[colin] Run completed successfully.")
	want := "[colin] Run completed successfully."
	if got != want {
		t.Fatalf("colinCommentBody() = %q, want %q", got, want)
	}
}

func TestColinCommentBodyPlacesHeadingOnNewLine(t *testing.T) {
	t.Parallel()

	got := colinCommentBody("## Why\n\nInteractive sessions render markdown headings.")
	want := "[colin]\n## Why\n\nInteractive sessions render markdown headings."
	if got != want {
		t.Fatalf("colinCommentBody() = %q, want %q", got, want)
	}
}

func TestRootCommentBodyOmitsRedundantIssueAndState(t *testing.T) {
	t.Parallel()

	got := rootCommentBody(&runningEntry{identifier: "COLIN-108"}, codex.Event{
		RunType:   codex.RunTypeCoding,
		Attempt:   0,
		State:     "In Progress",
		Workspace: "/tmp/COLIN-108",
		SessionID: "session-123",
		ThreadID:  "thread-456",
	})

	for _, want := range []string{
		"Colin started work on this issue.",
		"Run type: `coding`",
		"Attempt: `0`",
		"Workspace: `/tmp/COLIN-108`",
		"Session ID: `session-123`",
		"Thread ID: `thread-456`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rootCommentBody() = %q, want substring %q", got, want)
		}
	}

	for _, unwanted := range []string{"Issue: `", "State: `"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("rootCommentBody() = %q, want no substring %q", got, unwanted)
		}
	}
}

func TestPostIssueStatusDetailedReusesPersistedProgressRootComment(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{
			Tracker: tracker,
		},
	}

	issue, comment, commentID := orch.postIssueStatusDetailed(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-108",
		State:      "Todo",
		ColinMetadata: &domain.ColinMetadata{
			ProgressRootCommentID: "root-existing",
		},
	}, "COLIN-108", nil, "Review sync is waiting.")

	if commentID != "reply" {
		t.Fatalf("commentID = %q, want reply", commentID)
	}
	if got := len(tracker.issueComments); got != 0 {
		t.Fatalf("issueComments length = %d, want 0", got)
	}
	if got := len(tracker.commentReplies); got != 1 {
		t.Fatalf("commentReplies length = %d, want 1", got)
	}
	if comment == nil || comment.RootCommentID != "root-existing" {
		t.Fatalf("comment = %#v, want root-existing", comment)
	}
	if issue.ColinMetadata == nil || issue.ColinMetadata.ProgressRootCommentID != "root-existing" {
		t.Fatalf("issue.ColinMetadata = %#v, want persisted root-existing", issue.ColinMetadata)
	}
}

func TestPostIssueStatusDetailedPersistsCreatedProgressRootComment(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{
			Tracker: tracker,
		},
	}

	issue, comment, commentID := orch.postIssueStatusDetailed(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-108",
		State:      "Todo",
	}, "COLIN-108", nil, "Review sync is waiting.")

	if commentID != "root" {
		t.Fatalf("commentID = %q, want root", commentID)
	}
	if comment == nil || comment.RootCommentID != "root" {
		t.Fatalf("comment = %#v, want root", comment)
	}
	if tracker.metadata.ProgressRootCommentID != "root" {
		t.Fatalf("metadata.ProgressRootCommentID = %q, want root", tracker.metadata.ProgressRootCommentID)
	}
	if issue.ColinMetadata == nil || issue.ColinMetadata.ProgressRootCommentID != "root" {
		t.Fatalf("issue.ColinMetadata = %#v, want persisted root", issue.ColinMetadata)
	}
}
