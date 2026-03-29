package orchestrator

import "testing"

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
