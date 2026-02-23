package workflow

import "testing"

func TestStatesValidateRejectsDuplicateNames(t *testing.T) {
	err := States{
		Todo:       "Todo",
		InProgress: "todo",
		Refine:     "Refine",
		Review:     "Review",
		Merge:      "Merge",
		Merged:     "Merged",
		Done:       "Done",
	}.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want duplicate names error")
	}
}

func TestStatesIsCandidateAndDoneWithCustomNames(t *testing.T) {
	states := States{
		Todo:       "Backlog",
		InProgress: "Doing",
		Refine:     "Needs Spec",
		Review:     "Human Review",
		Merge:      "Merge Queue",
		Merged:     "Merged Queue",
		Done:       "Closed",
	}

	if !states.IsCandidate("Backlog") {
		t.Fatal("IsCandidate(Backlog) = false, want true")
	}
	if !states.IsCandidate("Doing") {
		t.Fatal("IsCandidate(Doing) = false, want true")
	}
	if !states.IsCandidate("Merge Queue") {
		t.Fatal("IsCandidate(Merge Queue) = false, want true")
	}
	if !states.IsCandidate("Merged Queue") {
		t.Fatal("IsCandidate(Merged Queue) = false, want true")
	}
	if !states.IsCandidate("Closed") {
		t.Fatal("IsCandidate(Closed) = false, want true")
	}
	if !states.IsDone("Closed") {
		t.Fatal("IsDone(Closed) = false, want true")
	}
}

func TestStatesCanTransitionWithCustomNames(t *testing.T) {
	states := States{
		Todo:       "Backlog",
		InProgress: "Doing",
		Refine:     "Needs Spec",
		Review:     "Human Review",
		Merge:      "Merge Queue",
		Merged:     "Merged Queue",
		Done:       "Closed",
	}

	if !states.CanTransition("Backlog", "Doing") {
		t.Fatal("CanTransition(Backlog, Doing) = false, want true")
	}
	if !states.CanTransition("Human Review", "Merge Queue") {
		t.Fatal("CanTransition(Human Review, Merge Queue) = false, want true")
	}
	if !states.CanTransition("Merge Queue", "Merged Queue") {
		t.Fatal("CanTransition(Merge Queue, Merged Queue) = false, want true")
	}
	if !states.CanTransition("Merged Queue", "Closed") {
		t.Fatal("CanTransition(Merged Queue, Closed) = false, want true")
	}
	if !states.CanTransition("Closed", "Merge Queue") {
		t.Fatal("CanTransition(Closed, Merge Queue) = false, want true")
	}
	if !states.CanTransition("Closed", "Merged Queue") {
		t.Fatal("CanTransition(Closed, Merged Queue) = false, want true")
	}
	if states.CanTransition("Backlog", "Closed") {
		t.Fatal("CanTransition(Backlog, Closed) = true, want false")
	}
}
