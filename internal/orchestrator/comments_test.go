package orchestrator

import (
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/agent/codex"
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
