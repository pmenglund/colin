package execplan

import (
	"errors"
	"testing"
)

func TestParseProgressExtractsChecklistItemsFromProgressSectionOnly(t *testing.T) {
	t.Parallel()

	progress, err := ParseProgress(`# Plan

## Progress

- [x] First done
- [ ] Second todo
- [X] Third done

## Artifacts and Notes

- [ ] Ignore this checkbox outside Progress
`)
	if err != nil {
		t.Fatalf("ParseProgress() error = %v", err)
	}
	if len(progress.Items) != 3 {
		t.Fatalf("len(progress.Items) = %d, want 3", len(progress.Items))
	}
	if progress.AllCompleted() {
		t.Fatal("AllCompleted() = true, want false")
	}
	got := progress.Remaining()
	if len(got) != 1 || got[0] != "Second todo" {
		t.Fatalf("Remaining() = %#v, want only second item", got)
	}
}

func TestParseProgressRequiresProgressSection(t *testing.T) {
	t.Parallel()

	_, err := ParseProgress(`# Plan

## Purpose

- [ ] Not in progress
`)
	if !errors.Is(err, ErrMissingProgressSection) {
		t.Fatalf("ParseProgress() error = %v, want ErrMissingProgressSection", err)
	}
}

func TestParseProgressRequiresChecklistItems(t *testing.T) {
	t.Parallel()

	_, err := ParseProgress(`# Plan

## Progress

No checklist yet.
`)
	if !errors.Is(err, ErrMissingProgressItems) {
		t.Fatalf("ParseProgress() error = %v, want ErrMissingProgressItems", err)
	}
}

func TestWorkingCopyRoundTripsTrimmedBody(t *testing.T) {
	t.Parallel()

	copy, err := NewWorkingCopy("\n# Plan\n\n## Progress\n\n- [x] Done\n")
	if err != nil {
		t.Fatalf("NewWorkingCopy() error = %v", err)
	}
	defer func() {
		if err := copy.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	body, err := copy.ReadBody()
	if err != nil {
		t.Fatalf("ReadBody() error = %v", err)
	}
	if body != "# Plan\n\n## Progress\n\n- [x] Done" {
		t.Fatalf("ReadBody() = %q", body)
	}
}
